package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/ai-gateway/internal/gatewaytest"
)

const ccRequest = `{"model":"gw-cc","messages":[{"role":"user","content":"hi"}],` +
	`"tools":[{"type":"function","function":{"name":"get_weather"}}],"vendor_extra":{"model":"nested"}}`

const responsesRequest = `{"model":"gw-resp","input":[{"role":"user","content":"hi"}],` +
	`"reasoning":{"effort":"low"},"store":false}`

// newOpenAIGateway 起一个入口协议为 proto 的网关，接入点对外名 apModel、纳管模型名
// upstreamName。
func newOpenAIGateway(t *testing.T, apModel, proto, upstreamName string) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, apModel, proto, up.URL, upstreamName, "sk-upstream-secret")
	return gatewaytest.Start(t, db), up
}

func TestChatCompletionsPassthrough(t *testing.T) {
	gw, up := newOpenAIGateway(t, "gw-cc", "openai_cc", "qwen3-max-2025-09-23")
	const upstreamBody = `{"id":"chatcmpl-1","object":"chat.completion",` +
		`"choices":[{"message":{"role":"assistant","content":"你好"}}],"usage":{"total_tokens":9}}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/chat/completions", ccRequest, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if body := gatewaytest.ReadBody(t, resp); body != upstreamBody {
		t.Errorf("响应体不是逐字节回传\n上游: %s\n客户端: %s", upstreamBody, body)
	}

	got := up.Last(t)
	if got.Path != "/v1/chat/completions" {
		t.Errorf("上游 path = %q", got.Path)
	}
	want := strings.Replace(ccRequest, `"model":"gw-cc"`, `"model":"qwen3-max-2025-09-23"`, 1)
	if string(got.Body) != want {
		t.Errorf("请求体除顶层 model 外应逐字节保真\n期望: %s\n收到: %s", want, got.Body)
	}
}

func TestResponsesPassthrough(t *testing.T) {
	gw, up := newOpenAIGateway(t, "gw-resp", "openai_responses", "gpt-5")
	const upstreamBody = `{"id":"resp_1","object":"response","output":[{"type":"message"}]}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/responses", responsesRequest, nil)

	if body := gatewaytest.ReadBody(t, resp); body != upstreamBody {
		t.Errorf("响应体不是逐字节回传\n上游: %s\n客户端: %s", upstreamBody, body)
	}
	got := up.Last(t)
	if got.Path != "/v1/responses" {
		t.Errorf("上游 path = %q", got.Path)
	}
	want := strings.Replace(responsesRequest, `"model":"gw-resp"`, `"model":"gpt-5"`, 1)
	if string(got.Body) != want {
		t.Errorf("请求体除顶层 model 外应逐字节保真\n期望: %s\n收到: %s", want, got.Body)
	}
}

func TestOpenAIStreamPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		proto   string
		apModel string
		body    string
	}{
		{"chat completions", "/v1/chat/completions", "openai_cc", "gw-cc",
			`{"model":"gw-cc","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", "openai_responses", "gw-resp",
			`{"model":"gw-resp","stream":true,"input":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := newOpenAIGateway(t, tc.apModel, tc.proto, "upstream-model")
			frames := []string{
				"data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n",
				"data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\r\n\r\n",
				"data: [DONE]\n\n",
			}
			streamUpstream(t, up, frames...)()

			resp := gw.Post(t, tc.path, tc.body, nil)
			body := gatewaytest.ReadBody(t, resp)

			if want := strings.Join(frames, ""); body != want {
				t.Errorf("流式字节与上游写出的不一致\n上游: %q\n客户端: %q", want, body)
			}
		})
	}
}

// 凭证注入按渠道协议分叉：OpenAI 系走 Authorization: Bearer，绝不能同时冒出
// Anthropic 的 x-api-key。
func TestOpenAIChannelUsesBearerCredential(t *testing.T) {
	gw, up := newOpenAIGateway(t, "gw-cc", "openai_cc", "qwen3-max")

	gw.Post(t, "/v1/chat/completions", ccRequest, map[string]string{
		"Authorization": "Bearer sk-aig-client-gateway-key",
	})

	got := up.Last(t)
	if v := got.Header.Get("Authorization"); v != "Bearer sk-upstream-secret" {
		t.Errorf("Authorization = %q, 期望注入渠道凭证", v)
	}
	if v := got.Header.Get("x-api-key"); v != "" {
		t.Errorf("openai_cc 渠道不该收到 x-api-key: %q", v)
	}
	if v := got.Header.Get("anthropic-version"); v != "" {
		t.Errorf("openai_cc 渠道不该收到 anthropic-version: %q", v)
	}
}

