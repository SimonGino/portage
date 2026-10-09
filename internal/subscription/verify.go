package subscription

// verify.go 是 ChatGPT 侧 ID token 的验签（§7.13：JWKS 由发现文档取、缓存一天；
// 验 iss / aud / nonce / exp——magpie 不验，本项目要验，不照抄）。#211 只交引擎，
// 运行期调用方是 #212 的登录 complete（换 code 得来的 token 在落库前过这一道）。
//
// 签名算法收 RS256 / ES256 / PS256 三种（OIDC 常规集合，sub2api 的 OIDC 默认同款）
// 且只收这三种：alg 由头声明、按白名单挑校验函数，「none」与 HS256 在名单外，
// 拿公钥当 HMAC 秘钥那类 alg-confusion 在这里进不来。

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// jwksTTL 是 JWKS 的缓存时长（口径钉「一天」）。登录是稀有动作，缓存的收益是
// 连续两次登录不再各拉一遍发现文档；到期后下一次验签重新取，轮换密钥等得起。
const jwksTTL = 24 * time.Hour

// expLeeway 是 exp 的宽限（与 magpie 的 siwcCheckID 同值）：签发到验证之间的
// 时钟微小漂移不该拒人，真过期一小时仍然拒。
const expLeeway = time.Minute

// jwk 是 JWKS 里的一把公钥；n/e 是 RSA，x/y/crv 是 EC，按 kty 取用。
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Crv string `json:"crv"`
}

// publicKey 把一把 JWK 翻成 crypto 可用的公钥。算法匹配不在这一步做：kid / alg
// 的挑选与「JWK 自己的 alg 字段须与 JWT 头一致」都在 verifySignature 里选完了才到这儿。
func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := b64Big(k.N)
		if err != nil || n == nil {
			return nil, errors.New("JWKS 里的 RSA 公钥缺模数")
		}
		e, err := b64Big(k.E)
		if err != nil || e == nil || !e.IsInt64() || e.Int64() <= 0 || e.Int64() > mathMaxInt32 {
			return nil, errors.New("JWKS 里的 RSA 公钥指数不合法")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("JWKS 里的 EC 曲线 %q 不支持（只认 P-256 / ES256）", k.Crv)
		}
		x, errX := b64Big(k.X)
		y, errY := b64Big(k.Y)
		if errX != nil || errY != nil || x == nil || y == nil {
			return nil, errors.New("JWKS 里的 EC 公钥缺坐标")
		}
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
	default:
		return nil, fmt.Errorf("JWKS 里的密钥类型 %q 不支持（只认 RSA / EC）", k.Kty)
	}
}

const mathMaxInt32 = 1<<31 - 1

