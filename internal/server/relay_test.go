package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

const (
	accessPointModel    = "gw-sonnet"
	upstreamModel       = "claude-sonnet-4-5-20250929"
	anthropicCredential = "sk-ant-upstream-secret"
)

// 刻意混入 cache_control、metadata.user_id、未知厂商字段、非常规键序与嵌套的同名
// model 键。除顶层 model 值外任何一个字节变了，都说明透传路径偷偷 re-marshal 了。
const anthropicRequest = `{"model":"gw-sonnet","metadata":{"user_id":"user_abc123"},"max_tokens":1024,` +
	`"system":[{"type":"text","text":"be brief","cache_control":{"type":"ephemeral"}}],` +
	`"vendor_unknown_field":{"model":"must-not-change","nested":[1,2,3],"weird":1.50},` +
	`"messages":[{"role":"user","content":"hi"}],"stream":false}`

// forwardedRequest 是上游应当收到的样子：只有顶层 model 值被翻译。
var forwardedRequest = strings.Replace(anthropicRequest,
	`"model":"`+accessPointModel+`"`, `"model":"`+upstreamModel+`"`, 1)

func newAnthropicGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	return gatewaytest.Start(t, db), up
}

func TestRelayForwardsBytesVerbatimApartFromModelName(t *testing.T) {
	gw, up := newAnthropicGateway(t)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}

	got := up.Last(t)
	if string(got.Body) != forwardedRequest {
		t.Errorf("上游收到的 body 与「仅翻译顶层 model」的期望不一致\n期望: %s\n收到: %s", forwardedRequest, got.Body)
	}
	if got.Path != "/v1/messages" {
		t.Errorf("上游 path = %q, 期望 /v1/messages", got.Path)
	}
}

func TestRelayLeavesBodyUntouchedWhenNamesCoincide(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, accessPointModel, anthropicCredential)
	gw := gatewaytest.Start(t, db)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if body := string(up.Last(t).Body); body != anthropicRequest {
		t.Errorf("对外名与纳管模型名相同时 body 仍被改动\n发出: %s\n收到: %s", anthropicRequest, body)
	}
}

func TestRelayReturnsUpstreamBytesVerbatim(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	const upstreamBody = `{"id":"msg_01","type":"message","role":"assistant",` +
		`"content":[{"type":"text","text":"你好"}],"usage":{"input_tokens":7,"output_tokens":3}}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if body := gatewaytest.ReadBody(t, resp); body != upstreamBody {
		t.Errorf("客户端收到的 body 与上游返回的不一致\n上游: %s\n客户端: %s", upstreamBody, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, 期望原样回传 application/json", ct)
	}
}

func TestRelayPassesUpstreamErrorsThrough(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		wantHdr [2]string
	}{
		{
			name:    "429 带 Retry-After",
			status:  http.StatusTooManyRequests,
			header:  map[string]string{"Content-Type": "application/json", "Retry-After": "30"},
			body:    `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
			wantHdr: [2]string{"Retry-After", "30"},
		},
		{
			name:   "500",
			status: http.StatusInternalServerError,
			header: map[string]string{"Content-Type": "application/json"},
			body:   `{"type":"error","error":{"type":"api_error","message":"boom"}}`,
		},
		{
			name:   "非 JSON 错误体",
			status: http.StatusBadGateway,
			header: map[string]string{"Content-Type": "text/html"},
			body:   "<html><body>502 Bad Gateway</body></html>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := newAnthropicGateway(t)
			up.RespondWith(tc.status, tc.header, tc.body)

			resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)

			if resp.StatusCode != tc.status {
				t.Errorf("状态码 = %d, 期望原样 %d", resp.StatusCode, tc.status)
			}
			if body := gatewaytest.ReadBody(t, resp); body != tc.body {
				t.Errorf("body = %q, 期望原样 %q", body, tc.body)
			}
			if tc.wantHdr[0] != "" && resp.Header.Get(tc.wantHdr[0]) != tc.wantHdr[1] {
				t.Errorf("%s = %q, 期望原样回传 %q", tc.wantHdr[0], resp.Header.Get(tc.wantHdr[0]), tc.wantHdr[1])
			}
		})
	}
}

