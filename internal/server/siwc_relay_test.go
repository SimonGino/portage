package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// #213 的订阅渠道 relay 集成：*_account 不走透传（同协议也进转换）、出口按 D6 改写
// （强制 store:false / stream:true）、非流式做 SSE 聚合、previous_response_id 按该类型
// 强制否、上游 {"detail":…} 拒绝形态照实回显。

// siwcGoldenPath 是真机样本 responses-stream-siwc-text 的目录（#205 采集）。
const siwcGoldenPath = "../../testdata/golden/responses-stream-siwc-text"

// serveSIWCGolden 让假上游回真机 SSE（usage 在 response.completed、completed.output
// 为空数组、正文只在 delta——meta.json 的 source 记过这三条）。
func serveSIWCGolden(t *testing.T, up *gatewaytest.Upstream) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(siwcGoldenPath, "response.raw"))
	if err != nil {
		t.Skipf("样本尚未采集：%v", err)
	}
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}
}

// newSIWCGateway 种一个 chatgpt_account 渠道并把上游指向真机 SSE。凭证剩 30 分钟，
// 不触发懒刷新——本文件的用例测改写与聚合，不测刷新（那条在 subscription_test.go）。
func newSIWCGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	gw, up, _, _ := seedSubscriptionGateway(t,
		time.Now().Add(30*time.Minute),
		func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("凭证剩 30 分钟不该刷新 token 端点")
		})
	serveSIWCGolden(t, up)
	return gw, up
}

// assertUpstreamRewritten 断言上游收到的出站体带 D6 改写的恒定项：强制 stream:true /
// store:false、模型名换成纳管名。
func assertUpstreamRewritten(t *testing.T, up *gatewaytest.Upstream) {
	t.Helper()
	got := up.Last(t)
	if got.Path != "/v1/responses" {
		t.Errorf("上游 path = %q，订阅渠道 R 出口该打 /v1/responses", got.Path)
	}
	var body map[string]any
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("出站体不是 JSON: %v；body=%s", err, got.Body)
	}
	if v, _ := body["stream"].(bool); !v {
		t.Errorf("出站体 stream = %v，期望 true（订阅渠道强制 SSE）", body["stream"])
	}
	if v, ok := body["store"].(bool); !ok || v {
		t.Errorf("出站体 store = %v，期望 false（恒发）", body["store"])
	}
	if body["model"] != "gpt-6.1-sol" {
		t.Errorf("出站体 model = %v，期望纳管名 gpt-6.1-sol", body["model"])
	}
}

