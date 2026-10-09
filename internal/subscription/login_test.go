package subscription

// login_test.go 判 login.go（#212 登录弹层的服务端状态机）。白盒进包（不像
// refresh_test.go / verify_test.go 那样用 subscription_test 外部包）只为一件事：
// TTL 过期那条用例等不了十分钟，得拧 loginTTL 这个包内变量。除它之外全部走导
// 出面，与外部包用例同一个姿势。
//
// 假签发方起全套：发现文档 + JWKS + token 端点 + revocation 端点，ID token 用
// 本地 RSA 现签——登录链路的验收就是「httptest 假上游」跑通全链（票面原文）。

import (
	"context"
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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/store"
)

// fakeIssuer 是登录链路的假签发方。授权 URL 里带出去的 state / nonce 由用例从
// 回包 query 里取回来，token 端点按同一枚 nonce 签 ID token——engine 发什么
// challenge，我们照什么回，链路才接得上；nonceOverride 顶掉它（错 nonce 用例）。
type fakeIssuer struct {
	URL        string
	key        *rsa.PrivateKey
	tokenHits  int
	revokeHits chan url.Values
	// nonce 是这轮登录发给授权页的那枚（startLogin 从授权 URL 抠回来交给它——
	// nonce 不进 token 端点的表单，假签发方得知道该给 id_token 放哪枚）。
	nonce         string
	nonceOverride string
	// noDirectScope 打开时 token 回包不带直连范围——「scope 须含」那条负例用。
	noDirectScope bool
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{key: key, revokeHits: make(chan url.Values, 4)}
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
		f.tokenHits++
		if err := r.ParseForm(); err != nil {
			t.Errorf("token 端点收不到表单: %v", err)
		}
		nonce := f.nonce
		if f.nonceOverride != "" {
			nonce = f.nonceOverride
		}
		w.Header().Set("Content-Type", "application/json")
		scope := "chatgpt.tokens.use.direct email openid"
		if f.noDirectScope {
			scope = "email openid"
		}
		_, _ = w.Write([]byte(`{"access_token":"at-login","refresh_token":"rt-login",` +
			`"id_token":"` + f.signID(r.FormValue("client_id"), nonce) + `",` +
			`"expires_in":3600,"scope":"` + scope + `"}`))
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("撤销端点收不到表单: %v", err)
		}
		f.revokeHits <- r.PostForm
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// signID 签一枚 RS256 的 id_token（claims 齐到能过验签：iss / aud / nonce / exp，
// 外带 sub / email / 套餐——完整登录换出的凭证字段表见 §7.13）。
func (f *fakeIssuer) signID(clientID, nonce string) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k1"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss":                         f.URL,
		"aud":                         clientID,
		"nonce":                       nonce,
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

func newLoginDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// startLogin 起一次登录，把授权 URL 的 state 抠出来、nonce 告诉假签发方——nonce
// 是发给授权页的（不在 token 端点的表单里），假签发方得知道该给 id_token 放哪枚
// nonce，链路才接得上。
func startLogin(t *testing.T, e *Engine, f *fakeIssuer, channelID, replaceID int64) (raw, state string) {
	t.Helper()
	raw, err := e.StartLogin(context.Background(), channelID, replaceID)
	if err != nil {
		t.Fatalf("StartLogin 失败: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("授权 URL 解不开: %v", err)
	}
	f.nonce = u.Query().Get("nonce")
	return raw, u.Query().Get("state")
}

// callbackURL 按真机证据的键拼一段回调整条 URL（code / scope / state / client_id，
// 见 testdata/golden/responses-siwc-evidence/callback-query.redacted.json）。
func callbackURL(state string) string {
	return siwcCallbackURL + "?code=ac-1&scope=chatgpt.tokens.use.direct&state=" +
		url.QueryEscape(state) + "&client_id=oaiapp-1"
}

// TestLoginStartCompleteRoundTrip：start → complete 全链（httptest 假上游）。
// 授权 URL 的口径参数、ext_agent_host_id 的每实例一份、换出的凭证字段与 §7.13
// 字段表的对齐、落库前要过的形状闸，全在这一条里钉住。
func TestLoginStartCompleteRoundTrip(t *testing.T) {
	f := newFakeIssuer(t)
	db := newLoginDB(t)
	e := NewEngine(db, f.URL)

	raw, state := startLogin(t, e, f, 7, 0)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("授权 URL 解不开: %v", err)
	}
	if u.Path != "/api/accounts/authorize" {
		t.Errorf("授权页路径 = %q，期望 /api/accounts/authorize", u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":             siwcDynamicClient,
		"agent_name_hint":       "portage",
		"response_type":         "code",
		"redirect_uri":          siwcCallbackURL,
		"code_challenge_method": "S256",
		"scope":                 siwcScopes,
	} {
		if got := q.Get(k); got != want {
			t.Errorf("授权 URL 的 %s = %q，期望 %q", k, got, want)
		}
	}
	hostID := q.Get("ext_agent_host_id")
	if !strings.HasPrefix(hostID, "urn:uuid:") {
		t.Errorf("ext_agent_host_id = %q，期望 urn:uuid: 前缀", hostID)
	}
	if q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatal("state / nonce 该是非空随机串")
	}
	if got := e.PollLogin(7); got.Status != "waiting" {
		t.Errorf("进行中的登录 poll 该回 waiting，得到 %q", got.Status)
	}

	cred, replaceID, err := e.CompleteLogin(context.Background(), 7, callbackURL(state))
	if err != nil {
		t.Fatalf("CompleteLogin 失败: %v", err)
	}
	if replaceID != 0 {
		t.Errorf("新增登录的 replaceID = %d，期望 0", replaceID)
	}
	if got := e.PollLogin(7); got.Status != "none" {
		t.Errorf("完成后的 poll 该回 none，得到 %q", got.Status)
	}
	if f.tokenHits != 1 {
		t.Errorf("token 端点该打一次，得到 %d", f.tokenHits)
	}
	if cred.ClientID != "oaiapp-1" || cred.HostID != hostID {
		t.Errorf("client_id / host_id = %q / %q，期望回调里的 oaiapp-1 与授权时那份 host_id",
			cred.ClientID, cred.HostID)
	}
	if cred.Sub != "user-123" || cred.Email != "po@example.com" {
		t.Errorf("sub / email = %q / %q，期望 ID token 里的那份", cred.Sub, cred.Email)
	}
	if cred.AccessToken != "at-login" || cred.RefreshToken != "rt-login" || cred.IDToken == "" {
		t.Errorf("token 三样没换对: %q / %q / %q", cred.AccessToken, cred.RefreshToken, cred.IDToken)
	}
	if d := time.Until(time.Unix(cred.ExpiresAt, 0)); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("expires_at 该按 expires_in=3600 推，得到 %v", d)
	}
	if !strings.Contains(strings.Join(cred.Scopes, " "), store.SIWCDirectScope) {
		t.Errorf("scopes = %v，期望含直连范围", cred.Scopes)
	}
	// 拼出来的 JSON 必须过写侧形状闸——complete 落库走的就是那道闸。
	rawCred, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ParseChatGPTCredential(string(rawCred)); err != nil {
		t.Errorf("登录拼出的凭证 JSON 过不了写侧形状闸: %v\n%s", err, rawCred)
	}
}

// TestLoginHostIDOnePerInstance：ext_agent_host_id 每实例一个、落 settings 表、
// 起第二次登录不重生成。
func TestLoginHostIDOnePerInstance(t *testing.T) {
	f := newFakeIssuer(t)
	db := newLoginDB(t)
	e := NewEngine(db, f.URL)

	if _, err := e.StartLogin(context.Background(), 1, 0); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetSetting(context.Background(), db, store.SettingExtAgentHostID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved, "urn:uuid:") {
		t.Fatalf("settings 里该有 urn:uuid: 前缀的 host id，得到 %q", saved)
	}
	// 同一个实例（同库同引擎）再起一次、换个渠道再起一次，都不重生成。
	if _, err := e.StartLogin(context.Background(), 2, 0); err != nil {
		t.Fatal(err)
	}
	again, err := store.GetSetting(context.Background(), db, store.SettingExtAgentHostID)
	if err != nil {
		t.Fatal(err)
	}
	if again != saved {
		t.Errorf("host id 被重新生成了：%q → %q（该每实例一份）", saved, again)
	}
}

