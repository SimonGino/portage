package server_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// fetch-models 的订阅分支（#214，口径层 §2.2 v1.52）：chatgpt_account 渠道拉官方
// 模型目录——出站凭证先过引擎懒刷新换成 Bearer access，解析只认 models[] 形状、
// 留 visibility=="list"、slug 作请求名；非 chatgpt_account 渠道行为零变化。

type fetchModelsResponse struct {
	Results []struct {
		Protocols []string `json:"protocols"`
		Models    []string `json:"models"`
		Status    int      `json:"status"`
		Detail    string   `json:"detail"`
	} `json:"results"`
}

// chatgptCredential 拼一份过了形状闸的凭证 JSON（#211 的存库格式）。
func chatgptCredential(t *testing.T, access, refresh string, expiresAt time.Time) string {
	t.Helper()
	return `{"client_id":"oaiapp-1","host_id":"urn:uuid:h","access_token":"` + access +
		`","refresh_token":"` + refresh + `","expires_at":` +
		strconv.FormatInt(expiresAt.Unix(), 10) + `,"scopes":["chatgpt.tokens.use.direct"]}`
}

// modelsCatalog 是官方 models[] 形状的最小样本：一个 list、一个 hide——
// hide 的不该进候选。
const modelsCatalog = `{"models":[` +
	`{"slug":"gpt-6.1-sol","visibility":"list","display_name":"GPT-6.1 Sol"},` +
	`{"slug":"gpt-reserve","visibility":"hide","display_name":"Reserve"}]}`

// seedSubscriptionChannel 建一个 chatgpt_account 渠道：SeedChannel 建的是 api_key
// 渠道，订阅分支的用例统一在这儿翻类型列（#214 的分支判据是 credential_type）。
func seedSubscriptionChannel(t *testing.T, db *sql.DB, name, baseURL string) int64 {
	t.Helper()
	ch := gatewaytest.SeedChannel(t, db, name, "openai_responses", baseURL, "")
	if _, err := db.Exec(
		`UPDATE channels SET credential_type = 'chatgpt_account' WHERE id = ?`, ch); err != nil {
		t.Fatalf("改凭证类型失败: %v", err)
	}
	return ch
}

// 凭证还新鲜（没到刷新门槛）时照旧值出站：上游收到的就是库里的 access、
// 只带 Bearer 头，候选是 models[] 里 visibility=="list" 的 slug。
func TestFetchModelsSubscriptionParsesOfficialCatalog(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := seedSubscriptionChannel(t, db, "siwc", up.URL)
	gatewaytest.SeedNamedCredential(t, db, ch, "主号",
		chatgptCredential(t, "at-old", "rt-old", time.Now().Add(time.Hour)))
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsCatalog))
	}
	g := gatewaytest.Start(t, db)

	var got fetchModelsResponse
	g.LoggedIn(t).JSONInto(t, http.MethodPost,
		"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

	if up.Count() != 1 {
		t.Fatalf("上游该被打一次，实得 %d 次", up.Count())
	}
	if bearer := up.Last(t).Header.Get("Authorization"); bearer != "Bearer at-old" {
		t.Errorf("上游收到的 Authorization = %q，期望 Bearer at-old", bearer)
	}
	if len(got.Results) != 1 {
		t.Fatalf("该出一份结果：%+v", got.Results)
	}
	r := got.Results[0]
	if len(r.Protocols) != 1 || r.Protocols[0] != "openai_responses" {
		t.Errorf("Protocols = %v，期望只剩声明的 openai_responses", r.Protocols)
	}
	if len(r.Models) != 1 || r.Models[0] != "gpt-6.1-sol" {
		t.Errorf("候选 = %v，期望只留 visibility==list 的 slug gpt-6.1-sol", r.Models)
	}
}

