package store

// api_keys 多用户化的迁移用例（展开层 §7.10.1，#73）：重建表前后行字节不变、
// 同名跨用户共存、key_hash 唯一不变；无主认领的幂等三态（有 admin 认领 / 无 admin
// 空转 / 撞名拒启）。与 user_internal_test.go 同一套打法：对同一个文件反复 Open，
// 第二次 Open 就是「重启」。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// openOldShapeDB 造一个 #73 之前的库：api_keys 还是全局 UNIQUE(name)、没有 user_id。
// 用手写 DDL 而不是老版本二进制——迁移认的是形状不是版本号（hasColumn），形状对上
// 就是那个老库。返回插好的两行的字节快照。
func openOldShapeDB(t *testing.T, path string) map[int64][]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开老库: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE api_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		key_hash TEXT NOT NULL UNIQUE,
		key_plain TEXT NOT NULL DEFAULT '',
		allowed_models TEXT NOT NULL DEFAULT '*',
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("建老 api_keys: %v", err)
	}
	// id 故意留洞（2 与 7）：重建搬行要保 id，连着编的行验不出「悄悄重编号」。
	for _, row := range [][]any{
		{int64(2), "笔记本", "hash-2", "sk-ptg-aa", "*", 0, "2026-01-02 03:04:05"},
		{int64(7), "ci", "hash-7", "", "claude-x,gpt-y", 1, "2026-02-03 04:05:06"},
	} {
		if _, err := db.Exec(`INSERT INTO api_keys
			(id, name, key_hash, key_plain, allowed_models, disabled, created_at)
			VALUES (?,?,?,?,?,?,?)`, row...); err != nil {
			t.Fatalf("灌老行: %v", err)
		}
	}
	return snapshotKeys(t, db)
}