// TestSubscriptionNonStreamAggregatesResponses：R 入口、客户端要非流式——上游恒为
// SSE，网关收完聚出完整 Responses 响应体，不拒非流式请求。
func TestSubscriptionNonStreamAggregatesResponses(t *testing.T) {
	gw, up := newSIWCGateway(t)

	// 客户端的杂头一个都不许到上游（出站头只 Authorization: Bearer <access>，
	// 其余走 applyHeaders 白名单）。挑两个真实 harness 会发的：OpenAI-Beta 与
	// anthropic-version——后者尤其不该出现在 openai_responses 渠道的出站头上。
	header := map[string]string{
		"OpenAI-Beta":       "responses=v1",
		"anthropic-version": "2023-06-01",
	}
	resp := gw.Post(t, "/v1/responses", siwcRequest, header)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q，非流式客户端该拿到完整 JSON 体", ct)
	}

	assertUpstreamRewritten(t, up)
	got := up.Last(t)
	if b := got.Header.Get("OpenAI-Beta"); b != "" {
		t.Errorf("客户端的 OpenAI-Beta 头透到了上游: %q", b)
	}
	if v := got.Header.Get("anthropic-version"); v != "" {
		t.Errorf("客户端的 anthropic-version 头透到了上游: %q", v)
	}
	if a := got.Header.Get("Authorization"); a != "Bearer at-old" {
		t.Errorf("上游 Authorization = %q，期望 Bearer <access>（凭证未到刷新窗口，access 原样）", a)
	}

	var body struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(gatewaytest.ReadBody(t, resp)), &body); err != nil {
		t.Fatalf("聚合体不是 JSON: %v", err)
	}
	if body.Status != "completed" {
		t.Errorf("status = %q，期望 completed", body.Status)
	}
	if len(body.Output) != 1 || body.Output[0].Type != "message" ||
		len(body.Output[0].Content) != 1 || body.Output[0].Content[0].Text != "pong" {
		t.Errorf("聚合体正文不对: %+v", body.Output)
	}
	if body.Usage.InputTokens != 16 || body.Usage.OutputTokens != 5 {
		t.Errorf("聚合体 usage = %d/%d，期望 16/5（usage 在 response.completed）",
			body.Usage.InputTokens, body.Usage.OutputTokens)
	}

	// 记账：usage 出自挂在原始字节上的 Tap（SSE 模式），客户端的非流式不改流水那格。
	row := gw.LastCallRow(t)
	if !row.InputTokens.Valid || row.InputTokens.Int64 != 16 ||
		!row.OutputTokens.Valid || row.OutputTokens.Int64 != 5 {
		t.Errorf("流水 token = %v/%v，期望 16/5", row.InputTokens, row.OutputTokens)
	}
	if row.IsStream.Valid && row.IsStream.Bool {
		t.Errorf("流水 is_stream = %v，客户端要的是非流式", row.IsStream.Bool)
	}
	if row.UpstreamEndpoint != "/v1/responses" {
		t.Errorf("出站端点 = %q，期望 /v1/responses", row.UpstreamEndpoint)
	}
}

// TestSubscriptionNonStreamAggregatesAnthropic：A 入口（/v1/messages、非流式）同一份
// 上游 SSE 聚成 Anthropic 完整响应体。
func TestSubscriptionNonStreamAggregatesAnthropic(t *testing.T) {
	gw, up := newSIWCGateway(t)
	const req = `{"model":"` + accessPointModel + `","max_tokens":64,"stream":false,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]}]}`

	resp := gw.Post(t, "/v1/messages", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	assertUpstreamRewritten(t, up)

	var body struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(gatewaytest.ReadBody(t, resp)), &body); err != nil {
		t.Fatalf("聚合体不是 JSON: %v", err)
	}
	if len(body.Content) != 1 || body.Content[0].Text != "pong" {
		t.Errorf("聚合体正文不对: %+v", body.Content)
	}
	if body.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q，期望 end_turn", body.StopReason)
	}
	if body.Usage.InputTokens != 16 || body.Usage.OutputTokens != 5 {
		t.Errorf("聚合体 usage = %d/%d，期望 16/5", body.Usage.InputTokens, body.Usage.OutputTokens)
	}
}

// TestSubscriptionNonStreamAggregatesChatCompletions：CC 入口
// （/v1/chat/completions、非流式）同一份上游 SSE 聚成 CC 完整响应体。
func TestSubscriptionNonStreamAggregatesChatCompletions(t *testing.T) {
	gw, up := newSIWCGateway(t)
	const req = `{"model":"` + accessPointModel + `","stream":false,` +
		`"messages":[{"role":"user","content":"ping"}]}`

	resp := gw.Post(t, "/v1/chat/completions", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	assertUpstreamRewritten(t, up)

	var body struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(gatewaytest.ReadBody(t, resp)), &body); err != nil {
		t.Fatalf("聚合体不是 JSON: %v", err)
	}
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != "pong" {
		t.Errorf("聚合体正文不对: %+v", body.Choices)
	}
	if body.Usage.PromptTokens != 16 || body.Usage.CompletionTokens != 5 {
		t.Errorf("聚合体 usage = %d/%d，期望 16/5", body.Usage.PromptTokens, body.Usage.CompletionTokens)
	}
}

