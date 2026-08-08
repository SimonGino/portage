package anthropic

import (
	"io"
	"net/http"

	"github.com/SimonGino/ai-gateway/internal/protocol"
)

// Codec 是 Anthropic Messages 协议的转换器。
//
// #11 落地的是 Anthropic 作**入口**要用的三个方法：DecodeRequest（decode.go）、
// EncodeStream / EncodeFullBody（encode.go）。作**出口**的那两个（EncodeRequest、
// DecodeStream / DecodeFullBody）属于 ② R→A、③ CC→A，仍是骨架——而且要等 #7 拿到
// 官方凭证才验得了。
//
// 编译期断言钉住接口一致性：任一方法漏实现，`go build` 当场红，而不是等 codecs 表
// 在运行时组装才发现。
var _ protocol.Codec = (*Codec)(nil)

type Codec struct{}

func NewCodec() *Codec { return &Codec{} }

// EncodeRequest 仍是骨架：Anthropic 作**出口**（CC→A / R→A）是 M2 后续批次，且
// 要等 #7 拿到官方凭证才验得了。
func (c *Codec) EncodeRequest(req *protocol.Request, stream bool) ([]byte, error) {
	return nil, protocol.ErrNotImplemented
}

func (c *Codec) DecodeStream(r io.Reader) (<-chan protocol.Event, error) {
	return nil, protocol.ErrNotImplemented
}

// DecodeFullBody 仍是骨架：本包的解码侧只在 Anthropic 作**上游**时才用得到，而那
// 条路径（CC→A / R→A）要等 #7 拿到官方凭证才验得了。#11 走的是 Anthropic 入口，
// 用不到它。
func (c *Codec) DecodeFullBody(body []byte) ([]protocol.Event, error) {
	return nil, protocol.ErrNotImplemented
}

// EncodeError 直接委托给 M0 就已落地的 protocol.WriteError——错误格式不是转换
// 逻辑，骨架期没有理由让它跟着返回「未实现」。
func (c *Codec) EncodeError(w http.ResponseWriter, status int, msg string) {
	protocol.Anthropic.WriteError(w, status, msg)
}
