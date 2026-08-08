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
