package subscription_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/subscription"
)

// #211 的引擎单测：请求前懒刷新、每把凭证一把锁、死亡码标停用。token 端点与
// OIDC 签发方都指向 httptest，凭证是真 SQLite 库里的行。

// tokenServer 是一个可控的 token 端点：记请求次数与表单，按给的剧本回。
type tokenServer struct {
	URL  string
	srv  *httptest.Server
	hits atomic.Int64

	mu       sync.Mutex
	lastForm url.Values
}

func newTokenServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.hits.Add(1)
		_ = r.ParseForm()
		ts.mu.Lock()
		ts.lastForm = r.PostForm
		ts.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(ts.srv.Close)
	ts.URL = ts.srv.URL + "/token"
	return ts
}

// okReply 回一次正常的 refresh 响应（#205 token-refresh.redacted.json 的形状：refresh_token 轮换）。
func okReply(w http.ResponseWriter, access, refresh string, expiresIn float64) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"access_token":"` + access + `","token_type":"Bearer","expires_in":` +
		strconv.FormatFloat(expiresIn, 'f', -1, 64) +
		`,"scope":"chatgpt.tokens.use.direct email openid","id_token":"idt-2","refresh_token":"` + refresh + `"}`))
}

// errReply 回一次 OAuth 错误（error 可以是字符串，也可以是 {code,message} 对象——两种形态都收）。
func errReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// seedSubDB 建一个带 chatgpt_account 渠道 + 一把凭证的库，回 (db, 凭证 id)。
func seedSubDB(t *testing.T, expiresAt int64) (*sql.DB, int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO channels (id, name, base_url_openai_responses, credential_type)
		 VALUES (1, 'sub', 'https://api.openai.example/v1', 'chatgpt_account')`); err != nil {
		t.Fatal(err)
	}
	cred := `{"client_id":"oaiapp-1","host_id":"urn:uuid:h","sub":"u1","email":"a@b.c",` +
		`"id_token":"idt-1","access_token":"at-old","refresh_token":"rt-old",` +
		`"expires_at":` + strconv.FormatInt(expiresAt, 10) +
		`,"scopes":["chatgpt.tokens.use.direct","openid"]}`
	res, err := db.Exec(
		`INSERT INTO channel_keys (channel_id, name, credential) VALUES (1, '主号', ?)`, cred)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return db, id
}

func engine(db *sql.DB, tokenURL string) *subscription.Engine {
	// tokenURL 形如 http://127.0.0.1:PORT/token；issuer 就是它的根。
	return subscription.NewEngine(db, strings.TrimSuffix(tokenURL, "/token"))
}

func credRow(t *testing.T, db *sql.DB, id int64) store.CredentialInfo {
	t.Helper()
	c, err := store.CredentialByID(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEffectiveSkipsFreshToken(t *testing.T) {
	db, id := seedSubDB(t, time.Now().Add(time.Hour).Unix())
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("access 还剩一小时，不该打 token 端点")
	})
	access, err := engine(db, ts.URL).Effective(context.Background(),
		store.Credential{ID: id, Name: "主号", Value: credRow(t, db, id).Credential})
	if err != nil {
		t.Fatal(err)
	}
	if access != "at-old" {
		t.Errorf("出站值 = %q, 期望库里的 access_token", access)
	}
	if ts.hits.Load() != 0 {
		t.Errorf("token 端点被打 %d 次, 期望 0", ts.hits.Load())
	}
}

func TestEffectiveRefreshesNearExpiryAndPersistsRotation(t *testing.T) {
	db, id := seedSubDB(t, time.Now().Add(time.Minute).Unix()) // 剩 1 分钟 < 3 分钟门槛
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		okReply(w, "at-new", "rt-new", 3600)
	})
	access, err := engine(db, ts.URL).Effective(context.Background(),
		store.Credential{ID: id, Name: "主号", Value: credRow(t, db, id).Credential})
	if err != nil {
		t.Fatal(err)
	}
	if access != "at-new" {
		t.Errorf("出站值 = %q, 期望刷新后的 at-new", access)
	}
	// 出站表单按 §7.13：grant_type / client_id / refresh_token / resource。
	ts.mu.Lock()
	f := ts.lastForm
	ts.mu.Unlock()
	if f.Get("grant_type") != "refresh_token" || f.Get("client_id") != "oaiapp-1" ||
		f.Get("refresh_token") != "rt-old" || f.Get("resource") != "https://api.openai.com/v1" {
		t.Errorf("refresh 表单不对: %v", f)
	}
	// 轮换后的整包 JSON 落库：refresh_token 与 expires_at 都换成新的。
	got := credRow(t, db, id)
	c, err := store.ParseChatGPTCredential(got.Credential)
	if err != nil {
		t.Fatalf("落库的不再是合法凭证 JSON: %v", err)
	}
	if c.RefreshToken != "rt-new" || c.AccessToken != "at-new" {
		t.Errorf("落库的 token 没换: access=%q refresh=%q", c.AccessToken, c.RefreshToken)
	}
	if c.ExpiresAt < time.Now().Add(3500*time.Second).Unix() {
		t.Errorf("落库的 expires_at = %d, 期望按 expires_in=3600 推到一小时后", c.ExpiresAt)
	}
	if c.IDToken != "idt-2" {
		t.Errorf("refresh 响应带回的新 id_token 该随包落库, got %q", c.IDToken)
	}
	// 第二次（token 新鲜）不再打端点。
	if _, err := engine(db, ts.URL).Effective(context.Background(),
		store.Credential{ID: id, Name: "主号", Value: got.Credential}); err != nil {
		t.Fatal(err)
	}
	if ts.hits.Load() != 1 {
		t.Errorf("token 端点被打 %d 次, 期望 1", ts.hits.Load())
	}
}

