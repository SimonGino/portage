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

func (t *TapCore) feed(frame []byte) {
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
