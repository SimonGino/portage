package protocol

import (
	"errors"
	"io"
	"net/http"
	"sync"
)

// ErrNotImplemented 是三个 codec 骨架在 M2 落地前的统一返回。
//
// 返回错误而不是 panic：这些方法在 M2 期间会被真实请求打到（转换闸门一放开就有
// 流量），panic 会带走整个进程，而一个能被 relay 转成 5xx 的错误只会让这一条请求
// 失败。骨架期的正确行为是「明确地不支持」，不是「崩给你看」。
var ErrNotImplemented = errors.New("protocol: codec 尚未实现")

// Codec 是一个协议的双向转换器。
//
// 架构约束：每个协议一个包实现 Codec，「A→B 转换」= CodecA 解码 + CodecB 编码，
// **不存在两两互转的转换器**。取枢纽式而非网桥式的实证依据见 §5。
//
// DecodeRequest 必须是**全函数**：任何该协议的合法入站字节都要有地方放，装不下的
// 字段进 Extras。跨协议丢什么是 Encode 侧按口径做的决策，不是 Decode 侧的借口。
type Codec interface {
	// DecodeRequest 把入口请求体解成 canonical。
	DecodeRequest(body []byte, stream bool) (*Request, error)
	// EncodeRequest 把 canonical 编成出口请求体。
	EncodeRequest(req *Request, stream bool) ([]byte, error)
	// DecodeStream 把上游 SSE 解成事件流。返回的 channel 由实现负责关闭。
	DecodeStream(r io.Reader) (<-chan Event, error)
	// EncodeStream 把事件流编成下行 SSE，含分帧、index 追踪与 flush。
	EncodeStream(w io.Writer, events <-chan Event) error

	// DecodeFullBody 把上游非流式响应体解成完整事件序列。
	//
	// v0.26 补进接口（PO 裁定 jinpenga，2026-08-08）：v0.25 定稿只有 EncodeFullBody，
	// 非流式转换路径的解码侧因此无处落脚。备选是「非流式也向上游发流式再聚合」，
	// 被否——上游看到的请求与客户端发的不是一回事（计费与限流口径可能不同），且流
	// 中途断连时手里只剩半截事件序列，而客户端等的是一个完整 JSON，无法收场。
	DecodeFullBody(body []byte) ([]Event, error)
	// EncodeFullBody 把完整事件序列聚合成非流式响应体。
	EncodeFullBody(events []Event) ([]byte, error)
	// EncodeError 按协议原生格式写出错误。
	//
	// 收 http.ResponseWriter 而非 io.Writer——它要设 Content-Type 与状态码，而这
	// 条路径只在**首字节写出之前**走得通：流一旦开头，错误就只能以 EvError 的
	// 形态走在流里，那是 EncodeStream 的活。
	//
	// msg 由调用方保证已脱敏：上游 key 与 base_url 严禁出现在错误回显里。
	EncodeError(w http.ResponseWriter, status int, msg string)
}

// RequestEncodeReporter 是 Codec 的可选扩展：编码时顺带交出**这次实际丢了什么**。
//
// 口径层 §2.6 要求跨协议丢弃「+ 日志警告」，而 codec 刻意不持有 logger——它是纯
// 函数，测试里不该冒日志。于是分工是：codec 登记，调用方打日志。
//
// 不并进 Codec 接口，是因为六条转换路径里只有出口侧用得上它；并进去等于让每个
// 入口侧实现都背一个恒空的返回值。调用方按需类型断言，断言不中就没警告可打。
type RequestEncodeReporter interface {
	// dropped 是丢弃登记清单（档位是各 codec 包的 DropXxx 常量，工具类三档附名单，
	// 见 Drops），没丢就是 nil。
	//
	// 编码后请求已不成立（messages 空了、tool_choice 的硬要求落空，口径层 v1.14 ⑦⑧）
	// 时回 *RequestError，调用方逐字回 400；**dropped 照常交出**——那条丢弃 Warn 与这个
	// 400 对照，才分得出「客户端发空」与「我们丢光」。
	EncodeRequestReport(req *Request, stream bool) ([]byte, Drops, error)
}

// StreamReadReporter 是 Codec 解码侧的可选扩展：交出「这次流式解码是不是**读上游
// 读断了**」。
//
// 为什么要它：转换路径上读断是**带内**往下传的——DecodeStream 放一条 EvError 就收
// 摊，入口 codec 照常把错误帧写给客户端然后正常返回，调用方从返回值里看不出这次其实
// 断在半路，流水就会把它记成一次干净的 200/ok。透传路径没有这个问题（那边读错误是
// relayBody 的返回值），两条路的收场词表因此对不齐。
//
// 只报**传输失败**，不报上游在流里回的错误对象：后者透传路径同样记 ok，硬要在这边
// 降级会让两条路径再次分叉。
type StreamReadReporter interface {
	// StreamReadError 返回读上游失败的原因；正常收尾（含上游回错误对象）为 nil。
	StreamReadError() error
}

// StreamReadFlag 是 StreamReadReporter 的现成实现，各 codec 内嵌即可。
//
// 上锁不是多余的：写在解码 goroutine 里，读在调用方。读的时机看着总在事件流收尾
// 之后，但编码侧遇到 EvError 是**提前 return** 的（上游在流里回错误对象就会这样），
// 那之后解码 goroutine 还在跑，再撞上读失败就是实打实的并发写。
type StreamReadFlag struct {
	mu  sync.Mutex
	err error
}

// SetStreamReadError 由解码侧在读上游失败时调用。只记第一次。
func (f *StreamReadFlag) SetStreamReadError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err == nil {
		f.err = err
	}
}

func (f *StreamReadFlag) StreamReadError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}
