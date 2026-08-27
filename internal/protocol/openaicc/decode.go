package openaicc

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Chat Completions 的**解码**侧：上游响应 → canonical 事件序列。
//
// 主工作量在 tool_calls 的增量重组。CC 把一次工具调用切成若干片，用 index 标序，
// 并行调用下这些片**可以交错到达**；canonical 事件流要的是「每个调用先 Start、
// 中间若干 ArgsDelta、最后 End」，所以必须按 index 缓存再按序放出。
//
// 实采（testdata/golden/cc-stream-parallel-tools）里三个调用其实是顺序发的，
// 没有交错——所以交错那条路径由 decode_test.go 里手搭的分片序列覆盖。手搭的是
// **测试输入**不是样本，不进 golden 库：那里只放真实字节。

// chunkPayload 是流式 chunk 与非流式响应共用的顶层形态。
//
// 两者结构一致，差别只在 choices 里是 delta 还是 message——这也是 Tap 能用一套字段
// 吃两种响应的原因（见 tap.go）。
type chunkPayload struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta        *choiceBody `json:"delta"`
		Message      *choiceBody `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *usagePayload `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

type choiceBody struct {
	Content string `json:"content"`
	// ReasoningContent 是 CC 侧承接推理正文的**事实标准**字段：DeepSeek 起头，
	// 兼容上游普遍跟随（sub2api 的 CC↔Anthropic bridge 也认它）。流式在
	// choices[].delta.reasoning_content，非流式在 choices[].message.reasoning_content，
	// 两者同名同义，所以这一格与 Content 一样两条路共用。
	//
	// 官方 OpenAI 不发它——官方模型的推理过程根本不出上游。所以它只在兼容上游上
	// 有值，零值即「这一帧没有推理正文」。
	ReasoningContent string `json:"reasoning_content"`
	// Reasoning 是同一份推理正文的另一种拼法：vLLM 较新版本的 CC 端点发
	// `delta.reasoning`（实采自 PAI-EAS 上的 vLLM，同一部署的 /v1/responses 端点
	// 反而发 reasoning_text，两个端点自己就不一致）。两个键同义，认哪个都不引入歧义，
	// 所以按别名收（取值见 reasoningText）——不认的话这类上游的思考正文会被整段丢掉，
	// 而且是静默丢：没有错误，只是 canonical 里一个 ThinkingDelta 都不出。
	Reasoning string `json:"reasoning"`
	ToolCalls []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function *struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// reasoningText 取这一帧的推理正文，reasoning_content 优先。
//
// 两键同时有值的上游没见过；真出现时以事实标准那个为准，另一个当没看见——同一帧
// 里把两份都放出去会让出口那边开两次思考块。
func (b *choiceBody) reasoningText() string {
	if b.ReasoningContent != "" {
		return b.ReasoningContent
	}
	return b.Reasoning
}

type usagePayload struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	// 这里用值不用 *int（与 Tap 那侧不同）：canonical 的 Usage 零值就是「没报」，
	// 「报了 0」与「没报」写出去的字节一样，不需要分。
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// DecodeStream 把上游 SSE 解成事件流。channel 由本函数负责关闭。
//
// r 读完（或读错）goroutine 就收摊；调用方关掉上游 body 即可让它退出，不需要另设
// 取消通道。
func (c *Codec) DecodeStream(r io.Reader) (<-chan protocol.Event, error) {
	out := make(chan protocol.Event, 32)
	go func() {
		defer close(out)
		st := &streamState{}
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
				if !errors.Is(err, io.EOF) {
					// 记一笔给调用方：这条流是**断的**不是说完的。带内的 EvError
					// 到了编码侧就与「上游回了个错误对象」混成一样，收场判不出来
					// （protocol.StreamReadReporter）。
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

// DecodeFullBody 把非流式响应体解成完整事件序列。
//
// 复用流式那套状态机：非流式响应就是「一帧到底」的流（canonical 模型对非流式的
// 定义就是完整事件序列一次性回放，见 protocol/event.go）。这样 message.content 与
// delta.content 只有一处解析，两条路径不会漂。
func (c *Codec) DecodeFullBody(body []byte) ([]protocol.Event, error) {
	var payload chunkPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("openaicc: 上游响应不是 JSON: %w", err)
	}
	out := make(chan protocol.Event, 32)
	st := &streamState{}
	go func() {
		defer close(out)
		st.payload(&payload, out)
		st.finish(out)
	}()
	var events []protocol.Event
	for ev := range out {
		events = append(events, ev)
	}
	return events, nil
}

// toolBuf 攒一个 index 上的工具调用分片。
type toolBuf struct {
	id   string
	name string
	// args 保留原始分片而不是拼成一整串：编码侧照着原分片发下去，客户端看到的
	// 增量节奏与上游一致。拼起来再切是凭空造出来的节奏。
	args []string
	// started 表示 EvToolCallStart 已经放出去了。
	started bool
	// seq 是这个 index 第一次出现的次序，决定收尾时的放出顺序。index 本身也能
	// 排序，但上游不保证 index 从 0 连号——见 flushTools。
	seq int
}

type streamState struct {
	started   bool // EvMessageStart 已放出
	id        string
	model     string
	stop      string
	tools     map[int]*toolBuf
	nextSeq   int
	usage     *protocol.Usage
	doneSent  bool
	errorSent bool
}

// frame 处理一个 SSE 帧。
func (st *streamState) frame(frame []byte, out chan<- protocol.Event) {
	_, data := protocol.SSEFields(frame)
	if len(data) == 0 || string(data) == "[DONE]" {
		return
	}
	var payload chunkPayload
	if json.Unmarshal(data, &payload) != nil {
		// 解不动的帧跳过而不是中止整条流：上游偶尔混入非 JSON 心跳/注释是常态，
		// 为一帧噪声把已经在下发的响应打断得不偿失（放弃解析的粒度是「丢那一帧」，
		// 与 M0 的 Tap 口径一致）。
		return
	}
	st.payload(&payload, out)
}

func (st *streamState) payload(payload *chunkPayload, out chan<- protocol.Event) {
	if payload.Error != nil && !st.errorSent {
		st.errorSent = true
		out <- protocol.Event{Type: protocol.EvError, Message: payload.Error.Message}
		return
	}

	if payload.ID != "" {
		st.id = payload.ID
	}
	if payload.Model != "" {
		st.model = payload.Model
	}
	if !st.started && (st.id != "" || st.model != "") {
		st.started = true
		out <- protocol.Event{Type: protocol.EvMessageStart, ID: st.id, Model: st.model}
	}

	for _, choice := range payload.Choices {
		body := choice.Delta
		if body == nil {
			body = choice.Message
		}
		if body != nil {
			st.body(body, out)
		}
		if choice.FinishReason != "" {
			st.stop = mapStopReason(choice.FinishReason)
		}
	}

	if payload.Usage != nil {
		st.observeUsage(payload.Usage, out)
	}
}

func (st *streamState) body(body *choiceBody, out chan<- protocol.Event) {
	// 推理正文先于正文放出：兼容上游是先流完 reasoning_content 再流 content，
	// 顺序照原样保留，出口那边才能按同样的顺序开块。
	//
	// 通道取 ThinkingBody 而非 ThinkingSummary：reasoning_content 是推理正文本身，
	// 不是面向展示的摘要（Responses 的 reasoning_summary_* 才是）。
	if text := body.reasoningText(); text != "" {
		out <- protocol.Event{Type: protocol.EvThinkingDelta, Text: text, Channel: protocol.ThinkingBody}
	}
	if body.Content != "" {
		out <- protocol.Event{Type: protocol.EvTextDelta, Text: body.Content}
	}
	for _, call := range body.ToolCalls {
		// index 缺省按 0 处理：单工具调用的上游有省略 index 的（协议上它可选）。
		idx := 0
		if call.Index != nil {
			idx = *call.Index
		}
		if st.tools == nil {
			st.tools = map[int]*toolBuf{}
		}
		buf, ok := st.tools[idx]
		if !ok {
			buf = &toolBuf{seq: st.nextSeq}
			st.nextSeq++
			st.tools[idx] = buf
		}
		if call.ID != "" {
			buf.id = call.ID
		}
		// name 只在非空时覆盖：实采里后续分片会带 "name":""（见
		// cc-stream-parallel-tools），照抄会把已经拿到的工具名擦掉。
		if call.Function != nil {
			if call.Function.Name != "" {
				buf.name = call.Function.Name
			}
			if call.Function.Arguments != "" {
				buf.args = append(buf.args, call.Function.Arguments)
			}
		}
	}
}

// observeUsage 放出一条 usage 快照。
//
// 语义是累计快照而非增量（protocol/event.go）：非零字段覆盖先前值，消费方不做加法。
// CC 的 prompt_tokens 本就是**毛值**（含缓存命中），与 canonical 的口径一致，直映即可
// （protocol.Usage 的约定）；cached_tokens 是它的明细，不再往 InputTokens 上加。
func (st *streamState) observeUsage(u *usagePayload, out chan<- protocol.Event) {
	if st.usage == nil {
		st.usage = &protocol.Usage{}
	}
	next := protocol.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	// CC 只有缓存命中（读）的概念，没有缓存写入，CacheWriteTokens 恒零。
	if d := u.PromptTokensDetails; d != nil {
		next.CacheReadTokens = d.CachedTokens
	}
	// 思考 token 是 completion_tokens 的明细，不从它里面减（口径层 v0.66）。
	if d := u.CompletionTokensDetails; d != nil {
		next.ReasoningTokens = d.ReasoningTokens
	}
	st.usage.MergeSnapshot(next)
	snapshot := *st.usage
	out <- protocol.Event{Type: protocol.EvUsage, Usage: &snapshot}
}

// finish 收尾：放出攒着的工具调用，再放 EvDone。
func (st *streamState) finish(out chan<- protocol.Event) {
	if st.errorSent || st.doneSent {
		return
	}
	st.doneSent = true
	if !st.started {
		// 空响应也要有开头：下游编码侧靠 EvMessageStart 决定响应的 id 与 model。
		st.started = true
		out <- protocol.Event{Type: protocol.EvMessageStart, ID: st.id, Model: st.model}
	}
	st.flushTools(out)
	stop := st.stop
	truncated := stop == ""
	if truncated {
		// Anthropic 非流式响应不接受空 stop_reason（§5 坑清单）。默认值在这里就
		// 给足，编码侧不必各自兜底。
		//
		// 兜底会抹掉「上游根本没发 finish_reason 就断了」这个事实，所以另开
		// Truncated 单独带下去（protocol.Event 的字段注释）。
		stop = "stop"
	}
	out <- protocol.Event{Type: protocol.EvDone, StopReason: stop, Truncated: truncated}
}

// flushTools 按**首次出现的次序**放出每个工具调用的 Start / ArgsDelta / End。
//
// 为什么攒到收尾才放：CC 没有「这个工具调用说完了」的信号，只有整条流的
// finish_reason。在此之前任何 index 都可能再来一片，提前 End 就会把后到的片扔在
// 一个已经关掉的调用上。
//
// 为什么按 seq 而不按 index 排：index 是上游给的序号，不保证从 0 连号（跳号、
// 从 1 起都见过）。按首次出现次序排能保证「先说的先出」，这与客户端看到的顺序一致；
// 而 canonical 事件的 Index 字段仍原样携带上游的 index，不重编号。
func (st *streamState) flushTools(out chan<- protocol.Event) {
	if len(st.tools) == 0 {
		return
	}
	idxs := slices.SortedFunc(maps.Keys(st.tools), func(a, b int) int {
		return cmp.Compare(st.tools[a].seq, st.tools[b].seq)
	})

	for _, idx := range idxs {
		buf := st.tools[idx]
		if !buf.started {
			buf.started = true
			out <- protocol.Event{
				Type:     protocol.EvToolCallStart,
				Index:    idx,
				ToolID:   buf.id,
				ToolName: buf.name,
				// CC 的 function.arguments 按契约就是 JSON 字符串。
				ArgsIsJSON: true,
			}
		}
		for _, frag := range buf.args {
			out <- protocol.Event{Type: protocol.EvToolArgsDelta, Index: idx, Text: frag}
		}
		out <- protocol.Event{Type: protocol.EvToolCallEnd, Index: idx}
	}
}

// mapStopReason 把 CC 的 finish_reason 映到 canonical 取值（§5 坑清单）。
// 未知值一律 stop——宁可少说一句，也不把上游的新词直接捅给客户端。
func mapStopReason(reason string) string {
	switch reason {
	case "tool_calls", "function_call":
		return "tool_calls"
	case "length":
		return "length"
	case "content_filter":
		return "content_filter"
	default:
		return "stop"
	}
}
