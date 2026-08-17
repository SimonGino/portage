package openaicc

import (
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Chat Completions 的**入口**编码：canonical 事件序列 → 下行响应。
//
// 线格式照 testdata/golden/cc-stream-* 三份真实上游 SSE 转录，不是照协议文档复述：
// 每帧只有 `data:` 一行（没有 `event:`），首帧是只带 role 的空 delta，正文逐字一帧，
// finish_reason 单独占一帧（delta 是 `{"content":""}`），usage 帧的 choices 是**空
// 数组**，最后 `data: [DONE]`。
//
// 与 Anthropic 那边最大的差别是**没有块边界纪律**：CC 的并行工具调用靠 index 区分，
// 允许交错，所以工具调用不必攒到 End 才放——Start/ArgsDelta 到一条发一条，客户端
// 看到的增量节奏与上游一致。

// Flusher 是 EncodeStream 每帧之后要调的钩子。理由同 anthropic 包：codec 是纯编码
// 层，测试里的 w 是 bytes.Buffer，没有也不该有 HTTP 概念。
type Flusher interface{ Flush() }

// EncodeStream 把事件流编成 Chat Completions SSE 下发。
func (c *Codec) EncodeStream(w io.Writer, events <-chan protocol.Event) error {
	enc := &streamEncoder{w: w, created: time.Now().Unix(), includeUsage: c.includeUsage}
	for ev := range events {
		if err := enc.event(ev); err != nil {
			return err
		}
	}
	return enc.finish()
}

type streamEncoder struct {
	w       io.Writer
	created int64

	started      bool
	id           string
	model        string
	stop         string
	usage        protocol.Usage
	sawUsage     bool
	includeUsage bool
	finished     bool
	// sawErrored：流内已经发过 error 帧，后面不再补 finish_reason / usage / [DONE]——
	// 那会让客户端以为响应正常收尾了。
	sawErrored bool

	// ccIndex 把上游的工具调用 index 重编成 CC 的 0..n-1。
	//
	// 必须重编：canonical 的 Index 原样携带上游序号，而 Anthropic 那边它是**内容块**
	// 下标（正文占了 0，第一个工具调用就是 1），CC 客户端拿 index 当 tool_calls 数组
	// 的下标用，跳号会在数组里留一个空洞。按首次出现次序编号，顺序与客户端看到的一致。
	ccIndex map[int]int
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
		return e.chunk(map[string]any{"content": ev.Text}, nil)

	case protocol.EvThinkingDelta:
		// 推理内容写 delta.reasoning_content（口径层 v0.62）。CC 协议里推理确实没有
		// 官方位置，但有事实标准：DeepSeek 起头的 reasoning_content，兼容客户端普遍
		// 认它，而且它与 content 分开，客户端分得出来——不是「塞进正文」。
		//
		// 丢弃判定与另两个出口共用一处（protocol.OutboundThinkingText）：signature
		// 通道与空文本都不写。
		text, ok := protocol.OutboundThinkingText(ev)
		if !ok {
			return nil
		}
		if err := e.ensureStarted(); err != nil {
			return err
		}
		return e.chunk(map[string]any{"reasoning_content": text}, nil)

	case protocol.EvToolCallStart:
		if err := e.ensureStarted(); err != nil {
			return err
		}
		return e.chunk(map[string]any{"tool_calls": []map[string]any{{
			"index": e.indexOf(ev.Index),
			"id":    ev.ToolID,
			"type":  "function",
			// arguments 给空串而不是省略：实采的首帧就是这个形状，客户端按它初始化
			// 那个 tool_calls 槽位。
			"function": map[string]any{"name": ev.ToolName, "arguments": ""},
		}}}, nil)

	case protocol.EvToolArgsDelta:
		if ev.Text == "" {
			return nil
		}
		if err := e.ensureStarted(); err != nil {
			return err
		}
		return e.chunk(map[string]any{"tool_calls": []map[string]any{{
			"index":    e.indexOf(ev.Index),
			"function": map[string]any{"arguments": ev.Text},
		}}}, nil)

	case protocol.EvToolCallEnd:
		// CC 没有「这个调用说完了」的信号，收尾靠整条流的 finish_reason。无事可做。
		return nil

	case protocol.EvUsage:
		if ev.Usage != nil {
			// 累计快照语义：后来者的非零字段覆盖，不做加法（protocol/event.go）。
			e.usage.MergeSnapshot(*ev.Usage)
			e.sawUsage = true
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

// indexOf 给上游 index 分配一个 CC 侧的连号下标（见 streamEncoder.ccIndex）。
func (e *streamEncoder) indexOf(upstream int) int {
	if e.ccIndex == nil {
		e.ccIndex = map[int]int{}
	}
	if idx, ok := e.ccIndex[upstream]; ok {
		return idx
	}
	idx := len(e.ccIndex)
	e.ccIndex[upstream] = idx
	return idx
}

// ensureStarted 发首帧：只带 role 的空 delta（实采如此）。
//
// 客户端靠它拿到 id 与 model 并把这一轮的 assistant 消息立起来，正文还没来。
func (e *streamEncoder) ensureStarted() error {
	if e.started {
		return nil
	}
	e.started = true
	if e.id == "" {
		e.id = fallbackChatID()
	}
	return e.chunk(map[string]any{"role": "assistant"}, nil)
}

func (e *streamEncoder) finish() error {
	if e.finished || e.sawErrored {
		return nil
	}
	e.finished = true
	if err := e.ensureStarted(); err != nil {
		return err
	}
	// finish_reason 帧的 delta 是 `{"content":""}` 而不是空对象：实采两份转录都如此，
	// 且 OpenAI SDK 按「delta 一定在」解析。
	stop := ccFinishReason(e.stop)
	if err := e.chunk(map[string]any{"content": ""}, &stop); err != nil {
		return err
	}
	// usage 帧只在客户端用 stream_options.include_usage 要过才发（见
	// decode_request.go 的 wantsUsage）。上游一帧 usage 都没给时也不凭空造零值——
	// 那会让客户端把「上游没说」记成「用了 0 个 token」。
	if e.includeUsage && e.sawUsage {
		if err := e.frame(map[string]any{
			"id":      e.id,
			"object":  "chat.completion.chunk",
			"created": e.created,
			"model":   e.model,
			"choices": []any{},
			"usage":   usageBody(e.usage),
		}); err != nil {
			return err
		}
	}
	_, err := io.WriteString(e.w, "data: [DONE]\n\n")
	e.flush()
	return err
}

// writeError 把流内错误按 CC 的形态发下去。
//
// 只在**已经写出首字节之后**才走得到这里——此前的错误由 EncodeError 走 HTTP 状态码
// 那条路。首字节一出格式承诺已生效，改不了状态码，只能在流里说。
//
// 发完不补 [DONE]：那是「正常收完」的记号，客户端见到它就会把这一轮当成功的。
func (e *streamEncoder) writeError(ev protocol.Event) error {
	e.sawErrored = true
	msg := ev.Message
	if msg == "" {
		msg = "上游响应流异常中断"
	}
	return e.frame(map[string]any{"error": map[string]any{
		"message": msg,
		"type":    "api_error",
	}})
}

// chunk 发一帧标准的 chat.completion.chunk。finish 为 nil 时 finish_reason 是 null。
func (e *streamEncoder) chunk(delta map[string]any, finish *string) error {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != nil {
		choice["finish_reason"] = *finish
	}
	return e.frame(map[string]any{
		"id":      e.id,
		"object":  "chat.completion.chunk",
		"created": e.created,
		"model":   e.model,
		"choices": []any{choice},
	})
}

func (e *streamEncoder) frame(payload any) error {
	data, err := marshal(payload)
	if err != nil {
		return err
	}
	var buf strings.Builder
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	if _, err := io.WriteString(e.w, buf.String()); err != nil {
		return err
	}
	// 每帧一 flush：攒着发等于把逐字输出重新变成一次性吐出。
	e.flush()
	return nil
}

func (e *streamEncoder) flush() {
	if f, ok := e.w.(Flusher); ok {
		f.Flush()
	}
}

// EncodeFullBody 把完整事件序列聚合成非流式 Chat Completions 响应体。
func (c *Codec) EncodeFullBody(events []protocol.Event) ([]byte, error) {
	var (
		id, model string
		stop      string
		usage     protocol.Usage
		text      strings.Builder
		thinking  strings.Builder
		calls     = map[int]*toolAccum{}
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
		case protocol.EvThinkingDelta:
			// 与流式共用同一处丢弃判定（signature 通道与空文本都不写）。
			if t, ok := protocol.OutboundThinkingText(ev); ok {
				thinking.WriteString(t)
			}
		case protocol.EvToolCallStart:
			if _, ok := calls[ev.Index]; !ok {
				order = append(order, ev.Index)
			}
			calls[ev.Index] = &toolAccum{id: ev.ToolID, name: ev.ToolName}
		case protocol.EvToolArgsDelta:
			buf, ok := calls[ev.Index]
			if !ok {
				buf = &toolAccum{}
				calls[ev.Index] = buf
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
		return nil, fmt.Errorf("openaicc: 上游响应错误: %s", errEvent.Message)
	}

	message := map[string]any{"role": "assistant"}
	// content 恒写出来（哪怕是空串）：OpenAI SDK 读 message.content 时缺键与空串不是
	// 一回事，纯工具调用轮实采里给的就是空串。tool_calls 反过来，没有就整键省略。
	message["content"] = text.String()
	// reasoning_content 反过来「有数才写」（同 usageBody 对 completion_tokens_details
	// 的处置）：它不是官方字段，凭空写个空串等于告诉客户端「这轮想过但想了个空」。
	if s := thinking.String(); s != "" {
		message["reasoning_content"] = s
	}
	if len(order) > 0 {
		list := make([]map[string]any, 0, len(order))
		for _, idx := range order {
			buf := calls[idx]
			args := strings.Join(buf.args, "")
			if strings.TrimSpace(args) == "" {
				// arguments 必须能解成 JSON，客户端拿它直接当参数用。上游一片没发时
				// 补 "{}"，否则客户端解一个空串会直接抛。
				args = "{}"
			}
			list = append(list, map[string]any{
				"id":       buf.id,
				"type":     "function",
				"function": map[string]any{"name": buf.name, "arguments": args},
			})
		}
		message["tool_calls"] = list
	}

	if id == "" {
		id = fallbackChatID()
	}
	return marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": ccFinishReason(stop),
		}},
		// 非流式恒带 usage，与 include_usage 无关：那个开关只管流式的额外帧，
		// 非流式响应里 usage 是 CC 契约的固定成员。
		"usage": usageBody(usage),
	})
}

