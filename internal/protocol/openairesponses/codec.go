package openairesponses

import (
	"io"
	"net/http"

	"github.com/SimonGino/ai-gateway/internal/protocol"
)

// Codec 是 OpenAI Responses 协议的转换器骨架。#10 只定架构 seam，实现见 M2 后续 issue。
//
// 编译期断言钉住接口一致性：三个骨架里任一个漏实现方法，`go build` 当场红，
// 而不是等 codecs 表在运行时组装才发现。
var _ protocol.Codec = (*Codec)(nil)

type Codec struct{}

func NewCodec() *Codec { return &Codec{} }

func (c *Codec) DecodeRequest(body []byte, stream bool) (*protocol.Request, error) {
	return nil, protocol.ErrNotImplemented
}

func (c *Codec) EncodeRequest(req *protocol.Request, stream bool) ([]byte, error) {
	return nil, protocol.ErrNotImplemented
}

func (c *Codec) DecodeStream(r io.Reader) (<-chan protocol.Event, error) {
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
	protocol.OpenAIResponses.WriteError(w, status, msg)
}
