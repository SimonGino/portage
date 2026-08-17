package server_test

// admin_test.go 走的是主接缝：真引擎、真库、真 cookie。管理端最要紧的几条不变量
// （与转发端分离、凭证不回读、写不进坏配置）都只在整条链路上才成立，单测 handler
// 验不出来。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// ── 鉴权与分离 ──────────────────────────────────────────────────────────

func TestAdminRejectsWithoutSession(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.Admin(t) // 没登录

	for _, path := range []string{"/admin/api/channels", "/admin/api/keys", "/admin/api/logs"} {
		if status, body := a.Do(t, http.MethodGet, path, ""); status != http.StatusUnauthorized {
			t.Errorf("%s 未登录时应 401，得到 %d：%s", path, status, body)
		}
	}
}

// 口径层 §2.7：管理端与转发端两套凭证**互不可用**。这是两个方向，缺一个都不算验过。
func TestGatewayKeyCannotReachAdminAPI(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, g.URL+"/admin/api/channels", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	// 两个头都试：一把有效的网关 key 无论放哪儿都不该换来管理权限。
	for _, h := range []string{"x-api-key", "Authorization"} {
		req.Header.Del("x-api-key")
		req.Header.Del("Authorization")
		if h == "Authorization" {
			req.Header.Set(h, "Bearer "+gatewaytest.DefaultKey)
		} else {
			req.Header.Set(h, gatewaytest.DefaultKey)
		}
		resp, err := g.Client().Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("网关 key 放在 %s 里居然进得了管理端：status=%d", h, resp.StatusCode)
		}
	}
}

func TestAdminSessionCannotReachRelay(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "claude-direct", "anthropic", up.URL, "claude-3-5-sonnet", "sk-up")
	g := gatewaytest.Start(t, db)

	a := g.LoggedIn(t)
	cookies := a.Cookies(t)
	if len(cookies) == 0 {
		t.Fatal("登录之后没有拿到会话 cookie")
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, g.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-direct","messages":[]}`))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := g.Client().Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("管理端 cookie 居然能转发请求：status=%d", resp.StatusCode)
	}
	if up.Count() != 0 {
		t.Error("请求打到上游去了——管理端 cookie 被当成转发凭证了")
	}
}

func TestAdminLoginRejectsWrongPassword(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.Admin(t)

	if status, _ := a.Do(t, http.MethodPost, "/admin/api/login", `{"password":"nope"}`); status != http.StatusUnauthorized {
		t.Errorf("密码错应 401，得到 %d", status)
	}
	if status, _ := a.Do(t, http.MethodGet, "/admin/api/channels", ""); status != http.StatusUnauthorized {
		t.Errorf("登录失败后不该拿到会话，得到 %d", status)
	}
}

func TestAdminLogoutEndsSession(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", nil)
	a.JSONInto(t, http.MethodPost, "/admin/api/logout", "", nil)
	if status, _ := a.Do(t, http.MethodGet, "/admin/api/channels", ""); status != http.StatusUnauthorized {
		t.Errorf("登出之后还能访问，得到 %d", status)
	}
}

// 改密码要把**所有**会话作废，包括发起修改的那一个——改密码的常见动机就是
// 「怕别人还拿着 cookie」，只挡住未登录的人等于没改。
func TestPasswordChangeRevokesEverySession(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	first, second := g.LoggedIn(t), g.LoggedIn(t)

	first.JSONInto(t, http.MethodPost, "/admin/api/password",
		`{"old_password":"`+gatewaytest.AdminPassword+`","new_password":"a-brand-new-one"}`, nil)

	for name, a := range map[string]*gatewaytest.AdminClient{"改密码的那个会话": first, "另一个会话": second} {
		if status, _ := a.Do(t, http.MethodGet, "/admin/api/channels", ""); status != http.StatusUnauthorized {
			t.Errorf("%s 在改密码之后仍然有效，得到 %d", name, status)
		}
	}

	fresh := g.Admin(t)
	if status, body := fresh.Do(t, http.MethodPost, "/admin/api/login", `{"password":"a-brand-new-one"}`); status != http.StatusOK {
		t.Errorf("新密码登不进去：%d %s", status, body)
	}
	if status, _ := fresh.Do(t, http.MethodPost, "/admin/api/login",
		`{"password":"`+gatewaytest.AdminPassword+`"}`); status != http.StatusUnauthorized {
		t.Errorf("旧密码还能用，得到 %d", status)
	}
}

