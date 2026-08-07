package protocol

import (
	"errors"
	"io"
	"net/http"
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
