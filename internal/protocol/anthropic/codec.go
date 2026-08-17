package anthropic

import (
	"net/http"

	"github.com/SimonGino/portage/internal/protocol"
)

// Codec 是 Anthropic Messages 协议的转换器。
//
// portage-legacy#11 落地入口半边（DecodeRequest、EncodeStream / EncodeFullBody），
// portage-legacy#25 补上出口半边（EncodeRequest、DecodeStream / DecodeFullBody），
// 两半都在了。
//
// 编译期断言钉住接口一致性：任一方法漏实现，`go build` 当场红，而不是等 codecs 表
// 在运行时组装才发现。
var _ protocol.Codec = (*Codec)(nil)
var _ protocol.StreamReadReporter = (*Codec)(nil)

// Options 是从配置注进来的编码参数，由 codecs.New 传入。
type Options struct {
	// DefaultMaxTokens 是 max_tokens 缺省时的兜底值。
	//
	// Anthropic 的 max_tokens 是**必填**，而 OpenAI 两个协议都可以不给：Responses
	// 的 max_output_tokens 缺省、CC 的 max_tokens 缺省，都是合法请求。所以补默认
	// 这件事只能发生在这里，不能在 convert.go 里对 canonical 无条件填——那会让
	// 已经上线的 R→CC 路径开始悄悄截断（口径见 codecs.Options）。
	DefaultMaxTokens int
}

type Codec struct {
	opts Options
	// StreamReadFlag 记「流式解码途中读上游读断了」，由 DecodeStream 填。
	protocol.StreamReadFlag
}

// NewCodec 建一个 Anthropic Codec。
//
// 取可变参数只为了让入口方向的用例（portage-legacy#11 那批，压根用不到 Options）不必逐个改；
// 出口方向由 codecs.New 显式传值。
func NewCodec(opts ...Options) *Codec {
	c := &Codec{}
	if len(opts) > 0 {
		c.opts = opts[0]
	}
	return c
}

// EncodeError 直接委托给 M0 就已落地的 protocol.WriteError——错误格式不是转换
// 逻辑，骨架期没有理由让它跟着返回「未实现」。
func (c *Codec) EncodeError(w http.ResponseWriter, status int, msg string) {
	protocol.Anthropic.WriteError(w, status, msg)
}