// 配置里的 admin_password 只在库里还没密码时生效。否则重启会把改过的密码悄悄改回去，
// 而配置文件常年躺在仓库或 compose 里——等于密码根本改不掉（口径层 §2.7）。
func TestConfigPasswordDoesNotOverrideChangedOne(t *testing.T) {
	db := gatewaytest.NewDB(t)
	g := gatewaytest.Start(t, db)
	g.LoggedIn(t).JSONInto(t, http.MethodPost, "/admin/api/password",
		`{"old_password":"`+gatewaytest.AdminPassword+`","new_password":"changed-by-admin"}`, nil)

	// 同一个库再起一次网关，等价于一次重启——Start 会照常拿配置里的密码去 Bootstrap。
	restarted := gatewaytest.Start(t, db)
	a := restarted.Admin(t)
	if status, _ := a.Do(t, http.MethodPost, "/admin/api/login",
		`{"password":"`+gatewaytest.AdminPassword+`"}`); status != http.StatusUnauthorized {
		t.Errorf("重启把配置里的旧密码又灌回去了，得到 %d", status)
	}
	if status, body := a.Do(t, http.MethodPost, "/admin/api/login", `{"password":"changed-by-admin"}`); status != http.StatusOK {
		t.Errorf("重启之后改过的密码不认了：%d %s", status, body)
	}
}

// ── 配置 CRUD ───────────────────────────────────────────────────────────

type adminChannel struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	KeyMode            string `json:"key_mode"`
	SupportsCompaction bool   `json:"supports_compaction"`
	EnabledKeys        int    `json:"enabled_keys"`
	DisabledKeys       int    `json:"disabled_keys"`
	Models             []struct {
		ID            int64  `json:"id"`
		UpstreamModel string `json:"upstream_model"`
	} `json:"models"`
}

// 从零把一条完整链路配出来，然后真的发一次转发请求——管理端配出来的东西能不能用，
// 只有让转发路径去读才算数。
func TestAdminCanConfigureAWorkingRoute(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	var created struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/channels", `{
		"name":"anthropic-main","protocols":["anthropic"],"base_url":"`+up.URL+`",
		"credential":"sk-upstream-secret"}`, &created)
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(created.ID)+"/models",
		`{"upstream_model":"claude-3-5-sonnet"}`, nil)

	var channels []adminChannel
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if len(channels) != 1 || len(channels[0].Models) != 1 {
		t.Fatalf("渠道/纳管模型没建上：%+v", channels)
	}

	var ap struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/access-points",
		`{"model":"claude-via-admin","channel_model_id":`+itoa(channels[0].Models[0].ID)+`}`, &ap)

	resp := g.Post(t, "/v1/messages", `{"model":"claude-via-admin","messages":[]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("管理端配出来的接入点转发失败：%d %s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if got := up.Last(t).Header.Get("x-api-key"); got != "sk-upstream-secret" {
		t.Errorf("上游没收到管理端配的凭证，收到的是 %q", got)
	}
}

// 凭证可回读（口径层 v0.47 推翻 v0.28）：凭证池那个接口要把值发出来，别处一律不发。
//
// 这条测试从「哪儿都不许出现」翻成「只许在这一个地方出现」。守的东西没变少：
// 值出现在渠道列表、接入点、流水里都是泄漏面，只有凭证池那一屏是人主动去看的地方。
func TestAdminReturnsCredentialOnlyFromThePool(t *testing.T) {
	const secret = "sk-upstream-super-secret-value"
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "claude-direct", "anthropic", up.URL, "claude-3-5-sonnet", secret)
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	// 凭证池：**要有**值，否则页面上没法认出这把是哪一把（PO 于 v0.47 裁定）。
	if _, body := a.Do(t, http.MethodGet, "/admin/api/channels/1/credentials", ""); !strings.Contains(body, secret) {
		t.Errorf("凭证池没把值发出来：%s", body)
	}

	// 其余读接口一个都不许带上它。回读是给凭证池那一屏开的口子，不是全局放开。
	for _, path := range []string{"/admin/api/channels", "/admin/api/access-points",
		"/admin/api/keys", "/admin/api/logs"} {
		_, body := a.Do(t, http.MethodGet, path, "")
		if strings.Contains(body, secret) {
			t.Errorf("%s 把上游凭证吐出来了：%s", path, body)
		}
	}

	// 没登录就什么都拿不到——回读的前提是这一层拦得住。
	if status, body := g.Admin(t).Do(t, http.MethodGet, "/admin/api/channels/1/credentials", ""); status == http.StatusOK || strings.Contains(body, secret) {
		t.Errorf("未登录也能读凭证：status=%d body=%s", status, body)
	}

	// 整把替换那个老接口连同它的路由一起退役（口径层 v0.38 改为逐条 CRUD）。
	if status, _ := a.Do(t, http.MethodPut, "/admin/api/channels/1/credential", `{"credential":"sk-x"}`); status != http.StatusNotFound {
		t.Errorf("整把替换的老接口还在，status=%d", status)
	}
}

