package store

// OAuth 身份（#62 决议 4）：provider + provider_user_id → user。这里只有映射的
// 增删查——上游怎么认证、邮箱怎么核对是 admin 包 oauth 那一侧的事，store 不掺和。

import (
	"context"
	"database/sql"
	"errors"
)

// OAuthIdentity 是账号设置里「已绑定」列表的一行。不带 provider_user_id——
// 上游的数字 id 对人没有意义，页面只需要知道「绑了哪家、什么时候绑的」。
type OAuthIdentity struct {
	Provider  string `json:"provider"`
	CreatedAt string `json:"created_at"`
}

// FindOAuthUser 按上游身份找用户。没绑过返回 (0, false, nil)——「没绑过」是 OAuth
// 首登的正常一态（进完成注册页），不是错误。
func FindOAuthUser(ctx context.Context, db Queryer, provider, providerUserID string) (int64, bool, error) {
	var userID int64
	err := db.QueryRowContext(ctx, `
		SELECT user_id FROM oauth_identities
		WHERE provider = ? AND provider_user_id = ?`, provider, providerUserID).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return userID, err == nil, err
}

// LinkOAuthIdentity 把一个上游身份挂到用户名下。两条 UNIQUE（同一上游账号不挂
// 两人、同一用户同家 provider 不挂两个上游账号）由表约束把守，冲突原样上抛，
// 调用方翻成能给人看的话。
func LinkOAuthIdentity(ctx context.Context, db Conn, userID int64, provider, providerUserID string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO oauth_identities (provider, provider_user_id, user_id)
		VALUES (?, ?, ?)`, provider, providerUserID, userID)
	return err
}

// UnlinkOAuthIdentity 解绑一个 provider。没绑过报 ErrNotFound。
func UnlinkOAuthIdentity(ctx context.Context, db Conn, userID int64, provider string) error {
	res, err := db.ExecContext(ctx,
		`DELETE FROM oauth_identities WHERE user_id = ? AND provider = ?`, userID, provider)
	return affectedOne(res, err)
}

// ListOAuthIdentities 列一个用户绑定的全部上游身份。
func ListOAuthIdentities(ctx context.Context, db Queryer, userID int64) ([]OAuthIdentity, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT provider, created_at FROM oauth_identities
		WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthIdentity{}
	for rows.Next() {
		var oi OAuthIdentity
		if err := rows.Scan(&oi.Provider, &oi.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, oi)
	}
	return out, rows.Err()
}
