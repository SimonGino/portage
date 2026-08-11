// Package codecs resolves a protocol to its Codec implementation.
//
// 与 internal/protocol/taps 同构、同理由：protocol 不能反向导入自己的三个子包
// （循环导入）。放这里之后，「协议 → Codec」这张表全仓只有一份。
package codecs

import (
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// Options 是建 Codec 时要从配置注进去的东西。
//
// 取显式结构体而非可变参数：字段少但每一个都会改变发给上游的字节，漏传一个的后果
// 是静默的行为变化（DefaultMaxTokens 传零就等于给 Anthropic 上游发一个必填字段
// 的零值）。必填参数会让编译器替我们记着。
type Options struct {
	// DefaultMaxTokens 补 Anthropic 出口的 max_tokens。
	//
	// 为什么不在 convert.go 里无条件填进 canonical：那会波及所有出口协议——
	// R→CC 路径上客户端没给 max_output_tokens 时，CC 上游本来不受限，填了就开始
	// 悄悄截断。这是 Anthropic 一家的必填要求，所以只由 anthropic 包在编码时用。
	DefaultMaxTokens int
}

// New 按协议挑 Codec；协议不认得时返回 nil，由调用方决定是报错还是退回透传。
//
// 转换路径要**两个** Codec：入口协议的解出 canonical，渠道协议的编回去。
// 两者相等时不该走这条路——同协议透传不做 decode→encode 转码（口径层硬约束）。
//
// **返回的实例是每请求一个，不可缓存、不可跨请求复用、不可并发共享。** Codec 允许
// 携带每请求状态（openairesponses 带解码侧传给编码侧的 custom 工具名，anthropic 带
// 配置注入的默认值），所以这里每次都真的 new 一个；而调用方拿到之后必须把同一个
// 实例用到底，解码时 New 一个、编码时再 New 一个，状态就断在中间了
// （internal/server/convert.go）。
func New(p protocol.Protocol, opts Options) protocol.Codec {
	switch p {
	case protocol.Anthropic:
		return anthropic.NewCodec(anthropic.Options{DefaultMaxTokens: opts.DefaultMaxTokens})
	case protocol.OpenAI:
		return openaicc.NewCodec()
	case protocol.OpenAIResponses:
		return openairesponses.NewCodec()
	}
	return nil
}
