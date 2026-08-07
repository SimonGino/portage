// Package codecs resolves a protocol to its Codec implementation.
//
// 与 internal/protocol/taps 同构、同理由：protocol 不能反向导入自己的三个子包
// （循环导入）。放这里之后，「协议 → Codec」这张表全仓只有一份。
package codecs

import (
	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/protocol/anthropic"
	"github.com/SimonGino/ai-gateway/internal/protocol/openaicc"
	"github.com/SimonGino/ai-gateway/internal/protocol/openairesponses"
)

// New 按协议挑 Codec；协议不认得时返回 nil，由调用方决定是报错还是退回透传。
//
// 转换路径要**两个** Codec：入口协议的解出 canonical，渠道协议的编回去。
// 两者相等时不该走这条路——同协议透传不做 decode→encode 转码（口径层硬约束）。
func New(p protocol.Protocol) protocol.Codec {
	switch p {
	case protocol.Anthropic:
		return anthropic.NewCodec()
	case protocol.OpenAICC:
		return openaicc.NewCodec()
	case protocol.OpenAIResponses:
		return openairesponses.NewCodec()
	}
	return nil
}
