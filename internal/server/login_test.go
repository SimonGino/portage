package server_test

// login_test.go 判订阅渠道登录弹层的 HTTP 面（#212）：三只 /panel/api/channels/:id/login/*
// 走真引擎、真库、真 cookie 会话，签发方指向 httptest 假上游——「start → complete 全链」
// 的验收钉在这里。引擎侧的状态机细节（TTL、nonce、回调校验）在 internal/subscription
// 的 login_test.go；这里管路由、鉴权、落库写事务与 400/409 的映射。

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// siwcIssuer 是登录链路的假签发方（发现文档 + JWKS + token + 撤销端点全套）。
// nonce 由用例从授权 URL 里抠出来塞回来——它发给授权页、不进 token 端点的表单；
// revokeFails 打开时撤销端点回 500（「撤销失败仍删」那条验收）。
type siwcIssuer struct {
	URL         string
	key         *rsa.PrivateKey
	nonce       string
	revokeFails bool
	revokeSeen  chan url.Values
}

func newSIWCIssuer(t *testing.T) *siwcIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &siwcIssuer{key: key, revokeSeen: make(chan url.Values, 4)}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + f.URL + `","jwks_uri":"` + f.URL + `/jwks",` +
			`"revocation_endpoint":"` + f.URL + `/revoke"}`))
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"k1","use":"sig","alg":"RS256","n":"` +
			base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()) + `","e":"AQAB"}]}`))
	})
	mux.HandleFunc("/api/accounts/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token 端点收不到表单: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-new","refresh_token":"rt-new",` +
			`"id_token":"` + f.signID(r.FormValue("client_id")) + `","expires_in":3600,` +
			`"scope":"chatgpt.tokens.use.direct email openid"}`))
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("撤销端点收不到表单: %v", err)
		}
		f.revokeSeen <- r.PostForm
		if f.revokeFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// signID 签一枚能过验签的 RS256 id_token（iss / aud / 当次 nonce / exp / sub /
// email 齐全； nonceOverride 打开时故意签错的那半由用例自己改 f.nonce 实现）。
func (f *siwcIssuer) signID(clientID string) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k1"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss":                         f.URL,
		"aud":                         clientID,
		"nonce":                       f.nonce,
		"sub":                         "user-123",
		"email":                       "po@example.com",
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
	})
	body := base64.RawURLEncoding.EncodeToString(claims)
	signing := []byte(head + "." + body)
	sum := sha256.Sum256(signing)
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// startLoginOverHTTP 从管理端起一次登录，回授权页地址，并把 nonce 塞回假签发方。
func startLoginOverHTTP(t *testing.T, a *gatewaytest.AdminClient, f *siwcIssuer, channelID int64, body string) (authURL string) {
	t.Helper()
	status, raw := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(channelID)+"/login/start", body)
	if status != http.StatusOK {
		t.Fatalf("login/start 期望 200，得到 %d：%s", status, raw)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out.URL == "" {
		t.Fatalf("login/start 的回包不是 {url}：%s（%v）", raw, err)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatalf("授权 URL 解不开: %v", err)
	}
	f.nonce = u.Query().Get("nonce")
	return out.URL
}

// strconvQuote 把一段串按 JSON 字符串的字面量拼进请求体（回调 URL 里的 ? 与 &
// 直接进裸字符串会被 JSON 解析器咬，得转义）。
func strconvQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// seedEmptySubscriptionChannel 直接落一个启用中、没有凭证的 chatgpt_account 渠道
// （「允许先建空渠道再登录」的现场）。启用中零凭证过不了启动闸，所以只能在网关
// 起来之后种——这正是那个中间态在现实里的来路：先建渠道、人还没来得及登录。
func seedEmptySubscriptionChannel(t *testing.T, db *sql.DB, disabled bool) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO channels
		(name, base_url_openai_responses, credential_type, disabled) VALUES (?, ?, 'chatgpt_account', ?)`,
		"chatgpt-sub", "https://api.openai.com/v1", disabled)
	if err != nil {
		t.Fatalf("种空订阅渠道失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// deadCredentialJSON 是一把「还活着」的凭证形状；用例再按需把它标成待重登。
func liveCredentialJSON() string {
	return `{"client_id":"oaiapp-1","host_id":"urn:uuid:h","access_token":"at-old",` +
		`"refresh_token":"rt-old","expires_at":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) +
		`,"scopes":["chatgpt.tokens.use.direct"]}`
}

// TestLoginStartCompleteOverHTTP：start → complete 全链（httptest 假上游）——空渠道
// 先建、登录落凭证、渠道行回读 enabled_keys、管理端回包带 credential_type（前端按
// 它切弹层形态）。同渠道第二个 start 的 409 与 state 不符的 400 也钉在这条链上。
func TestLoginStartCompleteOverHTTP(t *testing.T) {
	f := newSIWCIssuer(t)
	db := gatewaytest.NewDB(t)
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: f.URL})
	a := g.LoggedIn(t)
	ch := seedEmptySubscriptionChannel(t, db, false)

	// 未登录先拒——管理面没有免鉴权的口子。
	if status, _ := g.Admin(t).Do(t, http.MethodPost, "/panel/api/channels/1/login/start", ""); status != http.StatusUnauthorized {
		t.Errorf("未登录的 login/start 该 401，得到 %d", status)
	}

	raw := startLoginOverHTTP(t, a, f, ch, `{}`)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("agent_name_hint"); got != "portage" {
		t.Errorf("agent_name_hint = %q，期望 portage", got)
	}
	if got := u.Query().Get("redirect_uri"); got != "http://127.0.0.1:1455/auth/callback" {
		t.Errorf("redirect_uri = %q，期望钉死 127.0.0.1:1455", got)
	}

	// 同渠道并发第二个 start：409（口径「一个渠道同时只一个」）。
	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/login/start", `{}`); status != http.StatusConflict {
		t.Errorf("第二个 login/start 期望 409，得到 %d：%s", status, body)
	}

	// poll 是三只接口之一：有进行中的登录时 waiting。
	var poll struct {
		Status string `json:"status"`
	}
	a.JSONInto(t, http.MethodGet, "/panel/api/channels/"+itoa(ch)+"/login/poll", "", &poll)
	if poll.Status != "waiting" {
		t.Errorf("login/poll = %q，期望 waiting", poll.Status)
	}

	state := u.Query().Get("state")
	callback := "http://127.0.0.1:1455/auth/callback?code=ac-1&state=" +
		url.QueryEscape(state) + "&client_id=oaiapp-1"

	// state 不符先 400，且不清现场——改对再贴能完成。
	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/login/complete",
		`{"callback_url":"http://127.0.0.1:1455/auth/callback?code=x&state=wrong&client_id=oaiapp-1"}`); status != http.StatusBadRequest {
		t.Errorf("state 不符的 complete 期望 400，得到 %d：%s", status, body)
	}

	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/login/complete",
		`{"callback_url":`+strconvQuote(callback)+`}`); status != http.StatusNoContent {
		t.Fatalf("login/complete 期望 204，得到 %d：%s", status, body)
	}

	// 凭证落库：值是整包 JSON，email / client_id 来自回调与 ID token。
	var creds []struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Credential string `json:"credential"`
		Disabled   bool   `json:"disabled"`
	}
	a.JSONInto(t, http.MethodGet, "/panel/api/channels/"+itoa(ch)+"/credentials", "", &creds)
	if len(creds) != 1 {
		t.Fatalf("登录后该恰好一份凭证，得到 %d", len(creds))
	}
	var value struct {
		ClientID    string   `json:"client_id"`
		HostID      string   `json:"host_id"`
		Email       string   `json:"email"`
		Sub         string   `json:"sub"`
		AccessToken string   `json:"access_token"`
		Scopes      []string `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(creds[0].Credential), &value); err != nil {
		t.Fatalf("落库的凭证值不是 JSON：%v", err)
	}
	if value.ClientID != "oaiapp-1" || value.Email != "po@example.com" || value.AccessToken != "at-new" {
		t.Errorf("落库的凭证字段不对：%+v", value)
	}
	if len(value.Scopes) == 0 || value.Scopes[0] != "chatgpt.tokens.use.direct" {
		t.Errorf("scopes = %v，期望以直连范围开头", value.Scopes)
	}

	// 渠道行回读：enabled_keys 从 0 到 1（「缺凭证」标记消失），credential_type 随行下发。
	var channels []struct {
		EnabledKeys    int    `json:"enabled_keys"`
		CredentialType string `json:"credential_type"`
	}
	a.JSONInto(t, http.MethodGet, "/panel/api/channels", "", &channels)
	var found bool
	for _, c := range channels {
		if c.CredentialType == "chatgpt_account" && c.EnabledKeys == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("渠道清单该有一个 enabled_keys=1 的 chatgpt_account 渠道：%+v", channels)
	}

	// 完成之后 poll 回 none、complete 重放也 404 不到哪去（待完成态已消费）。
	a.JSONInto(t, http.MethodGet, "/panel/api/channels/"+itoa(ch)+"/login/poll", "", &poll)
	if poll.Status != "none" {
		t.Errorf("完成后的 login/poll = %q，期望 none", poll.Status)
	}
}

