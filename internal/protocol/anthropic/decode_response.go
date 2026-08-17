package anthropic

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Anthropic Messages 的**出口**解码：上游响应 → canonical 事件序列。
//
// 形状照 testdata/golden/raw/anthropic-* 五份**真实上游 SSE 转录**（Claude Code 2.x
// 实跑）定的，不是照协议文档。实测出来的几件事：
//
//   - 帧序：message_start → (每个内容块一组 content_block_start → *_delta* →
//     content_block_stop) → message_delta → message_stop。
//   - ping 帧随时可能插进来（五份里每份都有一个），忽略。
//   - data 负载尾部带**空格填充**（上游的抗缓冲手段），JSON 解析不受影响，但别拿
//     字节做相等比较。
//   - usage 出现两次且语义不同：message_start 给 input / cache 计数与一个几乎没用
//     的 output_tokens=1，message_delta 给最终 output_tokens。canonical 的
//     EvUsage 是累计快照（protocol/event.go），照发两次即可，消费方按**非零字段**
//     覆盖（Usage.MergeSnapshot）——五份转录里两帧都带全套 usage，但只报
//     output_tokens 的兼容上游存在，整体覆盖会把 input 清零。
//   - thinking 块的正文可能整段为空、只有 signature（anthropic-tool-turn1 实测）。
//   - 工具入参走 input_json_delta 分片，**恒是 JSON**，故 ArgsIsJSON 为 true。
//
// 没有实测覆盖的有两处，用例都是手写的——§9 缺口清单里记着：①`error` 帧，五份转录里
// 一次都没出现（都是 200 正常流），只能照协议文档实现；②**只带 output_tokens 的
// message_delta**，五份转录的两帧都是完整快照，这一形态来自「兼容上游可能这么发」的
// 推断（#72），用例钉的是「解出 InputTokens=0 而非凭空造数」这半边行为。

// DecodeStream 把上游 SSE 解成 canonical 事件流。
func (c *Codec) DecodeStream(r io.Reader) (<-chan protocol.Event, error) {
	out := make(chan protocol.Event, 32)
	go func() {
		defer close(out)
		st := &respState{}
		scanner := &protocol.FrameScanner{}
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				scanner.Push(buf[:n], func(frame []byte) { st.frame(frame, out) })
			}
			if err != nil {
				scanner.Flush(func(frame []byte) { st.frame(frame, out) })
				if err != io.EOF {
					// 记一笔给调用方：这条流是**断的**不是说完的。带内的 EvError
					// 到了编码侧就与「上游回了个错误对象」混成一样，收场判不出来
					// （protocol.StreamReadReporter）。
					c.SetStreamReadError(err)
					out <- protocol.Event{
						Type:    protocol.EvError,
						Message: "读取上游响应流失败: " + err.Error(),
					}
					return
				}
				break
			}
		}
		st.finish(out)
	}()
	return out, nil
}

