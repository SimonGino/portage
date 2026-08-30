package store

import (
	"context"
	"database/sql"
	"errors"
)

// 用户角色两档（口径层 §2.10，#61）：admin 治理面、user 自用面。单列可任免。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// FirstAdminEmail 是 migration 造第一个 admin 时的占位邮箱（#61）。登录后可补真邮箱；
// 造号时 email_verified 视为真——admin 账号豁免邮箱验证，SMTP 未配前 admin 必须能进。
const FirstAdminEmail = "admin@localhost"

// EnsureFirstAdmin 在「settings 有 admin_password_hash 且库中无 admin 用户」时造第一个
// admin（搬 hash、占位邮箱、豁免验证），返回这次有没有造。其余情况一律空转——这正是
// 横向约束要的形状：无 admin 的库（纯转发、声明形态）上它一行都不写。
//
// 幂等靠「有 admin 就不造」这一判断本身，与 migrate 的探列式同一精神：跑没跑过直接
// 问库里的状态，不另设标记。config 的 admin_password 语义因此不变——它经 Bootstrap
// 落成 settings 里的 hash，再由这里在无 admin 用户时初始化造号，此后再改配置不生效。
//
// hash 是**复制**不是搬走：settings 里那份留着，#71 阶段管理端登录仍验它（users 侧
// 成为登录事实源是 #72 的事），删掉会让回滚到旧二进制的库当场登不进去。
func EnsureFirstAdmin(ctx context.Context, db Conn) (bool, error) {
	hash, err := GetSetting(ctx, db, SettingAdminPasswordHash)
	if err != nil || hash == "" {
		return false, err
	}
	has, err := HasAdminUser(ctx, db)
	if err != nil || has {
		return false, err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO users (email, password_hash, role, email_verified)
		VALUES (?, ?, ?, 1)`, FirstAdminEmail, hash, RoleAdmin)
	if err != nil {
		return false, err
	}
	return true, nil
}

// HasAdminUser 问库里有没有 admin 用户。它是挂载闸的另一半判据（#61：「库里存在
// admin 用户 或 配了 admin_password → 挂载」）。停用的 admin 也算——停用管的是
// 这个人能不能进，不是管理面这一形态存不存在。
func HasAdminUser(ctx context.Context, db Queryer) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM users WHERE role = ? LIMIT 1`, RoleAdmin).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// FirstAdminID 返回第一个 admin 的 id（按 id 序），没有则 ErrNotFound。
//
// 「第一个 admin」是个有身份的角色：migration 造号归它、无主 key 认领归它（#66，
// 落在 #73）、老流水回填归它（#64，落在 #74）。取法收在这一处，三票不各查各的。
func FirstAdminID(ctx context.Context, db Queryer) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE role = ? ORDER BY id LIMIT 1`, RoleAdmin).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// SetUserPasswordHash 改一个用户的密码哈希。#71 阶段唯一的调用方是管理端改密码——
// settings 与第一个 admin 的两份哈希是复制关系（见 EnsureFirstAdmin），改一份不改
// 另一份会让 #72 的邮箱登录拿着旧密码，两处必须一起动。
func SetUserPasswordHash(ctx context.Context, db Conn, id int64, hash string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return affectedOne(res, err)
}
