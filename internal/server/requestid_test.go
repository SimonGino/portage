package server_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件钉的是展开层 §6.1 的响应头保真度（#2 从 portage-legacy#7 拆出的那两项之一）：上游那套
// 官方响应头原样到客户端，其中 request-id 还要**同时**落进 call_logs 供事后对账。
//
// 这里用的是假上游按官方文档形状发的头，**不是**官方直连实测：真实头名、大小写、
// 官方到底发不发全，仍要拿官方 key 跑一次才算数（#2 剩下的那半）。这套用例能兜住
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

// 口径层 v0.74 的三档取值：`request-id`（头）→ 错误响应体里的 `request_id` →
// `x-request-id`（头）。下面七条对着该票的七条验收。
//
// 这一条是实测撞见的真实形状，也是加第三档的全部理由：应用层中转把官方响应头裁了
// （小写 request-id 一个都不到），只回一个自己编号的 X-Request-Id，而官方那个号还在
// 错误信封的体里。取中转编的号去问官方什么都查不到，比空着更误导。
func TestRelayReadsRequestIDFromErrorBodyWhenOfficialHeaderIsStripped(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusTooManyRequests, map[string]string{
		"Content-Type": "application/json",
		// 中转编的号，实测那条链路上它**总是**有值——所以新档不能插在它后面。
		"X-Request-Id": "58b499a1-cdd5-4b78-81ad-766b71fe287e",
	}, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"},`+
		`"request_id":"req_011Ce2vfd1LHuZQWqwDRc5mm"}`)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	gatewaytest.ReadBody(t, resp)

	// 客户端那边照旧原样收到中转那个头：这一档只改流水记什么，不动透传的字节。
	if got := resp.Header.Get("X-Request-Id"); got != "58b499a1-cdd5-4b78-81ad-766b71fe287e" {
		t.Errorf("X-Request-Id = %q, 期望原样回传中转那个头", got)
	}
	if got := gw.LastCallRow(t).UpstreamRequestID; got != "req_011Ce2vfd1LHuZQWqwDRc5mm" {
		t.Errorf("call_logs.upstream_request_id = %q, 期望取错误体里官方那个 req_011Ce2…", got)
	}
}

// 头里有官方那个时体不参与：两处给不同的值，取到哪个一目了然（同值的话这条钉不住）。
func TestRelayPrefersOfficialHeaderOverErrorBodyRequestID(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusBadRequest, map[string]string{
		"Content-Type": "application/json",
		"Request-Id":   "req_from_header",
		"X-Request-Id": "proxy-uuid",
	}, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"},`+
		`"request_id":"req_from_body"}`)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

	if got := gw.LastCallRow(t).UpstreamRequestID; got != "req_from_header" {
		t.Errorf("call_logs.upstream_request_id = %q, 期望头里官方那个 req_from_header", got)
	}
}

// 三处都没有就是没有：不失败、不告警，落空串（v0.67 ⑤ 那一档吃掉的三种情况之一）。
func TestRelayLeavesRequestIDEmptyWhenErrorBodyHasNoneEither(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusInternalServerError, map[string]string{"Content-Type": "application/json"},
		`{"type":"error","error":{"type":"api_error","message":"boom"}}`)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

	row := gw.LastCallRow(t)
	if row.UpstreamRequestID != "" {
		t.Errorf("call_logs.upstream_request_id = %q, 三处都没有时期望空串", row.UpstreamRequestID)
	}
	// 解不出来不影响错误原文照落：两件事共用同一份字节，但互不牵连。
	if !strings.Contains(row.ErrorDetail.String, "boom") {
		t.Errorf("error_detail = %q, 期望仍含上游原文", row.ErrorDetail.String)
	}
}