func TestRelayRebuildsHeadersFromWhitelist(t *testing.T) {
	gw, up := newAnthropicGateway(t)

	// 两个凭证头填的是**真实有效**的网关 key（M1 起鉴权就认它），这样断言才有力：
	// 它能过鉴权、却一个字节都不该到上游。填一把无效 key 的话请求会停在 401，
	// 「没转发」变成恒真，白名单回归就抓不住了。
	gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{
		"Authorization":       "Bearer " + gatewaytest.DefaultKey,
		"x-api-key":           gatewaytest.DefaultKey,
		"Cookie":              "session=leak-me",
		"X-Forwarded-For":     "192.168.1.7",
		"anthropic-beta":      "context-1m-2025-08-07",
		"anthropic-version":   "2024-10-22",
		"Te":                  "trailers",
		"Proxy-Authorization": "Basic bGVhaw==",
		"X-Client-Trace-Id":   "should-not-cross",
	})

	got := up.Last(t)
	for _, name := range []string{"Te", "Proxy-Authorization", "Upgrade", "X-Client-Trace-Id"} {
		if v := got.Header.Get(name); v != "" {
			t.Errorf("白名单外的头 %s 透到了上游: %q", name, v)
		}
	}
	if strings.Contains(got.Host, gw.Listener.Addr().String()) {
		t.Errorf("Host = %q, 应是上游自己的 host 而非网关的", got.Host)
	}
	if v := got.Header.Get("x-api-key"); v != anthropicCredential {
		t.Errorf("x-api-key = %q, 期望注入渠道凭证", v)
	}
	if v := got.Header.Get("Authorization"); v != "" {
		t.Errorf("Authorization 泄漏到上游: %q（M1 起那里是网关 key）", v)
	}
	if v := got.Header.Get("Cookie"); v != "" {
		t.Errorf("Cookie 泄漏到上游: %q", v)
	}
	if v := got.Header.Get("X-Forwarded-For"); v != "" {
		t.Errorf("不该注入 X-Forwarded-For, 收到 %q", v)
	}
	if v := got.Header.Get("anthropic-beta"); v != "context-1m-2025-08-07" {
		t.Errorf("anthropic-beta = %q, 期望原样转发", v)
	}
	if v := got.Header.Get("anthropic-version"); v != "2024-10-22" {
		t.Errorf("anthropic-version = %q, 期望取客户端值", v)
	}
}

func TestRelayDefaultsAnthropicVersion(t *testing.T) {
	gw, up := newAnthropicGateway(t)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if v := up.Last(t).Header.Get("anthropic-version"); v != "2023-06-01" {
		t.Errorf("anthropic-version = %q, 客户端未给时应补 2023-06-01", v)
	}
}

func TestRelayNormalisesBaseURLTrailingSlash(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "gw-sonnet", "anthropic", up.URL+"/", "claude-sonnet-4-5-20250929", anthropicCredential)
	gw := gatewaytest.Start(t, db)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if path := up.Last(t).Path; path != "/v1/messages" {
		t.Errorf("path = %q, base_url 尾斜杠未归一化", path)
	}
}

// 客户端的查询串整串照抄给上游（portage-legacy#20）。实测 Claude Code 发的是
// `POST /v1/messages?beta=true`——原来 buildURL 只拼固定后缀，那个参数被静默丢掉，
// 而丢没丢不看日志根本发现不了。
func TestRelayForwardsClientQueryStringVerbatim(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"Claude Code 实发的形态", "/v1/messages?beta=true", "beta=true"},
		{"多个参数保序、不重排", "/v1/messages?z=1&a=2&z=3", "z=1&a=2&z=3"},
		{"值里的编码不解码再编码", "/v1/messages?q=a%2Bb%20c", "q=a%2Bb%20c"},
		{"没有查询串时不产生裸问号", "/v1/messages", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := newAnthropicGateway(t)

			gw.Post(t, tc.path, anthropicRequest, nil)

			got := up.Last(t)
			if got.RawQuery != tc.want {
				t.Errorf("上游收到的查询串 = %q, 期望 %q", got.RawQuery, tc.want)
			}
			if got.Path != "/v1/messages" {
				t.Errorf("上游 path = %q, 期望 /v1/messages（查询串不该混进 path）", got.Path)
			}
		})
	}
}

