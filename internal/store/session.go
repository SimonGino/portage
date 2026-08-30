package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// 会话 TTL 两档滑动（口径层 §2.10，#61）：一直在用就不掉线，搁久了要重登。
// admin 12 小时维持 §2.7 的旧口径；user 30 天是面板的常驻形态（#75 落面板，
// 档位先定在这儿——TTL 是 session 的语义，不是页面的）。
const (
	SessionTTLAdmin = 12 * time.Hour
	SessionTTLUser  = 30 * 24 * time.Hour
)

// sessionTTL 按角色取档。认不得的取值当 user：短的那档是给治理面的特权收紧的，
// 一个手写 SQL 灌出来的怪角色不该反而拿到它。
func sessionTTL(role string) time.Duration {
	if role == RoleAdmin {
		return SessionTTLAdmin
	}
	return SessionTTLUser
}

// CreateSession 给用户发一个新会话，返回 token 与它的 TTL（调用方拿去设 cookie 的
// MaxAge，两处必须是同一个数）。
//
// 32 字节 crypto/rand：token 是唯一的凭据，必须猜不出来。rand.Read 在现代 Go 上
// 不会失败，真失败了也只能是熵源坏了——那时宁可让登录报错，也不能退化成弱随机。
//
// 顺手清掉全部过期行，沿旧内存实现把 sweep 挂在 create 上的立论：会话表最多几条，
// 为它常驻一个 goroutine 不值当；而只在校验里删的话，登录后再没访问过的死条目
// 会一直留着。
func CreateSession(ctx context.Context, db Conn, userID int64) (string, time.Duration, error) {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix()); err != nil {
		return "", 0, err
	}
	var role string
	err := db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, err
	}
	token := hex.EncodeToString(buf)
	ttl := sessionTTL(role)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, time.Now().Add(ttl).Unix()); err != nil {
		return "", 0, err
	}
	return token, ttl, nil
}

// SessionUser 是一次有效会话背后的人——校验联查 users 顺手就把身份带出来了，
// 权限按角色判的调用方（#75 的两空间）不用再查一趟。
type SessionUser struct {
	ID   int64
	Role string
}

// TouchSession 判 token 是否有效，有效则滑动续期并返回背后的用户。
//
// 校验联查 users.disabled（#61，#63 Q7）：停用即对系统一切访问冻结，session 当场
// 失效。冻结而不是删行——停用是可逆的（v1 没有删用户），重新启用后已发出的会话
// 照旧能用；删了行则解封还得逐个重登，而「误停一个人」正是最需要平滑撤销的操作。
//
// 过期行就地删掉：过期不可逆，留着只是垃圾。SELECT 与 UPDATE 不包事务——连接池
// 是 1，两次并发校验最坏是各续一次期，结果一样。
func TouchSession(ctx context.Context, db Conn, token string) (SessionUser, bool, error) {
	if token == "" {
		return SessionUser{}, false, nil
	}
	var su SessionUser
	var disabled bool
	var exp int64
	err := db.QueryRowContext(ctx, `
		SELECT s.user_id, u.role, u.disabled, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ?`, token).Scan(&su.ID, &su.Role, &disabled, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionUser{}, false, nil
	}
	if err != nil {
		return SessionUser{}, false, err
	}
	if exp <= time.Now().Unix() {
		_, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
		return SessionUser{}, false, err
	}
	if disabled {
		return SessionUser{}, false, nil
	}
	if _, err := db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE token = ?`,
		time.Now().Add(sessionTTL(su.Role)).Unix(), token); err != nil {
		return SessionUser{}, false, err
	}
	return su, true, nil
}

// DeleteSession 登出：删这一个会话。token 不存在也算成功——登出的目的状态就是
// 「这个 token 不再有效」，它本来就无效时目的已达成。
func DeleteSession(ctx context.Context, db Conn, token string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// DeleteAllSessions 吊销全部会话。改密码之后调它：不然改密码只挡住了「还没登录的
// 人」，已经拿着 cookie 的那个浏览器照旧能用——而改密码的常见动机恰恰是「怕别人
// 还在用」。落库之后它也接过了旧内存版「重启即全吊销」的补救职责。
func DeleteAllSessions(ctx context.Context, db Conn) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sessions`)
	return err
}