func TestConcurrentRequestsRefreshOnlyOnce(t *testing.T) {
	db, id := seedSubDB(t, time.Now().Add(time.Minute).Unix())
	// 端点故意慢 150ms：把「第二个请求在锁上等」变成必经路径，再验它拿锁后复查、不再刷。
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		okReply(w, "at-new", "rt-new", 3600)
	})
	e := engine(db, ts.URL)
	raw := credRow(t, db, id).Credential
	var wg sync.WaitGroup
	accesses := make([]string, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := e.Effective(context.Background(), store.Credential{ID: id, Name: "主号", Value: raw})
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
			}
			accesses[i] = a
		}(i)
	}
	wg.Wait()
	if ts.hits.Load() != 1 {
		t.Errorf("同把凭证并发只该刷一次（拿锁后复查）, token 端点被打 %d 次", ts.hits.Load())
	}
	if accesses[0] != "at-new" || accesses[1] != "at-new" {
		t.Errorf("两条都该拿到刷新后的 access, got %q / %q", accesses[0], accesses[1])
	}
}

func TestDeathCodesDisableCredential(t *testing.T) {
	// §7.13 的七词死亡表：逐个回，逐个标停用、reason 落库。
	for _, code := range []string{
		"invalid_grant", "invalid_refresh_token", "token_expired",
		"refresh_token_expired", "refresh_token_invalidated",
		"refresh_token_reused", "invalid_client",
	} {
		t.Run(code, func(t *testing.T) {
			db, id := seedSubDB(t, time.Now().Add(time.Minute).Unix())
			ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
				errReply(w, 400, `{"error":"`+code+`","error_description":"no more"}`)
			})
			raw := credRow(t, db, id).Credential
			_, err := engine(db, ts.URL).Effective(context.Background(),
				store.Credential{ID: id, Name: "主号", Value: raw})
			if err == nil || !strings.Contains(err.Error(), "重新登录") {
				t.Fatalf("死亡码 %s 该回需要重新登录的错误, got %v", code, err)
			}
			got := credRow(t, db, id)
			if !got.Disabled {
				t.Fatalf("死亡码 %s 该把凭证标停用", code)
			}
			if got.DisabledReason != "reauth_required" {
				t.Errorf("停用原因 = %q, 期望 reauth_required", got.DisabledReason)
			}
			if got.DisabledAt == "" {
				t.Error("停用时刻该落库")
			}
			// 死亡码只标停用，凭证值本身一个字节不动。
			if got.Credential != raw {
				t.Errorf("死亡码不该动凭证值\nraw %s\ngot %s", raw, got.Credential)
			}
		})
	}
}

func TestDeathCodeObjectErrorShape(t *testing.T) {
	// #205 的证据形态之一：error 是 {code,message} 对象——死亡码藏在 code 里。
	db, id := seedSubDB(t, time.Now().Add(time.Minute).Unix())
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		errReply(w, 400, `{"error":{"code":"invalid_grant","message":"revoked"}}`)
	})
	_, err := engine(db, ts.URL).Effective(context.Background(),
		store.Credential{ID: id, Name: "主号", Value: credRow(t, db, id).Credential})
	if err == nil || !strings.Contains(err.Error(), "重新登录") {
		t.Fatalf("对象形态的死亡码也该认出, got %v", err)
	}
	if got := credRow(t, db, id); !got.Disabled {
		t.Error("该被标停用")
	}
}