// TestLoginCompleteRejectsWrongState：state 不符（票面验收各一例）。
func TestLoginCompleteRejectsWrongState(t *testing.T) {
	f := newFakeIssuer(t)
	e := NewEngine(newLoginDB(t), f.URL)
	_, state := startLogin(t, e, f, 1, 0)

	_, _, err := e.CompleteLogin(context.Background(), 1, callbackURL("别人家的-state"))
	if err == nil {
		t.Fatal("state 对不上该拒绝")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Errorf("错误该点名 state：%v", err)
	}
	// 拒绝不清现场：同一枚 state 改对再贴能完成（magpie 同判——贴错不该烧掉这次登录）。
	if _, _, err := e.CompleteLogin(context.Background(), 1, callbackURL(state)); err != nil {
		t.Fatalf("贴错之后再贴对的该成功: %v", err)
	}
}

// TestLoginCompleteRejectsExpired：TTL 过期（票面验收各一例）。白盒就在这一条：
// loginTTL 是包内变量，拧到毫秒级等它过期。
func TestLoginCompleteRejectsExpired(t *testing.T) {
	f := newFakeIssuer(t)
	e := NewEngine(newLoginDB(t), f.URL)
	orig := loginTTL
	loginTTL = 5 * time.Millisecond
	defer func() { loginTTL = orig }()

	_, state := startLogin(t, e, f, 1, 0)
	time.Sleep(10 * time.Millisecond)
	_, _, err := e.CompleteLogin(context.Background(), 1, callbackURL(state))
	if err == nil {
		t.Fatal("过期待完成态该拒绝")
	}
	if !strings.Contains(err.Error(), "进行中的登录") {
		t.Errorf("错误该说没有进行中的登录：%v", err)
	}
	if got := e.PollLogin(1); got.Status != "none" {
		t.Errorf("过期的登录 poll 该回 none，得到 %q", got.Status)
	}
}

// TestLoginStartRejectsSecondWhilePending：同渠道并发第二个 start（票面验收各一例）。
func TestLoginStartRejectsSecondWhilePending(t *testing.T) {
	f := newFakeIssuer(t)
	e := NewEngine(newLoginDB(t), f.URL)
	startLogin(t, e, f, 3, 0)
	if _, err := e.StartLogin(context.Background(), 3, 0); err == nil {
		t.Fatal("同渠道第二个 start 该拒绝")
	} else if err != ErrLoginInProgress {
		t.Errorf("错误该是 ErrLoginInProgress：%v", err)
	}
	// 「一个渠道同时只一个」锁的是渠道不是全局：别的渠道照起。
	if _, err := e.StartLogin(context.Background(), 4, 0); err != nil {
		t.Errorf("另一渠道的 start 不该被拦: %v", err)
	}
}

// TestLoginStartConcurrentSharesHostID：多渠道并发起登录，ext_agent_host_id 仍每实例
// 一份。host_id 的取造已挪到 loginMu 之外、由 hostIDMu 自串行（#212 集成评审，
// CodeRabbit：settings I/O 不再压着整个登录面的锁）——锁丢了的话，两个并发 start
// 各造各的 UUID、各写各的，这条就红；并发下拿不到同一份也红。
func TestLoginStartConcurrentSharesHostID(t *testing.T) {
	f := newFakeIssuer(t)
	e := NewEngine(newLoginDB(t), f.URL)
	const n = 8
	urls := make([]string, n)
	var wg sync.WaitGroup
	for i := range urls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, err := e.StartLogin(context.Background(), int64(i+1), 0)
			if err != nil {
				t.Errorf("start %d 失败: %v", i+1, err)
				return
			}
			urls[i] = raw
		}(i)
	}
	wg.Wait()
	if urls[0] == "" {
		t.Fatal("第一个 start 没拿到地址")
	}
	u0, err := url.Parse(urls[0])
	if err != nil {
		t.Fatal(err)
	}
	want := u0.Query().Get("ext_agent_host_id")
	if want == "" {
		t.Fatal("授权 URL 里没有 ext_agent_host_id")
	}
	for i, raw := range urls {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("URL %d 解不开: %v", i, err)
		}
		if got := u.Query().Get("ext_agent_host_id"); got != want {
			t.Errorf("start %d 的 host_id = %q，期望同一份 %q", i+1, got, want)
		}
	}
}