// TestSubscriptionStreamRelaysSSE：客户端要流式时上游 SSE → canonical → 入口协议
// SSE 逐帧转发（streamConverted），不做聚合。
func TestSubscriptionStreamRelaysSSE(t *testing.T) {
	gw, up := newSIWCGateway(t)
	const req = `{"model":"` + accessPointModel + `","stream":true,` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}]}`

	resp := gw.Post(t, "/v1/responses", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q，流式客户端该拿到 SSE", ct)
	}
	body := gatewaytest.ReadBody(t, resp)
	if !strings.Contains(body, "response.completed") {
		t.Errorf("SSE 里没有 response.completed 终态帧")
	}
	if !strings.Contains(body, "pong") {
		t.Errorf("SSE 里没有正文 delta")
	}
	assertUpstreamRewritten(t, up)
	row := gw.LastCallRow(t)
	if !row.IsStream.Valid || !row.IsStream.Bool {
		t.Errorf("流水 is_stream = %v，期望 true", row.IsStream)
	}
}

// TestSubscriptionRejectsPreviousResponseIDDespiteCapability：渠道能力位是「支持
// 有状态续链」也一样拒——订阅渠道 supports_stateful_responses 强制否（库默认 true，
// 正好证明这道闸不再看能力位）。
func TestSubscriptionRejectsPreviousResponseIDDespiteCapability(t *testing.T) {
	gw, up := newSIWCGateway(t)
	const req = `{"model":"` + accessPointModel + `","stream":false,` +
		`"previous_response_id":"resp_abc123",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"继续"}]}]}`

	resp := gw.Post(t, "/v1/responses", req, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	body := gatewaytest.ReadBody(t, resp)
	assertPreviousResponseRejection(t, body)
	if !strings.Contains(body, "订阅渠道") {
		t.Errorf("文案该说订阅渠道不支持有状态续链：%s", body)
	}
	if n := up.Count(); n != 0 {
		t.Errorf("打了上游 %d 次，期望 0", n)
	}
	row := gw.LastCallRow(t)
	if !row.Error.Valid || row.Error.String != "rejected" {
		t.Errorf("error 列 = %v，期望 rejected", row.Error)
	}
	if row.UpstreamEndpoint != "" {
		t.Errorf("出站端点 = %q，期望空串", row.UpstreamEndpoint)
	}
}

// TestSubscriptionCompactionTurnGoesConversion：Codex 压缩 turn 在订阅渠道上走转换
// 路径的本地合成，不再被透传前置闸拦——订阅渠道没有透传那半边，能力位不再参与判。
func TestSubscriptionCompactionTurnGoesConversion(t *testing.T) {
	gw, up := newSIWCGateway(t)
	const req = `{"model":"` + accessPointModel + `","stream":false,` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"长会话"}]},` +
		`{"type":"compaction_trigger"}]}`

	resp := gw.Post(t, "/v1/responses", req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（压缩 turn 本地合成，不拒）；body=%s",
			resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	assertUpstreamRewritten(t, up)
	var body map[string]any
	if err := json.Unmarshal(up.Last(t).Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, has := body["tools"]; has {
		t.Errorf("压缩 turn 该剥掉 tools: %s", body["tools"])
	}
	var sawSummaryPrompt bool
	for _, item := range body["input"].([]any) {
		im, _ := item.(map[string]any)
		parts, _ := im["content"].([]any)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if s, _ := pm["text"].(string); strings.HasPrefix(s, "You are performing a CONTEXT CHECKPOINT COMPACTION") {
				sawSummaryPrompt = true
			}
		}
	}
	if !sawSummaryPrompt {
		t.Errorf("出站 input 里没有总结指令，压缩 turn 没走本地合成：%s", up.Last(t).Body)
	}
}

// TestSubscriptionNonStreamMidStreamFailureReturns502：上游 200 开了流、中途
// response.failed——非流式客户端等的是完整 JSON，流内 error 帧表达不了，按上游错误
// 收场回 502，上游的 message 落 error_detail。
func TestSubscriptionNonStreamMidStreamFailureReturns502(t *testing.T) {
	gw, up := newSIWCGateway(t)
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}

event: response.failed
data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"model is overloaded"},"output":[]}}

