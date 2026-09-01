package upstream

import (
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
)

// 认证头写法逐档钉死（口径层 v1.13，#82）：default 按协议惯例，bearer/raw 改写
// Authorization 本身；raw 不带 Bearer 前缀——PAI-EAS 这类网关只认裸 token，带前缀
// 反而 401，这一档存在的意义就在那半个前缀上。协议头（anthropic-version）与认证
// 档位无关，哪档都得在。
func TestApplyHeadersAuthScheme(t *testing.T) {
	cases := []struct {
		name    string
		proto   protocol.Protocol
		scheme  string
		xAPIKey string
		auth    string
	}{
		{"default+anthropic 发 x-api-key", protocol.Anthropic, store.AuthSchemeDefault, "sk-x", ""},
		{"default+openai 发 Bearer", protocol.OpenAI, store.AuthSchemeDefault, "", "Bearer sk-x"},
		{"bearer+anthropic 改发 Bearer", protocol.Anthropic, store.AuthSchemeBearer, "", "Bearer sk-x"},
		{"raw+anthropic 发裸 Authorization", protocol.Anthropic, store.AuthSchemeRaw, "", "sk-x"},
		{"raw+openai 发裸 Authorization", protocol.OpenAI, store.AuthSchemeRaw, "", "sk-x"},
		// 手写 SQL 灌进来的脏值当 default 用——为一个拼错的档位名让请求失败不值当。
		{"认不得的档位当 default", protocol.Anthropic, "x-api-key", "sk-x", ""},
		{"空串当 default", protocol.OpenAI, "", "", "Bearer sk-x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			applyHeaders(h, http.Header{}, tc.proto, tc.scheme, "sk-x", false)
			if got := h.Get("x-api-key"); got != tc.xAPIKey {
				t.Errorf("x-api-key = %q，期望 %q", got, tc.xAPIKey)
			}
			if got := h.Get("Authorization"); got != tc.auth {
				t.Errorf("Authorization = %q，期望 %q", got, tc.auth)
			}
			if tc.proto == protocol.Anthropic && h.Get("anthropic-version") == "" {
				t.Error("anthropic-version 不该随认证档位丢：它是协议头，与认证无关")
			}
		})
	}
}
