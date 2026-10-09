package server_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// #211 的订阅渠道 relay 集成：chatgpt_account 的凭证在出站前走请求前懒刷新，
// 死亡码停用后的请求（当次与后续）落 reauth_required、出站端点为空、不打上游。

const siwcRequest = `{"model":"` + accessPointModel + `","stream":false,` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}]}`

// seedSubscriptionGateway 种一个 chatgpt_account 渠道（openai_responses 透传）+
// 一把 expires_at 过会儿就到期的凭证，把订阅引擎指向 token 端点。回的 *atomic.Int64
// 是 token 端点的命中计数。
func seedSubscriptionGateway(t *testing.T, expiresAt time.Time, token func(w http.ResponseWriter, r *http.Request)) (*gatewaytest.Gateway, *gatewaytest.Upstream, *atomic.Int64, int64) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "siwc", "openai_responses", up.URL, "")
	if _, err := db.Exec(
		`UPDATE channels SET credential_type = 'chatgpt_account' WHERE id = ?`, ch); err != nil {
		t.Fatalf("改凭证类型失败: %v", err)
	}
	cred := `{"client_id":"oaiapp-1","host_id":"urn:uuid:h","access_token":"at-old",` +
		`"refresh_token":"rt-old","expires_at":` + strconv.FormatInt(expiresAt.Unix(), 10) +
		`,"scopes":["chatgpt.tokens.use.direct"]}`
	credID := gatewaytest.SeedNamedCredential(t, db, ch, "主号", cred)
	m := gatewaytest.SeedChannelModel(t, db, ch, "gpt-6.1-sol")
	ap := gatewaytest.SeedAccessPoint(t, db, accessPointModel)
	gatewaytest.SeedCandidate(t, db, ap, m, 100)

	hits := &atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		token(w, r)
	}))
	t.Cleanup(ts.Close)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{SubscriptionIssuer: ts.URL})
	return gw, up, hits, credID
}

// TestSubscriptionRelayRefreshesBeforeUpstream：出站前懒刷新，上游收到的 Bearer 是
// 刷新后的 access，轮换的 refresh token 落了库。
func TestSubscriptionRelayRefreshesBeforeUpstream(t *testing.T) {
	gw, up, hits, credID := seedSubscriptionGateway(t,
		time.Now().Add(time.Minute),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"at-new","token_type":"Bearer","expires_in":3600,` +
				`"scope":"chatgpt.tokens.use.direct","id_token":"idt-2","refresh_token":"rt-new"}`))
		})

	resp := gw.Post(t, "/v1/responses", siwcRequest, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}

	got := up.Last(t)
	if bearer := got.Header.Get("Authorization"); bearer != "Bearer at-new" {
		t.Errorf("上游收到的 Authorization = %q, 期望 Bearer at-new——懒刷新该在出站前换值", bearer)
	}
	var rawCred string
	if err := gw.DB.QueryRow(`SELECT credential FROM channel_keys WHERE id = ?`, credID).Scan(&rawCred); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawCred, `"refresh_token":"rt-new"`) {
		t.Errorf("轮换后的 refresh token 该落库, got %s", rawCred)
	}
	// token 端点只该被打一次（凭证池里只有一把）。
	if n := hits.Load(); n != 1 {
		t.Errorf("token 端点被打 %d 次, 期望 1", n)
	}
}

// TestSubscriptionRelayDeathCodeRefusesWithReauthWord：死亡码停用当次的请求 503、
// 流水落 reauth_required、出站端点为空；后续请求（启用凭证已归零，Resolve 那层倒下）
// 也落同一个词。
func TestSubscriptionRelayDeathCodeRefusesWithReauthWord(t *testing.T) {
	gw, up, hits, credID := seedSubscriptionGateway(t,
		time.Now().Add(time.Minute),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"refresh_token_reused","error_description":"no more"}`))
		})

	resp := gw.Post(t, "/v1/responses", siwcRequest, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, 期望 503；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if n := up.Count(); n != 0 {
		t.Errorf("打了上游 %d 次, 期望 0——凭证待重登时一个字节都不该到上游", n)
	}
	row := gw.LastCallRow(t)
	if !row.Error.Valid || row.Error.String != "reauth_required" {
		t.Errorf("error 列 = %v, 期望 reauth_required", row.Error)
	}
	if row.UpstreamEndpoint != "" {
		t.Errorf("出站端点 = %q, 期望空串——没打到上游", row.UpstreamEndpoint)
	}
	var disabled int
	var reason string
	if err := gw.DB.QueryRow(
		`SELECT disabled, COALESCE(disabled_reason,'') FROM channel_keys WHERE id = ?`, credID).
		Scan(&disabled, &reason); err != nil {
		t.Fatal(err)
	}
	if disabled == 0 || reason != "reauth_required" {
		t.Errorf("凭证该带着 reauth_required 停用, got disabled=%d reason=%q", disabled, reason)
	}

	// 后续请求：启用凭证已归零，Resolve 升格成 ErrReauthRequired——词不丢。
	resp = gw.Post(t, "/v1/responses", siwcRequest, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("后续请求状态码 = %d, 期望 503", resp.StatusCode)
	}
	row = gw.LastCallRow(t)
	if !row.Error.Valid || row.Error.String != "reauth_required" {
		t.Errorf("后续请求的 error 列 = %v, 期望 reauth_required", row.Error)
	}
	if row.UpstreamEndpoint != "" {
		t.Errorf("后续请求的出站端点 = %q, 期望空串", row.UpstreamEndpoint)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("凭证已停用，后续请求不该再打 token 端点（被打 %d 次）", n)
	}
}