`))
	}

	resp := gw.Post(t, "/v1/responses", siwcRequest, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, 期望 502；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	row := gw.LastCallRow(t)
	if !row.Error.Valid || row.Error.String != "upstream_error" {
		t.Errorf("error 列 = %v, 期望 upstream_error", row.Error)
	}
	if !row.ErrorDetail.Valid || !strings.Contains(row.ErrorDetail.String, "model is overloaded") {
		t.Errorf("error_detail = %v, 期望上游的 message 落库", row.ErrorDetail)
	}
}

// TestSubscriptionNonStreamTruncatedStreamReturns502：上游 200 回的不是 SSE、没给
// 终态：不把空壳聚给客户端（200 + 空响应体是查不出因的静默失败），回 502。
func TestSubscriptionNonStreamTruncatedStreamReturns502(t *testing.T) {
	gw, up := newSIWCGateway(t)
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"foo":"bar"}`))
	}

	resp := gw.Post(t, "/v1/responses", siwcRequest, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, 期望 502；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	row := gw.LastCallRow(t)
	if !row.Error.Valid || row.Error.String != "upstream_error" {
		t.Errorf("error 列 = %v, 期望 upstream_error", row.Error)
	}
	if !row.ErrorDetail.Valid || !strings.Contains(row.ErrorDetail.String, "终态") {
		t.Errorf("error_detail = %v, 期望记下「没给终态」的原文", row.ErrorDetail)
	}
}

// TestSubscriptionUpstreamDetailRejectSurfaces：上游的 {"detail":…} 拒绝形态
// （responses-siwc-evidence/reject-* 一律此形态、无 error.code）——状态码保留、
// 句子照实回显给客户端、原文落 error_detail。构造同一形态，不另采样本。
func TestSubscriptionUpstreamDetailRejectSurfaces(t *testing.T) {
	gw, up := newSIWCGateway(t)
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"Store must be set to false"}`))
	}

	resp := gw.Post(t, "/v1/responses", siwcRequest, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400 原样保留；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	var env errorEnvelope
	if err := json.Unmarshal([]byte(gatewaytest.ReadBody(t, resp)), &env); err != nil {
		t.Fatalf("错误体不是 JSON: %v", err)
	}
	if env.Error.Message != "Store must be set to false" {
		t.Errorf("回显 = %q，上游的 detail 句子该照实给客户端", env.Error.Message)
	}
	row := gw.LastCallRow(t)
	if !row.Error.Valid || row.Error.String != "upstream_error" {
		t.Errorf("error 列 = %v，期望 upstream_error", row.Error)
	}
	if !row.ErrorDetail.Valid || !strings.Contains(row.ErrorDetail.String, "Store must be set to false") {
		t.Errorf("error_detail = %v，期望上游原文落库", row.ErrorDetail)
	}
}

// TestSubscriptionOutboundForcesBearerOverRawScheme：订阅渠道的出站鉴权头是 D6 逐字
// 契约（Authorization: Bearer <access>）——渠道上配了 raw 档也不照抄：access 由凭证票
// 换出、上游固定是 ChatGPT 后端，裸 access 出站只会 401。渠道额外静态头不在此拦
// （运维配置自担，handoff-213 已裁决的接受边界，Codex 终审同条发现的另一半）。
func TestSubscriptionOutboundForcesBearerOverRawScheme(t *testing.T) {
	gw, up := newSIWCGateway(t)
	if _, err := gw.DB.Exec(`UPDATE channels SET auth_scheme = 'raw' WHERE credential_type = 'chatgpt_account'`); err != nil {
		t.Fatalf("改 auth_scheme 失败: %v", err)
	}

	resp := gw.Post(t, "/v1/responses", siwcRequest, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if a := up.Last(t).Header.Get("Authorization"); a != "Bearer at-old" {
		t.Errorf("上游 Authorization = %q, 期望 Bearer <access>——raw 档不该把裸 access 发出去", a)
	}
}
