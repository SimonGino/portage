package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 口径层 v0.53：上游说不行时，把它说的那段话截前 2KB 落 call_logs.error_detail。
// 此前流水里只有我方固定词表，管理端点开失败行看不到任何细节，人只能去翻网关日志。
func TestUpstreamErrorDetailIsPersistedOnPassthrough(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	const upstreamSays = `{"type":"error","error":{"type":"rate_limit_error","message":"账户额度用尽"}}`
	up.RespondWith(http.StatusTooManyRequests,
		map[string]string{"Content-Type": "application/json"}, upstreamSays)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, 期望把上游的 429 原样透传", resp.StatusCode)
	}
	gatewaytest.ReadBody(t, resp)

	row := gw.LastCallRow(t)
	if row.Status != http.StatusTooManyRequests {
		t.Errorf("流水 status = %d, 期望 429", row.Status)
	}
	// 这一条正是新出现的列组合：透传成功不算网关侧错误（v0.28 纪律），error 列空着，
	// 而 error_detail 有值。管理端的「详情」按钮因此按状态码出，不按 error 非空。
	if row.Error.Valid {
		t.Errorf("透传上游 4xx 不该往 error 列写词：%q", row.Error.String)
	}
	if !row.ErrorDetail.Valid || !strings.Contains(row.ErrorDetail.String, "账户额度用尽") {
		t.Errorf("error_detail = %q, 期望含上游原文", row.ErrorDetail.String)
	}
}

// 截断有上限，且截断这件事本身要说出来——否则一段被砍掉一半的 JSON 看起来像上游
// 发了个坏包。
func TestUpstreamErrorDetailIsTruncated(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	huge := `{"error":{"message":"` + strings.Repeat("x", 8<<10) + `"}}`
	up.RespondWith(http.StatusInternalServerError, nil, huge)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

	row := gw.LastCallRow(t)
	got := row.ErrorDetail.String
	// 2KB 正文 + 那句截断标记，给一点余量；关键是它没把 8KB 整段吞进库里。
	if len(got) == 0 || len(got) > (2<<10)+64 {
		t.Errorf("error_detail 长度 = %d, 期望被截到 2KB 上下", len(got))
	}
	if !strings.HasSuffix(got, "…[truncated]") {
		t.Errorf("截断了却没说：%q", got[max(0, len(got)-40):])
	}
}

// 转换路径：回给客户端的只有 error.message 一句（我们的错误契约），落库落全。
// 两者是不同的两份文本，这条钉住的就是「别拿回显那句当排障依据」。
func TestUpstreamErrorDetailIsPersistedOnConvertedPath(t *testing.T) {
	gw, up := newConvertGateway(t)
	const upstreamSays = `{"error":{"message":"model not found","type":"invalid_request_error","param":"model"}}`
	up.RespondWith(http.StatusBadRequest,
		map[string]string{"Content-Type": "application/json"}, upstreamSays)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, 期望原样带回上游的 400", resp.StatusCode)
	}
	// 客户端拿到的是 Anthropic 形状的错误，只带 message 那一句——上游那些自有字段
	// （这里是 param）不该出现在回显里。挑 param 而不是 type 做判据：错误类型名两边
	// 恰好用同一套词，撞上了并不说明转发了原文。
	if strings.Contains(body, `"param"`) {
		t.Errorf("回显把上游错误体整段转发了：%s", body)
	}

	row := gw.LastCallRow(t)
	if !strings.Contains(row.ErrorDetail.String, `"param"`) {
		t.Errorf("error_detail = %q, 期望是上游原文而不是回显那一句", row.ErrorDetail.String)
	}
}

// 连不上上游那一支没有响应体可截，落的是传输错误本身——不落的话，最想看细节的
// 半边（连不上、握手失败、读超时）恰好永远是空。
func TestTransportErrorLandsInErrorDetail(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	// 关掉假上游，留一个必然连不上的地址。
	dead := up.URL
	up.Close()
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", dead, upstreamModel, anthropicCredential)
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, 期望 502", resp.StatusCode)
	}

	row := gw.LastCallRow(t)
	if row.Error.String != "upstream_error" {
		t.Errorf("error = %q, 期望 upstream_error", row.Error.String)
	}
	// 这条路径上没有响应体可截，能留的就是拨号/握手错误本身——不留的话，最想看细节的
	// 那一半失败反而永远是空的。
	if !strings.Contains(row.ErrorDetail.String, "connect") && !strings.Contains(row.ErrorDetail.String, "refused") {
		t.Errorf("error_detail 没有落下拨号错误：%q", row.ErrorDetail.String)
	}
	// Redact 摘掉 url.Error 外层，所以带方法与完整 URL 的那层不进库；拨号错误里的
	// host:port 仍会出现，这没问题——error_detail 只走管理端，管理端本来就在渠道
	// 表单里看得到 base_url。真正的红线是它不许进回显：转发路径写给客户端的永远是
	// 我方词表与 error.message 一句。
	if body := gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil)); strings.Contains(body, dead) {
		t.Errorf("回显泄露了 base_url：%s", body)
	}
}

// 成功的调用不留原文：这一列只为失败存在，成功也存等于给每行挂一段没人会看的文本。
func TestSuccessfulCallHasNoErrorDetail(t *testing.T) {
	gw, _ := newAnthropicGateway(t)
	gw.Post(t, "/v1/messages", anthropicRequest, nil)
	if row := gw.LastCallRow(t); row.ErrorDetail.Valid {
		t.Errorf("成功的调用不该有 error_detail：%q", row.ErrorDetail.String)
	}
}
