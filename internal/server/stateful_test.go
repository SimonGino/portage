package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件测 Responses 有状态续链（previous_response_id）在网关这一层的三种收场
// （口径层 v0.88）：转换路径无条件拒、透传路径按渠道能力位定、位为是照旧字节直传。
//
// 共同要钉的是同一句话：带 previous_response_id 的请求**绝不能**表现成「一次成功的
// 普通转发」——静默丢掉它之后每一轮都是单轮，客户端以为历史生效，产出静默劣化且无从
// 归因。这正是 v0.88 推翻 v1「静默丢弃」的全部理由。

// statefulRequest 是一次续链请求：只带最后一轮 input，历史靠 previous_response_id。
const statefulRequest = `{"model":"gw-sonnet","stream":false,` +
	`"previous_response_id":"resp_abc123",` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"继续"}]}]}`

// errorEnvelope 是 OpenAI 系错误体的形状（Chat Completions 与 Responses 共用）。
// code 与 param 两位是本次要钉的契约：客户端认 code 才知道该重发完整 input。
type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error"`
}

// assertPreviousResponseRejection 把两条路共用的那份断言收在一处：400 + 逐字的
// code/param + 一句给得出动作的文案 + 不许泄露上游 base_url 与 key。
func assertPreviousResponseRejection(t *testing.T, body string) {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("错误体不是 JSON: %v；body=%s", err, body)
	}
	if env.Error.Code != "previous_response_not_found" {
		t.Errorf("code = %q, 期望 previous_response_not_found（客户端认这个词才会重发完整 input）", env.Error.Code)
	}
	if env.Error.Param != "previous_response_id" {
		t.Errorf("param = %q, 期望 previous_response_id", env.Error.Param)
	}
	if env.Error.Type != "invalid_request_error" {
		t.Errorf("type = %q, 期望 invalid_request_error", env.Error.Type)
	}
	if !strings.Contains(env.Error.Message, "重发") {
		t.Errorf("文案没给可执行指引：%s", env.Error.Message)
	}
	// 错误回显严禁泄露上游 base_url 与 key（口径层 §2.7）。
	if strings.Contains(body, "127.0.0.1") || strings.Contains(body, "sk-") {
		t.Errorf("错误体泄露了上游地址或凭证：%s", body)
	}
}

// TestPreviousResponseRejectedOnConvert：转换路径无条件拒。R→A 与 R→CC 各跑一遍，
// 且**把渠道能力位打成「支持」**——这条路上根本不看那一位：id 是另一套协议的上游不认
// 的句柄，网关又不存会话历史，展不开。
func TestPreviousResponseRejectedOnConvert(t *testing.T) {
	for _, tc := range []struct {
		name       string
		protocol   string
		credential string
	}{
		{"R→A", "anthropic", anthropicCredential},
		{"R→CC", "openai", openaiCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := gatewaytest.NewUpstream(t)
			db := gatewaytest.NewDB(t)
			channelID := gatewaytest.SeedChannel(t, db, "test-"+tc.protocol, tc.protocol, up.URL, tc.credential)
			// 位为「支持」也照拒：这条路的判据里没有渠道能力位。
			gatewaytest.SetChannelStateful(t, db, channelID, true)
			modelID := gatewaytest.SeedChannelModel(t, db, channelID, upstreamModel)
			apID := gatewaytest.SeedAccessPoint(t, db, accessPointModel)
			gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
			gw := gatewaytest.Start(t, db)

			resp := gw.Post(t, "/v1/responses", statefulRequest, nil)
			body := gatewaytest.ReadBody(t, resp)

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, 期望 400；body=%s", resp.StatusCode, body)
			}
			assertPreviousResponseRejection(t, body)
			if up.Count() != 0 {
				t.Errorf("上游收到了 %d 个请求，被拒的请求一个字节都不该发出去", up.Count())
			}
		})
	}
}

// TestPreviousResponseRejectedOnPassthroughWithoutCapability：透传渠道明说不支持时同样拒。
func TestPreviousResponseRejectedOnPassthroughWithoutCapability(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "test-openai_responses", "openai_responses", up.URL, openaiCredential)
	gatewaytest.SetChannelStateful(t, db, channelID, false)
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "gpt-5.6")
	apID := gatewaytest.SeedAccessPoint(t, db, accessPointModel)
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/responses", statefulRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400；body=%s", resp.StatusCode, body)
	}
	assertPreviousResponseRejection(t, body)
	if !strings.Contains(body, "有状态续链") {
		t.Errorf("错误文案没指出去哪儿把能力位勾上：%s", body)
	}
	if up.Count() != 0 {
		t.Errorf("上游收到了 %d 个请求，闸在拨号之前", up.Count())
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "rejected" {
		t.Errorf("流水 error = %q, 期望 rejected（回绝那一半，一个字节都没到上游）", row.Error.String)
	}
	if row.UpstreamEndpoint != "" {
		t.Errorf("upstream_endpoint = %q, 回绝的行那一列必然是空串", row.UpstreamEndpoint)
	}
	if lines := gw.Lines("拒绝 Responses 有状态续链：透传渠道未声明支持 previous_response_id"); len(lines) != 1 {
		t.Fatalf("拒绝日志不对（%d 行）：%s", len(lines), gw.RawLog())
	}
}

// TestPreviousResponsePassthroughByDefault：能力位的默认值是**是**，所以一个什么都
// 没配过的 Responses 透传渠道照旧字节直传，previous_response_id 逐字节到达上游。
//
// 这一条钉的是默认方向：默认取否会把一批本来正常工作的续链一次性打断，而那种打断
// 在页面上看不出来。
func TestPreviousResponsePassthroughByDefault(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
	gw := gatewaytest.Start(t, db)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, `{"id":"resp_1"}`)

	resp := gw.Post(t, "/v1/responses", statefulRequest, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	want := strings.Replace(statefulRequest, `"model":"`+accessPointModel+`"`, `"model":"gpt-5.6"`, 1)
	if got := string(up.Last(t).Body); got != want {
		t.Errorf("请求体除顶层 model 外应逐字节保真\n期望: %s\n收到: %s", want, got)
	}
}

// TestPlainTurnUnaffectedByStatefulGate：能力位为否只挡**非空**的 previous_response_id。
// 三种空值形态照走，否则这一位就成了「Responses 透传总开关」——空串那一格尤其要钉：
// 它是客户端「显式表示没有上一轮」的常见写法，误拒即拒掉正常首轮。
func TestPlainTurnUnaffectedByStatefulGate(t *testing.T) {
	withPrev := func(v string) string {
		return strings.Replace(plainResponsesRequest, `{"model":`, `{"previous_response_id":`+v+`,"model":`, 1)
	}
	for _, tc := range []struct{ name, body string }{
		{"缺失", plainResponsesRequest},
		{"null", withPrev("null")},
		{"空串", withPrev(`""`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := gatewaytest.NewUpstream(t)
			db := gatewaytest.NewDB(t)
			channelID := gatewaytest.SeedChannel(t, db, "test-openai_responses", "openai_responses", up.URL, openaiCredential)
			gatewaytest.SetChannelStateful(t, db, channelID, false)
			modelID := gatewaytest.SeedChannelModel(t, db, channelID, "gpt-5.6")
			apID := gatewaytest.SeedAccessPoint(t, db, accessPointModel)
			gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
			gw := gatewaytest.Start(t, db)
			up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, `{"id":"resp_1"}`)

			resp := gw.Post(t, "/v1/responses", tc.body, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
			}
			if up.Count() != 1 {
				t.Errorf("上游收到 %d 个请求, 期望 1", up.Count())
			}
		})
	}
}
