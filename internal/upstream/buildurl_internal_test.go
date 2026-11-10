package upstream

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// buildURL 的端点路径覆盖（#175）：只替换三个消息端点；空覆盖与共享端点（含 Anthropic
// 的 count_tokens）行为照旧；客户端查询串仍整串接在尾部。
func TestBuildURLPathOverride(t *testing.T) {
	for _, tc := range []struct {
		name     string
		base     string
		override string
		ep       protocol.Endpoint
		query    string
		want     string
	}{
		{"无覆盖照旧", "https://api.example.com", "", protocol.EndpointMessages, "", "https://api.example.com/v1/messages"},
		{"覆盖 anthropic", "https://api.example.com", "/custom/messages", protocol.EndpointMessages, "", "https://api.example.com/custom/messages"},
		{"Codex 后端 responses", "https://chatgpt.com/backend-api/codex", "/responses", protocol.EndpointResponses, "", "https://chatgpt.com/backend-api/codex/responses"},
		{"共享端点不覆盖", "https://api.example.com", "/responses", protocol.Endpoint{Path: "/v1/models", Proto: protocol.OpenAI}, "", "https://api.example.com/v1/models"},
		{"count_tokens 不覆盖", "https://api.example.com", "/custom/messages", protocol.EndpointCountTokens, "", "https://api.example.com/v1/messages/count_tokens"},
		{"覆盖加查询串", "https://api.example.com", "/responses", protocol.EndpointResponses, "beta=true", "https://api.example.com/responses?beta=true"},
		{"base 尾斜杠", "https://api.example.com/", "/responses", protocol.EndpointResponses, "", "https://api.example.com/responses"},
	} {
		if got := buildURL(tc.base, tc.override, tc.ep, tc.query); got != tc.want {
			t.Errorf("%s：buildURL = %q，期望 %q", tc.name, got, tc.want)
		}
	}
}
