// Package auth resolves the gateway API key a client presented on a forwarding
// request. It owns the hash algorithm and the api_keys lookup; the gin
// middleware that uses it lives in internal/server.
//
// 拆这一刀的理由：中间件要写协议原生的 401、要往调用日志里塞 key 名，两件事都长在
// `internal/server` 的类型上（`protocol.Endpoint`、`callRecord`），搬过来会让 auth
// 反向依赖 server。留在这里的是能脱开 HTTP 单测的部分：hash、取头、查库。
// 展开层 §3 把本包写作「API key 中间件」，实际中间件在 server/auth.go，此处记一笔。
package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// ErrUnauthorized 是「没出示 key」与「出示的 key 不认」的**同一个**错误。
//
// 刻意不分家：区分开就等于告诉扫描者「这个 key 存在，只是停用了」，而调用方唯一
// 该做的事（回 401、不说为什么）两种情况完全一样。
var ErrUnauthorized = errors.New("unauthorized")

// Key is one row of api_keys, reduced to what the relay path needs.
type Key struct {
	Name string
	// AllowedModels 是模型白名单，逗号分隔，`*` 表示不限。M1 时这一列只建不校验，
	// M3 管理端能配了之后同期启用校验（PO 于 M3 裁定，兑现 v0.27 的「能配了再启用」）。
	AllowedModels string
}

// Allows 判断这把 key 能不能用某个模型名。
//
// 比的就是客户端请求里那个 model 字符串本身，接入点名和纳管模型限定名
// （`渠道名/纳管模型名`）都可以写在白名单里——两种名都能路由（口径层 v0.32），
// 白名单只管一种就等于留了条绕过去的路。`*` 与空串都表示不限：空串出现在手写 SQL
// 漏填的行上，那种情况按「没设限制」处理，而不是把这把 key 锁死。
func (k Key) Allows(model string) bool {
	list := strings.TrimSpace(k.AllowedModels)
	if list == "" || list == "*" {
		return true
	}
	for item := range strings.SplitSeq(list, ",") {
		if item := strings.TrimSpace(item); item == "*" || item == model {
			return true
		}
	}
	return false
}

// Hash 是 key_hash 的算法：SHA-256 裸哈希，小写十六进制，不加盐。
//
// 理由见展开层 §7.1：key 是网关自生成的高熵随机串而非人选密码，字典攻击不成立；
// 而鉴权每个转发请求都要走一遍，要吃 key_hash 上的唯一索引精确匹配，加盐则 hash
// 不可索引、须扫全表逐行比。
func Hash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// Presented 列出客户端在请求头里出示的候选 key，按 x-api-key、Authorization: Bearer
// 的顺序，去掉空值。
//
// 两个头都收、且**逐个试**而不是只认第一个非空的：harness 常常两个头一起发
// （M0 relay_test 实测），Claude Code 的 ANTHROPIC_API_KEY 与 ANTHROPIC_AUTH_TOKEN
// 会分别落到这两个头上。只认第一个的话，一个头里留着过期值、正确的 key 在另一个头
// 里，表现就是莫名其妙的 401。代价是失败路径最多两次带索引的查询。
func Presented(h http.Header) []string {
	var out []string
	if v := strings.TrimSpace(h.Get("x-api-key")); v != "" {
		out = append(out, v)
	}
	if v := strings.TrimSpace(h.Get("Authorization")); v != "" {
		if rest, ok := cutBearer(v); ok && rest != "" {
			out = append(out, rest)
		}
	}
	return out
}

// cutBearer 剥掉 Bearer 前缀，大小写不敏感——RFC 7235 的 auth-scheme 就是不敏感的，
// 各家 harness 里 `Bearer` / `bearer` 都见得到。
func cutBearer(v string) (string, bool) {
	const scheme = "bearer "
	if len(v) < len(scheme) || !strings.EqualFold(v[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(v[len(scheme):]), true
}

// Resolve 认出请求出示的 key。任何一把认得且未停用即通过；都不认返回 ErrUnauthorized。
//
// 返回的 error 除 ErrUnauthorized 外只可能是库错误——调用方要把两者分开：前者 401，
// 后者 500。把库故障当成 401 会让一次 SQLite 抖动看起来像「你的 key 不对了」。
func Resolve(ctx context.Context, db *sql.DB, h http.Header) (Key, error) {
	for _, plain := range Presented(h) {
		var k Key
		err := db.QueryRowContext(ctx,
			`SELECT name, allowed_models FROM api_keys WHERE key_hash = ? AND disabled = 0`,
			Hash(plain)).Scan(&k.Name, &k.AllowedModels)
		switch {
		case err == nil:
			return k, nil
		case errors.Is(err, sql.ErrNoRows):
			continue
		default:
			return Key{}, err
		}
	}
	return Key{}, ErrUnauthorized
}