// snapshotKeys 把 api_keys 的老七列读成 id → 各列文本，供字节比对。
func snapshotKeys(t *testing.T, db Queryer) map[int64][]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT id, name, key_hash, key_plain, allowed_models, disabled, created_at
		FROM api_keys ORDER BY id`)
	if err != nil {
		t.Fatalf("读 api_keys: %v", err)
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		cols := make([]string, 6)
		if err := rows.Scan(&id, &cols[0], &cols[1], &cols[2], &cols[3], &cols[4], &cols[5]); err != nil {
			t.Fatalf("扫行: %v", err)
		}
		out[id] = cols
	}
	return out
}

func TestMigrateRebuildsAPIKeysKeepingRowsByteForByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	before := openOldShapeDB(t, path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("迁移开库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db = reopen(t, db, path) // 重建是一次性的：第二次 Open 探到 user_id 即跳过

	after := snapshotKeys(t, db)
	if len(after) != len(before) {
		t.Fatalf("重建后 %d 行，重建前 %d 行", len(after), len(before))
	}
	for id, want := range before {
		got, ok := after[id]
		if !ok {
			t.Fatalf("id=%d 在重建后消失了——保 id 是重建表的硬要求", id)
		}
		for i, w := range want {
			if got[i] != w {
				t.Errorf("id=%d 第 %d 列重建前后不同：%q → %q", id, i, w, got[i])
			}
		}
		// 无 admin 的库上认领空转：user_id 停在 NULL。
		var owner sql.NullInt64
		if err := db.QueryRow(`SELECT user_id FROM api_keys WHERE id = ?`, id).Scan(&owner); err != nil {
			t.Fatalf("读 user_id: %v", err)
		}
		if owner.Valid {
			t.Errorf("id=%d 的 user_id = %d，无 admin 的库上该悬空（NULL）", id, owner.Int64)
		}
	}
}

func TestRebuiltAPIKeysConstraints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	openOldShapeDB(t, path)
	db, err := Open(path)
	if err != nil {
		t.Fatalf("迁移开库: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	var u1, u2 int64
	for i, email := range []string{"a@x", "b@x"} {
		res, err := db.Exec(`INSERT INTO users (email) VALUES (?)`, email)
		if err != nil {
			t.Fatalf("种用户: %v", err)
		}
		id, _ := res.LastInsertId()
		if i == 0 {
			u1 = id
		} else {
			u2 = id
		}
	}
	// 同名跨用户共存（UNIQUE 从全局改 (user_id, name)）……
	for _, ins := range []struct {
		hash string
		user int64
	}{{"h-u1", u1}, {"h-u2", u2}} {
		if _, err := db.Exec(`INSERT INTO api_keys (name, key_hash, user_id) VALUES ('同名', ?, ?)`,
			ins.hash, ins.user); err != nil {
			t.Fatalf("跨用户同名该共存，插不进去: %v", err)
		}
	}
	// ……但同一个用户名下不行。
	if _, err := db.Exec(
		`INSERT INTO api_keys (name, key_hash, user_id) VALUES ('同名', 'h-u1b', ?)`, u1); err == nil {
		t.Error("同一用户下同名居然插进去了——UNIQUE(user_id, name) 没生效")
	}
	// 无主行的名字也唯一（UNIQUE 对 NULL 逐行视为不同，这半靠 partial index）：
	// 名字是流水归因键与声明文件的自然键，无主撞名会把两处一起废掉。
	if _, err := db.Exec(
		`INSERT INTO api_keys (name, key_hash) VALUES ('笔记本', 'h-orphan')`); err == nil {
		t.Error("无主 key 撞名居然插进去了——idx_api_keys_unowned_name 没生效")
	}
	// key_hash 仍全局唯一：转发热路径按它查，跨用户也不许撞。
	if _, err := db.Exec(
		`INSERT INTO api_keys (name, key_hash, user_id) VALUES ('别名', 'hash-2', ?)`, u2); err == nil {
		t.Error("key_hash 撞值居然插进去了——全局 UNIQUE 丢了")
	}
}

// 有 admin 的库：重启认领全部无主 key 归第一个 admin，且幂等——再启动一次不多写
// 一行；认领后新灌的无主 key 下次重启也被接走。
func TestMigrateClaimsOrphanKeysForFirstAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := SetSetting(ctx, db, SettingAdminPasswordHash, "$2a$10$fake"); err != nil {
		t.Fatalf("写密码哈希: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (name, key_hash) VALUES ('laptop', 'h1'), ('ci', 'h2')`); err != nil {
		t.Fatalf("灌无主 key: %v", err)
	}

	db = reopen(t, db, path)
	adminID, err := FirstAdminID(ctx, db)
	if err != nil {
		t.Fatalf("造完 admin 却找不到: %v", err)
	}
	assertOwners := func() {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE user_id = ?`, adminID).Scan(&n); err != nil {
			t.Fatalf("数认领结果: %v", err)
		}
		if n != 2 {
			t.Fatalf("第一个 admin 名下 %d 把 key，期望 2（无主认领）", n)
		}
	}
	assertOwners()
	db = reopen(t, db, path) // 幂等：认领过的行 user_id 非 NULL，第二次零行命中
	assertOwners()

	if _, err := db.Exec(`INSERT INTO api_keys (name, key_hash) VALUES ('later', 'h3')`); err != nil {
		t.Fatalf("再灌一把无主 key: %v", err)
	}
	db = reopen(t, db, path)
	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT user_id FROM api_keys WHERE name = 'later'`).Scan(&owner); err != nil {
		t.Fatalf("读认领结果: %v", err)
	}
	if !owner.Valid || owner.Int64 != adminID {
		t.Errorf("后灌的无主 key 没被下一次启动认领，user_id=%v", owner)
	}
}

// 撞名拒启：admin 名下已有同名 key 时认领会撞 UNIQUE(user_id, name)，而这个形状
// 只有手写 SQL 造得出来——按 v0.21 通则启动即拒、点名，不让约束错误裸奔。
func TestClaimRefusesOnNameClashNamingTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	ctx := context.Background()
	if err := SetSetting(ctx, db, SettingAdminPasswordHash, "$2a$10$fake"); err != nil {
		t.Fatalf("写密码哈希: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys (name, key_hash) VALUES ('撞名的', 'h1')`); err != nil {
		t.Fatalf("灌无主 key: %v", err)
	}
	db = reopen(t, db, path) // 认领：'撞名的' 归 admin
	if _, err := db.Exec(`INSERT INTO api_keys (name, key_hash) VALUES ('撞名的', 'h2')`); err != nil {
		t.Fatalf("再灌同名无主 key: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关库: %v", err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("撞名的无主 key 该让启动拒绝，Open 却成功了")
	}
	if !strings.Contains(err.Error(), "撞名的") {
		t.Errorf("拒启报错没点名是哪把 key：%v", err)
	}
}
