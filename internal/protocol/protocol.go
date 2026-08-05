// Package protocol names the three wire protocols the gateway speaks and owns
// each one's native error representation.
//
// Tap（透传旁路 usage 提取）与 Codec（跨协议编解码）按 docs/MVP设计草案.md §3
// 落在本包的子包里，分别属于 M0-4 与 M2。
package protocol

import (
	"encoding/json"
	"net/http"
)

type Protocol string

const (
	Anthropic       Protocol = "anthropic"
	OpenAICC        Protocol = "openai_cc"
	OpenAIResponses Protocol = "openai_responses"
)

func (p Protocol) Valid() bool {
	switch p {
	case Anthropic, OpenAICC, OpenAIResponses:
		return true
	}
	return false
}

// Endpoint is one inbound 端点（口径层 §2.1 用词），and the protocol it speaks.
//
// Path doubles as the suffix appended to a 渠道 base_url: base_url 存「协议子路径
// 之前」的前缀，入站与出站子路径同形（docs/MVP设计草案.md §6.1）。
type Endpoint struct {
	Path  string
	Proto Protocol
}

// CC 与 Responses 端点随 M0-3 落地。
var (
	EndpointMessages = Endpoint{"/v1/messages", Anthropic}
	// count_tokens 是 Anthropic 独有端点；命中非 anthropic 渠道时由请求时临时闸
	// 回 501，不做估算（估算属 M2）。
	EndpointCountTokens = Endpoint{"/v1/messages/count_tokens", Anthropic}
)

// WriteError renders msg in the protocol's own error shape so a harness can
// parse it. Callers must never pass upstream credentials or base_url in msg.
func (p Protocol) WriteError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p.errorBody(status, msg))
}

func (p Protocol) errorBody(status int, msg string) any {
	if p == Anthropic {
		return map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    anthropicErrorType(status),
				"message": msg,
			},
		}
	}
	return map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    openaiErrorType(status),
			"param":   nil,
			"code":    nil,
		},
	}
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func openaiErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}
