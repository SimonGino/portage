package openairesponses

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Responses 的**解码**侧：上游响应 → canonical 事件序列（#80，喂 CC→R 与
// A→R 两条路径的回程）。
//
// 与 CC 解码侧的形状差别，都源于 Responses 的 output 是**有序 item 列表**而不是一条
// 扁平的 delta 流：
//
//   - 工具调用的起始信息只在 `response.output_item.added` 上。三份真实转录实测：
//     入参增量帧（custom_tool_call_input.delta / function_call_arguments.delta）只带
//     item_id，**没有 call_id 也没有 name**。所以 EvToolCallStart 必须由 added 帧发，
//     照 CC 那样等分片来了再补是发不出来的。
//   - 不需要像 CC 那样把分片攒到流末尾：item 严格顺序开合（实采里 message 与
//     custom_tool_call 各自 added…done 成对且不交错），Start/ArgsDelta/End 当场发。
//   - Index 直接用上游的 output_index，不重编号（与 canonical 的 Index 语义一致）。
//
// 事实来源：testdata/golden/responses-stream-* 九份真实上游转录。
//
// **reasoning 分两半处理**（口径层 v0.62 收敛，推翻此处原先的 v0.10 口径）：
//   - reasoning_summary_text.delta（以及 reasoning_text.delta）的文本产
//     EvThinkingDelta，出口那边物化成推理内容。摘要落到 A 出口的 thinking 块是
//     R→A 上摘要唯一的落点，§9.4 已明确**不算**「摘要冒充正文」——已经花掉的思考
//     成本不得静默吞没。
//   - encrypted_content 仍然一个字节都不产：那是上游侧密文，跨协议无人解得开，
//     写给客户端就是把密文当散文渲染（同 Anthropic 的 signature）。
//
// 思考量另由 usage.reasoning_tokens 带走，与文本两不相干。

// respFrame 是 SSE 帧的公共形态。Responses 的每种事件只用其中几个字段，合成一个
// struct 解是因为帧类型判别在 `type` 上，先解出来才知道该看哪几个字段。
type respFrame struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Input       string          `json:"input"`
	Arguments   string          `json:"arguments"`
	Item        *respItem       `json:"item"`
	Response    *respPayload    `json:"response"`
	Error       *respErrorField `json:"error"`
}

// respItem 是 output item。工具项的入参字段名随类型变：function_call 用 arguments，
// custom_tool_call 用 input（实采 meta 明确记过这一条）。
type respItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Input     string `json:"input"`
	Role      string `json:"role"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	// Summary 是 reasoning 项的摘要段落。非流式只有这一处能拿到推理文本（流式那边
	// 走 reasoning_summary_text.delta）。**不解 encrypted_content**：解出来也只能丢，
	// 不给它留字段就少一次误外带的机会。
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

// respPayload 是终帧里那个 response 对象。字段取舍与 tap.go 的 response 一致，
// 两边分别定义是因为 Tap 要的是「上游原样」（Has 标志区分没报与报 0），这里要的是
// canonical 归一后的值。
type respPayload struct {
	ID                string     `json:"id"`
	Model             string     `json:"model"`
	Status            string     `json:"status"`
	Output            []respItem `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *respErrorField `json:"error"`
	Usage *respUsage      `json:"usage"`
}

type respErrorField struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	// 兼容端点把缓存写入放在顶层这一项（同 tap.go 的注释，来源 sub2api
	// apicompat/types.go）；官方 OpenAI 发的是 input_tokens_details.cache_write_tokens。
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	OutputTokensDetails      *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// canonical 把上游 usage 归一成 canonical 口径：input_tokens 本就是**毛值**（含缓存
// 命中与写入），直映不加减；cached/cache_write 与 reasoning 是明细（口径层 v0.49 /
// v0.66）。流式与非流式共用这一份映射。
func (u *respUsage) canonical() protocol.Usage {
	out := protocol.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
	if d := u.InputTokensDetails; d != nil {
		out.CacheReadTokens, out.CacheWriteTokens = d.CachedTokens, d.CacheWriteTokens
	}
	if u.CacheCreationInputTokens != 0 {
		out.CacheWriteTokens = u.CacheCreationInputTokens
	}
	if d := u.OutputTokensDetails; d != nil {
		out.ReasoningTokens = d.ReasoningTokens
	}
	return out
}