// 临时闸两个方向都要按**入口**协议的原生格式回错——客户端只认得它自己那套。
func TestCrossProtocolGateAnswersInInboundFormat(t *testing.T) {
	t.Run("Anthropic 入口打到 openai_cc 渠道", func(t *testing.T) {
		gw, up := newOpenAIGateway(t, "gw-sonnet", "openai_cc", "qwen3-max")

		resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
		body := gatewaytest.ReadBody(t, resp)

		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("状态码 = %d, 期望 501；body=%s", resp.StatusCode, body)
		}
		assertAnthropicError(t, body, "api_error")
		if !strings.Contains(body, "尚未实现") {
			t.Errorf("文案应点明转换路径尚未实现: %s", body)
		}
		assertNoSecrets(t, body, up.URL)
	})

	t.Run("CC 入口打到 anthropic 渠道", func(t *testing.T) {
		up := gatewaytest.NewUpstream(t)
		db := gatewaytest.NewDB(t)
		gatewaytest.SeedPassthrough(t, db, "gw-cc", "anthropic", up.URL, "claude-sonnet-4-5", "sk-ant-upstream-secret")
		gw := gatewaytest.Start(t, db)

		resp := gw.Post(t, "/v1/chat/completions", ccRequest, nil)
		body := gatewaytest.ReadBody(t, resp)

		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("状态码 = %d, 期望 501；body=%s", resp.StatusCode, body)
		}
		assertOpenAIError(t, body)
		if !strings.Contains(body, "尚未实现") {
			t.Errorf("文案应点明转换路径尚未实现: %s", body)
		}
		if strings.Contains(body, "sk-ant-upstream-secret") || strings.Contains(body, up.URL) {
			t.Errorf("错误体泄漏了凭证或 base_url: %s", body)
		}
		if up.Count() != 0 {
			t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
		}
	})
}

func TestModelsListsEnabledAccessPoints(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "bailian", "openai_cc", up.URL, "sk-upstream")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "qwen3-max")
	enabled := gatewaytest.SeedAccessPoint(t, db, "gw-visible")
	gatewaytest.SeedCandidate(t, db, enabled, modelID, 100)

	retired := gatewaytest.SeedAccessPoint(t, db, "gw-retired")
	if _, err := db.Exec(`UPDATE access_points SET disabled = 1 WHERE id = ?`, retired); err != nil {
		t.Fatal(err)
	}

	gw := gatewaytest.Start(t, db)
	resp := gw.Get(t, "/v1/models")
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, body)
	}

	var parsed struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("不是合法 JSON: %v；body=%s", err, body)
	}
	if parsed.Object != "list" {
		t.Errorf("object = %q, 期望 list", parsed.Object)
	}
	if len(parsed.Data) != 1 {
		t.Fatalf("列出 %d 个接入点，期望只有未停用的那 1 个: %s", len(parsed.Data), body)
	}
	entry := parsed.Data[0]
	if entry.ID != "gw-visible" {
		t.Errorf("id = %q, 期望接入点对外名", entry.ID)
	}
	if entry.Object != "model" || entry.OwnedBy == "" || entry.Created == 0 {
		t.Errorf("条目字段不完整: %+v", entry)
	}
	if strings.Contains(body, "qwen3-max") {
		t.Errorf("列表泄漏了纳管模型名，对外只该暴露接入点名: %s", body)
	}
}

func assertOpenAIError(t *testing.T, body string) {
	t.Helper()
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v；body=%s", err, body)
	}
	if parsed.Error.Message == "" {
		t.Errorf("error.message 为空: %s", body)
	}
	if parsed.Error.Type == "" {
		t.Errorf("error.type 为空: %s", body)
	}
	if strings.Contains(body, `"type":"error"`) {
		t.Errorf("OpenAI 入口不该回 Anthropic 的错误外壳: %s", body)
	}
}
