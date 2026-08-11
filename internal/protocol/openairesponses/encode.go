package openairesponses

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 OpenAI Responses 的**编码**侧：canonical 事件序列 → 下行响应。
//
// 线格式照 testdata/golden/raw/resp-{text,tool,parallel} 三份**真实上游 SSE 转录**
// （Codex CLI 0.144 实跑），不是照 sub2api 复述的。M2-1 时 protocol/event.go 记了
// 一笔「Responses 侧没有真实上游转录，事件名以 sub2api 为准，M2 拿到真实上游流后
// 须复核」——这次复核了，结论列在这里：
//
//   - 帧序：response.created → response.in_progress → (每个 output item 一组
//     output_item.added … output_item.done) → response.completed。
//   - 正文 item 比工具 item 多一层 content_part：added → output_text.delta* →
//     output_text.done → content_part.done。工具 item **没有** content_part。
//   - 每一帧的 data 里都带 sequence_number，从 0 起全流连号（实测 102 帧无一例外）。
//   - 流**不发 `data: [DONE]`**——那是 Chat Completions 的收尾，Responses 以
//     response.completed 为终点。三份转录的最后一个字节就是它。
//   - usage 只在终帧的 response.usage 里，input_tokens_details.cached_tokens 记缓存命中。
//
// 与 sub2api 的差异：无，事件名对得上。event.go 那条待复核注释据此销账。

// Flusher 是 EncodeStream 每帧之后要调的钩子。理由同 anthropic 包：codec 是纯编码
// 层，测试里的 w 是 bytes.Buffer，不该有 HTTP 概念。
type Flusher interface{ Flush() }

// EncodeStream 把事件流编成 Responses SSE 下发。
func (c *Codec) EncodeStream(w io.Writer, events <-chan protocol.Event) error {
	enc := &streamEncoder{w: w, customTools: c.customTools}
	for ev := range events {
		if err := enc.event(ev); err != nil {
			return err
		}
	}
	return enc.finish()
}

type streamEncoder struct {
	w io.Writer

	// customTools 来自同一个 codec 实例的 DecodeRequest（见 codec.go 的实例约定）。
	customTools map[string]bool

	seq         int
	started     bool
	id          string
	model       string
	outputIndex int

	textOpen bool
	textItem string
	textBuf  strings.Builder

	// pending 是正在攒的工具调用。攒而不是逐片转发，理由见 flushTool。
	pending *toolPending
	// done 是已经收口的 output item，终帧的 response.output 要照原样列一遍。
	done []any

	usage      protocol.Usage
	stop       string
	finished   bool
	sawErrored bool
}