// 渠道级额外出站头（#137）刻意不带上订阅侧拉取：出站只 Bearer access（§7.13 对
// 订阅渠道的出站头口径）。这里钉住「不带」——将来要与转发侧 applyHeaders 对齐
// 得先过这条口径，不能悄悄改。
func TestFetchModelsSubscriptionDropsChannelHeaders(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := seedSubscriptionChannel(t, db, "siwc", up.URL)
	gatewaytest.SeedNamedCredential(t, db, ch, "主号",
		chatgptCredential(t, "at-old", "rt-old", time.Now().Add(time.Hour)))
	if _, err := db.Exec(`UPDATE channels SET headers = ? WHERE id = ?`,
		`{"X-Extra":"v"}`, ch); err != nil {
		t.Fatal(err)
	}
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsCatalog))
	}
	g := gatewaytest.Start(t, db)

	var got fetchModelsResponse
	g.LoggedIn(t).JSONInto(t, http.MethodPost,
		"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

	if up.Count() != 1 {
		t.Fatalf("上游该被打一次，实得 %d 次", up.Count())
	}
	if extra := up.Last(t).Header.Get("X-Extra"); extra != "" {
		t.Errorf("渠道额外出站头不该进订阅侧拉取，实得 X-Extra=%q", extra)
	}
	if bearer := up.Last(t).Header.Get("Authorization"); bearer != "Bearer at-old" {
		t.Errorf("上游收到的 Authorization = %q，期望 Bearer at-old", bearer)
	}
	if len(got.Results) != 1 || len(got.Results[0].Models) != 1 {
		t.Fatalf("目录该照常拉到：%+v", got.Results)
	}
}

// 凭证快到期时先过引擎懒刷新（刷新走凭证票，#214）：上游收到的是刷新后的
// access，轮换的 refresh token 落了库。
func TestFetchModelsSubscriptionRefreshesAccess(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := seedSubscriptionChannel(t, db, "siwc", up.URL)
	credID := gatewaytest.SeedNamedCredential(t, db, ch, "主号",
		chatgptCredential(t, "at-old", "rt-old", time.Now().Add(time.Minute)))
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsCatalog))
	}

	hits := &atomic.Int64{}
	ts := fakeTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-new","token_type":"Bearer","expires_in":3600,` +
			`"scope":"chatgpt.tokens.use.direct","refresh_token":"rt-new"}`))
	})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: ts})

	var got fetchModelsResponse
	g.LoggedIn(t).JSONInto(t, http.MethodPost,
		"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

	if n := hits.Load(); n != 1 {
		t.Errorf("token 端点被打 %d 次，期望 1", n)
	}
	if bearer := up.Last(t).Header.Get("Authorization"); bearer != "Bearer at-new" {
		t.Errorf("上游收到的 Authorization = %q，期望刷新后的 Bearer at-new", bearer)
	}
	var rawCred string
	if err := g.DB.QueryRow(`SELECT credential FROM channel_keys WHERE id = ?`, credID).Scan(&rawCred); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawCred, `"refresh_token":"rt-new"`) {
		t.Errorf("轮换后的 refresh token 该落库, got %s", rawCred)
	}
	if len(got.Results[0].Models) != 1 || got.Results[0].Models[0] != "gpt-6.1-sol" {
		t.Errorf("候选 = %v，期望 gpt-6.1-sol", got.Results[0].Models)
	}
}

