package store

// sessions 落库的行为面（口径层 §2.10 #61）：TTL 两档滑动、停用即失效（冻结而非
// 删行）、过期即删。会话是重启后仍要活着的状态，所以这些断言全对着库查——
// 「落库」本身没有单独的用例，每一条都在证明它。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seedUser(t *testing.T, db Conn, email, role string) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO users (email, role) VALUES (?, ?)`, email, role)
	if err != nil {
		t.Fatalf("种用户: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("种用户: %v", err)
	}
	return id
}

func sessionExpiry(t *testing.T, db Queryer, token string) int64 {
	t.Helper()
	var exp int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT expires_at FROM sessions WHERE token = ?`, token).Scan(&exp); err != nil {
		t.Fatalf("读会话过期时刻: %v", err)
	}
	return exp
}

// TTL 按角色两档：admin 12h、user 30d。差两个数量级，闭区间宽一点也混不了。
func TestCreateSessionTTLByRole(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, tc := range []struct {
		role string
		want time.Duration
	}{
		{RoleAdmin, SessionTTLAdmin},
		{RoleUser, SessionTTLUser},
	} {
		uid := seedUser(t, db, tc.role+"@example.com", tc.role)
		token, ttl, err := CreateSession(ctx, db, uid)
		if err != nil {
			t.Fatalf("%s 建会话: %v", tc.role, err)
		}
		if ttl != tc.want {
			t.Errorf("%s 的 TTL = %v, 期望 %v", tc.role, ttl, tc.want)
		}
		got := sessionExpiry(t, db, token) - time.Now().Unix()
		if diff := got - int64(tc.want/time.Second); diff < -5 || diff > 5 {
			t.Errorf("%s 落库的过期时刻偏了 %d 秒，TTL 没按角色取档", tc.role, diff)
		}
	}
}

// 滑动续期：每次有效校验都把过期时刻往后推。把库里的时刻往回拨再 Touch，
// 推回去了才算滑动——不拨的话「新写的和刚才一样」也能蒙混过去。
func TestTouchSessionSlidesExpiry(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleAdmin)
	token, _, err := CreateSession(ctx, db, uid)
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	backdated := time.Now().Add(time.Hour).Unix() // 还有 1h 才过期：有效但快到了
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`, backdated, token); err != nil {
		t.Fatalf("回拨过期时刻: %v", err)
	}

	su, ok, err := TouchSession(ctx, db, token)
	if err != nil || !ok {
		t.Fatalf("TouchSession = (%v, %v)，期望有效", ok, err)
	}
	if su.ID != uid || su.Role != RoleAdmin {
		t.Errorf("会话背后的用户 = %+v，期望 id=%d role=admin", su, uid)
	}
	if got := sessionExpiry(t, db, token); got <= backdated {
		t.Errorf("Touch 之后过期时刻没往后推（%d <= %d），滑动续期丢了", got, backdated)
	}
}

// 过期会话无效且行就地删掉；空 token 与不存在的 token 也无效。
func TestTouchSessionExpiredAndUnknown(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleAdmin)
	token, _, err := CreateSession(ctx, db, uid)
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`,
		time.Now().Add(-time.Minute).Unix(), token); err != nil {
		t.Fatalf("拨到过期: %v", err)
	}
	if _, ok, err := TouchSession(ctx, db, token); err != nil || ok {
		t.Fatalf("过期会话 TouchSession = (%v, %v)，期望无效", ok, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token = ?`, token).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("过期行没被删掉")
	}
	for _, tok := range []string{"", "no-such-token"} {
		if _, ok, err := TouchSession(ctx, db, tok); err != nil || ok {
			t.Errorf("token=%q TouchSession = (%v, %v)，期望无效", tok, ok, err)
		}
	}
}

// 停用即失效、启用即恢复（#63 Q7）：校验联查 users.disabled，停用的当场踢下线；
// 冻结而不是删行，误停撤销后已发出的会话不用重登。
func TestTouchSessionFreezesDisabledUser(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleUser)
	token, _, err := CreateSession(ctx, db, uid)
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, uid); err != nil {
		t.Fatalf("停用用户: %v", err)
	}
	if _, ok, err := TouchSession(ctx, db, token); err != nil || ok {
		t.Fatalf("停用用户的会话 TouchSession = (%v, %v)，期望当场失效", ok, err)
	}
	if _, err := db.Exec(`UPDATE users SET disabled = 0 WHERE id = ?`, uid); err != nil {
		t.Fatalf("重新启用: %v", err)
	}
	if _, ok, err := TouchSession(ctx, db, token); err != nil || !ok {
		t.Errorf("解封后的会话 TouchSession = (%v, %v)，冻结应当可逆", ok, err)
	}
}

// DeleteSession 只删自己、DeleteAllSessions 一锅端；create 顺手清过期行。
func TestSessionDeletionAndSweep(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleAdmin)
	t1, _, err := CreateSession(ctx, db, uid)
	if err != nil {
		t.Fatal(err)
	}
	t2, _, err := CreateSession(ctx, db, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteSession(ctx, db, t1); err != nil {
		t.Fatalf("登出: %v", err)
	}
	if _, ok, _ := TouchSession(ctx, db, t1); ok {
		t.Error("登出后的会话还有效")
	}
	if _, ok, _ := TouchSession(ctx, db, t2); !ok {
		t.Error("登出误伤了别的会话")
	}

	// 过期行靠下一次 create 清扫：登出后再没访问过的死条目不该永远留着。
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ? WHERE token = ?`,
		time.Now().Add(-time.Minute).Unix(), t2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateSession(ctx, db, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token = ?`, t2).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("create 没清掉过期行")
	}

	if err := DeleteAllSessions(ctx, db); err != nil {
		t.Fatalf("吊销全部: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("DeleteAllSessions 后还剩 %d 行", n)
	}
}

// 给不存在的用户建会话要报 ErrNotFound，不能静默发一个挂在空号上的 token。
func TestCreateSessionUnknownUser(t *testing.T) {
	db := openTestDB(t)
	if _, _, err := CreateSession(context.Background(), db, 12345); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，得到 %v", err)
	}
}
