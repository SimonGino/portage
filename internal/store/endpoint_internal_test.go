package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 老库补 call_logs.endpoint（#17）。
//
// schema.sql 那份 DDL 只对**新建**的库生效，真正要验的是这条 ALTER 路径。存量流水
// 一律落在默认空串上，**不回填**：那些行采集时根本没有这个信息，而从 client_protocol
// 反推更是错的——anthropic 那一档正是 messages 与 count_tokens 分不开的那一档。
func TestMigrateAddsEndpoint(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()
	// 一张没有这一列的老 call_logs，外加一行存量流水。
	if _, err := db.Exec(`CREATE TABLE call_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		api_key_name TEXT NOT NULL, client_protocol TEXT NOT NULL,
		upstream_protocol TEXT NOT NULL, model_requested TEXT NOT NULL,
		model_upstream TEXT NOT NULL, channel_name TEXT NOT NULL,
		status INTEGER NOT NULL, total_ms INTEGER NOT NULL)`); err != nil {
		t.Fatalf("建老表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name, client_protocol,
		upstream_protocol, model_requested, model_upstream, channel_name, status, total_ms)
		VALUES ('k', 'anthropic', 'anthropic', 'm', 'mu', 'ch', 200, 1)`); err != nil {
		t.Fatalf("种存量流水失败: %v", err)
	}

	if err := addEndpoint(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 幂等：网关每次启动都会跑一遍。
	if err := addEndpoint(db); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}

	// 空串而不是 NULL：这一列要拿去做等值筛选，NULL 会多出一档筛不出来的值。
	var got string
	if err := db.QueryRow(`SELECT endpoint FROM call_logs WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("读新列失败: %v", err)
	}
	if got != "" {
		t.Errorf("存量流水的 endpoint = %q, 期望空串", got)
	}

	// 存量行照样读得出来、也不挡新行写入——「列可空」这条 AC 说的是这个。
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name, endpoint, client_protocol,
		upstream_protocol, model_requested, model_upstream, channel_name, status, total_ms)
		VALUES ('k', '/v1/messages/count_tokens', 'anthropic', '', '', '', '', 501, 1)`); err != nil {
		t.Fatalf("迁移后写新行失败: %v", err)
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM call_logs WHERE endpoint = '/v1/messages/count_tokens'`).Scan(&n); err != nil {
		t.Fatalf("按端点筛失败: %v", err)
	}
	if n != 1 {
		t.Errorf("按端点筛出 %d 行, 期望 1", n)
	}
}
