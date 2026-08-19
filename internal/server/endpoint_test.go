package server_test

import (
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol"
)

// #17：`call_logs` 此前只记 client_protocol / upstream_protocol，而 `/v1/messages` 与
// `/v1/messages/count_tokens` 的入站协议同为 anthropic——这两者在流水里结构上不可分。
// 真机那一轮里一批 count_tokens 的 501 与一批限流的 429 在管理端长成一模一样的行
// （同样的入站 anthropic、同样的空 model、同样的零耗时），判断「这波 501 是什么」
// 只能靠时间戳猜。

// newFourEndpointGateway 起一个四个端点都走得通的网关：三个协议各一条渠道，
// count_tokens 蹭 anthropic 那条（它是 Anthropic 独有端点）。
func newFourEndpointGateway(t *testing.T) *gatewaytest.Gateway {
	t.Helper()
	db := gatewaytest.NewDB(t)
	for _, c := range []struct{ ap, proto, upstream, credential string }{
		{accessPointModel, "anthropic", upstreamModel, anthropicCredential},
		{"gw-cc", "openai", "qwen3-max-2025-09-23", openaiCredential},
		{"gw-resp", "openai_responses", "gpt-5-codex", openaiCredential},
	} {
		up := gatewaytest.NewUpstream(t)
		gatewaytest.SeedPassthrough(t, db, c.ap, c.proto, up.URL, c.upstream, c.credential)
	}
	return gatewaytest.Start(t, db)
}

// 四个转发端点各写各的路径。取值就是 protocol.Endpoint 那四条，不另造一套简写——
// slog 的 endpoint 字段写的也是这个值，两处同源才对得起来。
func TestEveryRelayEndpointRecordsItsPath(t *testing.T) {
	gw := newFourEndpointGateway(t)

	cases := []struct {
		ep   protocol.Endpoint
		body string
	}{
		{protocol.EndpointMessages, anthropicRequest},
		{protocol.EndpointCountTokens, countTokensRequest},
		{protocol.EndpointChatCompletions, ccRequest},
		{protocol.EndpointResponses, responsesRequest},
	}
	for i, c := range cases {
		resp := gw.Post(t, c.ep.Path, c.body, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d, 期望 200；body=%s",
				c.ep.Path, resp.StatusCode, gatewaytest.ReadBody(t, resp))
		}
		// 逐个等：LastCallRow 只等「有任意一行」，落库排在响应之后，不数着行数走
		// 就会把上一个端点那行读成这一个的。
		if n := gw.WaitCallRows(t, i+1); n != i+1 {
			t.Fatalf("打完 %s 后 call_logs 有 %d 行，期望 %d", c.ep.Path, n, i+1)
		}
		if got := gw.LastCallRow(t).Endpoint; got != c.ep.Path {
			t.Errorf("call_logs.endpoint = %q, 期望 %q", got, c.ep.Path)
		}
	}
}

// 本票的现象行：count_tokens 命中非 Anthropic 渠道就地本地估算（#18，此前是 501），
// 一个字节都不打上游。这种行没有 usage、没有出站端点，端点列是辨认它的**唯一**依据。
func TestCountTokensLocalRowCarriesTheEndpoint(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, "qwen3-max", openaiCredential)
	gw := gatewaytest.Start(t, db)

	if resp := gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（本地估算）", resp.StatusCode)
	}

	row := gw.LastCallRow(t)
	if row.Endpoint != protocol.EndpointCountTokens.Path {
		t.Errorf("call_logs.endpoint = %q, 期望 %q", row.Endpoint, protocol.EndpointCountTokens.Path)
	}
	if row.ClientProtocol != "anthropic" {
		t.Errorf("client_protocol = %q, 期望 anthropic——正是它分不开这两个端点", row.ClientProtocol)
	}
}

// 限流挂在鉴权之后、路由之前（docs/MVP设计草案.md §7.2），而 callLog 是最外层那一层，
// 端点在任何事情失败之前就写死了——429 这一格因此也有值。#16 之后两只桶分家，
// 「这波 429 是 count_tokens 那只桶还是生成面那只」正是要靠这一列回答的。
func TestRateLimited429RowCarriesTheEndpoint(t *testing.T) {
	gw, _ := newLimitedGateway(t, 1)

	gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil)
	if resp := gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("第 2 次 count_tokens = %d, 期望 429", resp.StatusCode)
	}
	if n := gw.WaitCallRows(t, 2); n != 2 {
		t.Fatalf("call_logs 落了 %d 行，期望 2", n)
	}

	row := gw.LastCallRow(t)
	if row.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, 期望 429", row.Status)
	}
	if row.Endpoint != protocol.EndpointCountTokens.Path {
		t.Errorf("call_logs.endpoint = %q, 期望 %q", row.Endpoint, protocol.EndpointCountTokens.Path)
	}
	// 现象表里那批 429 的 model_requested 就是空的：限流在路由之前，压根解析不到模型。
	// 这正是端点列非有不可的理由——这一行别的格子都空着。
	if row.ModelRequested != "" {
		t.Errorf("model_requested = %q, 限流在解析模型之前，期望空串", row.ModelRequested)
	}
}

// 401 停在鉴权层，比限流还早一格。端点由更外层的 callLog 写，所以照样有值。
func TestUnauthorizedRowCarriesTheEndpoint(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})

	row := gw.LastCallRow(t)
	if row.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, 期望 401", row.Status)
	}
	if row.Endpoint != protocol.EndpointMessages.Path {
		t.Errorf("call_logs.endpoint = %q, 期望 %q", row.Endpoint, protocol.EndpointMessages.Path)
	}
}
