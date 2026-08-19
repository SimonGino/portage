package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol"
)

// #20：#17 把**入站**端点落进了流水，出站那一半仍然只有 upstream_protocol——「这次
// 到底打到上游哪条路径」看不出来。协议推不出路径：count_tokens 透传时上游协议是
// anthropic，而 anthropic 那一档在 protocol.UpstreamEndpoint 里对着 /v1/messages，
// 照它反推就把 count_tokens 说成了 messages。

// 同协议透传：出站就是入站那条，四个端点各自成立。count_tokens 那条尤其要有——
// 它是上面那句「协议推不出路径」的现成反例。
func TestPassthroughRecordsTheSameUpstreamEndpoint(t *testing.T) {
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
		// 逐个等，理由同 endpoint_test.go：落库排在响应之后，不数着行数走就会把上
		// 一个端点那行读成这一个的。
		if n := gw.WaitCallRows(t, i+1); n != i+1 {
			t.Fatalf("打完 %s 后 call_logs 有 %d 行，期望 %d", c.ep.Path, n, i+1)
		}
		row := gw.LastCallRow(t)
		if row.UpstreamEndpoint != c.ep.Path {
			t.Errorf("%s 的 call_logs.upstream_endpoint = %q, 期望 %q（透传路径上入口即出口）",
				c.ep.Path, row.UpstreamEndpoint, c.ep.Path)
		}
		if row.Endpoint != c.ep.Path {
			t.Errorf("%s 的 call_logs.endpoint = %q, 期望 %q", c.ep.Path, row.Endpoint, c.ep.Path)
		}
	}
}

// 跨协议：Anthropic 入口落到 openai 渠道，出站打的是 /v1/chat/completions 而**不是**
// 入站那条 /v1/messages。这一格是本票最容易写错的地方——沿用入站那条会打到一个上游
// 根本不存在的路径，而流水里两列相等看起来还挺正常。
func TestConvertedRecordsTheOutboundEndpoint(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-2","object":"chat.completion","model":"`+ccUpstreamModel+`",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"读完了"},`+
			`"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":12,"completion_tokens":34}}`)

	nonStream := strings.Replace(convertRequest, `"stream":true`, `"stream":false`, 1)
	resp := gw.Post(t, "/v1/messages", nonStream, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	gw.WaitCallRows(t, 1)

	row := gw.LastCallRow(t)
	if row.Endpoint != protocol.EndpointMessages.Path {
		t.Errorf("call_logs.endpoint = %q, 期望 %q", row.Endpoint, protocol.EndpointMessages.Path)
	}
	if row.UpstreamEndpoint != protocol.EndpointChatCompletions.Path {
		t.Errorf("call_logs.upstream_endpoint = %q, 期望 %q（出口协议的路径，不是入口那条）",
			row.UpstreamEndpoint, protocol.EndpointChatCompletions.Path)
	}
	// 落库的那条与上游真收到的那条要是同一条，否则这一列只是个好看的常量。
	if got := up.Last(t).Path; got != row.UpstreamEndpoint {
		t.Errorf("上游收到的 path = %q，流水记的是 %q，两者必须同源", got, row.UpstreamEndpoint)
	}
}

// 没发到上游的三档：401 停在鉴权、429 停在限流、count_tokens 撞非 anthropic 渠道就地
// 501。这些行的入站端点都有值（#17 的构造性保证），出站必须空——它们一个字节都没
// 打出去，空就是事实，不从 upstream_protocol 反推补一条没走过的路径。
func TestRowsThatNeverReachedUpstreamHaveNoUpstreamEndpoint(t *testing.T) {
	t.Run("401 鉴权失败", func(t *testing.T) {
		gw, _ := newAnthropicGateway(t)
		gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})

		row := gw.LastCallRow(t)
		if row.Status != http.StatusUnauthorized {
			t.Fatalf("status = %d, 期望 401", row.Status)
		}
		assertNoUpstreamEndpoint(t, row, protocol.EndpointMessages.Path)
	})

	t.Run("429 限流", func(t *testing.T) {
		gw, _ := newLimitedGateway(t, 1)

		gw.Post(t, "/v1/messages", anthropicRequest, nil)
		if resp := gw.Post(t, "/v1/messages", anthropicRequest, nil); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("第 2 次 = %d, 期望 429", resp.StatusCode)
		}
		if n := gw.WaitCallRows(t, 2); n != 2 {
			t.Fatalf("call_logs 落了 %d 行，期望 2", n)
		}

		row := gw.LastCallRow(t)
		if row.Status != http.StatusTooManyRequests {
			t.Fatalf("status = %d, 期望 429", row.Status)
		}
		assertNoUpstreamEndpoint(t, row, protocol.EndpointMessages.Path)
	})

	t.Run("count_tokens 撞非 anthropic 渠道走本地估算", func(t *testing.T) {
		up := gatewaytest.NewUpstream(t)
		db := gatewaytest.NewDB(t)
		gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, "qwen3-max", openaiCredential)
		gw := gatewaytest.Start(t, db)

		// #18 之后这一格回 200，但「没打上游」没变——正是这条子测试要钉的那一列。
		if resp := gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("状态码 = %d, 期望 200（本地估算）", resp.StatusCode)
		}

		row := gw.LastCallRow(t)
		assertNoUpstreamEndpoint(t, row, protocol.EndpointCountTokens.Path)
		// 这一行的 upstream_protocol 是 openai——反推得出 /v1/chat/completions，而它
		// 一个字节都没发出去。这就是「不推导补值」要挡的那一格。
		if row.UpstreamProtocol != "openai" {
			t.Errorf("upstream_protocol = %q, 期望 openai", row.UpstreamProtocol)
		}
	})
}

func assertNoUpstreamEndpoint(t *testing.T, row gatewaytest.CallRow, wantInbound string) {
	t.Helper()
	if row.Endpoint != wantInbound {
		t.Errorf("call_logs.endpoint = %q, 期望 %q", row.Endpoint, wantInbound)
	}
	if row.UpstreamEndpoint != "" {
		t.Errorf("call_logs.upstream_endpoint = %q, 期望空串——这行从没发到上游", row.UpstreamEndpoint)
	}
}
