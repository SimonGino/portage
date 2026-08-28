package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件测纳管模型的输入上限（估算）闸（口径层 v0.99）：判据是入站原始 body 字节数
// ÷ 4，透传与转换两条路同一把尺，超限 413 + 流水词 request_too_large，一个字节不打
// 上游；count_tokens 豁免。

// oversizedMessagesBody 造一个估算必然超过 limit=100（即 400 字节）的 Anthropic 形状
// 请求体。内容是什么无所谓——闸只看字节数，不解析 messages。
func oversizedMessagesBody() string {
	return `{"model":"` + accessPointModel + `","messages":[{"role":"user","content":"` +
		strings.Repeat("a", 600) + `"}]}`
}

// TestInputLimitRejectsOversizedPassthrough：透传路径超限即 413，字节不出网关。
func TestInputLimitRejectsOversizedPassthrough(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	gatewaytest.SetModelMaxInputTokens(t, db, upstreamModel, 100)
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/messages", oversizedMessagesBody(), nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d, 期望 413；body=%s", resp.StatusCode, body)
	}
	// Anthropic 官方错误型正是给这一格预留的那个词。
	if !strings.Contains(body, `"request_too_large"`) {
		t.Errorf("错误型不是 request_too_large：%s", body)
	}
	// 文案说破估算、带上限值、只提对外模型名——上游模型名属于渠道内部，不外泄。
	if !strings.Contains(body, "估算") || !strings.Contains(body, "100") {
		t.Errorf("文案没带估算说明与上限值：%s", body)
	}
	if !strings.Contains(body, accessPointModel) || strings.Contains(body, upstreamModel) {
		t.Errorf("错误回显该提对外模型名、不提上游模型名：%s", body)
	}
	if up.Count() != 0 {
		t.Errorf("上游收到了 %d 个请求，超限请求一个字节都不该出网关", up.Count())
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "request_too_large" {
		t.Errorf("流水 error = %q, 期望 request_too_large", row.Error.String)
	}
}

// TestInputLimitExactAtLimitPasses：恰好压线放行——边界取 >，不取 >=。
func TestInputLimitExactAtLimitPasses(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	body := oversizedMessagesBody()
	gatewaytest.SetModelMaxInputTokens(t, db, upstreamModel, len(body)/4)
	gw := gatewaytest.Start(t, db)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_1","model":"`+upstreamModel+`","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":1,"output_tokens":1}}`)

	resp := gw.Post(t, "/v1/messages", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（估算 == 上限不算超）；body=%s",
			resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if up.Count() != 1 {
		t.Errorf("上游收到 %d 个请求, 期望 1", up.Count())
	}
}

// TestInputLimitAppliesOnConvertPath：转换路径同一把尺——估的也是入站原始字节，
// 不是转换后的 body。闸在分岔之前，两条路没有理由各一套语义。
func TestInputLimitAppliesOnConvertPath(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, "gpt-4o", openaiCredential)
	gatewaytest.SetModelMaxInputTokens(t, db, "gpt-4o", 100)
	gw := gatewaytest.Start(t, db)

	// Anthropic 入站 → openai 渠道 = 转换路径。
	resp := gw.Post(t, "/v1/messages", oversizedMessagesBody(), nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d, 期望 413；body=%s", resp.StatusCode, body)
	}
	if up.Count() != 0 {
		t.Errorf("上游收到了 %d 个请求", up.Count())
	}
	if row := gw.LastCallRow(t); row.Error.String != "request_too_large" {
		t.Errorf("流水 error = %q, 期望 request_too_large", row.Error.String)
	}
}

// TestCountTokensExemptFromInputLimit：count_tokens 豁免——那条路不打上游生成侧，
// 且它正是客户端用来自行判断「要不要压缩」的工具，拦它适得其反。
func TestCountTokensExemptFromInputLimit(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	gatewaytest.SetModelMaxInputTokens(t, db, upstreamModel, 1)
	gw := gatewaytest.Start(t, db)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"input_tokens":42}`)

	resp := gw.Post(t, "/v1/messages/count_tokens", oversizedMessagesBody(), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（count_tokens 不受输入上限管）；body=%s",
			resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if up.Count() != 1 {
		t.Errorf("上游收到 %d 个请求, 期望 1（anthropic 出口原样转发）", up.Count())
	}
}