// 成功行不读体（口径层 v0.74 ②：实测成功响应的体里没有这个字段，流式非流式都没有）。
// 判据是行为而不是计数：体里明摆着一个 request_id，只要成功路径去解了它就会被记下来，
// 而期望记的是头里那个。
func TestSuccessfulCallDoesNotReadRequestIDFromBody(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{
		"Content-Type": "application/json", "X-Request-Id": "proxy-uuid",
	}, `{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],`+
		`"usage":{"input_tokens":10,"output_tokens":2},"request_id":"req_from_success_body"}`)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

	row := gw.LastCallRow(t)
	if row.UpstreamRequestID != "proxy-uuid" {
		t.Errorf("call_logs.upstream_request_id = %q, 成功行期望仍取头里的 proxy-uuid", row.UpstreamRequestID)
	}
	// 成功行连错误原文都不收集，第二档因此**结构上**走不到——这一列为空即是那个证据。
	if row.ErrorDetail.Valid {
		t.Errorf("成功的调用不该收集错误原文：%q", row.ErrorDetail.String)
	}
}

// 截断顺序那一坑：`request_id` 排在 `error` 对象之后，落库截 2KB 会把它截在外面。
// 取键因此在**截断前**的完整字节上做——这条同时钉住「两个上限确实不是一个数」。
func TestErrorBodyRequestIDSurvivesErrorDetailTruncation(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	huge := `{"type":"error","error":{"type":"api_error","message":"` +
		strings.Repeat("x", 8<<10) + `"},"request_id":"req_beyond_2kb"}`
	up.RespondWith(http.StatusInternalServerError,
		map[string]string{"Content-Type": "application/json", "X-Request-Id": "proxy-uuid"}, huge)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

	row := gw.LastCallRow(t)
	if row.UpstreamRequestID != "req_beyond_2kb" {
		t.Errorf("call_logs.upstream_request_id = %q, 期望 2KB 之外那个 req_beyond_2kb", row.UpstreamRequestID)
	}
	// 落库这一半照旧截：内存里收完整字节不等于往库里塞 8KB。
	if got := row.ErrorDetail.String; !strings.HasSuffix(got, "…[truncated]") || len(got) > (2<<10)+64 {
		t.Errorf("error_detail 长度 = %d，期望仍被截到 2KB 上下并说出截断", len(got))
	}
}

// 体不是合法 JSON（中转的 HTML 错误页、截断的流）：落空串，且请求本身的透传不受影响。
func TestNonJSONErrorBodyLeavesRequestIDEmptyAndDoesNotDisturbPassthrough(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	const html = "<html><head><title>502 Bad Gateway</title></head><body>nginx</body></html>"
	up.RespondWith(http.StatusBadGateway, map[string]string{"Content-Type": "text/html"}, html)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, 期望原样透传上游的 502", resp.StatusCode)
	}
	// 透传保真：解不开体不该改动回给客户端的一个字节。
	if body != html {
		t.Errorf("客户端收到的 body 与上游不一致：%s", body)
	}
	if got := gw.LastCallRow(t).UpstreamRequestID; got != "" {
		t.Errorf("call_logs.upstream_request_id = %q, 解不开的体期望落空串", got)
	}
}

// 转换路径与透传路径同一套取值（v0.56 原则：对账与走哪条路无关）。三档的取舍收在
// logCall 一处，这条钉的就是「转换路径也走得到第二档」。
func TestConvertedCallReadsRequestIDFromErrorBody(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(http.StatusTooManyRequests, map[string]string{
		"Content-Type": "application/json", "X-Request-Id": "proxy-uuid",
	}, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"},`+
		`"request_id":"req_convert_body"}`)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, 期望原样带回上游的 429", resp.StatusCode)
	}

	if got := gw.LastCallRow(t).UpstreamRequestID; got != "req_convert_body" {
		t.Errorf("call_logs.upstream_request_id = %q, 期望取错误体里的 req_convert_body", got)
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

// 落库还不够：portage-legacy#81 之前这个 id 只在库里和 slog 里，管理端一个地方都没露，而它落库的
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
	// 成功行也带 id，正是「详情」按钮不能只按 status >= 400 出的理由（portage-legacy#81）。
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
