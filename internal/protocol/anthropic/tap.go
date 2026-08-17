// Package anthropic holds the Anthropic Messages protocol adapters: the Tap
// (P0, 旁路解析透传流) and later the Codec (P1, 协议转换).
package anthropic

import (
	"encoding/json"

	"github.com/SimonGino/portage/internal/protocol"
)

// Tap 从 Anthropic Messages 响应里提取 usage / model / stop_reason。
type Tap struct {
	protocol.TapCore
}

// NewTap 返回一个 Anthropic Tap；stream 决定按 SSE 帧还是按整包 JSON 解析。
func NewTap(stream bool) *Tap {
	t := &Tap{}
	t.TapCore = protocol.NewTapCore(stream, observeEvent, observeBody)
	return t
}

// usage 覆盖流式与非流式两处：Anthropic 的 input/cache 计数只在 message_start
// 里出现一次，output_tokens 则在 message_delta 里累计刷新。
type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type message struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Usage      *usage `json:"usage"`
}

type event struct {
	Type    string   `json:"type"`
	Message *message `json:"message"` // message_start
	Delta   *struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"` // message_delta（content_block_delta 也有 delta，但不带 stop_reason）
	Usage *usage `json:"usage"` // message_delta
}

func observeEvent(sum *protocol.Summary, data []byte) {
	var e event
	if json.Unmarshal(data, &e) != nil {
		// 心跳注释、ping、上游自定义事件都可能不是对象——不是错误，跳过即可。
		return
	}
	if e.Message != nil {
		apply(sum, e.Message.Model, e.Message.StopReason, e.Message.Usage)
	}
	if e.Delta != nil {
		setStopReason(sum, e.Delta.StopReason)
	}
	applyUsage(sum, e.Usage)
}

func observeBody(sum *protocol.Summary, body []byte) {
	var m message
	if json.Unmarshal(body, &m) != nil {
		return
	}
	apply(sum, m.Model, m.StopReason, m.Usage)
}

func apply(sum *protocol.Summary, model, stopReason string, u *usage) {
	if model != "" {
		sum.Model = model
	}
	setStopReason(sum, stopReason)
	applyUsage(sum, u)
}

// applyUsage 只覆盖非零值：message_delta 里的 usage 往往只带 output_tokens，
// 整体赋值会把 message_start 报过的 input/cache 计数清掉。
func applyUsage(sum *protocol.Summary, u *usage) {
	if u == nil {
		return
	}
	if u.InputTokens != 0 {
		sum.InputTokens = u.InputTokens
	}
	if u.OutputTokens != 0 {
		sum.OutputTokens = u.OutputTokens
	}
	if u.CacheReadInputTokens != 0 {
		sum.CacheReadTokens = u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != 0 {
		sum.CacheWriteTokens = u.CacheCreationInputTokens
	}
}

func setStopReason(sum *protocol.Summary, reason string) {
	if reason != "" {
		sum.StopReason = reason
	}
}