// PUT 不带 key_mode 时那一列不动：这个字段 v0.38 才露到表单上，老前端与手写的请求体
// 里没有它，在服务端补默认等于把一个配好 random 的渠道静默改回轮询——而「为什么总是
// 第一把在跑」正是多凭证放开后最难自己想明白的问题，让它被一次无关的改名悄悄改掉更甚。
func TestUpdateChannelKeepsKeyModeWhenAbsent(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	var created struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/channels", `{
		"name":"pool","protocols":["anthropic"],"base_url":"https://api.example.com",
		"key_mode":"random","credential":"sk-x"}`, &created)

	// 一次只改名字的保存，请求体里没有 key_mode。
	a.JSONInto(t, http.MethodPut, "/admin/api/channels/"+itoa(created.ID), `{
		"name":"pool-renamed","protocols":["anthropic"],"base_url":"https://api.example.com"}`, nil)

	var channels []adminChannel
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if len(channels) != 1 || channels[0].KeyMode != "random" {
		t.Fatalf("key_mode 被静默重置了：%+v", channels)
	}
	// 显式给的仍然要生效，否则「不动」就变成了「改不动」。
	a.JSONInto(t, http.MethodPut, "/admin/api/channels/"+itoa(created.ID), `{
		"name":"pool-renamed","protocols":["anthropic"],"base_url":"https://api.example.com",
		"key_mode":"polling"}`, nil)
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if channels[0].KeyMode != "polling" {
		t.Errorf("显式改成 polling 没生效：%+v", channels)
	}
}

// compaction 能力位（口径层 v0.54）与 key_mode 同一个整体覆盖陷阱，但更险：它的哨兵
// 是 nil 而不是零值——false 是**有意义的默认取值**，缺省当 false 处理与「不动那一列」
// 在页面上长得一模一样，而后果是一个勾过的渠道在别处保存一次就被静默关掉压缩。
func TestUpdateChannelKeepsCompactionBitWhenAbsent(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	var created struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/channels", `{
		"name":"resp","protocols":["openai_responses"],"base_url":"https://api.example.com",
		"credential":"sk-x"}`, &created)

	var channels []adminChannel
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if len(channels) != 1 || channels[0].SupportsCompaction {
		t.Fatalf("新建渠道的能力位默认该是否（PO 2026-08-13 裁定）：%+v", channels)
	}

	id := itoa(created.ID)
	a.JSONInto(t, http.MethodPut, "/admin/api/channels/"+id, `{
		"name":"resp","protocols":["openai_responses"],"base_url":"https://api.example.com",
		"supports_compaction":true}`, nil)
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if !channels[0].SupportsCompaction {
		t.Fatalf("显式勾上没生效：%+v", channels)
	}

	// 一次只改名字的保存，请求体里没有这个字段。
	a.JSONInto(t, http.MethodPut, "/admin/api/channels/"+id, `{
		"name":"resp-renamed","protocols":["openai_responses"],"base_url":"https://api.example.com"}`, nil)
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if !channels[0].SupportsCompaction {
		t.Errorf("能力位被静默关掉了：%+v", channels)
	}

	// 显式取消仍然要生效，否则「不动」就变成了「关不掉」。
	a.JSONInto(t, http.MethodPut, "/admin/api/channels/"+id, `{
		"name":"resp-renamed","protocols":["openai_responses"],"base_url":"https://api.example.com",
		"supports_compaction":false}`, nil)
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if channels[0].SupportsCompaction {
		t.Errorf("显式取消没生效：%+v", channels)
	}
}

// 哈希不回读。明文从 v0.47 起回读（见 TestAdminKeyIsReadableAndWorks），但哈希没有
// 任何理由发给页面——它是转发热路径的校验依据，多发一份只是多一个泄露面。
func TestAdminNeverReturnsKeyHash(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	_, body := g.LoggedIn(t).Do(t, http.MethodGet, "/admin/api/keys", "")
	if strings.Contains(body, "key_hash") {
		t.Errorf("key 列表里带上了哈希：%s", body)
	}
}

// 管理端能保存下去的配置，一定是能启动的配置——否则它就成了「把网关改到起不来」的
// 唯一入口，而且下次重启才炸。
func TestAdminRejectsConfigTheStartupGateWouldReject(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	// 建一个没有凭证的渠道：启用渠道至少要有一份启用凭证（口径层 v0.18 可达性通则，
	// 上限那一半已于 v0.38 放开），这一步就该被挡。
	status, body := a.Do(t, http.MethodPost, "/admin/api/channels",
		`{"name":"no-credential","protocols":["anthropic"],"base_url":"https://api.anthropic.com"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("无凭证渠道应被校验挡下，得到 %d：%s", status, body)
	}
	if !strings.Contains(body, "启用凭证") {
		t.Errorf("回给前端的应该是校验原文，得到：%s", body)
	}
	// 回滚要真的回滚：挡下来的渠道不能留在库里。
	var channels []adminChannel
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if len(channels) != 0 {
		t.Errorf("被挡下的写操作没有回滚：%+v", channels)
	}

	// base_url 缺 scheme 同样过不了——这类配置能存进去、能过启动，只在请求时炸。
	// 用一个不会跟校验文案里的示例撞上的主机名，否则「回显了没有」根本断言不出来。
	status, body = a.Do(t, http.MethodPost, "/admin/api/channels",
		`{"name":"bad-url","protocols":["anthropic"],"base_url":"tenant7.internal.example","credential":"sk-x"}`)
	if status != http.StatusBadRequest {
		t.Errorf("缺 scheme 的 base_url 应被挡下，得到 %d：%s", status, body)
	}
	// 且错误里不能回显 base_url 本身（它可能带 userinfo）。
	if strings.Contains(body, "tenant7.internal.example") {
		t.Errorf("校验错误里回显了 base_url：%s", body)
	}
}