// DecodeFullBody 把非流式响应体解成完整事件序列。
//
// 非流式响应就是「一帧到底」的流（canonical 对非流式的定义，protocol/event.go），
// 所以这里把整包 message 摊成与流式同形的事件，走同一个状态机收尾——两条路径只有
// 一处解析，不会漂。
//
// **五份转录全是流式**（Claude Code 恒发 stream:true），所以这条路上没有真实样本
// 背书，形状是照协议文档 + 流式那半边的对称性写的。同一类缺口 R→CC 那边也有，记在
// §9。
func (c *Codec) DecodeFullBody(body []byte) ([]protocol.Event, error) {
	var payload struct {
		ID         string            `json:"id"`
		Model      string            `json:"model"`
		StopReason string            `json:"stop_reason"`
		Content    []json.RawMessage `json:"content"`
		Usage      *usagePayload     `json:"usage"`
		Type       string            `json:"type"`
		Error      *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("anthropic: 上游响应不是 JSON: %w", err)
	}

	if payload.Type == "error" && payload.Error != nil {
		return []protocol.Event{{
			Type:    protocol.EvError,
			Message: payload.Error.Message,
		}}, nil
	}

	events := []protocol.Event{{
		Type: protocol.EvMessageStart, ID: payload.ID, Model: payload.Model,
	}}
	if u := payload.Usage.canonical(); u != nil {
		events = append(events, protocol.Event{Type: protocol.EvUsage, Usage: u})
	}

	for i, raw := range payload.Content {
		var block struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		}
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		switch block.Type {
		case "text":
			if block.Text != "" {
				events = append(events, protocol.Event{Type: protocol.EvTextDelta, Text: block.Text})
			}
		case "thinking":
			if block.Thinking != "" {
				events = append(events, protocol.Event{
					Type: protocol.EvThinkingDelta, Text: block.Thinking, Channel: protocol.ThinkingBody,
				})
			}
			if block.Signature != "" {
				events = append(events, protocol.Event{
					Type: protocol.EvThinkingDelta, Text: block.Signature, Channel: protocol.ThinkingSignature,
				})
			}
		case "tool_use":
			events = append(events,
				protocol.Event{
					Type: protocol.EvToolCallStart, Index: i,
					ToolID: block.ID, ToolName: block.Name, ArgsIsJSON: true,
				},
				protocol.Event{Type: protocol.EvToolArgsDelta, Index: i, Text: string(block.Input)},
				protocol.Event{Type: protocol.EvToolCallEnd, Index: i},
			)
		}
	}

	// Truncated 与流式那半边同判据（emitDone）：非流式的 stop_reason 缺失不是「流断
	// 了」——包解得开——而是「上游没声明这轮是怎么收的」，对压缩合成是同一个失格理由
	// （openairesponses 的 compactionNoItem）。这一位不置，一份没声明收尾的响应会被
	// 当成完整摘要装回 Codex 的历史。
	//
	// 这里没走 emitDone 是因为非流式不经 respState（没有 channel、没有跨帧状态），
	// 但两处的 EvDone 必须同形——上面那句「两条路径只有一处解析」管的是内容块，收尾
	// 这一处是手搓的，加字段时两边都要改。
	return append(events, protocol.Event{
		Type:       protocol.EvDone,
		StopReason: canonicalStopReason(payload.StopReason),
		Truncated:  payload.StopReason == "",
	}), nil
}

