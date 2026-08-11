package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 每个转发端点都得认 key。逐个列而不是只测一个：闸是按端点挂的，漏挂一个的表现
// 就是那一条路对全网段敞开，而其余用例照常绿。
func TestEveryRelayEndpointRejectsMissingKey(t *testing.T) {
	gw, up := newAnthropicGateway(t)

	for _, tc := range []struct{ path, body string }{
		{"/v1/messages", anthropicRequest},
		{"/v1/messages/count_tokens", anthropicRequest},
		{"/v1/chat/completions", ccRequest},
		{"/v1/responses", `{"model":"gw-sonnet","input":"hi"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			before := up.Count()
			// 空串即「不出示凭证」——Post 看键在不在，不看值。
			resp := gw.Post(t, tc.path, tc.body, map[string]string{"x-api-key": ""})
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("状态码 = %d, 期望 401", resp.StatusCode)
			}
			if up.Count() != before {
				t.Error("401 的请求居然打到了上游")
			}
		})
	}
}

func TestModelsListRejectsMissingKey(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	resp := gw.GetWith(t, "/v1/models", map[string]string{"x-api-key": ""})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, 期望 401——不认就等于把接入点清单对全网段公开", resp.StatusCode)
	}
	// 401 的 body 里不该出现任何接入点名。
	if body := gatewaytest.ReadBody(t, resp); strings.Contains(body, accessPointModel) {
		t.Errorf("401 响应泄漏了接入点清单: %s", body)
	}
}

// /healthz 不鉴权：探活没地方放 key，而它只回一个「库还连得上吗」。
func TestHealthzNeedsNoKey(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	resp := gw.GetWith(t, "/healthz", map[string]string{"x-api-key": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", resp.StatusCode)
	}
}

// 两个凭证头都要认：harness 各发各的，Claude Code 走 x-api-key、Codex 走 Bearer。
func TestBothCredentialHeadersAreAccepted(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	for name, header := range map[string]map[string]string{
		"x-api-key":   {"x-api-key": gatewaytest.DefaultKey},
		"Bearer":      {"Authorization": "Bearer " + gatewaytest.DefaultKey},
		"bearer 小写":   {"Authorization": "bearer " + gatewaytest.DefaultKey},
		"两个头一起发（同一把）": {"x-api-key": gatewaytest.DefaultKey, "Authorization": "Bearer " + gatewaytest.DefaultKey},
	} {
		t.Run(name, func(t *testing.T) {
			resp := gw.Post(t, "/v1/messages", anthropicRequest, header)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
			}
		})
	}
}

// 一个头里留着过期值、正确的 key 在另一个头里——两个头逐个试，不该 401。
// 这是「只认第一个非空头」那种写法的回归护栏。
func TestStaleHeaderDoesNotMaskTheValidOne(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{
		"x-api-key":     "sk-ptg-stale-and-wrong",
		"Authorization": "Bearer " + gatewaytest.DefaultKey,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
}

func TestDisabledKeyIsRejected(t *testing.T) {
	gw, _ := newAnthropicGateway(t)
	gatewaytest.SeedAPIKey(t, gw.DB, "已停用", "sk-ptg-disabled")
	if _, err := gw.DB.Exec(`UPDATE api_keys SET disabled = 1 WHERE name = ?`, "已停用"); err != nil {
		t.Fatal(err)
	}

	resp := gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": "sk-ptg-disabled"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, 期望 401", resp.StatusCode)
	}
	// 「不存在」与「已停用」回同一句话：区分开就等于告诉扫描者这把 key 是存在的。
	body := gatewaytest.ReadBody(t, resp)
	for _, leaked := range []string{"disabled", "停用", "sk-ptg-disabled", "已停用"} {
		if strings.Contains(body, leaked) {
			t.Errorf("401 回显泄漏了 %q: %s", leaked, body)
		}
	}
}

// 401 走协议原生格式，不是 gin 默认 JSON——否则 harness 认不出来，表现成解析失败
// 而不是「key 不对」。
func TestUnauthorizedUsesIngressProtocolShape(t *testing.T) {
	gw, up := newAnthropicGateway(t)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})
	body := gatewaytest.ReadBody(t, resp)
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"error"`) {
		t.Errorf("401 body 不是 Anthropic 的错误形状: %s", body)
	}
	assertNoSecrets(t, body, anthropicCredential, up.URL)
}

// 鉴权失败也要落一行调用日志，否则被刷的时候日志里什么都看不到（#22）。
func TestUnauthorizedStillLogsACall(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})

	line := gw.LastCall(t)
	if got := line.Int64("status"); got != http.StatusUnauthorized {
		t.Errorf("status = %d, 期望 401", got)
	}
	if got := line.Str("outcome"); got != "unauthorized" {
		t.Errorf("outcome = %q, 期望 unauthorized", got)
	}
	if got := line.Str("api_key"); got != "" {
		t.Errorf("api_key = %q, 没认出来就该是空的", got)
	}
}

// 认出来之后，key 的**名字**进日志——用量按 key 归集要靠它。
func TestCallLogCarriesKeyNameNotTheKey(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	line := gw.LastCall(t)
	if got := line.Str("api_key"); got != "test-default" {
		t.Errorf("api_key = %q, 期望 key 的名字 test-default", got)
	}
	// 整行渲染文本里不该出现 key 明文或它的 hash。
	raw := gw.RawLog()
	for _, leaked := range []string{gatewaytest.DefaultKey, auth.Hash(gatewaytest.DefaultKey)} {
		if strings.Contains(raw, leaked) {
			t.Errorf("日志里出现了凭证材料 %q", leaked)
		}
	}
}