// toolAccum 攒一次工具调用的分片（非流式聚合用）。
type toolAccum struct {
	id   string
	name string
	args []string
}

// usageBody 按 CC 的形态写 usage。
//
// prompt_tokens 直映 canonical 的 InputTokens：两边同为**毛值**（含缓存命中，见
// protocol.Usage），cached_tokens 是它的明细而非另一笔。
//
// total_tokens 由我们相加而不是等上游给：canonical 没有这个字段（它是冗余的），
// 而 CC 客户端普遍读它。prompt_tokens_details.cached_tokens 恒写出来（哪怕是 0），
// 理由同 anthropic 的 cache_read_input_tokens——缺键与 0 在读它算命中率的客户端
// 那里不是一回事。CC 没有缓存写入的概念，CacheWriteTokens 在这一侧无处可去。
// completion_tokens_details 与上面那句相反——**有数才写**（口径层 v0.66）。
// cached_tokens 的 0 是真的「一次都没命中」，而这里的 0 会被读成「这次没思考」，
// 可上游多半是根本不报这个数（Anthropic 一路没有这一格）。宁可不说，不能瞎说；
// 那笔成本的可见性由流水那一列兜底。它不从 completion_tokens 里减：是明细不是加数。
func usageBody(u protocol.Usage) map[string]any {
	body := map[string]any{
		"prompt_tokens":         u.InputTokens,
		"completion_tokens":     u.OutputTokens,
		"total_tokens":          u.InputTokens + u.OutputTokens,
		"prompt_tokens_details": map[string]any{"cached_tokens": u.CacheReadTokens},
	}
	if u.ReasoningTokens != 0 {
		body["completion_tokens_details"] = map[string]any{"reasoning_tokens": u.ReasoningTokens}
	}
	return body
}

// ccFinishReason 把 canonical 取值映回 CC。
//
// **不允许空串**：finish_reason 是 CC 响应的必填字段，客户端按它决定要不要继续下
// 一轮（尤其 tool_calls）。未知值一律 stop——宁可少说一句，也不把没见过的词捅给客户端。
func ccFinishReason(stop string) string {
	switch stop {
	case "tool_calls":
		return "tool_calls"
	case "length":
		return "length"
	case "content_filter":
		return "content_filter"
	default:
		return "stop"
	}
}

// fallbackChatID 在上游一个 id 都没给时补一个。
//
// 前缀用 CC 原生的 `chatcmpl-` 而不是照抄上游的 `msg_`：有上游 id 时一律原样透传
// （口径层 v0.31），所以 `msg_` 出现即代表「上游给的」、`chatcmpl-` 出现即代表
// 「上游没给、网关补的」，排障时一眼能分。
func fallbackChatID() string { return "chatcmpl-" + rand.Text() }