// 第一把启用凭证待重登时换下一把：模型目录对哪把凭证都一样，死的那把不该
// 拦住拉取——它该被死亡码停用（引擎的账），拉取用兄弟凭证照常出站。
func TestFetchModelsSubscriptionFallsBackToSiblingCredential(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := seedSubscriptionChannel(t, db, "siwc", up.URL)
	dead := gatewaytest.SeedNamedCredential(t, db, ch, "死号",
		chatgptCredential(t, "at-dead", "rt-dead", time.Now().Add(time.Minute)))
	gatewaytest.SeedNamedCredential(t, db, ch, "备号",
		chatgptCredential(t, "at-spare", "rt-spare", time.Now().Add(time.Hour)))
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsCatalog))
	}

	ts := fakeTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"no more"}`))
	})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: ts})

	var got fetchModelsResponse
	g.LoggedIn(t).JSONInto(t, http.MethodPost,
		"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

	if len(got.Results) != 1 || len(got.Results[0].Models) != 1 || got.Results[0].Models[0] != "gpt-6.1-sol" {
		t.Fatalf("兄弟凭证在就该照常拉到目录：%+v", got.Results)
	}
	if bearer := up.Last(t).Header.Get("Authorization"); bearer != "Bearer at-spare" {
		t.Errorf("上游收到的 Authorization = %q，期望 Bearer at-spare", bearer)
	}
	var disabled int
	var reason string
	if err := g.DB.QueryRow(
		`SELECT disabled, COALESCE(disabled_reason,'') FROM channel_keys WHERE id = ?`, dead).
		Scan(&disabled, &reason); err != nil {
		t.Fatal(err)
	}
	if disabled == 0 || reason != "reauth_required" {
		t.Errorf("死亡的那把该带 reauth_required 停用, got disabled=%d reason=%q", disabled, reason)
	}
}

// 混合失败池的收场与 EffectiveAll 同序：死号（死亡码）在场时优先说「需要重新登录」，
// 不让最后那把「刷不动又过期」的瞬时错误盖掉该重登的提示。
func TestFetchModelsSubscriptionMixedFailurePrefersReauthHint(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := seedSubscriptionChannel(t, db, "siwc", up.URL)
	dead := gatewaytest.SeedNamedCredential(t, db, ch, "死号",
		chatgptCredential(t, "at-dead", "rt-dead", time.Now().Add(time.Minute)))
	gatewaytest.SeedNamedCredential(t, db, ch, "坏号",
		chatgptCredential(t, "at-broken", "rt-broken", time.Now().Add(-time.Minute)))
	ts := fakeTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("refresh_token") == "rt-dead" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"no more"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: ts})

	var got fetchModelsResponse
	g.LoggedIn(t).JSONInto(t, http.MethodPost,
		"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

	if up.Count() != 0 {
		t.Errorf("换不到 access 就不该出站，实得 %d 次", up.Count())
	}
	if len(got.Results) != 1 || !strings.Contains(got.Results[0].Detail, "需要重新登录") {
		t.Fatalf("有死亡码在场该优先说「需要重新登录」：%+v", got.Results)
	}
	var disabled int
	var reason string
	if err := g.DB.QueryRow(
		`SELECT disabled, COALESCE(disabled_reason,'') FROM channel_keys WHERE id = ?`, dead).
		Scan(&disabled, &reason); err != nil {
		t.Fatal(err)
	}
	if disabled == 0 || reason != "reauth_required" {
		t.Errorf("死亡的那把该带 reauth_required 停用, got disabled=%d reason=%q", disabled, reason)
	}
}

// 一把能用的 access 都换不到时不该把请求打去上游：原因摆进结果的 detail 里，
// 待重登、刷不动又过期、渠道停用且凭证全停各说各的话。
func TestFetchModelsSubscriptionReportsWhenNoCredentialWorks(t *testing.T) {
	t.Run("死亡码当场停用", func(t *testing.T) {
		up := gatewaytest.NewUpstream(t)
		db := gatewaytest.NewDB(t)
		ch := seedSubscriptionChannel(t, db, "siwc", up.URL)
		credID := gatewaytest.SeedNamedCredential(t, db, ch, "主号",
			chatgptCredential(t, "at-old", "rt-old", time.Now().Add(time.Minute)))
		ts := fakeTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"no more"}`))
		})
		g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: ts})

		var got fetchModelsResponse
		g.LoggedIn(t).JSONInto(t, http.MethodPost,
			"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

		if up.Count() != 0 {
			t.Errorf("待重登时一个字节都不该到上游，实得 %d 次", up.Count())
		}
		if len(got.Results) != 1 || !strings.Contains(got.Results[0].Detail, "需要重新登录") {
			t.Fatalf("该把「需要重新登录」摆进 detail：%+v", got.Results)
		}
		var disabled int
		var reason string
		if err := g.DB.QueryRow(
			`SELECT disabled, COALESCE(disabled_reason,'') FROM channel_keys WHERE id = ?`, credID).
			Scan(&disabled, &reason); err != nil {
			t.Fatal(err)
		}
		if disabled == 0 || reason != "reauth_required" {
			t.Errorf("凭证该带 reauth_required 停用, got disabled=%d reason=%q", disabled, reason)
		}
	})
	t.Run("刷不动又过期", func(t *testing.T) {
		up := gatewaytest.NewUpstream(t)
		db := gatewaytest.NewDB(t)
		ch := seedSubscriptionChannel(t, db, "siwc", up.URL)
		gatewaytest.SeedNamedCredential(t, db, ch, "主号",
			chatgptCredential(t, "at-old", "rt-old", time.Now().Add(-time.Minute)))
		up.Handler = func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(modelsCatalog))
		}
		ts := fakeTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: ts})

		var got fetchModelsResponse
		g.LoggedIn(t).JSONInto(t, http.MethodPost,
			"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

		if up.Count() != 0 {
			t.Errorf("换不到 access 就不该出站，实得 %d 次", up.Count())
		}
		if len(got.Results) != 1 || !strings.Contains(got.Results[0].Detail, "订阅凭证暂不可用") {
			t.Fatalf("该把「暂不可用」摆进 detail：%+v", got.Results)
		}
	})
	t.Run("渠道停用且凭证全停", func(t *testing.T) {
		// 启动闸要求未停用渠道至少一份启用凭证，所以这个状态只出现在停用渠道上
		// （检测允许选已停用的凭证，拉列表同理不禁-disabled 渠道）。
		up := gatewaytest.NewUpstream(t)
		db := gatewaytest.NewDB(t)
		ch := seedSubscriptionChannel(t, db, "siwc", up.URL)
		gatewaytest.SeedNamedCredential(t, db, ch, "停用号",
			chatgptCredential(t, "at-old", "rt-old", time.Now().Add(time.Hour)))
		if _, err := db.Exec(`UPDATE channels SET disabled = 1 WHERE id = ?`, ch); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1 WHERE channel_id = ?`, ch); err != nil {
			t.Fatal(err)
		}
		g := gatewaytest.Start(t, db)

		var got fetchModelsResponse
		g.LoggedIn(t).JSONInto(t, http.MethodPost,
			"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

		if up.Count() != 0 {
			t.Errorf("没有启用凭证时一个字节都不该到上游，实得 %d 次", up.Count())
		}
		if len(got.Results) != 1 || !strings.Contains(got.Results[0].Detail, "没有启用的订阅凭证") {
			t.Fatalf("该把「没有启用的凭证」摆进 detail：%+v", got.Results)
		}
		if len(got.Results[0].Protocols) != 1 || got.Results[0].Protocols[0] != "openai_responses" {
			t.Errorf("失败结果也该标清适用协议：%+v", got.Results[0])
		}
	})
}

// 无 chatgpt_account 凭证的渠道 fetch 行为零变化（验收第三条）：data[] 照旧解析、
// 头照旧按渠道档位打。
func TestFetchModelsAPIKeyChannelUnchanged(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "relay", "openai", up.URL, "")
	gatewaytest.SeedNamedCredential(t, db, ch, "主力", "sk-x")
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
	}
	g := gatewaytest.Start(t, db)

	var got fetchModelsResponse
	g.LoggedIn(t).JSONInto(t, http.MethodPost,
		"/panel/api/channels/"+itoa(ch)+"/fetch-models", "", &got)

	if bearer := up.Last(t).Header.Get("Authorization"); bearer != "Bearer sk-x" {
		t.Errorf("上游收到的 Authorization = %q，期望 Bearer sk-x", bearer)
	}
	if len(got.Results) != 1 || len(got.Results[0].Models) != 2 ||
		got.Results[0].Models[0] != "gpt-4o" || got.Results[0].Models[1] != "gpt-4o-mini" {
		t.Fatalf("data[] 该照旧解析：%+v", got.Results)
	}
	if !strings.Contains(got.Results[0].Detail, "2 个模型") {
		t.Errorf("Detail = %q，该照旧报拉到几个", got.Results[0].Detail)
	}
}

// fakeTokenServer 起一个假 OIDC 签发方（只实现 token 端点那一半），回它的地址。
func fakeTokenServer(t *testing.T, token func(w http.ResponseWriter, r *http.Request)) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(token))
	t.Cleanup(ts.Close)
	return ts.URL
}