// TestLoginCompleteNonceMustMatch：id_token 的 nonce 必须是当次的——这条不验，
// 验签就形同虚设（#211 local-reviewer 转告：登录 complete 必须传非空 nonce）。
func TestLoginCompleteNonceMustMatch(t *testing.T) {
	f := newFakeIssuer(t)
	f.nonceOverride = "上一次的 nonce"
	e := NewEngine(newLoginDB(t), f.URL)
	_, state := startLogin(t, e, f, 1, 0)

	_, _, err := e.CompleteLogin(context.Background(), 1, callbackURL(state))
	if err == nil {
		t.Fatal("id_token 带错 nonce 该被验签拒绝")
	}
	if !strings.Contains(err.Error(), "nonce") {
		t.Errorf("错误该点名 nonce：%v", err)
	}
}

// TestLoginCallbackValidation：粘贴回调的 scheme / host / port / path / query 校验，
// 与缺 code / 缺 client_id 各一例。
func TestLoginCallbackValidation(t *testing.T) {
	f := newFakeIssuer(t)
	e := NewEngine(newLoginDB(t), f.URL)
	_, state := startLogin(t, e, f, 1, 0)
	s := url.QueryEscape(state)

	for name, pasted := range map[string]string{
		"https协议": "https://127.0.0.1:1455/auth/callback?code=1&state=" + s + "&client_id=oaiapp-1",
		"非环回域名":   "http://example.com/auth/callback?code=1&state=" + s + "&client_id=oaiapp-1",
		"端口不对":    "http://127.0.0.1:8080/auth/callback?code=1&state=" + s + "&client_id=oaiapp-1",
		"路径不对":    "http://127.0.0.1:1455/elsewhere?code=1&state=" + s + "&client_id=oaiapp-1",
		"没有query": "http://127.0.0.1:1455/auth/callback",
		"裸串":      "不是地址",
	} {
		if _, _, err := e.CompleteLogin(context.Background(), 1, pasted); err == nil {
			t.Errorf("%s 该被拒绝", name)
		}
	}
	for name, pasted := range map[string]string{
		"没有code":      siwcCallbackURL + "?state=" + s + "&client_id=oaiapp-1",
		"没有client_id": siwcCallbackURL + "?code=1&state=" + s,
	} {
		if _, _, err := e.CompleteLogin(context.Background(), 1, pasted); err == nil {
			t.Errorf("%s 该被拒绝", name)
		}
	}
}