// 新建的 key 能用，而且**之后还能再看到**（口径层 v0.47）。
func TestAdminKeyIsReadableAndWorks(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "claude-direct", "anthropic", up.URL, "claude-3-5-sonnet", "sk-up")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var created struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/keys", `{"name":"laptop"}`, &created)
	if !strings.HasPrefix(created.Key, "sk-ptg-") {
		t.Fatalf("新 key 形状不对：%q", created.Key)
	}

	resp := g.Post(t, "/v1/messages", `{"model":"claude-direct","messages":[]}`,
		map[string]string{"x-api-key": created.Key})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("管理端新建的 key 用不了：%d %s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}

	// 之后照样读得到：明文跟哈希各存一列。
	_, body := a.Do(t, http.MethodGet, "/admin/api/keys", "")
	if !strings.Contains(body, created.Key) {
		t.Errorf("key 列表里没有明文，PO 要的「能看能复制」落不了地：%s", body)
	}

	// 加 key_plain 之前种下的 key 只有哈希，明文栏是空串——不是「空 key」，是拿不回来。
	var keys []struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	a.JSONInto(t, http.MethodGet, "/admin/api/keys", "", &keys)
	for _, k := range keys {
		if k.Name == "test-default" && k.Key != "" {
			t.Errorf("只种了哈希的存量 key 不该有明文：%+v", k)
		}
	}
}

func TestAdminDisabledKeyStopsWorking(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "claude-direct", "anthropic", up.URL, "claude-3-5-sonnet", "sk-up")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var created struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/keys", `{"name":"temp"}`, &created)
	a.JSONInto(t, http.MethodPut, "/admin/api/keys/"+itoa(created.ID),
		`{"name":"temp","disabled":true}`, nil)

	resp := g.Post(t, "/v1/messages", `{"model":"claude-direct","messages":[]}`,
		map[string]string{"x-api-key": created.Key})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("停用的 key 还能用：%d", resp.StatusCode)
	}
}

