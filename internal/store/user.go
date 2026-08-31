package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

// SetUserPasswordHash 改一个用户的密码哈希。settings 与第一个 admin 的两份哈希是
// 复制关系（见 EnsureFirstAdmin），改第一个 admin 的必须两处一起动——#72 起邮箱
// 登录验的是 users 这份，settings 那份只剩「回滚到旧二进制还能登」的兜底职责。
func SetUserPasswordHash(ctx context.Context, db Conn, id int64, hash string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return affectedOne(res, err)
}

// User 是管理端用户列表与会话回包用的一行，永不带 password_hash——哈希只在登录
// 校验那一条路上出现（UserAuth），列表结构里带着它就是等着哪次序列化漏出去。
type User struct {
	ID            int64  `json:"id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	Role          string `json:"role"`
	Disabled      bool   `json:"disabled"`
	EmailVerified bool   `json:"email_verified"`
	// HasPassword 区分密码账号与 OAuth-only 账号：账号页要据此说「设置密码」还是
	// 「修改密码」，列表要能看出「这个人只能走 OAuth 进来」。
	HasPassword bool `json:"has_password"`
	// MonthlyQuotaUSD 是月度限额（#65）：null = 不限额（默认），0 = 封停。
	MonthlyQuotaUSD *float64 `json:"monthly_quota_usd"`
	CreatedAt       string   `json:"created_at"`
}

// NormalizeEmail 是邮箱的统一形态：去空白、折小写。邮箱即登录标识（#61），
// 大小写不同的同一邮箱注册成两个账号，OAuth 的「同邮箱自动关联」就会失灵——
// 所以所有写入与查找都必须先过这一道，两边各自 lower 迟早漏一处。
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// CreateUser 建一个用户。passwordHash 可空（OAuth-only 账号无密码，#61）；邮箱
// 必须像个邮箱——这里只把「明显不是」的拦下，真伪由验证邮件裁决。
func CreateUser(ctx context.Context, db Conn, email string, passwordHash *string, displayName, role string, verified bool) (int64, error) {
	email = NormalizeEmail(email)
	at := strings.Index(email, "@")
	if at < 1 || at == len(email)-1 || strings.ContainsAny(email, " \t") {
		return 0, InvalidInput{"邮箱格式不对"}
	}
	if role != RoleAdmin && role != RoleUser {
		return 0, InvalidInput{"角色只有 admin / user 两档"}
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO users (email, password_hash, display_name, role, email_verified)
		VALUES (?, ?, ?, ?, ?)`, email, passwordHash, displayName, role, verified)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetUser 按 id 取一个用户（不带哈希）。会话背后的人、token 背后的人都从这儿取。
func GetUser(ctx context.Context, db Queryer, id int64) (User, error) {
	var u User
	var hash sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT id, email, display_name, role, disabled, email_verified, password_hash, monthly_quota_usd, created_at
		FROM users WHERE id = ?`, id).Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Disabled, &u.EmailVerified, &hash, &u.MonthlyQuotaUSD, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	u.HasPassword = hash.Valid && hash.String != ""
	return u, err
}

// UserAuth 是登录校验要的那几列：除了 User 的公开面，多一份哈希。
type UserAuth struct {
	User
	// PasswordHash 为 nil 即 OAuth-only 账号——它与「哈希是空串」必须分得开，
	// 理由见 schema.sql 那一列的注释。
	PasswordHash *string
}

// GetUserAuthByEmail 按邮箱取登录校验面。找不到报 ErrNotFound——调用方要把它与
// 「密码错」折成同一句话回给客户端，这里不替它折。
func GetUserAuthByEmail(ctx context.Context, db Queryer, email string) (UserAuth, error) {
	return getUserAuth(ctx, db, `email = ?`, NormalizeEmail(email))
}

// GetUserAuthByID 按 id 取登录校验面：改密码要验本人的旧密码，会话里只有 id。
func GetUserAuthByID(ctx context.Context, db Queryer, id int64) (UserAuth, error) {
	return getUserAuth(ctx, db, `id = ?`, id)
}

func getUserAuth(ctx context.Context, db Queryer, where string, arg any) (UserAuth, error) {
	var u UserAuth
	var hash sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT id, email, display_name, role, disabled, email_verified, password_hash, created_at
		FROM users WHERE `+where, arg).Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Disabled, &u.EmailVerified, &hash, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UserAuth{}, ErrNotFound
	}
	if err != nil {
		return UserAuth{}, err
	}
	if hash.Valid && hash.String != "" {
		u.PasswordHash = &hash.String
		u.HasPassword = true
	}
	return u, nil
}

