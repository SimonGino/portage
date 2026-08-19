package server_test

import (
	"encoding/json"
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

// count_tokens 命中非 anthropic 渠道走本地估算回 200（#18，口径层 v0.80）。此前这
// 一格回 501，真机代价是 Claude Code 每轮都打、开场连打二十几次，拿不到数就只能
// 瞎估压缩时机，几十条 501 还冲空限流桶（#16）。
func TestCountTokensEstimatesLocallyOnNonAnthropicChannel(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, "qwen3-max", openaiCredential)
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, body)
	}
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("回包不是 JSON: %v；body=%s", err, body)
	}
	if got.InputTokens < 1 {
		t.Errorf("input_tokens = %d, 估算恒 ≥ 1", got.InputTokens)
	}
	assertNoSecrets(t, body, openaiCredential, up.URL)
	// 边界二：为了更准去打上游是禁手——这条路一个字节都不该发出去。
	if up.Count() != 0 {
		t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
	}

	// 流水侧的三条边界：不写 usage 列（估算不是上游报的数）、不记出站端点（没拨过
	// 号，「非空 ⟺ 真的发起过」不变量照旧）、成功行没有错误词。行靠端点列辨认（#17）。
	row := gw.LastCallRow(t)
	if row.Status != http.StatusOK {
		t.Errorf("status = %d, 期望 200", row.Status)
	}
	if row.InputTokens.Valid || row.OutputTokens.Valid {
		t.Errorf("usage 列 = (%v, %v)，估算不进 usage，两列都该是 NULL", row.InputTokens, row.OutputTokens)
	}
	if row.UpstreamEndpoint != "" {
		t.Errorf("upstream_endpoint = %q, 没拨号的行该是空串", row.UpstreamEndpoint)
	}
	if row.Error.Valid {
		t.Errorf("error = %q, 本地估算是一次成功，不该有错误词", row.Error.String)
	}
	if row.Endpoint != "/v1/messages/count_tokens" {
		t.Errorf("endpoint = %q, 这种行全靠它辨认", row.Endpoint)
	}
}

// 此处原有 TestMessagesRejectsCrossProtocolCandidate，钉的是「/v1/messages 命中还没
// 放开的那几格时回错而不是乱发」。portage-legacy#80 九宫格全开之后 /v1/messages 对三种渠道协议都
// 走得通，这条性质不再存在，删除。它先后指过 openai 与 openai_responses 两个渠道
// 协议，两格如今都开了。
//
// 上面那条 TestCountTokensRejectsNonAnthropicChannel 不受影响：count_tokens 的 501
// 不是「还没做」，是**上游没有对应端点**，永久成立。
