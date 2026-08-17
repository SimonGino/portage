package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

const countTokensRequest = `{"model":"gw-sonnet","messages":[{"role":"user","content":"hi"}]}`

func TestCountTokensPassesThroughToAnthropicChannel(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	const upstreamBody = `{"input_tokens":9}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if body := gatewaytest.ReadBody(t, resp); body != upstreamBody {
		t.Errorf("body = %q, 期望原样 %q", body, upstreamBody)
	}

	got := up.Last(t)
	if got.Path != "/v1/messages/count_tokens" {
		t.Errorf("上游 path = %q, 期望 /v1/messages/count_tokens", got.Path)
	}
	if !strings.Contains(string(got.Body), upstreamModel) {
		t.Errorf("count_tokens 的 model 没被翻译成纳管模型名: %s", got.Body)
	}
}

// Claude Code 启动阶段会打 count_tokens。命中非 anthropic 渠道时必须立刻回一个
// 它认得的错误格式，不能挂着——挂住就是启动卡死。
func TestCountTokensRejectsNonAnthropicChannel(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, "qwen3-max", openaiCredential)
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("状态码 = %d, 期望 501；body=%s", resp.StatusCode, body)
	}
	assertAnthropicError(t, body, "api_error")
	assertNoSecrets(t, body, openaiCredential, up.URL)
	if up.Count() != 0 {
		t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
	}
}

// 此处原有 TestMessagesRejectsCrossProtocolCandidate，钉的是「/v1/messages 命中还没
// 放开的那几格时回错而不是乱发」。#80 九宫格全开之后 /v1/messages 对三种渠道协议都
// 走得通，这条性质不再存在，删除。它先后指过 openai 与 openai_responses 两个渠道
// 协议，两格如今都开了。
//
// 上面那条 TestCountTokensRejectsNonAnthropicChannel 不受影响：count_tokens 的 501
// 不是「还没做」，是**上游没有对应端点**，永久成立。
