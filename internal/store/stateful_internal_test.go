package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 老库补 channels.supports_stateful_responses（口径层 v0.88）。
//
// 与 supports_compaction 那条同形、默认相反：存量渠道一律落 1，迁移前后行为一字不变。
// 这正是这条测试要钉的——落 0 会把所有老库上本来能用的 Responses 续链一次性打断，
// 而那种打断在页面上看不出来，只表现成客户端忽然开始报 previous_response_not_found。
func TestMigrateAddsSupportsStatefulResponses(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()
	// 一张没有这一列的老 channels 表，外加一行存量渠道。
	if _, err := db.Exec(`CREATE TABLE channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		protocols TEXT NOT NULL,
		base_url TEXT NOT NULL,
		disabled INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("建老表失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO channels (name, protocols, base_url) VALUES ('old', 'openai_responses', 'https://x')`); err != nil {
		t.Fatalf("种存量渠道失败: %v", err)
	}

	if err := addSupportsStatefulResponses(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 幂等：网关每次启动都会跑一遍。
	if err := addSupportsStatefulResponses(db); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}

	var v int
	if err := db.QueryRow(
		`SELECT supports_stateful_responses FROM channels WHERE name = 'old'`).Scan(&v); err != nil {
		t.Fatalf("读新列失败: %v", err)
	}
	if v != 1 {
		t.Errorf("存量渠道的能力位 = %d, 期望 1（默认支持，迁移不改变既有行为）", v)
	}
}
