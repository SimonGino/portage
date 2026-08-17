package server_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件钉的是展开层 §6.1 的响应头保真度（#37 从 #7 拆出的那两项之一）：上游那套
// 官方响应头原样到客户端，其中 request-id 还要**同时**落进 call_logs 供事后对账。
//
// 这里用的是假上游按官方文档形状发的头，**不是**官方直连实测：真实头名、大小写、
// 官方到底发不发全，仍要拿官方 key 跑一次才算数（#37 剩下的那半）。这套用例能兜住
// 的是回归——网关这边把整套头吃掉一个，当场红。

const messageWithRequestID = `{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],` +
	`"usage":{"input_tokens":10,"output_tokens":2}}`

func TestRelayPassesDocumentedAnthropicHeadersThrough(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, withContentType(gatewaytest.AnthropicResponseHeaders), messageWithRequestID)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)

	for k, want := range gatewaytest.AnthropicResponseHeaders {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, 期望原样回传 %q", k, got, want)
		}
	}
}

// Content-Length 由 Go 按实际写入量重设（展开层 §6.1 的唯一例外）：既不能照抄上游
// 那个数，也不能因为 CopyResponseHeaders 用的是 Add 而出现两条。
func TestRelayRecomputesContentLength(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, messageWithRequestID)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if got := resp.Header.Values("Content-Length"); len(got) > 1 {
		t.Errorf("Content-Length = %v, 期望至多一条（Add 复制上游那条就会变两条）", got)
	}
	body := gatewaytest.ReadBody(t, resp)
	if got := resp.Header.Get("Content-Length"); got != "" && got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %s, 实际写出 %d 字节", got, len(body))
	}
}

func TestRelayRecordsUpstreamRequestID(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, withContentType(gatewaytest.AnthropicResponseHeaders), messageWithRequestID)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)

	want := gatewaytest.AnthropicResponseHeaders["request-id"]
	// 同一个值要在两处都拿得到：客户端手上一份（报障时贴的那个），流水里一份
	// （事后自己查的那个）。只有一处的话对账就得去翻另一边的日志。
	if got := resp.Header.Get("request-id"); got != want {
		t.Errorf("客户端拿到的 request-id = %q, 期望 %q", got, want)
	}
	if got := gw.LastCallRow(t).UpstreamRequestID; got != want {
		t.Errorf("call_logs.upstream_request_id = %q, 期望与客户端同值 %q", got, want)
	}
	if got := gw.LastCall(t).Str("upstream_request_id"); got != want {
		t.Errorf("slog upstream_request_id = %q, 期望 %q", got, want)
	}
}

// 中转与自建上游多半按通用惯例发 x-request-id，那个也得记下来——否则最需要对账的
// 那一档（第三方链路出问题）反而是空的。
func TestRelayFallsBackToXRequestID(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{
		"Content-Type": "application/json", "X-Request-Id": "9f2c8b1e",
	}, messageWithRequestID)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if got := gw.LastCallRow(t).UpstreamRequestID; got != "9f2c8b1e" {
		t.Errorf("call_logs.upstream_request_id = %q, 期望兜底取 x-request-id 的 9f2c8b1e", got)
	}
}

// 上游不发这个头是常态（自建、部分中转），落一行空串而不是让落库失败。
func TestRelayLeavesRequestIDEmptyWhenUpstreamSendsNone(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, messageWithRequestID)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if got := gw.LastCallRow(t).UpstreamRequestID; got != "" {
		t.Errorf("call_logs.upstream_request_id = %q, 上游没发时期望空串", got)
	}
}

// 上游报错那一行更要记得下 id：报障贴的就是它。
func TestRelayRecordsRequestIDOnUpstreamError(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusTooManyRequests, map[string]string{
		"Content-Type": "application/json",
		"Request-Id":   "req_011CSHoEeqs5C35K2UUqR7Fy",
		"Retry-After":  "30",
	}, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"},`+
		`"request_id":"req_011CSHoEeqs5C35K2UUqR7Fy"}`)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if got := resp.Header.Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, 期望原样回传 30", got)
	}
	if got := gw.LastCallRow(t).UpstreamRequestID; got != "req_011CSHoEeqs5C35K2UUqR7Fy" {
		t.Errorf("call_logs.upstream_request_id = %q, 429 那一行也要有 id", got)
	}
}

// 转换路径不回传上游响应头（出口协议的头是这边重造的），但流水里照记这个 id：
// 找上游对账与走的是哪条路无关，而 A→CC 恰恰是最常出问题、最需要对账的那条。
func TestConvertedCallRecordsUpstreamRequestID(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{
		"Content-Type": "application/json", "Request-Id": "req_convert_01",
	}, `{"id":"chatcmpl-2","object":"chat.completion","model":"`+ccUpstreamModel+`",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"读完了"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":12,"completion_tokens":34}}`)

	nonStream := strings.Replace(convertRequest, `"stream":true`, `"stream":false`, 1)
	resp := gw.Post(t, "/v1/messages", nonStream, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}

	if got := gw.LastCallRow(t).UpstreamRequestID; got != "req_convert_01" {
		t.Errorf("call_logs.upstream_request_id = %q, 期望 req_convert_01", got)
	}
}

// 落库还不够：#81 之前这个 id 只在库里和 slog 里，管理端一个地方都没露，而它落库的
// 全部理由就是「事后不用翻日志就能查到」。接口这一层因此要钉两件事——字段在，且
// **空串照原样给不转 null**（库里这一列不可空，前端只判空串）。
func TestAdminLogsExposeUpstreamRequestID(t *testing.T) {
	type row struct {
		Status int `json:"status"`
		// 指针是为了分开「给了空串」与「给了 null / 根本没这个键」。
		UpstreamRequestID *string `json:"upstream_request_id"`
	}
	type page struct {
		Rows []row `json:"rows"`
	}

	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, withContentType(gatewaytest.AnthropicResponseHeaders), messageWithRequestID)
	gw.Post(t, "/v1/messages", anthropicRequest, nil)
	// 上游不发这个头的那一行：与「没走到上游」「老流水」同档，都是空串。
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, messageWithRequestID)
	gw.Post(t, "/v1/messages", anthropicRequest, nil)
	gw.WaitCallRows(t, 2)

	var got page
	gw.LoggedIn(t).JSONInto(t, http.MethodGet, "/admin/api/logs", "", &got)
	if len(got.Rows) != 2 {
		t.Fatalf("流水 %d 行，期望 2", len(got.Rows))
	}
	// 最新在前：第 0 行是没发头的那次，第 1 行是发了的那次。
	for i, want := range []string{"", gatewaytest.AnthropicResponseHeaders["request-id"]} {
		id := got.Rows[i].UpstreamRequestID
		if id == nil {
			t.Fatalf("第 %d 行 upstream_request_id 是 null，期望 %q——这一列不可空，空串照原样给", i, want)
		}
		if *id != want {
			t.Errorf("第 %d 行 upstream_request_id = %q, 期望 %q", i, *id, want)
		}
	}
	// 成功行也带 id，正是「详情」按钮不能只按 status >= 400 出的理由（#81）。
	if got.Rows[1].Status >= 400 {
		t.Errorf("status = %d, 这一行应当是成功的", got.Rows[1].Status)
	}
}

func withContentType(h map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for k, v := range h {
		out[k] = v
	}
	return out
}
