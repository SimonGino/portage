package anthropic

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Anthropic Messages 的**编码**侧：canonical 事件序列 → 下行响应。
//
// 线格式照 testdata/golden/raw/anthropic-* 五份真实上游 SSE 转录，不是照协议文档
// 复述的：message_start → (content_block_start → content_block_delta* →
// content_block_stop)* → message_delta → message_stop，每帧 `event:` 与 `data:`
// 两行。ping 帧不发——它是上游的保活手段，网关这一侧的保活由 HTTP 层负责，凭空
// 造心跳只会让转录对不上。

// Flusher 是 EncodeStream 每帧之后要调的钩子。
//
// 不直接依赖 http.Flusher：codec 是纯编码层，测试里的 w 是 bytes.Buffer，没有也
// 不该有 HTTP 概念。谁提供 Flush 谁被调，提供不了就当作无缓冲。
type Flusher interface{ Flush() }

// EncodeStream 把事件流编成 Anthropic SSE 下发。
//
// **块边界纪律**：Anthropic 的内容块是严格顺序的——同一时刻只能有一个块开着，
// 下一个块必须等上一个 content_block_stop。而 CC 的并行工具调用分片是按 index
// 交错来的，所以工具调用一律攒到 EvToolCallEnd 再整块放出（Start/Delta*/Stop 一次
// 写完）。正文不受影响，EvTextDelta 到一条发一条，**逐字下发**。
func (c *Codec) EncodeStream(w io.Writer, events <-chan protocol.Event) error {
	enc := &streamEncoder{w: w}
	for ev := range events {
		if err := enc.event(ev); err != nil {
			return err
		}
	}
	return enc.finish()
}

type streamEncoder struct {
	w io.Writer

	started   bool
	id        string
	model     string
	nextIndex int
	textOpen  bool
	stop      string
	usage     protocol.Usage
	finished  bool
	// sawErrored：流内已经发过 error 帧，后面不再补 message_delta/message_stop——
	// 那会让客户端以为响应正常收尾了。
	sawErrored bool
	// pending 是正在攒的工具调用，同一时刻至多一个（见 EncodeStream 的块边界纪律）。
	pending *toolPending
}

func (e *streamEncoder) event(ev protocol.Event) error {
	switch ev.Type {
	case protocol.EvMessageStart:
		e.id, e.model = ev.ID, ev.Model
		return e.ensureStarted()

	case protocol.EvTextDelta:
		if ev.Text == "" {
			return nil
		}
		if err := e.ensureStarted(); err != nil {
			return err
		}
		if !e.textOpen {
			if err := e.frame("content_block_start", map[string]any{
				"type":          "content_block_start",
				"index":         e.nextIndex,
				"content_block": map[string]any{"type": "text", "text": ""},
			}); err != nil {
				return err
			}
			e.textOpen = true
		}
		return e.frame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.nextIndex,
			"delta": map[string]any{"type": "text_delta", "text": ev.Text},
		})

	case protocol.EvThinkingDelta:
		// 跨协议丢弃，不做伪映射（口径层 §2.6）。伪造 thinking 块要连 signature
		// 一起造，而 signature 是上游侧凭据——造出来的块回带给上游会被拒。
		return nil

	case protocol.EvToolCallStart, protocol.EvToolArgsDelta:
		// 攒着，等 EvToolCallEnd 一次整块放出。解码侧已保证同一 index 的
		// Start/Delta*/End 是连续且按序的（见 openaicc.flushTools）。
		return e.bufferTool(ev)

	case protocol.EvToolCallEnd:
		return e.flushTool(ev.Index)

	case protocol.EvUsage:
		if ev.Usage != nil {
			// 累计快照语义：后来者的非零字段覆盖，不做加法（protocol/event.go）。
			e.usage.MergeSnapshot(*ev.Usage)
		}
		return nil

	case protocol.EvDone:
		e.stop = ev.StopReason
		return nil

	case protocol.EvError:
		return e.writeError(ev)
	}
	return nil
}

// toolPending 是正在攒的工具调用。同一时刻只可能有一个——解码侧按 index 顺序放，
// 一个 End 之后才轮到下一个 Start。
type toolPending struct {
	index int
	id    string
	name  string
	args  []string
}

func (e *streamEncoder) bufferTool(ev protocol.Event) error {
	if e.pending == nil || e.pending.index != ev.Index {
		if ev.Type != protocol.EvToolCallStart {
			// 没见过 Start 的 index 冒出来了。补一个空壳而不是丢弃：丢了客户端会
			// 拿到一个引用不存在工具的结果，而空壳至少让它看得见发生过什么。
			e.pending = &toolPending{index: ev.Index}
		} else {
			e.pending = &toolPending{index: ev.Index, id: ev.ToolID, name: ev.ToolName}
			return nil
		}
	}
	if ev.Type == protocol.EvToolCallStart {
		e.pending.id, e.pending.name = ev.ToolID, ev.ToolName
		return nil
	}
	if ev.Text != "" {
		e.pending.args = append(e.pending.args, ev.Text)
	}
	return nil
}