// allowed_models：PO 于 M3 裁定「管理端能配了就同期启用校验」。
func TestAllowedModelsIsEnforced(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "claude-direct", "anthropic", up.URL, "claude-3-5-sonnet", "sk-up")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var created struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/keys",
		`{"name":"scoped","allowed_models":"some-other-point"}`, &created)

	resp := g.Post(t, "/v1/messages", `{"model":"claude-direct","messages":[]}`,
		map[string]string{"x-api-key": created.Key})
	// 403 而不是 404/401：这把 key 不能用它，不是它不存在，也不是 key 不认。
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("白名单外的接入点应 403，得到 %d", resp.StatusCode)
	}
	if up.Count() != 0 {
		t.Error("请求打到上游去了——白名单是在转发之后才判的")
	}
	// 错误体要是入站协议的原生形状，harness 才认得出来。
	body := gatewaytest.ReadBody(t, resp)
	if !strings.Contains(body, `"type":"error"`) {
		t.Errorf("403 不是 Anthropic 形状：%s", body)
	}
	// 落库要留痕，否则「谁被挡了」查不到。
	if row := g.LastCallRow(t); row.Status != http.StatusForbidden || row.APIKeyName != "scoped" {
		t.Errorf("403 的流水不对：status=%d api_key_name=%q", row.Status, row.APIKeyName)
	}

	// 放开之后立刻生效，不需要重启。
	a.JSONInto(t, http.MethodPut, "/admin/api/keys/"+itoa(created.ID),
		`{"name":"scoped","allowed_models":"claude-direct, another"}`, nil)
	resp = g.Post(t, "/v1/messages", `{"model":"claude-direct","messages":[]}`,
		map[string]string{"x-api-key": created.Key})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("放进白名单后仍被挡：%d %s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
}

func TestAdminUsageReflectsRelayedCalls(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "claude-direct", "anthropic", up.URL, "claude-3-5-sonnet", "sk-up")
	g := gatewaytest.Start(t, db)

	g.Post(t, "/v1/messages", `{"model":"claude-direct","messages":[]}`, nil)
	g.LastCallRow(t) // 等落库

	a := g.LoggedIn(t)
	var logs struct {
		Rows []struct {
			ModelRequested string `json:"model_requested"`
			Status         int    `json:"status"`
		} `json:"rows"`
	}
	a.JSONInto(t, http.MethodGet, "/admin/api/logs?limit=10", "", &logs)
	if len(logs.Rows) == 0 || logs.Rows[0].ModelRequested != "claude-direct" {
		t.Fatalf("流水接口没返回刚才那次调用：%+v", logs)
	}

	var usage struct {
		Rows []struct {
			ModelRequested string `json:"model_requested"`
			Calls          int64  `json:"calls"`
		} `json:"rows"`
	}
	a.JSONInto(t, http.MethodGet, "/admin/api/usage", "", &usage)
	if len(usage.Rows) == 0 || usage.Rows[0].Calls == 0 {
		t.Errorf("用量汇总是空的：%+v", usage)
	}
}

// ── 静态资源 ────────────────────────────────────────────────────────────

// 默认构建不含前端，/admin 该回一句说明而不是 404——404 会让人以为路由挂错了。
// 带 -tags webui 时这里拿到的是真页面，两种都算通过，断言只卡「不是 404」。
func TestAdminUIPathAnswers(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	for _, path := range []string{"/admin", "/admin/keys"} {
		resp := g.Get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s 应有响应，得到 %d", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s 的 Content-Type 是 %q，不是 HTML", path, ct)
		}
	}
}

// NoRoute 是全局的：非 /admin 的未知路径必须照常 404，不能拿到管理端页面。
func TestUnknownNonAdminPathStays404(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	if resp := g.Get(t, "/v1/nonexistent"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("未知业务路径应 404，得到 %d", resp.StatusCode)
	}
}

// /admin/api 下的未知路径要回 JSON：回 HTML 的话前端在 JSON.parse 上炸，
// 报出来的错跟真正的原因（接口写错了）毫无关系。
func TestUnknownAdminAPIPathReturnsJSON(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	status, body := g.LoggedIn(t).Do(t, http.MethodGet, "/admin/api/nope", "")
	if status != http.StatusNotFound {
		t.Errorf("未知管理接口应 404，得到 %d", status)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Errorf("未知管理接口回的不是 JSON：%s", body)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
