package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件测 Codex 压缩止血闸（口径层 v0.54，#71）。要钉的就一句话：压缩 turn 绝不能
// 表现成「一次成功的普通转发」——那正是让 Codex 收到 0 个 compaction item、当场 Fatal
// 且不重试不降级的那条路。

// compactionRequest 是一次压缩 turn 的请求形态：正常历史 + 尾部一个 compaction_trigger。
const compactionRequest = `{"model":"gw-sonnet","stream":true,` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
	`{"type":"compaction_trigger"}]}`

// plainResponsesRequest 是同一个渠道上的普通 turn，用来钉「闸不误伤」。
const plainResponsesRequest = `{"model":"gw-sonnet","stream":false,` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

// TestCompactionRejectedOnConvert：转换路径无条件拒。能力位在这条路上不参与判断——
// trigger 根本到不了上游，渠道认不认它没有意义。
func TestCompactionRejectedOnConvert(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proto string
		model string
	}{
		{"R→CC", "openai", ccUpstreamModel},
		{"R→A", "anthropic", upstreamModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := gatewaytest.NewUpstream(t)
			db := gatewaytest.NewDB(t)
			gatewaytest.SeedPassthrough(t, db, accessPointModel, tc.proto, up.URL, tc.model, openaiCredential)
			gw := gatewaytest.Start(t, db)

			resp := gw.Post(t, "/v1/responses", compactionRequest, nil)
			body := gatewaytest.ReadBody(t, resp)

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, 期望 400；body=%s", resp.StatusCode, body)
			}
			if !strings.Contains(body, "不支持 Codex 压缩") {
				t.Errorf("错误文案没点明压缩：%s", body)
			}
			if strings.Contains(body, up.URL) || strings.Contains(body, openaiCredential) {
				t.Errorf("错误回显泄露了 base_url 或上游凭证：%s", body)
			}
			// 最要紧的一条：没打给上游。打过去就等于上游拿到一个它不知道要压缩的
			// 请求，然后正常回话——正是本票要消灭的那个「成功的转发」。
			if up.Count() != 0 {
				t.Errorf("上游收到了 %d 个请求，压缩 turn 不该转发出去", up.Count())
			}

			row := gw.LastCallRow(t)
			if row.Status != http.StatusBadRequest || row.Error.String != "compaction_unsupported" {
				t.Errorf("流水 status=%d error=%q, 期望 400 / compaction_unsupported", row.Status, row.Error.String)
			}
			lines := gw.Lines("拒绝 Codex 压缩 turn：渠道不支持 compaction")
			if len(lines) != 1 {
				t.Fatalf("drop 日志 %d 行, 期望 1 行；已落日志：%s", len(lines), gw.RawLog())
			}
			if got := lines[0].Str("path"); got != "convert" {
				t.Errorf("日志 path = %q, 期望 convert", got)
			}
		})
	}
}

// TestCompactionRejectedOnPassthroughWithoutCapability：透传路径同样拒——Responses
// 形状的 wire 不等于支持压缩，这一格正是能力位为否时保护的那格。
func TestCompactionRejectedOnPassthroughWithoutCapability(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/responses", compactionRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400；body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "支持 Codex 压缩") {
		t.Errorf("错误文案没指出去哪儿把能力位勾上：%s", body)
	}
	if up.Count() != 0 {
		t.Errorf("上游收到了 %d 个请求，压缩 turn 不该透传出去", up.Count())
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "compaction_unsupported" {
		t.Errorf("流水 error = %q, 期望 compaction_unsupported", row.Error.String)
	}
	lines := gw.Lines("拒绝 Codex 压缩 turn：渠道不支持 compaction")
	if len(lines) != 1 || lines[0].Str("path") != "passthrough" {
		t.Fatalf("drop 日志不对（%d 行）：%s", len(lines), gw.RawLog())
	}
}

// TestCompactionPassthroughWithCapability：能力位勾上的透传渠道照旧放行，且 trigger
// 逐字节到达上游——闸只拦「压缩注定失败」的那两格，不改变本来就能用的那格。
func TestCompactionPassthroughWithCapability(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "test-openai_responses", "openai_responses", up.URL, openaiCredential)
	gatewaytest.SetChannelCompaction(t, db, channelID, true)
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "gpt-5.6")
	apID := gatewaytest.SeedAccessPoint(t, db, accessPointModel)
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
	gw := gatewaytest.Start(t, db)

	const upstreamBody = `{"id":"resp_1","object":"response","output":[{"type":"compaction"}]}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/responses", compactionRequest, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	want := strings.Replace(compactionRequest, `"model":"`+accessPointModel+`"`, `"model":"gpt-5.6"`, 1)
	if got := string(up.Last(t).Body); got != want {
		t.Errorf("请求体除顶层 model 外应逐字节保真\n期望: %s\n收到: %s", want, got)
	}
}

// TestPlainTurnUnaffectedByCompactionGate：能力位为否只挡压缩 turn。普通请求照走，
// 否则这一位就成了「Responses 透传总开关」。
func TestPlainTurnUnaffectedByCompactionGate(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
	gw := gatewaytest.Start(t, db)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, `{"id":"resp_1"}`)

	resp := gw.Post(t, "/v1/responses", plainResponsesRequest, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if up.Count() != 1 {
		t.Errorf("上游收到 %d 个请求, 期望 1", up.Count())
	}
}

// TestV1CompactNotImplemented：legacy 的 v1 compact 回 501 而不是裸 404 或一页 SPA。
// 不带 key 也一样——它无条件拒绝，先撞 401 只会让人以为端点存在但没授权。
func TestV1CompactNotImplemented(t *testing.T) {
	db := gatewaytest.NewDB(t)
	up := gatewaytest.NewUpstream(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
	gw := gatewaytest.Start(t, db)

	for _, tc := range []struct {
		name   string
		header map[string]string
	}{
		{"带 key", nil},
		{"不带 key", map[string]string{"x-api-key": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := gw.Post(t, "/v1/responses/compact", `{"model":"gw-sonnet"}`, tc.header)
			body := gatewaytest.ReadBody(t, resp)
			if resp.StatusCode != http.StatusNotImplemented {
				t.Fatalf("状态码 = %d, 期望 501；body=%s", resp.StatusCode, body)
			}
			if !strings.Contains(body, "网关不支持 v1 compact") {
				t.Errorf("501 文案不对：%s", body)
			}
		})
	}
}
