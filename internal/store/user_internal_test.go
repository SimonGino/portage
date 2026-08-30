package store

// 多用户迁移的幂等三态（展开层 §7.10.1 第一条，#71 只覆盖第 ② 步造 admin；
// 认领与回填是 #73/#74 的事，届时补进同一组三态）。迁移是否跑过全看库里的状态，
// 所以每个用例都对同一个文件反复 Open——第二次、第三次 Open 就是「重启」。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// reopen 关掉旧连接、对同一个文件再跑一遍 schema + migrate——模拟一次重启。
func reopen(t *testing.T, db *sql.DB, path string) *sql.DB {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("关库: %v", err)
	}
	nd, err := Open(path)
	if err != nil {
		t.Fatalf("重开库: %v", err)
	}
	t.Cleanup(func() { nd.Close() })
	return nd
}

func countUsers(t *testing.T, db Queryer, role string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM users WHERE role = ?`, role).Scan(&n); err != nil {
		t.Fatalf("数用户: %v", err)
	}
	return n
}

// 态一：空库。users/sessions 两张表要在，但一行都不该有——migration 只加表。
func TestMigrateEmptyDBCreatesTablesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db = reopen(t, db, path) // 再跑一遍也一样：幂等

	for _, table := range []string{"users", "sessions"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("表 %s 不存在或查不动: %v", table, err)
		}
		if n != 0 {
			t.Errorf("空库上 %s 表有 %d 行，migration 不该造任何行", table, n)
		}
	}
}

// 态二：只有 admin_password_hash 的旧库。重启（= 重跑 migrate）要造出第一个 admin：
// 搬 hash、占位邮箱 admin@localhost、email_verified 视为真、配额 NULL（不限额）。
// 再重启不造第二个——幂等靠「有 admin 就不造」这一判断本身。
func TestMigrateSeedsFirstAdminFromSettingsHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	const hash = "$2a$10$fakedhashfortest"
	if err := SetSetting(ctx, db, SettingAdminPasswordHash, hash); err != nil {
		t.Fatalf("写密码哈希: %v", err)
	}
	db = reopen(t, db, path)

	var email, gotHash string
	var verified, disabled bool
	var quota sql.NullFloat64
	err = db.QueryRow(`SELECT email, password_hash, email_verified, disabled, monthly_quota_usd
		FROM users WHERE role = 'admin'`).Scan(&email, &gotHash, &verified, &disabled, &quota)
	if err != nil {
		t.Fatalf("第一个 admin 没造出来: %v", err)
	}
	if email != FirstAdminEmail {
		t.Errorf("email = %q, 期望占位邮箱 %q", email, FirstAdminEmail)
	}
	if gotHash != hash {
		t.Errorf("password_hash 没搬过来：got %q", gotHash)
	}
	if !verified {
		t.Error("email_verified 应视为真——SMTP 未配前 admin 必须能进")
	}
	if disabled {
		t.Error("新造的 admin 不该是停用的")
	}
	if quota.Valid {
		t.Errorf("monthly_quota_usd = %v, 期望 NULL（不限额是默认；0 是封停，绝不能拿它当默认）", quota.Float64)
	}

	db = reopen(t, db, path)
	if n := countUsers(t, db, RoleAdmin); n != 1 {
		t.Errorf("重启后 admin 用户 %d 个，幂等要求恰好 1 个", n)
	}
}

// 已有 admin（哪怕是后来改了邮箱/密码的）就不再造号：admin_password 的语义是
// 「仅在无 admin 用户时初始化」，有号之后配置与 settings 里的哈希都不再长出新号。
func TestMigrateSkipsWhenAdminExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := SetSetting(ctx, db, SettingAdminPasswordHash, "some-hash"); err != nil {
		t.Fatalf("写密码哈希: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (email, password_hash, role, email_verified)
		VALUES ('me@example.com', 'other-hash', 'admin', 1)`); err != nil {
		t.Fatalf("预置 admin: %v", err)
	}
	db = reopen(t, db, path)

	if n := countUsers(t, db, RoleAdmin); n != 1 {
		t.Errorf("已有 admin 的库重启后 admin 用户 %d 个，不该多造", n)
	}
}

// 态三：无 admin 的声明形态库（有业务配置、没有密码哈希）。②在这种库上必须空转：
// users 不长行，业务配置一字不动——纯转发/声明形态零负担的横向约束。
func TestMigrateNoopOnDBWithoutAdminHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO channels (name, base_url_anthropic) VALUES ('ch', 'https://x')`); err != nil {
		t.Fatalf("预置渠道: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (name, key_hash) VALUES ('k', 'h')`); err != nil {
		t.Fatalf("预置 key: %v", err)
	}
	db = reopen(t, db, path)

	var users, channels, keys int
	if err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM users),
		(SELECT COUNT(*) FROM channels),
		(SELECT COUNT(*) FROM api_keys)`).Scan(&users, &channels, &keys); err != nil {
		t.Fatalf("清点: %v", err)
	}
	if users != 0 {
		t.Errorf("无哈希的库上造出了 %d 个用户，②应当空转", users)
	}
	if channels != 1 || keys != 1 {
		t.Errorf("业务配置被动过了：channels=%d keys=%d，migration 只该加表", channels, keys)
	}
}

// EnsureFirstAdmin 与 HasAdminUser / FirstAdminID 的直接面：Bootstrap 那条路
// （新库先写 hash 再造号）不经过 migrate，走的就是这几个函数。
func TestEnsureFirstAdminDirect(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if has, err := HasAdminUser(ctx, db); err != nil || has {
		t.Fatalf("空库 HasAdminUser = (%v, %v)，期望 (false, nil)", has, err)
	}
	if _, err := FirstAdminID(ctx, db); err == nil {
		t.Fatal("空库 FirstAdminID 应报 ErrNotFound")
	}
	if made, err := EnsureFirstAdmin(ctx, db); err != nil || made {
		t.Fatalf("无哈希时 EnsureFirstAdmin = (%v, %v)，期望空转", made, err)
	}
	if err := SetSetting(ctx, db, SettingAdminPasswordHash, "h"); err != nil {
		t.Fatalf("写哈希: %v", err)
	}
	if made, err := EnsureFirstAdmin(ctx, db); err != nil || !made {
		t.Fatalf("有哈希无 admin 时 EnsureFirstAdmin = (%v, %v)，期望造号", made, err)
	}
	if made, err := EnsureFirstAdmin(ctx, db); err != nil || made {
		t.Fatalf("已有 admin 时 EnsureFirstAdmin = (%v, %v)，期望空转", made, err)
	}
	id, err := FirstAdminID(ctx, db)
	if err != nil || id == 0 {
		t.Fatalf("FirstAdminID = (%d, %v)", id, err)
	}
}