// TestLoginCompleteWithoutPending：没有进行中的登录就 complete，400 说清。
func TestLoginCompleteWithoutPending(t *testing.T) {
	f := newSIWCIssuer(t)
	db := gatewaytest.NewDB(t)
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: f.URL})
	a := g.LoggedIn(t)
	ch := seedEmptySubscriptionChannel(t, db, false)

	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/login/complete",
		`{"callback_url":"http://127.0.0.1:1455/auth/callback?code=1&state=zzz&client_id=oaiapp-1"}`); status != http.StatusBadRequest {
		t.Errorf("没有进行中登录的 complete 期望 400，得到 %d：%s", status, body)
	}
}

// TestLoginRejectsNonSubscriptionChannel：api_key 渠道不开登录弹层。
func TestLoginRejectsNonSubscriptionChannel(t *testing.T) {
	f := newSIWCIssuer(t)
	db := gatewaytest.NewDB(t)
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: f.URL})
	a := g.LoggedIn(t)
	ch := gatewaytest.SeedChannel(t, db, "plain", "openai", "https://api.example.com", "sk-x")

	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/login/start", `{}`); status != http.StatusBadRequest {
		t.Errorf("api_key 渠道的 login/start 期望 400，得到 %d：%s", status, body)
	} else if !strings.Contains(body, "订阅渠道") {
		t.Errorf("错误该说明登录弹层只对订阅渠道开放：%s", body)
	}
}

