package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 老库补 channels.supports_compaction（口径层 v0.54）。
//
// schema.sql 那份 DDL 只对**新建**的库生效（CREATE TABLE IF NOT EXISTS 对已存在的表
// 是空操作），所以真正要验的是这条 ALTER 路径：存量渠道行一律落在默认 0 上——这是本
// 批唯一一处行为会变的迁移，勾回来是人工动作，不能靠回填猜。
func TestMigrateAddsSupportsCompaction(t *testing.T) {
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

	if err := addSupportsCompaction(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 幂等：网关每次启动都会跑一遍。
	if err := addSupportsCompaction(db); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}

	var v int
	if err := db.QueryRow(`SELECT supports_compaction FROM channels WHERE name = 'old'`).Scan(&v); err != nil {
		t.Fatalf("读新列失败: %v", err)
	}
	if v != 0 {
		t.Errorf("存量渠道的能力位 = %d, 期望 0（默认不支持）", v)
	}
}