// base_url 自带路径前缀（百炼那类兼容端点）时，查询串仍接在协议子路径之后而不是
// 前缀之后——拼错的话请求会打到 .../compatible-mode?beta=true/v1/messages。
func TestRelayAppendsQueryAfterPrefixedBaseURL(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic",
		up.URL+"/compatible-mode", upstreamModel, anthropicCredential)
	gw := gatewaytest.Start(t, db)

	gw.Post(t, "/v1/messages?beta=true", anthropicRequest, nil)

	got := up.Last(t)
	if got.Path != "/compatible-mode/v1/messages" {
		t.Errorf("上游 path = %q, 期望 /compatible-mode/v1/messages", got.Path)
	}
	if got.RawQuery != "beta=true" {
		t.Errorf("上游查询串 = %q, 期望 beta=true", got.RawQuery)
	}
}

func TestRelayRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantType   string
	}{
		{"接入点不存在", `{"model":"no-such-access-point","messages":[]}`, http.StatusNotFound, "not_found_error"},
		{"model 缺失", `{"messages":[]}`, http.StatusBadRequest, "invalid_request_error"},
		{"body 非法 JSON", `{"model":`, http.StatusBadRequest, "invalid_request_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := newAnthropicGateway(t)

			resp := gw.Post(t, "/v1/messages", tc.body, nil)
			body := gatewaytest.ReadBody(t, resp)

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("状态码 = %d, 期望 %d；body=%s", resp.StatusCode, tc.wantStatus, body)
			}
			assertAnthropicError(t, body, tc.wantType)
			assertNoSecrets(t, body, anthropicCredential, up.URL)
			if up.Count() != 0 {
				t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
			}
		})
	}
}

func TestRelayReportsUnreachableUpstreamWithoutLeakingSecrets(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	deadURL := up.URL
	up.Close() // 端口随即空出，网关将连不上

	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "gw-sonnet", "anthropic", deadURL, "claude-sonnet-4-5-20250929", anthropicCredential)
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("状态码 = %d, 期望 502；body=%s", resp.StatusCode, body)
	}
	assertAnthropicError(t, body, "api_error")
	assertNoSecrets(t, body, anthropicCredential, deadURL)
}

func assertAnthropicError(t *testing.T, body, wantType string) {
	t.Helper()
	var parsed struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v；body=%s", err, body)
	}
	if parsed.Type != "error" {
		t.Errorf("顶层 type = %q, Anthropic 原生错误应为 error", parsed.Type)
	}
	if parsed.Error.Type != wantType {
		t.Errorf("error.type = %q, 期望 %q", parsed.Error.Type, wantType)
	}
	if parsed.Error.Message == "" {
		t.Error("error.message 为空")
	}
}

// assertNoSecrets 必须显式收下这次配置里用的凭证——写死一个常量的话，凡是种了
// 别的凭证的用例，凭证那一半断言就恒真、抓不住任何泄露。
func assertNoSecrets(t *testing.T, body, credential, baseURL string) {
	t.Helper()
	if credential == "" {
		t.Fatal("assertNoSecrets 需要本次配置里真实使用的凭证")
	}
	if strings.Contains(body, credential) {
		t.Errorf("错误响应泄漏了上游凭证: %s", body)
	}
	if strings.Contains(body, baseURL) {
		t.Errorf("错误响应泄漏了上游 base_url: %s", body)
	}
}
