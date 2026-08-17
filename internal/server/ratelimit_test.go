package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 口径层 §2.7（v0.15 定，#16 修订）：两只全局令牌桶——生成面三个端点共用一只，
// count_tokens 独占一只，两只都用同一份 rate_limit_qps/burst 配置造。超限回 429
// 带 Retry-After，不分维度。
// 这些用例把桶调到 qps=1 才跑得动——出厂的 10/20 要打二十几个请求才见得到 429，
// 那种用例在慢机器上会因为令牌回得及时而变成间歇性绿。

// newLimitedGateway 起一个限流网关。burst 是桶容量，qps 固定 1（一秒才回一个令牌，
// 进程内连打几个请求期间约等于不回），于是「第 burst+1 个必被拒」是确定的。
func newLimitedGateway(t *testing.T, burst int) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{RateLimitQPS: 1, RateLimitBurst: burst}), up
}

func TestRateLimitRejectsOnceBurstIsSpent(t *testing.T) {
	gw, _ := newLimitedGateway(t, 2)

	for i := 1; i <= 2; i++ {
		if resp := gw.Post(t, "/v1/messages", anthropicRequest, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 个请求 = %d, 桶里还有令牌就不该拒", i, resp.StatusCode)
		}
	}

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("第 3 个请求 = %d, 期望 429", resp.StatusCode)
	}
	// 没有 Retry-After 的 429，harness 只能自己瞎猜退避（M0 实测 #6：Codex 对 429
	// 一次即弃）。口径把它列进来就是为了让客户端有得依据。
	if ra := resp.Header.Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, 期望 1", ra)
	}

	// 协议原生格式：回 gin 默认 JSON 的话 harness 认不出来，表现成解析失败。
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(gatewaytest.ReadBody(t, resp)), &body); err != nil {
		t.Fatalf("429 的 body 不是合法 JSON: %v", err)
	}
	if body.Type != "error" || body.Error.Type != "rate_limit_error" {
		t.Errorf("body = %+v, 期望 Anthropic 原生的 rate_limit_error", body)
	}
	if body.Error.Message == "" {
		t.Error("429 没有 message")
	}
}

// 生成面那三个端点共用**一只**桶，不是每个端点一只。写成在 rateLimit(ep) 里无脑
// new 的话这里会全绿——而线上的 10 QPS 悄悄变成 3×10。
//
// 第二个请求用 /v1/chat/completions 而不是 count_tokens（#16 之后后者自己一只桶）。
// 它的 model 没在这个网关里纳管，但无妨：限流在路由之前，压根解析不到模型——真机
// 的 429 流水行 model_requested 就是空的（#16 现象表）。
func TestRateLimitBucketIsSharedAcrossGenerationEndpoints(t *testing.T) {
	gw, _ := newLimitedGateway(t, 1)

	if resp := gw.Post(t, "/v1/messages", anthropicRequest, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/messages = %d, 期望 200", resp.StatusCode)
	}
	resp := gw.Post(t, "/v1/chat/completions", ccRequest, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("换个端点就 = %d 了, 期望 429——说明桶不是共用的", resp.StatusCode)
	}
}

// #16：Claude Code 一开场连打二十几次 count_tokens，桶被打空，紧随其后的
// /v1/messages 被 429 掉——而那批 count_tokens 命中非 Anthropic 渠道时一个字节都
// 不打上游。桶是为「防 key 泄露刷爆上游账单」立的，不该被这种请求饿死。
func TestCountTokensStormDoesNotStarveMessages(t *testing.T) {
	gw, _ := newLimitedGateway(t, 2)

	// 30 次是真机观测量级（26/24 次）的上取整。这里不看它们各自的状态码：
	// count_tokens 自己那只桶被打空是应有的，要测的是它没动到生成面那只。
	for i := 0; i < 30; i++ {
		gw.Post(t, "/v1/messages/count_tokens", anthropicRequest, nil)
	}

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatal("/v1/messages 被 429 了——count_tokens 风暴把生成面那只桶打空了")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/messages = %d, 期望 200", resp.StatusCode)
	}
}

// count_tokens 自己那只桶照样限得住：它在 Anthropic 出口是真打上游的，
// 「防账单」这条对它一样成立，不能整个豁免。
func TestCountTokensIsStillRateLimited(t *testing.T) {
	gw, _ := newLimitedGateway(t, 1)

	gw.Post(t, "/v1/messages/count_tokens", anthropicRequest, nil)
	resp := gw.Post(t, "/v1/messages/count_tokens", anthropicRequest, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("第 2 次 count_tokens = %d, 期望 429——它自己那只桶漏了", resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, 期望 1", ra)
	}
}

// 被限的请求也要落流水：表里看不到 429 的话，「怎么突然变慢了」根本无从查起。
func TestRateLimitedCallLandsInCallLogs(t *testing.T) {
	gw, _ := newLimitedGateway(t, 1)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if resp := gw.Post(t, "/v1/messages", anthropicRequest, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("第 2 个请求 = %d, 期望 429——没被拒的话查它的流水没有意义", resp.StatusCode)
	}
	// 等两行都进库再读最后一行。落库在 callLog 的 defer 里，也就是响应已经发给客户端
	// 之后（persistCall），Post 回来不代表那一行到了；而 LastCallRow 只等「有任意一行」，
	// 第一个请求的 200 早就在表里，429 那行慢一步就会被读成 200。CI 上实见，且整个用例
	// 只跑了 0.09s——不是令牌回得及时，就是这个。
	// 比对行数而不是干等：WaitCallRows 超时不 Fatal，只回最终行数，不看的话
	// 「那一行始终没落」又会退化成一模一样的 status = 200。
	if n := gw.WaitCallRows(t, 2); n != 2 {
		t.Fatalf("call_logs 落了 %d 行，期望 2", n)
	}

	row := gw.LastCallRow(t)
	if row.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, 期望 429", row.Status)
	}
	// 挂在鉴权之后，所以这一行认得出是哪把 key 在刷——正是排查泄露时要看的。
	if row.APIKeyName != "test-default" {
		t.Errorf("api_key_name = %q, 期望 test-default", row.APIKeyName)
	}
}

// rate_limit_qps 配 0 就是要关掉。若在 Load 里给它兜零值，这里会红。
func TestRateLimitZeroQPSDisablesIt(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{RateLimitQPS: 0, RateLimitBurst: 1})

	for i := 1; i <= 5; i++ {
		if resp := gw.Post(t, "/v1/messages", anthropicRequest, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 个请求 = %d, 关了限流就不该有 429", i, resp.StatusCode)
		}
	}
}

// 只挂转发面。探活被限会让监控在最忙的时候先报警，/v1/models 压根不打上游。
func TestRateLimitLeavesHealthzAndModelsAlone(t *testing.T) {
	gw, _ := newLimitedGateway(t, 1)

	// 先把桶抽干。
	gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if resp := gw.Post(t, "/v1/messages", anthropicRequest, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("桶没抽干，前置条件不成立: %d", resp.StatusCode)
	}

	for _, path := range []string{"/healthz", "/v1/models"} {
		if resp := gw.Get(t, path); resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d, 期望 200——它不该被限流", path, resp.StatusCode)
		}
	}
}