func TestNetworkErrorLeavesCredentialIntact(t *testing.T) {
	// 拨不通 + access 没过期：照用旧 access，凭证一个字段都不动。
	db, id := seedSubDB(t, time.Now().Add(10*time.Minute).Unix())
	dead := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {})
	dead.srv.Close()
	e := engine(db, dead.URL)
	raw := credRow(t, db, id).Credential
	access, err := e.Effective(context.Background(), store.Credential{ID: id, Name: "主号", Value: raw})
	if err != nil {
		t.Fatalf("网络错误 + access 未过期，该照用旧 access: %v", err)
	}
	if access != "at-old" {
		t.Errorf("出站值 = %q, 期望 at-old", access)
	}
	if got := credRow(t, db, id); got.Disabled || got.Credential != raw {
		t.Errorf("网络错误不该动凭证: disabled=%v 值变没变=%v", got.Disabled, got.Credential != raw)
	}

	// 拨不通 + access 已过期：报错（非死亡），凭证仍启用——「不动凭证」的另一半。
	db2, id2 := seedSubDB(t, time.Now().Add(-time.Minute).Unix())
	dead2 := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {})
	dead2.srv.Close()
	_, err = engine(db2, dead2.URL).Effective(context.Background(),
		store.Credential{ID: id2, Name: "主号", Value: credRow(t, db2, id2).Credential})
	if err == nil {
		t.Fatal("access 过期又刷不动，该报错")
	}
	if strings.Contains(err.Error(), "重新登录") {
		t.Errorf("网络错误不是死亡码，不该说需要重新登录: %v", err)
	}
	if got := credRow(t, db2, id2); got.Disabled {
		t.Errorf("非死亡码不该停用凭证: %v", got.DisabledReason)
	}
}

func TestMalformedTokenReplyUsesUnexpiredAccess(t *testing.T) {
	// 200 但回包畸形（缺 refresh_token）：与其余错误同判（§2.2 v1.52）——不动凭证，
	// access 还没过期就照用；过期了才认输。凭证一个字节不动、也不停用。
	db, id := seedSubDB(t, time.Now().Add(time.Minute).Unix())
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-new","expires_in":3600,"scope":"chatgpt.tokens.use.direct"}`))
	})
	raw := credRow(t, db, id).Credential
	access, err := engine(db, ts.URL).Effective(context.Background(),
		store.Credential{ID: id, Name: "主号", Value: raw})
	if err != nil {
		t.Fatalf("回包畸形 + access 未过期，该照用旧 access: %v", err)
	}
	if access != "at-old" {
		t.Errorf("出站值 = %q, 期望 at-old", access)
	}
	if got := credRow(t, db, id); got.Disabled || got.Credential != raw {
		t.Errorf("畸形回包不该动凭证: disabled=%v 值变没变=%v", got.Disabled, got.Credential != raw)
	}
}

func TestEffectiveAllSubstitutesAccessTokens(t *testing.T) {
	// relay 用的一层：全部可用时回换了值的子集；死亡的那把被摘掉、请求照发剩下的。
	db, id := seedSubDB(t, time.Now().Add(time.Hour).Unix())
	res, err := db.Exec(
		`INSERT INTO channel_keys (channel_id, name, credential) VALUES (1, '二号', ?)`,
		`{"client_id":"oaiapp-2","host_id":"urn:uuid:h","access_token":"at-2","refresh_token":"rt-2",`+
			`"expires_at":`+strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)+`,"scopes":["chatgpt.tokens.use.direct"]}`)
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := res.LastInsertId()
	ts := newTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		errReply(w, 400, `{"error":{"code":"refresh_token_reused","message":"no"}}`)
	})
	creds := []store.Credential{
		{ID: id, Name: "主号", Value: credRow(t, db, id).Credential},
		{ID: id2, Name: "二号", Value: credRow(t, db, id2).Credential},
	}
	out, err := engine(db, ts.URL).EffectiveAll(context.Background(), creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Name != "主号" || out[0].Value != "at-old" {
		t.Fatalf("该只回主号且值换成 access_token, got %+v", out)
	}
	if got := credRow(t, db, id2); !got.Disabled || got.DisabledReason != "reauth_required" {
		t.Errorf("二号该被标停用 reauth_required, got disabled=%v reason=%q", got.Disabled, got.DisabledReason)
	}

	// 全部死亡（其中一把已在库里带着 reauth_required 的停用现场）：回需要重新登录。
	dead := []store.Credential{{ID: id2, Name: "二号", Value: credRow(t, db, id2).Credential}}
	if _, err := engine(db, ts.URL).EffectiveAll(context.Background(), dead); err == nil ||
		!strings.Contains(err.Error(), "重新登录") {
		t.Errorf("全部死亡该回需要重新登录, got %v", err)
	}
}
