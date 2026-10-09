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
	// CacheCreation 是写入的 TTL 细分（#198）：5m + 1h = 上面的总数。容器缺席
	// = 上游没报细分（老式/兼容上游），1h 落 0、整笔按 5 分钟档计。真字节里两键
	// 恒同现（golden/anthropic-cache-*，Claude Code 的 usage 也两个都带——tokscale
	// #1373），只读 1h：5m 那半边 = 总数 - 1h，两处各存一份只会漂。
	CacheCreation *struct {
		Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	// service_tier / speed 都住在 usage 里（#103），不在 message 顶层。
	ServiceTier string `json:"service_tier"`
	Speed       string `json:"speed"`
	// OutputTokensDetails 装思考 token。**键名三家各不同形**：容器名与 Responses
	// 一样（output_tokens_details）、字段名与 CC 一样（reasoning_tokens）——而这一侧
	// 两个都不能照抄，Anthropic 叫 thinking_tokens（口径层 v0.79，实采字节 249/310）。
	//
	// ThinkingTokens 用 *int：0 与「没这个键」要分得开（见 Summary 那边的
	// HasReasoningTokens）。**不带思考的调用两种形态都实见过**（口径层 v0.85）——
	// 本库六份非思考转录连这个容器都不发，#5 现场则是容器在、值为 0——所以判据只能
	// 是键在不在，不能预设上游发哪一种。
	OutputTokensDetails *struct {
		ThinkingTokens *int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
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

// observeEvent 返回这一帧是不是收尾帧：message_stop，或流内 error 帧（#162）。
func observeEvent(sum *protocol.Summary, _ string, data []byte) bool {
	var e event
	if json.Unmarshal(data, &e) != nil {
		// 心跳注释、ping、上游自定义事件都可能不是对象——不是错误，跳过即可。
		return false
	}
	if e.Message != nil {
		apply(sum, e.Message.Model, e.Message.StopReason, e.Message.Usage)
	}
	if e.Delta != nil {
		setStopReason(sum, e.Delta.StopReason)
	}
	applyUsage(sum, e.Usage)
	return e.Type == "message_stop" || e.Type == "error"
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
	// 1h 细分同上面那批「只覆盖非零」：message_start 带了容器、message_delta 只刷新
	// output_tokens（真实字节如此），整体赋值会把 start 那份细分吃掉。
	if c := u.CacheCreation; c != nil && c.Ephemeral1hInputTokens != 0 {
		sum.CacheWrite1hTokens = c.Ephemeral1hInputTokens
	}
	if u.ServiceTier != "" {
		sum.ServiceTier = u.ServiceTier
	}
	if u.Speed != "" {
		sum.Speed = u.Speed
	}
	// 思考 token 不受上面那条「只覆盖非零值」管：判据是**键在不在**，不是值是不是 0。
	// 流式下这一格只在 message_delta 里出现（message_start 那帧没有这个容器），报 0
	// 的那一档若按非零覆盖就会被吃掉，落库退回 NULL——正是三档要分开的那两档。
	if d := u.OutputTokensDetails; d != nil && d.ThinkingTokens != nil {
		sum.ReasoningTokens, sum.HasReasoningTokens = *d.ThinkingTokens, true
	}
}

func setStopReason(sum *protocol.Summary, reason string) {
	if reason != "" {
		sum.StopReason = reason
	}
}
