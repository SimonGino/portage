package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 口径层 §2.5「每请求一行流水，SQLite 落库」——表建了一直空着到 M1 才兑现。
func TestSuccessfulCallLandsInCallLogs(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	row := gw.LastCallRow(t)
	if row.APIKeyName != "test-default" {
		t.Errorf("api_key_name = %q, 期望 test-default", row.APIKeyName)
	}
	if row.ClientProtocol != "anthropic" || row.UpstreamProtocol != "anthropic" {
		t.Errorf("协议 = %q → %q, 期望 anthropic → anthropic", row.ClientProtocol, row.UpstreamProtocol)
	}
	if row.ModelRequested != accessPointModel || row.ModelUpstream != upstreamModel {
		t.Errorf("模型 = %q → %q, 期望 %q → %q",
			row.ModelRequested, row.ModelUpstream, accessPointModel, upstreamModel)
	}
	if row.ChannelName == "" {
		t.Error("channel_name 空了——排障时没法定位是哪条渠道")
	}
	if row.Status != http.StatusOK {
		t.Errorf("status = %d, 期望 200", row.Status)
	}
	if row.Error.Valid {
		t.Errorf("成功的调用不该有 error: %q", row.Error.String)
	}
	if row.TotalMs < 0 {
		t.Errorf("total_ms = %d", row.TotalMs)
	}
}

// ttft_ms 只记流式（展开层 §7 该列原文如此）。非流式也填的话，它约等于总耗时，
// 混合流量下「平均首字延迟」就成了一个没有意义的数。非流式的首字节耗时仍在 slog 的
// ttfb_ms 里，没有丢。
func TestTTFTOnlyRecordedForStreaming(t *testing.T) {
	t.Run("非流式留 NULL", func(t *testing.T) {
		gw, _ := newAnthropicGateway(t)
		gw.Post(t, "/v1/messages", anthropicRequest, nil)
		if row := gw.LastCallRow(t); row.TTFTMs.Valid {
			t.Errorf("ttft_ms = %d, 非流式请求该是 NULL", row.TTFTMs.Int64)
		}
	})

	t.Run("流式有值", func(t *testing.T) {
		gw, up := newAnthropicGateway(t)
		up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "text/event-stream"},
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		resp := gw.Post(t, "/v1/messages",
			strings.Replace(anthropicRequest, `"stream":false`, `"stream":true`, 1), nil)
		gatewaytest.ReadBody(t, resp)
		if row := gw.LastCallRow(t); !row.TTFTMs.Valid {
			t.Error("流式请求的 ttft_ms 是 NULL")
		}
	})
}

// is_stream 三态：0/1 是解析过请求体的行，NULL 是没走到那一步的行（鉴权失败那类，
// 迁移前的老行同）——落 false 会把「不知道」说成「同步」。
func TestStreamFlagRecorded(t *testing.T) {
	t.Run("非流式记 0", func(t *testing.T) {
		gw, _ := newAnthropicGateway(t)
		gw.Post(t, "/v1/messages", anthropicRequest, nil)
		row := gw.LastCallRow(t)
		if !row.IsStream.Valid || row.IsStream.Bool {
			t.Errorf("is_stream = %+v, 非流式请求期望 0", row.IsStream)
		}
	})

	t.Run("流式记 1", func(t *testing.T) {
		gw, up := newAnthropicGateway(t)
		up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "text/event-stream"},
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		resp := gw.Post(t, "/v1/messages",
			strings.Replace(anthropicRequest, `"stream":false`, `"stream":true`, 1), nil)
		gatewaytest.ReadBody(t, resp)
		row := gw.LastCallRow(t)
		if !row.IsStream.Valid || !row.IsStream.Bool {
			t.Errorf("is_stream = %+v, 流式请求期望 1", row.IsStream)
		}
	})

	t.Run("没解析到请求体留 NULL", func(t *testing.T) {
		gw, _ := newAnthropicGateway(t)
		gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})
		if row := gw.LastCallRow(t); row.IsStream.Valid {
			t.Errorf("is_stream = %v, 鉴权失败的行不知道类型，期望 NULL", row.IsStream.Bool)
		}
	})
}

// 鉴权失败也要落一行，否则被刷的时候表里什么都看不到（#22）。
func TestUnauthorizedCallLandsInCallLogs(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})

	row := gw.LastCallRow(t)
	if row.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, 期望 401", row.Status)
	}
	if row.APIKeyName != "" {
		t.Errorf("api_key_name = %q, 没认出来就该是空的", row.APIKeyName)
	}
	// 401 停在鉴权层，路由都没走，所以渠道相关的列必然是空——这不是缺陷，
	// 是这行本来就只知道这么多。
	if row.ChannelName != "" || row.UpstreamProtocol != "" {
		t.Errorf("401 的行不该有渠道信息: channel=%q upstream=%q", row.ChannelName, row.UpstreamProtocol)
	}
	if row.Error.String != "unauthorized" {
		t.Errorf("error = %q, 期望 unauthorized", row.Error.String)
	}
}

// 上游自己回 429，网关原样透了——这是一次**成功**的透传，error 列该空着。
// 「上游说不行」由 status 列表达，往 error 里也塞一句会让「网关出过多少错」这个
// 问题查出一堆其实是上游限流的行。
func TestUpstreamRejectionIsNotAGatewayError(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusTooManyRequests, map[string]string{"Content-Type": "application/json"},
		`{"type":"error","error":{"type":"rate_limit_error","message":"配额打满了"}}`)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	row := gw.LastCallRow(t)
	if row.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, 期望 429", row.Status)
	}
	if row.Error.Valid {
		t.Errorf("error = %q, 透传成功的行不该有网关侧错误", row.Error.String)
	}
}

// 上游连不上才是网关侧的错。error 列写固定词表而不是上游原文——传输错误的文案里
// 带着 base_url。
func TestUnreachableUpstreamRecordsOurOwnVocabulary(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	deadURL := up.URL
	up.Close() // 端口随即空出，网关将连不上

	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", deadURL, upstreamModel, anthropicCredential)
	gw := gatewaytest.Start(t, db)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	row := gw.LastCallRow(t)
	if row.Status != http.StatusBadGateway {
		t.Errorf("status = %d, 期望 502", row.Status)
	}
	if row.Error.String != "upstream_error" {
		t.Errorf("error = %q, 期望固定词 upstream_error", row.Error.String)
	}
	if strings.Contains(row.Error.String, deadURL) {
		t.Errorf("error 列漏了 base_url: %q", row.Error.String)
	}
}

// 一次请求恰好一行：中间件叠了三层，重复落库的表现是账翻倍而所有断言照常绿。
func TestOneRequestWritesExactlyOneRow(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	for range 3 {
		gw.Post(t, "/v1/messages", anthropicRequest, nil)
	}

	// 要等：落库在响应之后，Post 返回时最后一行可能还没进库（见 WaitCallRows）。
	if got := gw.WaitCallRows(t, 3); got != 3 {
		t.Errorf("call_logs 有 %d 行, 期望 3", got)
	}
}
