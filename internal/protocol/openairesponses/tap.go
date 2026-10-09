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
	// Error 是终态事件里的错误对象：response.failed 带 {code, message}。code 由
	// apply 透出到 Summary.ErrorCode（词表映射归记账票 #215，这里只透出）。
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
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
		// 同款上游也可能把 TTL 细分照搬进来（#198）：Anthropic 兼容层（new-api 的
		// PatchClaudeMessageDeltaUsageData 就写这两个键）在 Responses 壳里也这么发过。
		// 容器缺席 = 没报细分，1h 落 0。
		CacheCreation *struct {
			Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
		// ReasoningTokens 用 *int：0 与「没这个键」要分得开（见 Summary 那边的
		// HasReasoningTokens）。
		OutputTokensDetails *struct {
			ReasoningTokens *int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

// 流式下每个生命周期事件都裹一层 response 对象，最终值来自 response.completed /
// response.incomplete / response.failed。裸 error 帧不裹 response，code 有两种
// 形状：顶层 code 与嵌套 error.code（解码侧 decode_response.go #162 起只认嵌套
// 那种；两读法分叉会让该形态从词表映射里漏掉，见 observeEvent 那条）。
type event struct {
	Type     string    `json:"type"`
	Response *response `json:"response"`
	Code     string    `json:"code"`
	Error    *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// observeEvent 返回这一帧是不是收尾帧：response.completed / incomplete，或流内错误
// （response.failed、裸 error 帧），同解码侧（#162）。JSON 里没写 type 时拿 SSE 的
// event 名兜底，同解码侧 respStreamState.frame——否则那种上游转换路径记 ok、透传却
// 记 stream_aborted。
func observeEvent(sum *protocol.Summary, sseEvent string, data []byte) bool {
	var e event
	if json.Unmarshal(data, &e) != nil {
		return false
	}
	if e.Type == "" {
		e.Type = sseEvent
	}
	// output_text.delta 之类的增量事件没有 response 字段，不取值。
	if e.Response != nil {
		apply(sum, e.Response)
	}
	// 裸 error 帧（不裹 response 对象）的 code 两种形状都认：顶层 code 与嵌套
	// error.code——解码侧只认嵌套那种，这里少认一种就会让该形态从词表映射
	// （#215 收场改判）里漏掉。response.failed 的 code 在 response.error 里，
	// 由 apply 取。两路都只透出，不在此做任何词表映射。
	if e.Type == "error" {
		if e.Code == "" && e.Error != nil {
			e.Code = e.Error.Code
		}
		if e.Code != "" {
			sum.ErrorCode = e.Code
		}
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
	// 流内错误码透出（#213）：记账票拿它做词表映射（如撞限 → plan_limit_exceeded，
	// #215），这里一个字都不改。拒绝形态（HTTP 层 {"detail":…}）没有 error.code，
	// 这一格恒空，那是 #215 从错误体里自己取的落点。
	if r.Error != nil && r.Error.Code != "" {
		sum.ErrorCode = r.Error.Code
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
	if c := u.CacheCreation; c != nil && c.Ephemeral1hInputTokens != 0 {
		sum.CacheWrite1hTokens = c.Ephemeral1hInputTokens
	}
	// 与上面几个「非零才覆盖」不同：这里 0 是有意义的取值（这次没思考），所以按
	// 键在不在来判，不按值。
	if d := u.OutputTokensDetails; d != nil && d.ReasoningTokens != nil {
		sum.ReasoningTokens, sum.HasReasoningTokens = *d.ReasoningTokens, true
	}
}