func b64Big(s string) (*big.Int, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

// jwksSet 取签发方当下的公钥集：发现文档 → jwks_uri → keys，成功后缓存一天。
func (e *Engine) jwksSet(ctx context.Context) ([]jwk, error) {
	e.jwksMu.Lock()
	defer e.jwksMu.Unlock()
	if e.jwksKeys != nil && time.Since(e.jwksAt) < jwksTTL {
		return e.jwksKeys, nil
	}
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := e.fetchJSON(ctx, e.issuer+"/.well-known/openid-configuration", &doc); err != nil {
		return nil, fmt.Errorf("拉 OIDC 发现文档失败: %w", err)
	}
	if doc.JWKSURI == "" {
		return nil, errors.New("发现文档没给 jwks_uri")
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := e.fetchJSON(ctx, doc.JWKSURI, &set); err != nil {
		return nil, fmt.Errorf("拉 JWKS 失败: %w", err)
	}
	if len(set.Keys) == 0 {
		return nil, errors.New("JWKS 里一把公钥都没有")
	}
	e.jwksKeys, e.jwksAt = set.Keys, time.Now()
	return e.jwksKeys, nil
}

// fetchJSON 拉一个小 JSON（发现文档与 JWKS 都是有界响应）。
func (e *Engine) fetchJSON(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("状态 %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(into)
}

// VerifyIDToken 验一枚 id_token：签名（JWKS）+ iss / aud / exp，nonce 非空时也验。
// 过了回 claims（#212 的登录 complete 从里面取 sub / email / 套餐）。
//
// clientID 是动态注册签发的那个（回调 query 里的 `oaiapp_…`）：aud 要是它。
// nonce 传空串表示「不验」（refresh 回包里带的 id_token 没有当次的 nonce）。
func (e *Engine) VerifyIDToken(ctx context.Context, idToken, clientID, nonce string) (map[string]any, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("ID token 不是三段 JWT")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeJSONPart(parts[0], &head); err != nil {
		return nil, fmt.Errorf("ID token 的头读不动: %w", err)
	}
	switch head.Alg {
	case "RS256", "PS256", "ES256":
	default:
		return nil, fmt.Errorf("ID token 的签名算法 %q 不在认的名单里（RS256 / PS256 / ES256）", head.Alg)
	}
	claims := map[string]any{}
	if err := decodeJSONPart(parts[1], &claims); err != nil {
		return nil, fmt.Errorf("ID token 的 claims 读不动: %w", err)
	}
	if err := e.verifySignature(ctx, parts, head); err != nil {
		return nil, err
	}
	return claims, checkClaims(claims, e.issuer, clientID, nonce, time.Now())
}

// decodeJSONPart 把一段 base64url 编码的 JSON 解开。
func decodeJSONPart(part string, into any) error {
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// verifySignature 用 JWKS 里的公钥验第三段签名。kid 有则按它找，找不到就是
// 「签发方没这把钥匙」，不猜。
func (e *Engine) verifySignature(ctx context.Context, parts []string, head struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}) error {
	keys, err := e.jwksSet(ctx)
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("ID token 的签名段不是合法的 base64url")
	}
	var lastErr error
	for _, k := range keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if head.Kid != "" && k.Kid != head.Kid {
			continue
		}
		if k.Alg != "" && k.Alg != head.Alg {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			lastErr = err
			continue
		}
		if err = verifySig(pub, head.Alg, []byte(parts[0]+"."+parts[1]), sig); err == nil {
			return nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return fmt.Errorf("ID token 验签失败: %w", lastErr)
	}
	return fmt.Errorf("JWKS 里没有 kid=%q 的签名公钥", head.Kid)
}

// verifySig 按算法走对应的验签原语。哈希恒 SHA-256（三种算法都是 -256 档）。
func verifySig(pub crypto.PublicKey, alg string, signing, sig []byte) error {
	sum := sha256.Sum256(signing)
	switch alg {
	case "RS256":
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("公钥类型与 RS256 不匹配")
		}
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig)
	case "PS256":
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return errors.New("公钥类型与 PS256 不匹配")
		}
		return rsa.VerifyPSS(k, crypto.SHA256, sum[:], sig, nil)
	case "ES256":
		k, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("公钥类型与 ES256 不匹配")
		}
		if len(sig) != 64 {
			return errors.New("ES256 的签名该是 64 字节的 r||s")
		}
		return verifyECDSA(k, sum[:], sig)
	}
	return fmt.Errorf("算法 %q 不支持", alg)
}

// verifyECDSA 把 JWT 的裸 r||s 翻成 Verify 要的两个大数。
func verifyECDSA(k *ecdsa.PublicKey, hash, sig []byte) error {
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(k, hash, r, s) {
		return errors.New("签名对不上")
	}
	return nil
}

// checkClaims 验声明四件套：iss / aud / exp，nonce 非空也验。scope 不在 id_token
// 里——它在 token 回包的 scope 字段上（magpie chatgpt_api.go 同判：siwcCheckID
// 只验这四件，直连 scope 验在 token 回包），登录侧拿 ScopeHasDirect 把关。
func checkClaims(claims map[string]any, issuer, clientID, nonce string, now time.Time) error {
	if iss, _ := claims["iss"].(string); strings.TrimRight(iss, "/") != strings.TrimRight(issuer, "/") {
		return fmt.Errorf("ID token 的签发方 %q 不是 %q", iss, issuer)
	}
	switch aud := claims["aud"].(type) {
	case string:
		if aud != clientID {
			return fmt.Errorf("ID token 的 aud 不是本 client（%s）", clientID)
		}
	case []any:
		found := false
		for _, a := range aud {
			if s, ok := a.(string); ok && s == clientID {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("ID token 的 aud 里没有本 client（%s）", clientID)
		}
	default:
		return errors.New("ID token 没有 aud")
	}
	if nonce != "" {
		if got, _ := claims["nonce"].(string); got != nonce {
			return errors.New("ID token 的 nonce 对不上——它不是这次登录的那一枚")
		}
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		return errors.New("ID token 没有 exp")
	}
	if now.After(time.Unix(int64(exp), 0).Add(expLeeway)) {
		return errors.New("ID token 已过期")
	}
	return nil
}