// flushTool 把攒好的工具调用整块写出去：start → delta* → stop。
func (e *streamEncoder) flushTool(index int) error {
	if e.pending == nil || e.pending.index != index {
		return nil
	}
	pending := e.pending
	e.pending = nil

	if err := e.ensureStarted(); err != nil {
		return err
	}
	if err := e.closeText(); err != nil {
		return err
	}
	if err := e.frame("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": e.nextIndex,
		"content_block": map[string]any{
			"type": "tool_use",
			"id":   pending.id,
			"name": pending.name,
			// 实采的 content_block_start 里 input 恒是空对象，真正的入参全靠
			// 后面的 input_json_delta 拼。照抄这个形态。
			"input": map[string]any{},
		},
	}); err != nil {
		return err
	}
	// 原样转发上游的分片节奏；上游一片没发（入参为空）时补一个 "{}"，否则客户端
	// 拼出来的是空串，解析不成 JSON。
	frags := pending.args
	if len(frags) == 0 {
		frags = []string{"{}"}
	}
	for _, frag := range frags {
		if err := e.frame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.nextIndex,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": frag},
		}); err != nil {
			return err
		}
	}
	if err := e.frame("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": e.nextIndex,
	}); err != nil {
		return err
	}
	e.nextIndex++
	return nil
}

func (e *streamEncoder) closeText() error {
	if !e.textOpen {
		return nil
	}
	if err := e.frame("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": e.nextIndex,
	}); err != nil {
		return err
	}
	e.textOpen = false
	e.nextIndex++
	return nil
}

func (e *streamEncoder) ensureStarted() error {
	if e.started {
		return nil
	}
	e.started = true
	if e.id == "" {
		e.id = fallbackMessageID()
	}
	// message_start 的 usage 只能给零：CC 的 usage 要到流末才来（哪怕带了
	// stream_options.include_usage），而 Anthropic 的 input_tokens 按线格式在开头。
	// 补零而不是等——等就得把整条流缓冲起来，首字延迟直接归零收益。真实数字在
	// message_delta 里补齐，那一帧 Anthropic 自己也带全套 usage（实采转录如此）。
	return e.frame("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            e.id,
			"type":          "message",
			"role":          "assistant",
			"model":         e.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

func (e *streamEncoder) finish() error {
	if e.finished || e.sawErrored {
		return nil
	}
	e.finished = true
	if err := e.ensureStarted(); err != nil {
		return err
	}
	// 上游流断在半截（没等到 EvToolCallEnd）时，攒着的那个工具调用照样放出去：
	// 半个工具调用总比凭空消失强，客户端至少能看到调用意图并自行处置。
	if e.pending != nil {
		if err := e.flushTool(e.pending.index); err != nil {
			return err
		}
	}
	if err := e.closeText(); err != nil {
		return err
	}
	if err := e.frame("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   mapStopReason(e.stop),
			"stop_sequence": nil,
		},
		"usage": usageBody(e.usage),
	}); err != nil {
		return err
	}
	return e.frame("message_stop", map[string]any{"type": "message_stop"})
}

// writeError 把流内错误按 Anthropic 的 error 事件发下去。
//
// 只在**已经写出首字节之后**才走得到这里——此前的错误由 EncodeError 走 HTTP 状态
// 码那条路。首字节一出格式承诺已生效，改不了状态码，只能在流里说。
func (e *streamEncoder) writeError(ev protocol.Event) error {
	e.sawErrored = true
	msg := ev.Message
	if msg == "" {
		msg = "上游响应流异常中断"
	}
	return e.frame("error", map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "api_error",
			"message": msg,
		},
	})
}

func (e *streamEncoder) frame(event string, payload any) error {
	data, err := marshal(payload)
	if err != nil {
		return err
	}
	var buf strings.Builder
	buf.WriteString("event: ")
	buf.WriteString(event)
	buf.WriteString("\ndata: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	if _, err := io.WriteString(e.w, buf.String()); err != nil {
		return err
	}
	// 每帧一 flush：攒着发等于把逐字输出重新变成一次性吐出，而「逐字出现」是
	// #11 的验收项。
	if f, ok := e.w.(Flusher); ok {
		f.Flush()
	}
	return nil
}

