package server_test

// 认证头写法的主缝用例（口径层 v1.13，#82）：raw 档从「上游设置」写进去之后，
// 转发路径打到上游的就得是 Authorization: <凭证原文>——不带 Bearer、不带 x-api-key。
// 起因是 PAI-EAS 网关只认裸 token，x-api-key 与 Bearer 都 401，而 401 的表象指向
// 凭证本身，人根本想不到是头名不对。

import (
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

func TestAuthSchemeRawSendsBareAuthorization(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "claude-direct", "anthropic", up.URL, "claude-3-5-sonnet", "eas-token==")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/settings",
		`{"name":"test-anthropic","auth_scheme":"raw"}`, nil)

	resp := g.Post(t, "/v1/messages", `{"model":"claude-direct","messages":[]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("转发失败：%d %s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	h := up.Last(t).Header
	if got := h.Get("Authorization"); got != "eas-token==" {
		t.Errorf("raw 档上游收到 Authorization = %q，期望裸凭证（无 Bearer 前缀）", got)
	}
	if got := h.Get("x-api-key"); got != "" {
		t.Errorf("raw 档不该再发 x-api-key，收到 %q", got)
	}
	if h.Get("anthropic-version") == "" {
		t.Error("anthropic-version 不该随认证档位丢")
	}
}