// DecodeStream 把上游 Responses SSE 解成事件流。channel 由本函数负责关闭。
//
// 结构与 openaicc.DecodeStream 一致（读—喂 FrameScanner—收摊）：两条解码路径的
// 读取错误处置必须一样，各写一版必然漂。
func (c *Codec) DecodeStream(r io.Reader) (<-chan protocol.Event, error) {
	out := make(chan protocol.Event, 32)
	go func() {
		defer close(out)
		st := &respStreamState{}
		scanner := &protocol.FrameScanner{}
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				scanner.Push(buf[:n], func(frame []byte) {
					st.frame(frame, out)
				})
			}
			if err != nil {
				scanner.Flush(func(frame []byte) { st.frame(frame, out) })
				if err != io.EOF {
					// 记一笔给调用方：这条流是**断的**不是说完的（同 CC 侧，
					// protocol.StreamReadReporter）。
					c.SetStreamReadError(err)
					out <- protocol.Event{
						Type:    protocol.EvError,
						Status:  0,
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

// respStreamState 是流式解码的状态机。
type respStreamState struct {
	started  bool
	id       string
	model    string
	stop     string
	usage    *protocol.Usage
	sawTool  bool
	doneSent bool
	errSent  bool

	// pending 是当前正开着的 custom_tool_call：它的入参要攒满再一次性放出，理由见
	// flushCustom。function_call 不进这里，分片当场转发。
	pending *customPending
}

// customPending 攒一个 custom_tool_call 的入参。
type customPending struct {
	index int
	args  []string
}

func (st *respStreamState) frame(frame []byte, out chan<- protocol.Event) {
	event, data := protocol.SSEFields(frame)
	if len(data) == 0 {
		return
	}
	var f respFrame
	if json.Unmarshal(data, &f) != nil {
		// 解不动的帧跳过而不是中止整条流，同 CC 侧口径：一帧噪声不该打断已经在
		// 下发的响应。
		return
	}
	if f.Type == "" {
		// 极少数上游把类型只写在 SSE 的 event: 行上（线格允许），拿它兜底。
		f.Type = event
	}
	st.event(&f, out)
}

func (st *respStreamState) event(f *respFrame, out chan<- protocol.Event) {
	switch f.Type {
	case "response.created":
		if f.Response != nil {
			st.id, st.model = f.Response.ID, f.Response.Model
		}
		st.start(out)

	case "response.output_item.added":
		st.itemAdded(f, out)

	case "response.output_text.delta":
		if f.Delta != "" {
			st.start(out)
			out <- protocol.Event{Type: protocol.EvTextDelta, Text: f.Delta, Index: f.OutputIndex}
		}

	case "response.reasoning_summary_text.delta":
		// 只认 .delta，不认 .done：done 帧带的是这一段的**全文**（实采见
		// responses-stream-reasoning-turn1 的 text 字段），两个都收会把摘要发两遍。
		// output_item.done 里那份 summary[] 同理不收。
		if f.Delta != "" {
			st.start(out)
			out <- protocol.Event{Type: protocol.EvThinkingDelta, Text: f.Delta, Channel: protocol.ThinkingSummary, Index: f.OutputIndex}
		}

	case "response.reasoning_text.delta":
		// 推理**正文**流。九份 responses-stream-* 转录里一次都没出现过（线上两条
		// R 上游都只回摘要），所以这条分支只保形态完备，覆盖靠构造帧。
		if f.Delta != "" {
			st.start(out)
			out <- protocol.Event{Type: protocol.EvThinkingDelta, Text: f.Delta, Channel: protocol.ThinkingBody, Index: f.OutputIndex}
		}

	case "response.function_call_arguments.delta":
		// function 形态的入参按契约就是 JSON 字符串，分片原样转发，上游的节奏保住。
		if f.Delta != "" {
			st.start(out)
			out <- protocol.Event{Type: protocol.EvToolArgsDelta, Index: f.OutputIndex, Text: f.Delta}
		}

	case "response.custom_tool_call_input.delta":
		// 只进缓冲，不上线。为什么见 flushCustom。
		if st.pending != nil && st.pending.index == f.OutputIndex && f.Delta != "" {
			st.pending.args = append(st.pending.args, f.Delta)
		}

	case "response.custom_tool_call_input.done":
		// 终帧的 input 是完整入参（实采实测），优先用它——比拼分片少一层出错可能。
		st.flushCustom(f.OutputIndex, f.Input, out)

	case "response.output_item.done":
		st.itemDone(f, out)

	case "response.completed", "response.incomplete":
		st.terminal(f, out)

	case "response.failed":
		msg := "上游响应失败"
		if f.Response != nil && f.Response.Error != nil && f.Response.Error.Message != "" {
			msg = f.Response.Error.Message
		}
		st.emitError(msg, out)

	case "error":
		// 裸 error 帧（不裹 response 对象）。上游 key 与 base_url 不进 Message：
		// 只取上游自己给的 message 文本，不拼接任何本地连接信息。
		msg := "上游响应失败"
		if f.Error != nil && f.Error.Message != "" {
			msg = f.Error.Message
		}
		st.emitError(msg, out)
	}
	// 其余帧（in_progress / content_part.added|done / output_text.done /
	// reasoning_summary_part.added|done / reasoning_summary_text.done / …）一律跳过：
	// 要么是 canonical 里没有的结构信号，要么是上面说的「同一段文本的第二份拷贝」。
}

// start 放出 EvMessageStart，幂等。
func (st *respStreamState) start(out chan<- protocol.Event) {
	if st.started {
		return
	}
	st.started = true
	out <- protocol.Event{Type: protocol.EvMessageStart, ID: st.id, Model: st.model}
}

func (st *respStreamState) itemAdded(f *respFrame, out chan<- protocol.Event) {
	if f.Item == nil {
		return
	}
	switch f.Item.Type {
	case "function_call":
		st.start(out)
		st.sawTool = true
		out <- protocol.Event{
			Type: protocol.EvToolCallStart, Index: f.OutputIndex,
			// call_id 而不是 item id：canonical 的 ToolID 要与工具结果回带时用的
			// 那个标识对齐，Responses 里那是 call_id（见 decode.go 的 decodeToolCall）。
			ToolID: f.Item.CallID, ToolName: f.Item.Name,
			ArgsIsJSON: true,
		}
	case "custom_tool_call":
		st.start(out)
		st.sawTool = true
		st.pending = &customPending{index: f.OutputIndex}
		out <- protocol.Event{
			Type: protocol.EvToolCallStart, Index: f.OutputIndex,
			ToolID: f.Item.CallID, ToolName: f.Item.Name,
			// 明知上游发的是自由文本仍报 JSON：见 flushCustom，入参会被包装成
			// `{"input":"…"}` 再放出去，放出去的东西确实是 JSON。
			ArgsIsJSON: true,
		}
	}
	// message 项不产事件：正文由 output_text.delta 带。reasoning 项静默跳过。
}

func (st *respStreamState) itemDone(f *respFrame, out chan<- protocol.Event) {
	if f.Item == nil {
		return
	}
	switch f.Item.Type {
	case "function_call":
		out <- protocol.Event{Type: protocol.EvToolCallEnd, Index: f.OutputIndex}
	case "custom_tool_call":
		// 正常序里 custom_tool_call_input.done 已经冲过缓冲了，这里是兜底：上游漏发
		// input.done 时靠攒着的分片补上，别让一个调用只有 Start 没有 End。
		st.flushCustom(f.OutputIndex, f.Item.Input, out)
		out <- protocol.Event{Type: protocol.EvToolCallEnd, Index: f.OutputIndex}
	}
}

// flushCustom 把 custom 工具的入参**整条**包成 `{"input":"…"}` 放出去。
//
// 为什么攒满再发、不逐片转发：全仓的响应侧不变式是「工具入参一律是 JSON」——
// anthropic/decode_response.go 与 openaicc/decode.go 两个解码侧都硬写 ArgsIsJSON=true，
// 两个入口编码侧也都不看这一位、把分片原样透下去。custom 工具的入参是 JS 源码，
// 逐片放出等于往 CC/A 客户端灌一串解不动的东西。而 JSON 字符串的转义没法按分片增量
// 生成（同 encode.go 的 flushTool，方向相反），所以只能整条包。
//
// 代价只是丢掉 custom 入参的分片节奏。线上两条路径（CC→R / A→R）的客户端都只声明
// function 工具，上游回 custom_tool_call 的情形实际不会发生——这里存在是为了让形态
// 完备，不是为了跑热路径。
func (st *respStreamState) flushCustom(index int, complete string, out chan<- protocol.Event) {
	if st.pending == nil || st.pending.index != index {
		return
	}
	args := complete
	if args == "" {
		for _, frag := range st.pending.args {
			args += frag
		}
	}
	st.pending = nil
	out <- protocol.Event{
		Type:  protocol.EvToolArgsDelta,
		Index: index,
		Text:  wrapCustomArgs(args),
	}
}

// wrapCustomArgs 是 protocol.WrapCustomToolArgs 的不返错版本。
//
// 它只在 json.Marshal 一个 map[string]string 失败时出错——那不可能发生（string 总能
// 编码）。而这里的调用方是解码状态机，它在 goroutine 里往 channel 写事件，没有能把
// 错误交出去的位置；兜成 `{}`（合法 JSON，语义是「没参数」）比让一个不可能的分支
// 撑起一条错误传播链清楚。
func wrapCustomArgs(args string) string {
	wrapped, err := protocol.WrapCustomToolArgs(args)
	if err != nil {
		return "{}"
	}
	return wrapped
}

// terminal 处理 response.completed / response.incomplete：先 usage 后 EvDone。
func (st *respStreamState) terminal(f *respFrame, out chan<- protocol.Event) {
	st.start(out)
	if f.Response != nil {
		st.observeUsage(f.Response.Usage, out)
		st.stop = st.stopReason(f.Response)
	}
	st.done(out)
}

// stopReason 把 Responses 的 status + incomplete_details 映到 canonical 停因。
//
// Responses **没有 stop_reason 字段**（tap.go 记过同一条事实），终止信息在 status 上，
// 截断的具体原因在 incomplete_details.reason 里。
func (st *respStreamState) stopReason(r *respPayload) string {
	if r.Status == "incomplete" {
		if r.IncompleteDetails != nil {
			switch r.IncompleteDetails.Reason {
			case "max_output_tokens":
				return "length"
			case "content_filter":
				return "content_filter"
			}
		}
		// 其余 incomplete 理由（上游自造词）没有对得上的 canonical 取值，兜成
		// stop——同 CC 侧 mapStopReason，宁可少说一句。
		return "stop"
	}
	if st.sawTool {
		// Responses 不像 CC 那样发 finish_reason=tool_calls，「这轮以工具调用收尾」
		// 只能由 output 里有没有工具项判出来。漏了的话 A 出口会写 end_turn，
		// 客户端就不知道该去执行工具了。
		return "tool_calls"
	}
	return "stop"
}

// observeUsage 放出一条 usage 快照。语义是累计快照（protocol/event.go），字段映射
// 见 respUsage.canonical。
func (st *respStreamState) observeUsage(u *respUsage, out chan<- protocol.Event) {
	if u == nil {
		return
	}
	if st.usage == nil {
		st.usage = &protocol.Usage{}
	}
	st.usage.MergeSnapshot(u.canonical())
	snapshot := *st.usage
	out <- protocol.Event{Type: protocol.EvUsage, Usage: &snapshot}
}

func (st *respStreamState) emitError(msg string, out chan<- protocol.Event) {
	if st.errSent || st.doneSent {
		return
	}
	st.errSent = true
	out <- protocol.Event{Type: protocol.EvError, Message: msg}
}

func (st *respStreamState) done(out chan<- protocol.Event) {
	if st.errSent || st.doneSent {
		return
	}
	st.doneSent = true
	stop := st.stop
	truncated := stop == ""
	if truncated {
		// 同 CC 侧：Anthropic 非流式不接受空 stop_reason（§5 坑清单），默认值在解码
		// 侧就给足；「上游没说就断了」这个事实另开 Truncated 带下去。
		stop = "stop"
	}
	out <- protocol.Event{Type: protocol.EvDone, StopReason: stop, Truncated: truncated}
}

// finish 收尾。走到这里说明流读完了；正常情况下终帧已经把 EvDone 发过，done 幂等。
func (st *respStreamState) finish(out chan<- protocol.Event) {
	if st.errSent || st.doneSent {
		return
	}
	st.start(out)
	if st.pending != nil {
		// 流断在 custom 入参收尾之前：攒着的照样放出去，半个调用也比凭空消失强
		// （同 encode.go finish 里那条 flushTool）。
		idx := st.pending.index
		st.flushCustom(idx, "", out)
		out <- protocol.Event{Type: protocol.EvToolCallEnd, Index: idx}
	}
	st.done(out)
}

// DecodeFullBody 把非流式 Responses 响应体解成完整事件序列。
//
// 这里**手搭事件列表**而不复用流式状态机（与 CC 侧的取法相反，同 anthropic 侧）：
// 流式状态机吃的是帧序，非流式只有一个终态 response 对象，为它伪造一串帧比直接照
// output 数组走一遍更绕、也更容易在 item 生命周期上出错。
//
// **没有真实样本**：手上九份 Responses 转录全是 stream:true，golden 终帧里那个
// output 数组是中转侧的降级重建（tool-turn1 里 custom_tool_call 被重建成了没有入参的
// function_call），不能当线格真值用。所以这条路径由手搭 fixture 覆盖，缺口记在 §9.4②。
func (c *Codec) DecodeFullBody(body []byte) ([]protocol.Event, error) {
	var payload respPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("openairesponses: 上游响应不是 JSON: %w", err)
	}

	events := []protocol.Event{{
		Type: protocol.EvMessageStart, ID: payload.ID, Model: payload.Model,
	}}

	if payload.Status == "failed" {
		msg := "上游响应失败"
		if payload.Error != nil && payload.Error.Message != "" {
			msg = payload.Error.Message
		}
		return append(events, protocol.Event{Type: protocol.EvError, Message: msg}), nil
	}

	sawTool := false
	for i, item := range payload.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					events = append(events, protocol.Event{
						Type: protocol.EvTextDelta, Text: part.Text, Index: i,
					})
				}
			}
		case "function_call":
			sawTool = true
			events = append(events,
				protocol.Event{
					Type: protocol.EvToolCallStart, Index: i,
					ToolID: item.CallID, ToolName: item.Name, ArgsIsJSON: true,
				},
				protocol.Event{Type: protocol.EvToolArgsDelta, Index: i, Text: item.Arguments},
				protocol.Event{Type: protocol.EvToolCallEnd, Index: i},
			)
		case "custom_tool_call":
			sawTool = true
			events = append(events,
				protocol.Event{
					Type: protocol.EvToolCallStart, Index: i,
					ToolID: item.CallID, ToolName: item.Name, ArgsIsJSON: true,
				},
				// 同流式：自由文本入参包成 JSON 再交出去（flushCustom 的理由）。
				protocol.Event{
					Type: protocol.EvToolArgsDelta, Index: i,
					Text: wrapCustomArgs(item.Input),
				},
				protocol.Event{Type: protocol.EvToolCallEnd, Index: i},
			)
		case "reasoning":
			// 摘要段落逐段放出，段间不补分隔符：canonical 是增量流，出口按到达顺序
			// 拼进同一个推理块（与 message 项逐 part 放 EvTextDelta 同理）。
			for _, s := range item.Summary {
				if s.Text != "" {
					events = append(events, protocol.Event{
						Type: protocol.EvThinkingDelta, Text: s.Text,
						Channel: protocol.ThinkingSummary, Index: i,
					})
				}
			}
		}
	}

	if payload.Usage != nil {
		u := payload.Usage.canonical()
		events = append(events, protocol.Event{Type: protocol.EvUsage, Usage: &u})
	}

	st := &respStreamState{sawTool: sawTool}
	stop := st.stopReason(&payload)
	// status 为空才算「上游没声明收尾」；completed / incomplete 都是明确的结束
	// （同 anthropic/decode_response.go 用 StopReason 是否为空判 Truncated）。
	events = append(events, protocol.Event{
		Type: protocol.EvDone, StopReason: stop, Truncated: payload.Status == "",
	})
	return events, nil
}
