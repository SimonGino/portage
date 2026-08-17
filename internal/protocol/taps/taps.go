// Package taps resolves a protocol to its Tap implementation.
//
// 单独成一个包，是因为 protocol 不能反向导入自己的三个子包（循环导入）。放这里
// 之后，「协议 → Tap」这张表全仓只有一份：转发路径、golden 驱动、录制反代都调它。
package taps

import (
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// New 按协议挑 Tap；协议不认得时返回 nil，由调用方决定是跳过旁路还是报错。
//
// 传的是**渠道**协议而非入口协议：Tap 看的是上游回的字节。临时闸下两者必然相等，
// M2 放开转换后就不是了。
func New(p protocol.Protocol, stream bool) protocol.Tap {
	switch p {
	case protocol.Anthropic:
		return anthropic.NewTap(stream)
	case protocol.OpenAI:
		return openaicc.NewTap(stream)
	case protocol.OpenAIResponses:
		return openairesponses.NewTap(stream)
	}
	return nil
}