// EncodeFullBody 把完整事件序列聚合成非流式 Anthropic 响应体。
func (c *Codec) EncodeFullBody(events []protocol.Event) ([]byte, error) {
	var (
		id, model string
		stop      string
		usage     protocol.Usage
		text      strings.Builder
		content   []any
		tools     = map[int]*toolPending{}
		order     []int
		errEvent  *protocol.Event
	)

	for i := range events {
		ev := events[i]
		switch ev.Type {
		case protocol.EvMessageStart:
			id, model = ev.ID, ev.Model
		case protocol.EvTextDelta:
			text.WriteString(ev.Text)
		case protocol.EvToolCallStart:
			if _, ok := tools[ev.Index]; !ok {
				order = append(order, ev.Index)
			}
			tools[ev.Index] = &toolPending{index: ev.Index, id: ev.ToolID, name: ev.ToolName}
		case protocol.EvToolArgsDelta:
			buf, ok := tools[ev.Index]
			if !ok {
				buf = &toolPending{index: ev.Index}
				tools[ev.Index] = buf
				order = append(order, ev.Index)
			}
			buf.args = append(buf.args, ev.Text)
		case protocol.EvUsage:
			if ev.Usage != nil {
				usage.MergeSnapshot(*ev.Usage)
			}
		case protocol.EvDone:
			stop = ev.StopReason
		case protocol.EvError:
			errEvent = &events[i]
		}
	}

	if errEvent != nil {
		return nil, fmt.Errorf("anthropic: 上游响应错误: %s", errEvent.Message)
	}

	if s := text.String(); s != "" {
		content = append(content, map[string]any{"type": "text", "text": s})
	}
	for _, idx := range order {
		buf := tools[idx]
		input, err := decodeToolInput(strings.Join(buf.args, ""))
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]any{
			"type": "tool_use", "id": buf.id, "name": buf.name, "input": input,
		})
	}
	if content == nil {
		content = []any{}
	}
	if id == "" {
		id = fallbackMessageID()
	}

	return marshal(map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   mapStopReason(stop),
		"stop_sequence": nil,
		"usage":         usageBody(usage),
	})
}

// fallbackMessageID 在上游一个 id 都没给时补一个。
//
// 触发条件是「上游发了 model 但没发 id」：CC 解码侧的 message_start 门槛是两者有一个
// 非空（openaicc/decode.go），而不发 id 的第三方中转是存在的。不补就会输出 `"id": ""`，
// 而 id 在 Anthropic 响应里是必填字段。
//
// 前缀用 Anthropic 原生的 `msg_` 而不是照抄上游的 `chatcmpl-`：有上游 id 时一律原样
// 透传（口径层 v0.31），所以 `chatcmpl-` 出现即代表「上游给的」、`msg_` 出现即代表
// 「上游没给、网关补的」，排障时一眼能分。
//
// 用 crypto/rand 不是为了安全——这个 id 不承担任何鉴权语义——是因为它不需要处理
// 错误（Go 1.24 起 rand.Text 失败即 panic 在库内，调用侧没有失败分支要写）。
func fallbackMessageID() string { return "msg_" + rand.Text() }

// decodeToolInput 把拼好的入参解成对象。
//
// 非流式的 tool_use.input 必须是**对象**，不能是字符串——客户端拿它直接当参数用。
// 解不动时退回 {} 而不是报错：一个参数缺失的工具调用客户端还能报错给用户看，而整
// 条响应失败会让这一轮直接消失。
func decodeToolInput(args string) (any, error) {
	if strings.TrimSpace(args) == "" {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return map[string]any{}, nil
	}
	if _, ok := v.(map[string]any); !ok {
		return map[string]any{}, nil
	}
	return v, nil
}

// usageBody 按 Anthropic 的形态写 usage。
//
// input_tokens 要**减回净值**（`Usage.NetInput`，钳零理由记在那里）：canonical 的
// InputTokens 是毛值（含缓存两项，见 protocol.Usage），而 Anthropic 的契约是
// input_tokens 与两项缓存互不相交、客户端自己相加。不减等于把缓存重复计一遍。
//
// cache_creation_input_tokens / cache_read_input_tokens 恒写出来（哪怕是 0）：
// Claude Code 读这两个字段算缓存命中率，缺键与 0 在它那里不是一回事。CC 出口只有
// 缓存读没有缓存写，所以写入侧恒 0——这是协议差异，不是漏填。
func usageBody(u protocol.Usage) map[string]any {
	return map[string]any{
		"input_tokens":                u.NetInput(),
		"output_tokens":               u.OutputTokens,
		"cache_creation_input_tokens": u.CacheWriteTokens,
		"cache_read_input_tokens":     u.CacheReadTokens,
	}
}

// mapStopReason 把 canonical 取值映回 Anthropic（§5 坑清单）。
//
// **不允许空串**：Anthropic 非流式响应的 stop_reason 不接受 null/空，客户端按它
// 决定要不要继续下一轮。未知值一律 end_turn。
func mapStopReason(stop string) string {
	switch stop {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

// marshal 关掉 HTML 转义：工具描述与正文里 < > & 很常见，转义后语义不变但人工
// 排障与样本比对时满屏 < 没法看。
func marshal(v any) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("anthropic: 编码响应: %w", err)
	}
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}
