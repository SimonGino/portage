package store

import (
	"context"
	"database/sql"
	"errors"
)

// Conn 是 *sql.DB 与 *sql.Tx 的公共面（读 + 写）。管理端的写操作全在事务里做，
// 因此这些函数不能只认 *sql.DB——理由同 Queryer 的注释。
type Conn interface {
	Queryer
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SettingAdminPasswordHash 是管理端密码的 bcrypt 哈希在 settings 表里的键。
const SettingAdminPasswordHash = "admin_password_hash"

// 用户体系的运行期配置键（口径层 §2.10，#62 决议 7）：SMTP、OAuth client、站点外部
// URL 全落 settings 表、管理端 UI 配置、改完即生效——config.yaml 不加新键。
//
// smtp_password 与两个 client_secret 是 secret 类：同「上游 key 只存服务端」口径，
// 读接口只回「设没设」，永不回值，错误信息里也不回显。
const (
	SettingSMTPHost = "smtp_host"
	SettingSMTPPort = "smtp_port"
	// 加密三态：starttls / ssl / none（#69 选型：wneessen/go-mail 的三态映射）。
	SettingSMTPEncryption = "smtp_encryption"
	SettingSMTPUsername   = "smtp_username"
	SettingSMTPPassword   = "smtp_password"
	SettingSMTPFrom       = "smtp_from"
	// 站点外部 URL：邮件里的验证/重置链接与 OAuth 回调地址都从它拼。
	SettingSiteURL            = "site_external_url"
	SettingGitHubClientID     = "oauth_github_client_id"
	SettingGitHubClientSecret = "oauth_github_client_secret"
	SettingGoogleClientID     = "oauth_google_client_id"
	SettingGoogleClientSecret = "oauth_google_client_secret"
)

// DeleteSetting 删掉一项设置。清空 secret 类配置（比如撤掉 OAuth client）走它，
// 不留一行空串占位——GetSetting 本来就不区分「没设过」与「空串」。
func DeleteSetting(ctx context.Context, db Conn, key string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// GetSetting 返回 key 对应的值；不存在时返回空串而不是错误。
//
// 「没设过」与「设成了空串」在这里不区分：settings 的值都是非空才有意义的东西
// （密码哈希），空串本来就不是一个合法取值。
func GetSetting(ctx context.Context, db Queryer, key string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting 写入或覆盖一项设置。
func SetSetting(ctx context.Context, db Conn, key, value string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	return err
}