type toolPending struct {
	index int
	id    string
	name  string
	args  []string
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
			if err := e.openText(); err != nil {
				return err
			}
		}
		e.textBuf.WriteString(ev.Text)
		return e.frame("response.output_text.delta", map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       e.textItem,
			"output_index":  e.outputIndex,
			"content_index": 0,
			"delta":         ev.Text,
			"logprobs":      []any{},
		})

	case protocol.EvThinkingDelta:
		// 丢弃——而且 #25（R→A）之后这条**真的会被走到**：Anthropic 解码侧产
		// thinking_delta 与 signature_delta（五份真实转录实测）。写这条注释时它还是
		// 死路（CC 解码侧不产推理事件），现在不是了。
		//
		// 仍然丢，理由没变：Responses 确实有 response.reasoning_summary_text.delta
		// 可以承接，但要写它得先有真实转录来钉 reasoning item 的生命周期——手上三份
		// Responses 转录里的 reasoning item 只有 encrypted_content，一条 delta 都
		// 没有。照文档猜着写一个会在流里凭空造 item 的分支，比明着丢更危险。
		//
		// 代价是 Codex 在 R→A 路径上看不到 Claude 的推理过程（§9.2 缺口）。signature
		// 尤其不能漏进正文：那是一串 base64，漏了客户端会把它当回答渲染出来。
		return nil

	case protocol.EvToolCallStart, protocol.EvToolArgsDelta:
		return e.bufferTool(ev)

	case protocol.EvToolCallEnd:
		return e.flushTool(ev.Index)

	case protocol.EvUsage:
		if ev.Usage != nil {
			// 累计快照语义：后来者覆盖，不做加法（protocol/event.go）。
			e.usage = *ev.Usage
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

func (e *streamEncoder) bufferTool(ev protocol.Event) error {
	if e.pending == nil || e.pending.index != ev.Index {
		if ev.Type != protocol.EvToolCallStart {
			// 没见过 Start 的 index 冒出来了：补空壳而不是丢弃，同 anthropic 侧的
			// 理由——半个调用也比凭空消失强。
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

// flushTool 把攒好的工具调用整组写出去。
//
// 为什么必须攒满再发：custom 工具的入参要**对称解包**——CC 出口把非 JSON 入参包成
// 了 `{"input":"<原文>"}`（openaicc/encode.go 的 argsWrapKey），这里得原样拆回来，
// 而 JSON 字符串的转义没法按分片增量解。代价是丢掉了上游的分片节奏，但这条路上
// 本来就没有节奏可丢：CC 流里没有逐条工具终止符，解码侧已经把分片攒到流末尾一次性
// 冲出（§5 坑清单）。
func (e *streamEncoder) flushTool(index int) error {
	if e.pending == nil || e.pending.index != index {
		return nil
	}
	pending := e.pending
	e.pending = nil

	if err := e.ensureStarted(); err != nil {
		return err
	}
	// 正文 item 先收口：Responses 的 output 是**有序 item 列表**，一个 item 没
	// done 就开下一个，output_index 会对不上。
	if err := e.closeText(); err != nil {
		return err
	}

	args := strings.Join(pending.args, "")
	custom := e.customTools[pending.name]

	itemID, itemType, argsField := "fc_"+rand.Text(), "function_call", "arguments"
	deltaEvent, doneEvent := "response.function_call_arguments.delta", "response.function_call_arguments.done"
	if custom {
		args = protocol.UnwrapCustomToolArgs(args)
		itemID, itemType, argsField = "ctc_"+rand.Text(), "custom_tool_call", "input"
		deltaEvent, doneEvent = "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done"
	} else if args == "" {
		// function 形态的 arguments 按契约是 JSON 字符串，空串客户端解不动。
		args = "{}"
	}

	item := map[string]any{
		"id": itemID, "type": itemType, "status": "in_progress",
		"call_id": pending.id, "name": pending.name, argsField: "",
	}
	if err := e.frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": e.outputIndex, "item": item,
	}); err != nil {
		return err
	}
	if args != "" {
		if err := e.frame(deltaEvent, map[string]any{
			"type": deltaEvent, "item_id": itemID, "output_index": e.outputIndex, "delta": args,
		}); err != nil {
			return err
		}
	}
	if err := e.frame(doneEvent, map[string]any{
		"type": doneEvent, "item_id": itemID, "output_index": e.outputIndex,
		"call_id": pending.id, "name": pending.name, argsField: args,
	}); err != nil {
		return err
	}

	final := map[string]any{
		"id": itemID, "type": itemType, "status": "completed",
		"call_id": pending.id, "name": pending.name, argsField: args,
	}
	if err := e.frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": e.outputIndex, "item": final,
	}); err != nil {
		return err
	}
	e.done = append(e.done, final)
	e.outputIndex++
	return nil
}

func (e *streamEncoder) openText() error {
	e.textItem = "msg_" + rand.Text()
	e.textBuf.Reset()
	if err := e.frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": e.outputIndex,
		"item": map[string]any{
			"id": e.textItem, "type": "message", "status": "in_progress",
			"role": "assistant", "content": []any{},
		},
	}); err != nil {
		return err
	}
	if err := e.frame("response.content_part.added", map[string]any{
		"type": "response.content_part.added", "item_id": e.textItem,
		"output_index": e.outputIndex, "content_index": 0,
		"part": textPart(""),
	}); err != nil {
		return err
	}
	e.textOpen = true
	return nil
}

func (e *streamEncoder) closeText() error {
	if !e.textOpen {
		return nil
	}
	e.textOpen = false
	text := e.textBuf.String()

	if err := e.frame("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "item_id": e.textItem,
		"output_index": e.outputIndex, "content_index": 0,
		"text": text, "logprobs": []any{},
	}); err != nil {
		return err
	}
	if err := e.frame("response.content_part.done", map[string]any{
		"type": "response.content_part.done", "item_id": e.textItem,
		"output_index": e.outputIndex, "content_index": 0,
		"part": textPart(text),
	}); err != nil {
		return err
	}
	item := map[string]any{
		"id": e.textItem, "type": "message", "status": "completed",
		"role": "assistant", "content": []any{textPart(text)},
	}
	if err := e.frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": e.outputIndex, "item": item,
	}); err != nil {
		return err
	}
	e.done = append(e.done, item)
	e.outputIndex++
	return nil
}