// TestReLoginReplacesDeadCredential：「重新登录」的原行替换——死亡停用（reason
// reauth_required、渠道 enabled_keys=0、其余管理写全被校验打回）之后，这条入口
// 仍要把渠道救活：同一行 id 与名字不变、换值、重新启用、清停用现场。
func TestReLoginReplacesDeadCredential(t *testing.T) {
	f := newSIWCIssuer(t)
	db := gatewaytest.NewDB(t)
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: f.URL})
	a := g.LoggedIn(t)
	ch := seedEmptySubscriptionChannel(t, db, false)
	credID := gatewaytest.SeedNamedCredential(t, db, ch, "主号", liveCredentialJSON())
	if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1, disabled_reason = 'reauth_required',
		disabled_at = CURRENT_TIMESTAMP WHERE id = ?`, credID); err != nil {
		t.Fatal(err)
	}

	// 死亡现场：别的管理写（改名）被写后校验打回——已知背景，PO 待裁；重新登录
	// 的入口必须照样能用。
	if status, _ := a.Do(t, http.MethodPut, "/panel/api/channels/"+itoa(ch)+"/settings",
		`{"name":"改名试试"}`); status != http.StatusBadRequest {
		t.Errorf("全灭渠道的普通管理写期望被校验打回 400，得到 %d——这道闸不是本票动的", status)
	}

	// start 带 credential_id（「重新登录」的入口形态），complete 用返回的 state。
	raw := startLoginOverHTTP(t, a, f, ch, `{"credential_id":`+itoa(credID)+`}`)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	callback := "http://127.0.0.1:1455/auth/callback?code=ac-2&state=" +
		url.QueryEscape(u.Query().Get("state")) + "&client_id=oaiapp-2"
	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/login/complete",
		`{"callback_url":`+strconvQuote(callback)+`}`); status != http.StatusNoContent {
		t.Fatalf("重新登录的 complete 期望 204，得到 %d：%s", status, body)
	}

	var creds []struct {
		ID             int64  `json:"id"`
		Name           string `json:"name"`
		Credential     string `json:"credential"`
		Disabled       bool   `json:"disabled"`
		DisabledReason string `json:"disabled_reason"`
	}
	a.JSONInto(t, http.MethodGet, "/panel/api/channels/"+itoa(ch)+"/credentials", "", &creds)
	if len(creds) != 1 || creds[0].ID != credID {
		t.Fatalf("重新登录该替换原行（id=%d），得到 %+v", credID, creds)
	}
	if creds[0].Name != "主号" || creds[0].Disabled || creds[0].DisabledReason != "" {
		t.Errorf("原行该留名复用并清停用现场：name=%q disabled=%v reason=%q",
			creds[0].Name, creds[0].Disabled, creds[0].DisabledReason)
	}
	if !strings.Contains(creds[0].Credential, `"client_id":"oaiapp-2"`) ||
		!strings.Contains(creds[0].Credential, `"access_token":"at-new"`) {
		t.Errorf("原行的值该换成这次登录换到的：%s", creds[0].Credential)
	}
}

// TestPasteImportRejectsBadJSON：粘贴导入坏 JSON 拒因原文（票面验收）；好 JSON 落库。
func TestPasteImportRejectsBadJSON(t *testing.T) {
	f := newSIWCIssuer(t)
	db := gatewaytest.NewDB(t)
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: f.URL})
	a := g.LoggedIn(t)
	ch := seedEmptySubscriptionChannel(t, db, false)

	status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/credentials",
		`{"credential":"这不是 JSON"}`)
	if status != http.StatusBadRequest {
		t.Errorf("坏 JSON 期望 400，得到 %d：%s", status, body)
	}
	if !strings.Contains(body, "凭证 JSON") {
		t.Errorf("拒因该带原文说明：%s", body)
	}

	// 缺必填字段的形态也拒、且点名缺哪个。
	status, body = a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/credentials",
		`{"credential":"{\"client_id\":\"oaiapp-1\"}"}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "缺少字段") {
		t.Errorf("缺字段的坏凭证期望 400 且点名缺哪个，得到 %d：%s", status, body)
	}

	// 好 JSON（换机迁移贴的那份）落库成功。
	status, _ = a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/credentials",
		`{"credential":`+strconvQuote(liveCredentialJSON())+`}`)
	if status != http.StatusNoContent {
		t.Errorf("合法凭证 JSON 期望 204，得到 %d", status)
	}
}

