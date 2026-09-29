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
	ServiceTier       string `json:"service_tier"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
			// 官方 OpenAI 的缓存写入报在这里（new-api 48068ce92、sub2api 4a2b10c94）。
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		// Anthropic 兼容端点把缓存写入放在顶层这一项（见 sub2api apicompat/types.go
		// ResponsesUsage）。与上面那个键的先后同 decode_response.go：顶层非零时它说了算。
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
	Type     string    `json:"type"`
	Response *response `json:"response"`
}

// observeEvent 返回这一帧是不是收尾帧：response.completed / incomplete，或流内错误
// （response.failed、裸 error 帧），同解码侧（#162）。
func observeEvent(sum *protocol.Summary, data []byte) bool {
	var e event
	if json.Unmarshal(data, &e) != nil {
		return false
	}
	// output_text.delta 之类的增量事件没有 response 字段，不取值。
	if e.Response != nil {
		apply(sum, e.Response)
	}
	switch e.Type {
	case "response.completed", "response.incomplete", "response.failed", "error":
		return true
	}
	return false
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
	// in_progress / queued 是中间态，不当终止原因记——记了会让截断判据（#162）把
	// 断在终态之前的流当成报过停因。
	if d := r.IncompleteDetails; d != nil && d.Reason != "" {
		sum.StopReason = d.Reason
	} else if r.Status != "" && r.Status != "in_progress" && r.Status != "queued" {
		sum.StopReason = r.Status
	}
	// service_tier 同理（#103）：created / in_progress（后台模式还有 queued）回显的
	// 是请求里的档（常见 auto），终态事件才报实际走的档（真实转录 auto → default；
	// sub2api upstream_response_model.go 同一判据）。流断在终态之前就宁可留空。
	if r.ServiceTier != "" && r.Status != "in_progress" && r.Status != "queued" {
		sum.ServiceTier = r.ServiceTier
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
	if d := u.InputTokensDetails; d != nil {
		if d.CachedTokens != 0 {
			sum.CacheReadTokens = d.CachedTokens
		}
		if d.CacheWriteTokens != 0 {
			sum.CacheWriteTokens = d.CacheWriteTokens
		}
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
