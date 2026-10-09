package subscription_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/subscription"
)

// #211 的 ID token 验签单测（口径层 §2.2 v1.52：magpie 不验，本项目要验）：
// 发现文档取 JWKS、缓存一天；验签名 + iss / aud / nonce / exp。
// 密钥与发现文档全在本地 httptest 上现造。

// oidcServer 起一个发现文档 + JWKS 的假签发方，公钥是本用例现造的 RSA。
type oidcServer struct {
	URL       string
	key       *rsa.PrivateKey
	discovery atomic.Int64
}

func newOIDCServer(t *testing.T) *oidcServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	o := &oidcServer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		o.discovery.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + o.URL + `","jwks_uri":"` + o.URL + `/jwks"}`))
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"k1","use":"sig","alg":"RS256","n":"` +
			base64.RawURLEncoding.EncodeToString(o.key.N.Bytes()) + `","e":"AQAB"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	o.URL = srv.URL
	return o
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signID 造一枚 RS256 的 id_token；claims 由调用方给。
func (o *oidcServer) signID(t *testing.T, header string, claims map[string]any) string {
	t.Helper()
	if header == "" {
		header = `{"alg":"RS256","kid":"k1","typ":"JWT"}`
	}
	h := b64([]byte(header))
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	p := b64(c)
	sum := sha256.Sum256([]byte(h + "." + p))
	sig, err := rsa.SignPKCS1v15(rand.Reader, o.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return h + "." + p + "." + b64(sig)
}

func defaultClaims(issuer, clientID string) map[string]any {
	return map[string]any{
		"iss": issuer, "aud": clientID, "nonce": "n-1",
		"exp": float64(time.Now().Add(10 * time.Minute).Unix()),
		"sub": "user-1", "email": "a@b.c",
	}
}

func TestVerifyIDTokenAcceptsValidToken(t *testing.T) {
	o := newOIDCServer(t)
	e := subscription.NewEngine(nil, o.URL)
	claims, err := e.VerifyIDToken(context.Background(), o.signID(t, "", defaultClaims(o.URL, "oaiapp-1")), "oaiapp-1", "n-1")
	if err != nil {
		t.Fatal(err)
	}
	if claims["sub"] != "user-1" || claims["email"] != "a@b.c" {
		t.Errorf("claims 该带 sub 与 email, got %v", claims)
	}
}

func TestVerifyIDTokenRejectsBadTokens(t *testing.T) {
	o := newOIDCServer(t)
	e := subscription.NewEngine(nil, o.URL)
	cases := []struct {
		name   string
		header string
		mutate func(c map[string]any)
	}{
		{"签发方不对", "", func(c map[string]any) { c["iss"] = "https://evil.example" }},
		{"aud 不是本 client", "", func(c map[string]any) { c["aud"] = "oaiapp-other" }},
		{"nonce 对不上", "", func(c map[string]any) { c["nonce"] = "n-2" }},
		{"过期", "", func(c map[string]any) { c["exp"] = float64(time.Now().Add(-time.Hour).Unix()) }},
		{"alg 不认", `{"alg":"HS256","kid":"k1"}`, nil},
		{"kid 不在 JWKS 里", `{"alg":"RS256","kid":"nope"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultClaims(o.URL, "oaiapp-1")
			if tc.mutate != nil {
				tc.mutate(c)
			}
			if _, err := e.VerifyIDToken(context.Background(), o.signID(t, tc.header, c), "oaiapp-1", "n-1"); err == nil {
				t.Fatal("该被验签拒掉")
			}
		})
	}
	// 签名被换过：claims 改成另一个人的，签名自然对不上。
	tampered := strings.Split(o.signID(t, "", defaultClaims(o.URL, "oaiapp-1")), ".")
	forged, _ := json.Marshal(map[string]any{
		"iss": o.URL, "aud": "oaiapp-1", "nonce": "n-1",
		"exp": float64(time.Now().Add(time.Hour).Unix()), "sub": "attacker",
	})
	tampered[1] = b64(forged)
	if _, err := e.VerifyIDToken(context.Background(), strings.Join(tampered, "."), "oaiapp-1", "n-1"); err == nil {
		t.Fatal("换过签名的 token 该被拒")
	}
}

func TestJWKSCachedForADay(t *testing.T) {
	o := newOIDCServer(t)
	e := subscription.NewEngine(nil, o.URL)
	tok := o.signID(t, "", defaultClaims(o.URL, "oaiapp-1"))
	for range 2 {
		if _, err := e.VerifyIDToken(context.Background(), tok, "oaiapp-1", "n-1"); err != nil {
			t.Fatal(err)
		}
	}
	if o.discovery.Load() != 1 {
		t.Errorf("JWKS 该缓存一天：发现文档被打 %d 次, 期望 1", o.discovery.Load())
	}
}

func TestScopeHasDirect(t *testing.T) {
	if !subscription.ScopeHasDirect("chatgpt.tokens.use.direct email openid") {
		t.Error("空格分隔的 scope 串该认出直连范围")
	}
	if subscription.ScopeHasDirect("email openid") {
		t.Error("没有直连范围不该认出")
	}
}
