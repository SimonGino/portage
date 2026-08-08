package openaicc

import (
	"io"
	"net/http"

	"github.com/SimonGino/ai-gateway/internal/protocol"
)

// Codec 是 OpenAI Chat Completions 协议的转换器。
//
// #11 落地的是 CC 作**出口**要用的三个方法：EncodeRequest（encode.go）、
// DecodeStream / DecodeFullBody（decode.go）。CC 作**入口**的那两个（DecodeRequest、
// EncodeStream / EncodeFullBody）属于 ③ CC→A、CC→R，仍是骨架。
//
// 编译期断言钉住接口一致性：任一方法漏实现，`go build` 当场红，而不是等 codecs 表
// 在运行时组装才发现。
var _ protocol.Codec = (*Codec)(nil)
var _ protocol.RequestEncodeReporter = (*Codec)(nil)

type Codec struct{}

func NewCodec() *Codec { return &Codec{} }

func (c *Codec) DecodeRequest(body []byte, stream bool) (*protocol.Request, error) {
	return nil, protocol.ErrNotImplemented
}

func (c *Codec) EncodeStream(w io.Writer, events <-chan protocol.Event) error {
	return protocol.ErrNotImplemented
}

func (c *Codec) EncodeFullBody(events []protocol.Event) ([]byte, error) {
	return nil, protocol.ErrNotImplemented
}

// EncodeError 直接委托给 M0 就已落地的 protocol.WriteError——错误格式不是转换
// 逻辑，骨架期没有理由让它跟着返回「未实现」。
func (c *Codec) EncodeError(w http.ResponseWriter, status int, msg string) {
	protocol.OpenAICC.WriteError(w, status, msg)
}
