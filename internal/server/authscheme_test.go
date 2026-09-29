package server_test

// 认证头写法的主缝用例（口径层 v1.13，#82）：raw 档从「上游设置」写进去之后，
// 转发路径打到上游的就得是 Authorization: <凭证原文>——不带 Bearer、不带 x-api-key。
// 起因是 PAI-EAS 网关只认裸 token，x-api-key 与 Bearer 都 401，而 401 的表象指向
// 凭证本身，人根本想不到是头名不对。

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/store"
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

// 渠道额外出站头（#137）：opencode-go 缺 x-opencode-session 一律 400。上游得**收到**
// 那个头；管理端保存渠道设置不带这个字段，不能把它清掉。
func TestChannelHeadersReachUpstream(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "go-model", "openai", up.URL, "glm", "sk-go")
	if _, err := db.Exec(`UPDATE channels SET headers = '{"User-Agent":"portage/1","x-opencode-session":"s1"}'`); err != nil {
		t.Fatalf("写 headers: %v", err)
	}
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)
	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/settings", `{"name":"test-openai"}`, nil)

	resp := g.Post(t, "/v1/chat/completions", `{"model":"go-model","messages":[]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("转发失败：%d %s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	h := up.Last(t).Header
	if got := h.Get("x-opencode-session"); got != "s1" {
		t.Errorf("上游收到 x-opencode-session = %q，期望 s1", got)
	}
	if got := h.Get("User-Agent"); got != "portage/1" {
		t.Errorf("上游收到 User-Agent = %q，期望 portage/1", got)
	}
}

// 管理端写额外出站头（#167）：从「上游设置」那一笔 PUT 写进去之后，转发 / 检测 /
// 拉模型列表三条出站路都得带上新值；读接口把键和值原样带出（额外头不是凭证）。
func TestAdminWrittenHeadersReachAllThreePaths(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "go-model", "openai", up.URL, "glm", "sk-go")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/headers",
		`{"headers":{"x-opencode-session":"s2","User-Agent":"portage/2"}}`, nil)

	var list []struct {
		Headers map[string]string `json:"headers"`
	}
	a.JSONInto(t, http.MethodGet, "/panel/api/channels", "", &list)
	if len(list) != 1 || list[0].Headers["x-opencode-session"] != "s2" || list[0].Headers["User-Agent"] != "portage/2" {
		t.Fatalf("读接口没带出 headers：%+v", list)
	}

	sent := 0
	check := func(path string) {
		t.Helper()
		if up.Count() <= sent {
			t.Fatalf("%s：上游没收到新请求", path)
		}
		sent = up.Count()
		h := up.Last(t).Header
		if got := h.Get("x-opencode-session"); got != "s2" {
			t.Errorf("%s：上游收到 x-opencode-session = %q，期望 s2", path, got)
		}
		if got := h.Get("User-Agent"); got != "portage/2" {
			t.Errorf("%s：上游收到 User-Agent = %q，期望 portage/2", path, got)
		}
	}

	resp := g.Post(t, "/v1/chat/completions", `{"model":"go-model","messages":[]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("转发失败：%d %s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	check("转发")

	a.Do(t, http.MethodPost, "/panel/api/channels/1/probe", `{"credential_id":1,"model":"","protocols":["openai"]}`)
	check("检测")

	a.Do(t, http.MethodPost, "/panel/api/channels/1/fetch-models", "{}")
	check("拉模型列表")

	// 清空也走同一笔：空对象 = 不带额外头。
	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/headers", `{"headers":{}}`, nil)
	resp = g.Post(t, "/v1/chat/completions", `{"model":"go-model","messages":[]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("转发失败：%d %s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if got := up.Last(t).Header.Get("x-opencode-session"); got != "" {
		t.Errorf("清空后上游仍收到 x-opencode-session = %q", got)
	}
}

// 保留头名走同一道 store.ValidateHeaders 闸，错误原文回显；被拒的写不落库。
func TestAdminHeadersRejectReservedName(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedChannel(t, db, "ch", "openai", "https://example.internal", "sk-up")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	status, body := a.Do(t, http.MethodPut, "/panel/api/channels/1/headers",
		`{"headers":{"x-ok":"1","Authorization":"Bearer evil"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("保留头名应 400，得到 %d：%s", status, body)
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("响应不是合法 JSON：%s", body)
	}
	if want := store.ValidateHeaders(map[string]string{"Authorization": "x"}).Error(); got.Error != want {
		t.Errorf("错误没回显原文：得到 %q，期望 %q", got.Error, want)
	}
	var headers string
	if err := db.QueryRow(`SELECT headers FROM channels WHERE id = 1`).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if headers != "" {
		t.Errorf("被拒的写落库了：%q", headers)
	}
}

// 其他四笔字段写不碰 headers（#167 验收）：管理端写上去的头，改设置 / 地址 / 选取
// 模式 / 启停之后仍在。
func TestOtherChannelWritesKeepHeaders(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedChannel(t, db, "ch", "openai", "https://example.internal", "sk-up")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/headers", `{"headers":{"x-keep":"1"}}`, nil)
	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/settings", `{"name":"ch2","max_concurrency":3}`, nil)
	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/base-url", `{"base_url":{"openai":"https://other.internal"}}`, nil)
	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/key-mode", `{"key_mode":"random"}`, nil)
	a.JSONInto(t, http.MethodPut, "/panel/api/channels/1/disabled", `{"disabled":true}`, nil)

	var headers string
	if err := db.QueryRow(`SELECT headers FROM channels WHERE id = 1`).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if headers != `{"x-keep":"1"}` {
		t.Errorf("其他字段写动了 headers：%q", headers)
	}
}
