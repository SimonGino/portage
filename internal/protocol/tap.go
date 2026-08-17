package protocol

import "io"

// Summary 是 Tap 从上游响应里旁路提取出来的日志字段。
//
// 字段缺失即零值：上游没回 usage 不是错误，只是这一行日志少几个数。
//
// token 数一律保留各协议的**原始语义**，不在 M0 做归一：Anthropic 的
// input_tokens 不含缓存命中部分，OpenAI 的 prompt_tokens 含。归一是 P1 codec
// 的活，在这里做只会把「上游到底报了什么」这个排障线索抹掉。
type Summary struct {
	// Model 是上游响应里自报的模型名——它未必等于请求里发出去的纳管模型名
	// （上游可能把别名解析成带日期的具体版本），差异本身就是排障线索。
	Model            string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	// ReasoningTokens 是上游报的思考 token 数（口径层 v0.66，#97）。它是
	// **OutputTokens 的明细，不是另一笔加数**——真实字节里 total 恒等于
	// input + output，reasoning 一次都没进这个加法（CC 三份 + Responses 一份，
	// 见 #97）。同 CacheRead/WriteTokens 之于 InputTokens。
	//
	// 载体各协议不同键：CC 是 completion_tokens_details.reasoning_tokens，
	// Responses 是 output_tokens_details.reasoning_tokens，Anthropic 没有这一格。
	ReasoningTokens int
	// HasReasoningTokens 区分**上游没报**与**上游报了 0**。
	//
	// 这一列上两者不是一回事：报 0 是「这次没思考」，没报是「这上游根本不报这个
	// 数」（Anthropic 一路，以及 CC 上游挂非推理模型时整个 details 都不发）。只看
	// 值的话，Anthropic 渠道的思考成本会恒显示为零——那正是口径层 v0.65
	// 「已发生的成本不得静默吞没」要防的静默。
	//
	// 用 bool 而不是 *int：Summary 全仓靠 `!=` 整体比对（golden 驱动与各 Tap 用例
	// 都是），指针会退化成地址相等。
	HasReasoningTokens bool
	// StopReason 同样保留原生取值（end_turn / tool_calls / completed …）。
	StopReason string
	// Degraded 表示 Tap 主动放弃了解析（单帧超限，或解析途中 panic 被兜住）。
	// 它只削弱这一行日志的可信度，永远不影响转发出去的字节。
	Degraded bool
}

// Tap 旁路观察上游响应的原始字节流，只读不改。
//
// 硬约束：Write 永不返回错误、永不让 panic 冒泡。Tap 挂在 io.TeeReader 上，
// 一旦返回错误，TeeReader 会把它变成读错误、直接打断转发——那是拿正确性换日志
// 字段，方向反了。
type Tap interface {
	io.Writer
	// Summary 结束观察并给出结果；可重复调用，结果幂等。
	Summary() Summary
}

// TapCore 是三个协议 Tap 的共用骨架：流式时按 SSE 帧喂给 onFrame，非流式时把
// 整包 JSON 攒齐后喂给 onBody。panic 兜底与缓冲上限都收在这里，各协议只写自己的
// 字段提取。
type TapCore struct {
	stream   bool
	scanner  FrameScanner
	body     []byte
	sum      Summary
	onFrame  func(*Summary, []byte)
	onBody   func(*Summary, []byte)
	finished bool
}

// NewTapCore 组装骨架。onFrame 收到的是单帧的 data 负载，onBody 收到的是完整响应体。
func NewTapCore(stream bool, onFrame, onBody func(*Summary, []byte)) TapCore {
	return TapCore{stream: stream, onFrame: onFrame, onBody: onBody}
}

func (t *TapCore) Write(p []byte) (int, error) {
	t.consume(p)
	// 恒定返回 len(p), nil——见 Tap 的硬约束。
	return len(p), nil
}

func (t *TapCore) consume(p []byte) {
	defer t.guard()
	if t.finished {
		return
	}
	if t.stream {
		t.scanner.Push(p, t.feed)
		if t.scanner.Overflowed() {
			t.sum.Degraded = true
		}
		return
	}
	if len(t.body)+len(p) > BufferLimit {
		// 非流式响应大到这个地步只可能是异常，停止累积、降级即可。
		t.sum.Degraded = true
		t.finished = true
		t.body = nil
		return
	}
	t.body = append(t.body, p...)
}

// feed 的 recover 必须在**这一层**，不能只靠 consume 外面那层。
//
// onFrame 是从 FrameScanner.Push 内部回调的：panic 从这里冒上去会把 Push 自己的
// 「帧已消费、缓冲往前挪」那一步跳过。于是同一个坏帧在下一块字节到来时被重放，
// 再 panic、再重放，其后所有帧都被压在缓冲里出不来，直到攒满上限才靠丢帧路径重新
// 对齐——一帧的解析失败就这样毒化了整条流，而 Anthropic 的 output_tokens 恰恰在
// 流的最后一帧。就地 recover 才真的做到「放弃的是那一帧」。
func (t *TapCore) feed(frame []byte) {
	defer t.guard()
	if _, data := SSEFields(frame); len(data) > 0 {
		t.onFrame(&t.sum, data)
	}
}

func (t *TapCore) Summary() Summary {
	t.finish()
	return t.sum
}

func (t *TapCore) finish() {
	defer t.guard()
	if t.finished {
		return
	}
	t.finished = true
	if t.stream {
		t.scanner.Flush(t.feed)
	} else if len(t.body) > 0 {
		t.onBody(&t.sum, t.body)
	}
	t.body = nil
}

// guard 是「绝不 panic 冒泡」那条硬约束的落点：解析代码再怎么被畸形响应绊倒，
// 代价上限也只是这一行日志降级。
func (t *TapCore) guard() {
	if r := recover(); r != nil {
		t.sum.Degraded = true
	}
}
