package server_test

import (
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// X-Accel-Buffering: no（口径层 v0.30）。nginx 认这个头就对本次响应关掉 proxy_buffering，
// 于是网关跑在一份别人维护的 nginx 后面时也不会被攒住（展开层 §11.3 实测：单单关掉
// 缓冲就足以让 SSE 恢复逐条下发）。

func TestPassthroughStreamCarriesNoBufferingHeader(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "text/event-stream"},
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	defer gatewaytest.ReadBody(t, resp)

	if v := resp.Header.Get("X-Accel-Buffering"); v != "no" {
		t.Errorf("X-Accel-Buffering = %q, 期望 no", v)
	}
}

func TestConvertedStreamCarriesNoBufferingHeader(t *testing.T) {
	gw, up := newConvertGateway(t)
	ccStreamAll(t, up)

	resp := gw.Post(t, "/v1/messages", convertRequest, nil)
	defer gatewaytest.ReadBody(t, resp)

	if v := resp.Header.Get("X-Accel-Buffering"); v != "no" {
		t.Errorf("X-Accel-Buffering = %q, 期望 no", v)
	}
}

// 非流式不加：这个头只对流有意义，无差别地盖上去就成了「透传路径永远多一个上游
// 没发的头」，与透传保真的张力比它换来的好处大。
func TestNonStreamingResponseHasNoBufferingHeader(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	defer gatewaytest.ReadBody(t, resp)

	if v := resp.Header.Get("X-Accel-Buffering"); v != "" {
		t.Errorf("非流式响应上出现了 X-Accel-Buffering = %q", v)
	}
}

// 上游自己发了这个头时以我们的为准——真见过上游发 "yes" 的中转，那等于把整条流
// 交回给反代去攒。
func TestUpstreamNoBufferingHeaderIsOverridden(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{
		"Content-Type":      "text/event-stream",
		"X-Accel-Buffering": "yes",
	}, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	defer gatewaytest.ReadBody(t, resp)

	if got := resp.Header.Values("X-Accel-Buffering"); len(got) != 1 || got[0] != "no" {
		t.Errorf("X-Accel-Buffering = %v, 期望恰好一个 no", got)
	}
}
