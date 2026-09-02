package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// Responses namespace 摊平的两道就地 400（口径层 v1.14 ③④，#94）在 HTTP 面上的形状：
// DecodeRequest 返回的 *protocol.RequestError 经 convert.go 逐字回成 OpenAI 错误信封，
// code=invalid_value、param 指到那一格、message 点名来源；上游一个请求都收不到。
func TestResponsesNamespaceGates(t *testing.T) {
	cases := []struct {
		name      string
		fixture   string
		wantParam string
		wantIn    []string
	}{
		{
			name:      "撞名",
			fixture:   "in-responses-namespace-collision",
			wantParam: "tools[1].tools[0].name",
			wantIn:    []string{"tools[0]（顶层工具 request_user_input）", "tools[1].tools[0]（默认命名空间的子工具 request_user_input）"},
		},
		{
			name:      "摊平名超限",
			fixture:   "in-responses-namespace-badname",
			wantParam: "tools[0].tools[1].name",
			wantIn:    []string{"mcp__ade_asset_knowledge__readKnowledgeIndexFileFromCorporateSharedDrive", "长 72 个字符"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := newResponsesConvertGateway(t)
			resp := gw.Post(t, "/v1/responses", fixtureBody(t, tc.fixture), nil)
			body := gatewaytest.ReadBody(t, resp)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, 期望 400: %s", resp.StatusCode, body)
			}
			var env errorEnvelope
			if err := json.Unmarshal([]byte(body), &env); err != nil {
				t.Fatalf("响应不是错误信封: %v: %s", err, body)
			}
			if env.Error.Code != "invalid_value" || env.Error.Param != tc.wantParam {
				t.Errorf("code/param = %q/%q, 期望 invalid_value/%s", env.Error.Code, env.Error.Param, tc.wantParam)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(env.Error.Message, want) {
					t.Errorf("message 没点名 %q: %s", want, env.Error.Message)
				}
			}
			if up.Count() != 0 {
				t.Errorf("被拒的请求不该到上游, 上游收到 %d 个", up.Count())
			}
		})
	}
}