// TestLoginRequiresDirectScope：token 回包缺直连范围该拒（§7.13「scope 须含」是硬
// 要求——没有它账号的订阅额度用不到 API 上；这条拒绝路径在 refresh 与登录共用
// 的 applyToken 里，此前全仓无负例）。
func TestLoginRequiresDirectScope(t *testing.T) {
	f := newFakeIssuer(t)
	f.noDirectScope = true
	e := NewEngine(newLoginDB(t), f.URL)
	_, state := startLogin(t, e, f, 1, 0)

	_, _, err := e.CompleteLogin(context.Background(), 1, callbackURL(state))
	if err == nil {
		t.Fatal("token 回包缺直连范围该拒绝")
	}
	if !strings.Contains(err.Error(), store.SIWCDirectScope) {
		t.Errorf("错误该点名直连范围：%v", err)
	}
	// 换 token 之后才拒的，刚发出来的 refresh token 不能悬在上游：best-effort 撤掉
	// （#212 审查，Codex：postTokenForm 成功后的失败路径都走 revokeIssued）。
	select {
	case form := <-f.revokeHits:
		if got := form.Get("token"); got != "rt-login" {
			t.Errorf("撤销的 token = %q，期望刚发出来的 rt-login", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("缺直连范围被拒后，没有 best-effort 撤销刚发出来的登录态")
	}
}

// TestRevoke：best-effort 撤销打发现文档点名的 revocation_endpoint，带 refresh
// token 与 client_id（magpie siwcRevoke 同判）。
func TestRevoke(t *testing.T) {
	f := newFakeIssuer(t)
	e := NewEngine(newLoginDB(t), f.URL)
	cred := store.ChatGPTCredential{ClientID: "oaiapp-1", RefreshToken: "rt-1"}

	if err := e.Revoke(context.Background(), cred); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	select {
	case form := <-f.revokeHits:
		if form.Get("token") != "rt-1" || form.Get("client_id") != "oaiapp-1" ||
			form.Get("token_type_hint") != "refresh_token" {
			t.Errorf("撤销表单 = %v，期望带 refresh token 与 client_id", form)
		}
	default:
		t.Fatal("撤销端点没被打到")
	}
}

// TestRevokeFailureIsError：发现文档没点名 revocation_endpoint、端点回 500——错误
// 原样回给调用方记日志，不在这里吞掉（删凭证那条路靠它「失败不阻止删除」）。
func TestRevokeFailureIsError(t *testing.T) {
	// 起一个不带 revocation_endpoint 的发现文档。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + "http://127.0.0.1:1" + `"}`))
	}))
	t.Cleanup(srv.Close)
	e := NewEngine(newLoginDB(t), srv.URL)
	cred := store.ChatGPTCredential{ClientID: "oaiapp-1", RefreshToken: "rt-1"}
	if err := e.Revoke(context.Background(), cred); err == nil {
		t.Error("发现文档没点名 revocation_endpoint 该报错")
	} else if !strings.Contains(err.Error(), "revocation_endpoint") {
		t.Errorf("错误该点名 revocation_endpoint：%v", err)
	}

	// 再起一个点名了、但端点回 500 的——错误该带状态码，端点确实被打到。
	// srvURL 在 handler 外声明、起完再赋：请求都发生在起服务之后，拿得到。
	var hits int
	var srvURL string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/revoke") {
			hits++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"revocation_endpoint":"` + srvURL + `/revoke"}`))
	}))
	srvURL = srv2.URL
	t.Cleanup(srv2.Close)
	e2 := NewEngine(newLoginDB(t), srv2.URL)
	if err := e2.Revoke(context.Background(), cred); err == nil {
		t.Error("撤销端点回 500 该报错")
	} else if !strings.Contains(err.Error(), "500") {
		t.Errorf("错误该带状态码：%v", err)
	}
	if hits != 1 {
		t.Errorf("撤销端点该打一次，得到 %d", hits)
	}
}

// TestLoginCompleteDetachesExchangeFromCancellation：换 token 不随请求取消（Codex
// 评审）：code 一次性、待完成态已消费——浏览器中途断开时若交换随 ctx 取消，上游
// 可能已发了一把自己再拿不回的 refresh token，重贴回调也换不回来。交换改走
// WithoutCancel 后：取消的 ctx 照样打得到 token 端点；后续校验链还在原 ctx 里、
// 会因取消而失败——失败路径该把刚发出来的 token 撤掉（revokeIssued）。
func TestLoginCompleteDetachesExchangeFromCancellation(t *testing.T) {
	f := newFakeIssuer(t)
	e := NewEngine(newLoginDB(t), f.URL)
	_, state := startLogin(t, e, f, 1, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _ = e.CompleteLogin(ctx, 1, callbackURL(state))
	if f.tokenHits == 0 {
		t.Fatal("换 token 随请求取消了：上游可能已发过 token，这把会悬死")
	}
	select {
	case form := <-f.revokeHits:
		if got := form.Get("token"); got != "rt-login" {
			t.Errorf("撤销的 token = %q，期望刚发出来的 rt-login", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消路径的校验失败没有撤销刚发出来的登录态")
	}
}