// usagePayload 是 message_start 与 message_delta 共用的 usage 形态。
type usagePayload struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// canonical 把 usage 转成 canonical 形态；全零或缺失时给 nil（没什么可报的）。
//
// 归一在这一侧做（protocol.Usage 的约定）：Anthropic 的 input_tokens **不含**缓存，
// canonical 的 InputTokens 是毛值，所以把缓存两项加回去；明细仍照原样留着。加法按帧
// 做而不是跨帧累加——message_start 与 message_delta 各是一份完整快照，实采两帧都带
// 全套缓存字段；只带 output_tokens 的兼容上游那一帧解出 InputTokens=0，靠消费方的
// MergeSnapshot（非零字段覆盖）保住先前的毛值。
//
// 明知的残留窄口：某个兼容上游若在 message_delta 里给了净 input_tokens **却省掉**
// 缓存两项，这里算出的毛值偏小且非零，会盖掉 message_start 那份对的（而缓存明细靠
// 非零合并留着，于是 A 出口再减一次、多半钳到 0）。不为它加
// 「只在缓存两项都在时才覆盖」的启发式——那要拿一条猜出来的规则去改所有正常上游的
// 路径，而真实转录（testdata/golden/raw/anthropic-stream-*）两帧都是完整快照。
func (u *usagePayload) canonical() *protocol.Usage {
	if u == nil {
		return nil
	}
	out := protocol.Usage{
		InputTokens:      u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
	if out == (protocol.Usage{}) {
		return nil
	}
	return &out
}

// respState 是流式解码的状态。
//
// 要记的只有两样：每个 content block index 是什么类型（content_block_stop 只给
// index，得靠它判断该不该发 EvToolCallEnd），以及 stop_reason（它在 message_delta
// 里给，而 EvDone 要等 message_stop 才发）。
type respState struct {
	blockKind map[int]string
	stop      string
	done      bool
}

func (st *respState) frame(frame []byte, out chan<- protocol.Event) {
	_, data := protocol.SSEFields(frame)
	if len(data) == 0 {
		return
	}
	var ev struct {
		Type  string `json:"type"`
		Index int    `json:"index"`

		Message *struct {
			ID    string        `json:"id"`
			Model string        `json:"model"`
			Usage *usagePayload `json:"usage"`
		} `json:"message"`

		ContentBlock *struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`

		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`

		Usage *usagePayload `json:"usage"`

		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &ev) != nil {
		// 解不动的帧跳过而不是报错：ping 的负载、上游的自定义事件、注释心跳都可能
		// 长得不像我们认得的对象。一帧解不动不该打死整条流。
		return
	}

	switch ev.Type {
	case "message_start":
		if ev.Message == nil {
			return
		}
		out <- protocol.Event{
			Type: protocol.EvMessageStart, ID: ev.Message.ID, Model: ev.Message.Model,
		}
		if u := ev.Message.Usage.canonical(); u != nil {
			out <- protocol.Event{Type: protocol.EvUsage, Usage: u}
		}

	case "content_block_start":
		if ev.ContentBlock == nil {
			return
		}
		if st.blockKind == nil {
			st.blockKind = map[int]string{}
		}
		st.blockKind[ev.Index] = ev.ContentBlock.Type
		if ev.ContentBlock.Type == "tool_use" {
			out <- protocol.Event{
				Type: protocol.EvToolCallStart, Index: ev.Index,
				ToolID: ev.ContentBlock.ID, ToolName: ev.ContentBlock.Name,
				// Anthropic 的 tool_use.input 恒是 JSON 对象，分片拼起来也是。
				ArgsIsJSON: true,
			}
		}

	case "content_block_delta":
		if ev.Delta == nil {
			return
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				out <- protocol.Event{Type: protocol.EvTextDelta, Text: ev.Delta.Text}
			}
		case "thinking_delta":
			// 空 thinking 也放行：实采里 thinking 正文整段为空、只有 signature
			// （anthropic-tool-turn1），但「有过一个推理块」这件事本身对下游有意义。
			out <- protocol.Event{
				Type: protocol.EvThinkingDelta, Text: ev.Delta.Thinking, Channel: protocol.ThinkingBody,
			}
		case "signature_delta":
			out <- protocol.Event{
				Type: protocol.EvThinkingDelta, Text: ev.Delta.Signature, Channel: protocol.ThinkingSignature,
			}
		case "input_json_delta":
			out <- protocol.Event{
				Type: protocol.EvToolArgsDelta, Index: ev.Index, Text: ev.Delta.PartialJSON,
			}
		}

	case "content_block_stop":
		// 只有工具块要收口。正文与 thinking 的块边界在 canonical 里是**故意丢掉**的
		// （protocol/event.go 的 EvTextDelta 注释）。
		if st.blockKind[ev.Index] == "tool_use" {
			out <- protocol.Event{Type: protocol.EvToolCallEnd, Index: ev.Index}
		}

	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			st.stop = ev.Delta.StopReason
		}
		if u := ev.Usage.canonical(); u != nil {
			out <- protocol.Event{Type: protocol.EvUsage, Usage: u}
		}

	case "message_stop":
		st.emitDone(out)

	case "error":
		msg := "上游返回错误"
		if ev.Error != nil && ev.Error.Message != "" {
			msg = ev.Error.Message
		}
		out <- protocol.Event{Type: protocol.EvError, Message: msg}
		// 错误帧之后不该再发 EvDone：客户端已经知道这轮废了，再补一个正常收尾
		// 会让它以为还有救。
		st.done = true
	}
}

// finish 兜底收尾：上游流断在 message_stop 之前时，仍要给下游一个终点。
//
// 不补就是把「流没了」变成「流永远不结束」——编码侧在等 EvDone，客户端在等
// response.completed，两边一起挂着。
func (st *respState) finish(out chan<- protocol.Event) { st.emitDone(out) }

func (st *respState) emitDone(out chan<- protocol.Event) {
	if st.done {
		return
	}
	st.done = true
	// st.stop 空 = message_delta 里那个 stop_reason 一次都没到，这个收尾纯是上面
	// finish 兜出来的。StopReason 照旧兜成 stop（下游要一个合法取值），另开
	// Truncated 把「上游没说话就断了」这件事带下去。
	out <- protocol.Event{
		Type:       protocol.EvDone,
		StopReason: canonicalStopReason(st.stop),
		Truncated:  st.stop == "",
	}
}

// canonicalStopReason 把 Anthropic 的 stop_reason 映到 canonical 取值。
//
// 是 encode.go 里 mapStopReason 那条的反向。未知值一律 stop——宁可少说一句，也不把
// 上游的新词直接捅给客户端（同 openaicc 解码侧的口径）。
func canonicalStopReason(reason string) string {
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}
