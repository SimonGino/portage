package store

// 一次性动作 token（#62 决议 2/5/6）：邮箱验证、重置密码、OAuth 完成注册、OAuth 绑定。
// TTL 常量收在这里；表结构见 schema.sql 的 auth_tokens。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// token 的用途。消费必须带用途——同一张表存权限完全不同的凭据，
// 不带用途的消费等于让验证邮件里的链接能重置密码。
const (
	TokenVerifyEmail   = "verify_email"
	TokenResetPassword = "reset_password"
	TokenOAuthSignup   = "oauth_signup"
	// TokenOAuthLink 是账号页「绑定」的 start → callback 接力：回调是从上游跳回来
	// 的跨站导航，SameSite=Strict 的会话 cookie 在那一跳上不发，「绑给谁」只能在
	// start 时（同站请求，会话还读得到）就签进服务端一次性令牌里。
	TokenOAuthLink = "oauth_link"
)

// TTL（口径层 §2.10）：验证链接 24h、重置链接 30min。OAuth 完成注册那一档口径
// 没定数——它只是回调页与填码页之间的接力棒，够填完一个邀请码即可，给 15 分钟。
// 绑定接力与装 state 的接力 cookie 同寿（10 分钟）。
const (
	TokenTTLVerifyEmail   = 24 * time.Hour
	TokenTTLResetPassword = 30 * time.Minute
	TokenTTLOAuthSignup   = 15 * time.Minute
	TokenTTLOAuthLink     = 10 * time.Minute
)

// CreateAuthToken 发一个一次性 token。userID 可空（OAuth 完成注册时用户还不存在），
// payload 是用途自带的负载。顺手清掉全部过期行，理由同 CreateSession 的 sweep。
func CreateAuthToken(ctx context.Context, db Conn, purpose string, userID *int64, payload string, ttl time.Duration) (string, error) {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM auth_tokens WHERE expires_at <= ?`, time.Now().Unix()); err != nil {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO auth_tokens (token, purpose, user_id, payload, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		token, purpose, userID, payload, time.Now().Add(ttl).Unix()); err != nil {
		return "", err
	}
	return token, nil
}

// PeekAuthToken 读一个 token 的负载但**不消费**。OAuth 完成注册页加载时要显示
// 「拿到的邮箱是什么」，那一眼不该把 token 烧掉——烧掉的话填错一次邀请码就得
// 整个重走 OAuth。无效或过期一律 ErrNotFound。
func PeekAuthToken(ctx context.Context, db Queryer, token, purpose string) (userID *int64, payload string, err error) {
	var exp int64
	err = db.QueryRowContext(ctx, `
		SELECT user_id, payload, expires_at FROM auth_tokens
		WHERE token = ? AND purpose = ?`, token, purpose).Scan(&userID, &payload, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if exp <= time.Now().Unix() {
		return nil, "", ErrNotFound
	}
	return userID, payload, nil
}

// ConsumeAuthToken 消费一个 token：读出负载并删行，一次性就是这一删。
//
// 先删后判行数而不是先查后删：删掉恰好一行即消费成功，两个并发消费只有一个能赢，
// 「一次性」不靠调用方自觉。过期行可能还没被 sweep 扫走，删完还要再验一次 expires_at
// ——所以这里是 RETURNING 一步拿回负载顺便判过期。
func ConsumeAuthToken(ctx context.Context, db Conn, token, purpose string) (userID *int64, payload string, err error) {
	var exp int64
	err = db.QueryRowContext(ctx, `
		DELETE FROM auth_tokens WHERE token = ? AND purpose = ?
		RETURNING user_id, payload, expires_at`, token, purpose).Scan(&userID, &payload, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if exp <= time.Now().Unix() {
		return nil, "", ErrNotFound
	}
	return userID, payload, nil
}

// LastAuthTokenIssue 返回某人某用途最近一次发 token 的 unix 秒，一次都没发过是 0。
// 重发 60s 冷却（#62 决议 2）靠它判——冷却看的是「上次发是什么时候」，跟那个 token
// 还活着没有无关。换算在 SQL 里做，不依赖驱动解析 DATETIME 文本（同 ListExposedModels）。
func LastAuthTokenIssue(ctx context.Context, db Queryer, purpose string, userID int64) (int64, error) {
	var last int64
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(CAST(strftime('%s', created_at) AS INTEGER)), 0)
		FROM auth_tokens WHERE purpose = ? AND user_id = ?`, purpose, userID).Scan(&last)
	return last, err
}

// DeleteAuthTokens 删掉某人某用途的全部 token。重置密码成功后调它把同用途的
// 兄弟链接一并作废——旧邮件里那条链接不该还能再改一次密码。
func DeleteAuthTokens(ctx context.Context, db Conn, purpose string, userID int64) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM auth_tokens WHERE purpose = ? AND user_id = ?`, purpose, userID)
	return err
}