func (e *streamEncoder) ensureStarted() error {
	if e.started {
		return nil
	}
	e.started = true
	if e.id == "" {
		e.id = fallbackResponseID()
	}
	// created 与 in_progress 载的是同一个 response 对象（实采转录里两帧逐字节相同，
	// 只差 sequence_number）。两帧都发是因为 Codex 按 in_progress 判定「上游真的开工
	// 了」，只发 created 会让它一直等。
	body := e.responseBody("in_progress", nil, false)
	if err := e.frame("response.created", map[string]any{
		"type": "response.created", "response": body,
	}); err != nil {
		return err
	}
	return e.frame("response.in_progress", map[string]any{
		"type": "response.in_progress", "response": body,
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
	// 上游流断在半截（没等到 EvToolCallEnd）时，攒着的那个调用照样放出去。
	if e.pending != nil {
		if err := e.flushTool(e.pending.index); err != nil {
			return err
		}
	}
	if err := e.closeText(); err != nil {
		return err
	}

	status, event := "completed", "response.completed"
	if e.stop == "length" {
		// 截断有独立的终帧与状态：客户端据此决定要不要续写。混在 completed 里发，
		// 被截断的回答看上去就是「模型说完了」。
		status, event = "incomplete", "response.incomplete"
	}
	return e.frame(event, map[string]any{
		"type": event, "response": e.responseBody(status, e.done, true),
	})
}

// responseBody 造 response 对象。created / in_progress / completed 三帧共用一个形状，
// 差别只在 status、output 与 usage。
func (e *streamEncoder) responseBody(status string, output []any, withUsage bool) map[string]any {
	if output == nil {
		output = []any{}
	}
	body := map[string]any{
		"id":                   e.id,
		"object":               "response",
		"created_at":           time.Now().Unix(),
		"status":               status,
		"model":                e.model,
		"output":               output,
		"error":                nil,
		"incomplete_details":   nil,
		"instructions":         nil,
		"previous_response_id": nil,
		"metadata":             map[string]any{},
		"parallel_tool_calls":  false,
		"tool_choice":          "auto",
		"tools":                []any{},
	}
	if status == "incomplete" {
		body["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	if withUsage {
		body["usage"] = usageBody(e.usage)
	}
	return body
}

// writeError 把流内错误按 Responses 的 response.failed 发下去。
//
// 只在首字节写出之后才走得到这里——此前的错误由 EncodeError 走 HTTP 状态码那条路。
func (e *streamEncoder) writeError(ev protocol.Event) error {
	e.sawErrored = true
	msg := ev.Message
	if msg == "" {
		msg = "上游响应流异常中断"
	}
	if err := e.ensureStarted(); err != nil {
		return err
	}
	body := e.responseBody("failed", e.done, true)
	body["error"] = map[string]any{"code": "server_error", "message": msg}
	return e.frame("response.failed", map[string]any{
		"type": "response.failed", "response": body,
	})
}

func (e *streamEncoder) frame(event string, payload map[string]any) error {
	// sequence_number 全流连号，实采每帧都有。客户端拿它判丢帧，缺了或跳号等于告诉
	// 对方「这条流不完整」。
	payload["sequence_number"] = e.seq
	e.seq++

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
	// 每帧一 flush：攒着发等于把逐字输出重新变成一次性吐出。
	if f, ok := e.w.(Flusher); ok {
		f.Flush()
	}
	return nil
}

// EncodeFullBody 把完整事件序列聚合成非流式 Responses 响应体。
//
// 复用流式那台状态机：把事件喂给同一个 encoder，写到一个丢弃 writer 上，只为让它
// 把 done 列表和 usage 攒齐。两套聚合逻辑各写一遍必然漂移——非流式路径的样本远比
// 流式少，漂了也不容易发现。
func (c *Codec) EncodeFullBody(events []protocol.Event) ([]byte, error) {
	enc := &streamEncoder{w: io.Discard, customTools: c.customTools}
	for _, ev := range events {
		if ev.Type == protocol.EvError {
			return nil, fmt.Errorf("openairesponses: 上游响应错误: %s", ev.Message)
		}
		if err := enc.event(ev); err != nil {
			return nil, err
		}
	}
	if err := enc.finish(); err != nil {
		return nil, err
	}
	status := "completed"
	if enc.stop == "length" {
		status = "incomplete"
	}
	return marshal(enc.responseBody(status, enc.done, true))
}

// usageBody 按 Responses 的 usage 形状写计数。
//
// 不在这里归一各协议的 token 语义（protocol.Usage 的约定）：CC 的 prompt_tokens 含
// 缓存命中，照搬进 input_tokens 即可，cached_tokens 是它的**明细**而非另一笔。
func usageBody(u protocol.Usage) map[string]any {
	return map[string]any{
		"input_tokens": u.InputTokens,
		"input_tokens_details": map[string]any{
			"cached_tokens":      u.CacheReadTokens,
			"cache_write_tokens": u.CacheWriteTokens,
		},
		"output_tokens":         u.OutputTokens,
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
		"total_tokens":          u.InputTokens + u.OutputTokens,
	}
}

func textPart(text string) map[string]any {
	return map[string]any{
		"type": "output_text", "text": text,
		"annotations": []any{}, "logprobs": []any{},
	}
}

// fallbackResponseID 在上游一个 id 都没给时补一个。
//
// 与 anthropic 侧同规矩（口径层 v0.31 + 展开层 §7.4）：有上游 id 一律原样透传，所以
// `chatcmpl-` 出现即代表「上游给的」、`resp_` 出现即代表「网关补的」，排障一眼能分。
func fallbackResponseID() string { return "resp_" + rand.Text() }

// marshal 关掉 HTML 转义：工具入参里 `<` `>` `&` 很常见（exec 收的是 JS 源码），
// 转义后语义不变但人工排障与样本比对时满屏 < 没法看。
func marshal(v any) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("openairesponses: 编码响应: %w", err)
	}
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}
