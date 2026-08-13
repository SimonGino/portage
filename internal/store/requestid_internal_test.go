package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 老库补 call_logs.upstream_request_id（口径层 v0.56，#37）。
//
// schema.sql 那份 DDL 只对**新建**的库生效，所以真正要验的是这条 ALTER 路径。存量
// 流水一律落在默认空串上：那些行采集时网关根本没读这个头，回填不出来，空串就是
// 「没有可用的 id」。
func TestMigrateAddsUpstreamRequestID(t *testing.T) {
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

	if err := addUpstreamRequestID(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 幂等：网关每次启动都会跑一遍。
	if err := addUpstreamRequestID(db); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}

	// 空串而不是 NULL：用量页按这一列做等值筛选时，NULL 会多出一档比不出来的值。
	var got string
	if err := db.QueryRow(`SELECT upstream_request_id FROM call_logs WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("读新列失败: %v", err)
	}
	if got != "" {
		t.Errorf("存量流水的 upstream_request_id = %q, 期望空串", got)
	}
}