// ListUsers 列全部用户，按 id 序（第一个 admin 恒在最上面，它是有身份的角色）。
func ListUsers(ctx context.Context, db Queryer) ([]User, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, email, display_name, role, disabled, email_verified, password_hash, monthly_quota_usd, created_at
		FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var hash sql.NullString
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Disabled,
			&u.EmailVerified, &hash, &u.MonthlyQuotaUSD, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.HasPassword = hash.Valid && hash.String != ""
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetUserDisplayName 改展示名（#76 账号页）。展示名是纯显示字段，空串合法——
// 清空即回退到「显示邮箱」。
func SetUserDisplayName(ctx context.Context, db Conn, id int64, name string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE users SET display_name = ? WHERE id = ?`, strings.TrimSpace(name), id)
	return affectedOne(res, err)
}

// SetEmailVerified 把用户标成已验证。验证是单向的：没有反向接口，改邮箱重验是
// v1 之外的事。
func SetEmailVerified(ctx context.Context, db Conn, id int64) error {
	res, err := db.ExecContext(ctx,
		`UPDATE users SET email_verified = 1 WHERE id = ?`, id)
	return affectedOne(res, err)
}

// DeleteUserSessions 吊销**一个用户**的全部会话。重置密码成功后调它（#62 决议 6）；
// 改自己密码也走它——多用户之后 DeleteAllSessions 会把无辜的人一起踢下线。
func DeleteUserSessions(ctx context.Context, db Conn, userID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// lastAdminGuard 是任免/停用共用的守门条件：目标是**最后一个启用的 admin** 时不放行。
// 写成 UPDATE 的 WHERE 而不是先查后改——判定与写入同一条语句，两个并发降级不会
// 各自查到「还有俩」然后一起成功（连接池是 1，但正确性不该依赖那个配置，同
// ConsumeInviteCode）。
const lastAdminGuard = `NOT (
	role = 'admin' AND disabled = 0
	AND (SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0) = 1
)`

// SetUserRole 任免角色（#61：admin 可在管理端任免，多 admin 允许）。
//
// 降级最后一个启用的 admin 被守门条件拦下：治理面只有 admin 进得来，最后一个
// 降掉等于把整套用户治理锁死——没有人再能生成邀请码、建号、改配置。升级不受限。
func SetUserRole(ctx context.Context, db Conn, id int64, role string) error {
	if role != RoleAdmin && role != RoleUser {
		return InvalidInput{"角色只有 admin / user 两档"}
	}
	guard := ``
	if role == RoleUser {
		guard = ` AND ` + lastAdminGuard
	}
	res, err := db.ExecContext(ctx,
		`UPDATE users SET role = ? WHERE id = ?`+guard, role, id)
	return explainGuarded(ctx, db, res, err, id, "不能降级最后一个启用的管理员——先把别人升上来")
}

// SetUserDisabled 停用/启用（#61：停用即对系统一切访问冻结）。
//
// 只改标志不删会话：TouchSession 联查 users.disabled，已发出的 cookie 当场失效；
// 冻结可逆，重新启用后老会话照旧能用（同 #71 的裁定）。停用最后一个启用的 admin
// 与降级同罪，被同一个守门条件拦下。
func SetUserDisabled(ctx context.Context, db Conn, id int64, disabled bool) error {
	guard := ``
	if disabled {
		guard = ` AND ` + lastAdminGuard
	}
	res, err := db.ExecContext(ctx,
		`UPDATE users SET disabled = ? WHERE id = ?`+guard, disabled, id)
	return explainGuarded(ctx, db, res, err, id, "不能停用最后一个启用的管理员——先把别人升上来")
}

// explainGuarded 把「带守门条件的 UPDATE 改了 0 行」翻成能给人看的错：行不存在是
// ErrNotFound，行在但被守门条件拦了是 InvalidInput（同 RevokeInviteCode 的拆法）。
func explainGuarded(ctx context.Context, db Conn, res sql.Result, err error, id int64, blocked string) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	var one int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return InvalidInput{blocked}
}
