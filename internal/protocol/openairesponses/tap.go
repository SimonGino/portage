// Package openairesponses holds the OpenAI Responses protocol adapters: the Tap
// (P0, 旁路解析透传流) and later the Codec (P1, 协议转换).
package openairesponses

import (
	"encoding/json"

	"github.com/SimonGino/portage/internal/protocol"
)

// Tap 从 Responses 响应里提取 usage / model / 终止状态。
type Tap struct {
	protocol.TapCore
}

func NewTap(stream bool) *Tap {
	t := &Tap{}
	t.TapCore = protocol.NewTapCore(stream, observeEvent, observeBody)
	return t
}

type response struct {
	Model             string `json:"model"`
	Status            string `json:"status"` // completed | incomplete | failed | in_progress
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		// Anthropic 兼容端点会在 Responses 形状上多带这一项（见 sub2api
		// apicompat/types.go ResponsesUsage）；官方 OpenAI 不发，缺失即零值。
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		// ReasoningTokens 用 *int：0 与「没这个键」要分得开（见 Summary 那边的
		// HasReasoningTokens）。
		OutputTokensDetails *struct {
			ReasoningTokens *int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

// 流式下每个生命周期事件都裹一层 response 对象，最终值来自 response.completed /
// response.incomplete / response.failed。
type event struct {
	Response *response `json:"response"`
}

func observeEvent(sum *protocol.Summary, data []byte) {
	var e event
	if json.Unmarshal(data, &e) != nil || e.Response == nil {
		// output_text.delta 之类的增量事件没有 response 字段，跳过。
		return
	}
	apply(sum, e.Response)
}

func observeBody(sum *protocol.Summary, body []byte) {
	var r response
	if json.Unmarshal(body, &r) != nil {
		return
	}
	apply(sum, &r)
}

func apply(sum *protocol.Summary, r *response) {
	if r.Model != "" {
		sum.Model = r.Model
	}
	// Responses 没有独立的 stop_reason 字段，终止信息在 status 上；截断时具体原因
	// （max_output_tokens 等）在 incomplete_details.reason 里，取更具体的那个。
	// in_progress 是中间态，不当终止原因记。
	if d := r.IncompleteDetails; d != nil && d.Reason != "" {
		sum.StopReason = d.Reason
	} else if r.Status != "" && r.Status != "in_progress" {
		sum.StopReason = r.Status
	}
	u := r.Usage
	if u == nil {
		return
	}
	if u.InputTokens != 0 {
		sum.InputTokens = u.InputTokens
	}
	if u.OutputTokens != 0 {
		sum.OutputTokens = u.OutputTokens
	}
	if d := u.InputTokensDetails; d != nil && d.CachedTokens != 0 {
		sum.CacheReadTokens = d.CachedTokens
	}
	if u.CacheCreationInputTokens != 0 {
		sum.CacheWriteTokens = u.CacheCreationInputTokens
	}
	// 与上面几个「非零才覆盖」不同：这里 0 是有意义的取值（这次没思考），所以按
	// 键在不在来判，不按值。
	if d := u.OutputTokensDetails; d != nil && d.ReasoningTokens != nil {
		sum.ReasoningTokens, sum.HasReasoningTokens = *d.ReasoningTokens, true
	}
}