// TestLoginStartGuards：start 的三道 400 护栏——credential_id 不属于本渠道 / 根本不存在
// （归属判据回归坏掉，重新登录会把 A 渠道的行换到 B 渠道名下，其余测试发现不了）、
// copilot_account 渠道（设备码随其实现票，结构立好但不放行）。
func TestLoginStartGuards(t *testing.T) {
	f := newSIWCIssuer(t)
	db := gatewaytest.NewDB(t)
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: f.URL})
	a := g.LoggedIn(t)
	ch := seedEmptySubscriptionChannel(t, db, false)

	// credential_id 属于别的渠道：从 api_key 渠道借一把凭证的 id 来。
	other := gatewaytest.SeedChannel(t, db, "plain", "openai", "https://api.example.com", "sk-x")
	var otherCred int64
	if err := db.QueryRow(`SELECT id FROM channel_keys WHERE channel_id = ?`, other).Scan(&otherCred); err != nil {
		t.Fatal(err)
	}
	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/login/start",
		`{"credential_id":`+itoa(otherCred)+`}`); status != http.StatusBadRequest {
		t.Errorf("他渠道凭证的 start 期望 400，得到 %d：%s", status, body)
	} else if !strings.Contains(body, "不在这个渠道里") {
		t.Errorf("拒因该点名归属：%s", body)
	}

	// credential_id 不存在：同一句拒因。
	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(ch)+"/login/start",
		`{"credential_id":99999}`); status != http.StatusBadRequest || !strings.Contains(body, "不在这个渠道里") {
		t.Errorf("不存在凭证的 start 期望 400 且点名归属，得到 %d：%s", status, body)
	}

	// copilot_account：设备码随其实现票，现在 400 点名。
	res, err := db.Exec(`INSERT INTO channels
		(name, base_url_openai_responses, credential_type, disabled) VALUES ('copilot-sub', 'https://api.example.com/v1', 'copilot_account', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	copilot, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if status, body := a.Do(t, http.MethodPost, "/panel/api/channels/"+itoa(copilot)+"/login/start", `{}`); status != http.StatusBadRequest || !strings.Contains(body, "Copilot") {
		t.Errorf("copilot 渠道的 start 期望 400 且点名随实现票启用，得到 %d：%s", status, body)
	}
}

// TestDeleteMissingCredentialIs404：重复删同一条凭证，第二次（行已不在）该是 404
// 而不是 500——CredentialForRevocation 的 ErrNoRows 必须映射 ErrNotFound，不然
// 删前读值那步就把双开页面各删一次的现场变成「读取失败」。
func TestDeleteMissingCredentialIs404(t *testing.T) {
	f := newSIWCIssuer(t)
	db := gatewaytest.NewDB(t)
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: f.URL})
	a := g.LoggedIn(t)
	// 渠道停着种：删掉唯一凭证不会撞「启用渠道至少一份启用凭证」的写后校验。
	ch := seedEmptySubscriptionChannel(t, db, true)
	credID := gatewaytest.SeedNamedCredential(t, db, ch, "主号", liveCredentialJSON())
	if status, body := a.Do(t, http.MethodDelete, "/panel/api/credentials/"+itoa(credID), ""); status != http.StatusNoContent {
		t.Fatalf("第一次删除期望 204，得到 %d：%s", status, body)
	}
	if status, body := a.Do(t, http.MethodDelete, "/panel/api/credentials/"+itoa(credID), ""); status != http.StatusNotFound || !strings.Contains(body, "记录不存在") {
		t.Errorf("第二次删除（行已不在）期望 404「记录不存在」，得到 %d：%s", status, body)
	}
}

// TestDeleteCredentialRevokesBestEffort：撤销端点失败仍删（票面验收）——best-effort
// 的「删照删」要在真链路上钉住：端点 500，DELETE 仍 204、行真没了；端点正常时
// 撤销确实被打到、带的是 refresh token 与 client_id。
func TestDeleteCredentialRevokesBestEffort(t *testing.T) {
	f := newSIWCIssuer(t)
	db := gatewaytest.NewDB(t)
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: f.URL})
	a := g.LoggedIn(t)
	// 渠道停着种：删掉唯一凭证不会撞「启用渠道至少一份启用凭证」的写后校验。
	ch := seedEmptySubscriptionChannel(t, db, true)
	credID := gatewaytest.SeedNamedCredential(t, db, ch, "主号", liveCredentialJSON())

	f.revokeFails = true
	if status, body := a.Do(t, http.MethodDelete, "/panel/api/credentials/"+itoa(credID), ""); status != http.StatusNoContent {
		t.Fatalf("撤销失败时删除该照删（204），得到 %d：%s", status, body)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM channel_keys WHERE id = ?`, credID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("凭证行没删掉")
	}

	// 端点正常时：撤销打得到、带对东西。
	credID2 := gatewaytest.SeedNamedCredential(t, db, ch, "二号", liveCredentialJSON())
	f.revokeFails = false
	if status, body := a.Do(t, http.MethodDelete, "/panel/api/credentials/"+itoa(credID2), ""); status != http.StatusNoContent {
		t.Fatalf("删除期望 204，得到 %d：%s", status, body)
	}
	select {
	case form := <-f.revokeSeen:
		if form.Get("token") != "rt-old" || form.Get("client_id") != "oaiapp-1" {
			t.Errorf("撤销表单 = %v，期望带旧 refresh token 与 client_id", form)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("撤销端点没被打到")
	}
}
