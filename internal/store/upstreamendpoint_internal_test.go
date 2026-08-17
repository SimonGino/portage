package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 老库补 call_logs.upstream_endpoint（#20）。
//
// schema.sql 那份 DDL 只对**新建**的库生效，真正要验的是这条 ALTER 路径。存量流水
// 一律落在默认空串上，**不回填**：跨协议的历史行倒是能从 upstream_protocol 反推一条
// 路径出来，但那是重算不是记录，而同协议透传的 count_tokens 那批压根推不出来——它
// 没有出口对应物。
func TestMigrateAddsUpstreamEndpoint(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()
	// 一张 #17 之后、#20 之前的 call_logs：入站端点那列有，出站那列没有。外加一行
	// 存量流水，它的入站端点是有值的——这正是本列空串的两义要靠它分辨的那种行。
	if _, err := db.Exec(`CREATE TABLE call_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		api_key_name TEXT NOT NULL, endpoint TEXT NOT NULL DEFAULT '',
		client_protocol TEXT NOT NULL,
		upstream_protocol TEXT NOT NULL, model_requested TEXT NOT NULL,
		model_upstream TEXT NOT NULL, channel_name TEXT NOT NULL,
		status INTEGER NOT NULL, total_ms INTEGER NOT NULL)`); err != nil {
		t.Fatalf("建老表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name, endpoint, client_protocol,
		upstream_protocol, model_requested, model_upstream, channel_name, status, total_ms)
		VALUES ('k', '/v1/messages', 'anthropic', 'openai', 'm', 'mu', 'ch', 200, 1)`); err != nil {
		t.Fatalf("种存量流水失败: %v", err)
	}

	if err := addUpstreamEndpoint(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 幂等：网关每次启动都会跑一遍。
	if err := addUpstreamEndpoint(db); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}

	// 空串而不是 NULL：与 endpoint 那列同款，NULL 会多出一档谁都不认的值。这一行
	// 的 upstream_protocol 是 openai，反推得出 /v1/chat/completions——不回填正是不让
	// 这种「算得出来」冒充「记下来了」。
	var got string
	if err := db.QueryRow(`SELECT upstream_endpoint FROM call_logs WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("读新列失败: %v", err)
	}
	if got != "" {
		t.Errorf("存量流水的 upstream_endpoint = %q, 期望空串", got)
	}

	// 存量行照样读得出来、也不挡新行写入。两种新行都写一遍：跨协议那条两列不等，
	// 没发到上游那条只有入站有值。
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name, endpoint, upstream_endpoint,
		client_protocol, upstream_protocol, model_requested, model_upstream, channel_name, status, total_ms)
		VALUES ('k', '/v1/messages', '/v1/chat/completions', 'anthropic', 'openai', 'm', 'mu', 'ch', 200, 1),
		       ('k', '/v1/messages/count_tokens', '', 'anthropic', '', '', '', '', 501, 1)`); err != nil {
		t.Fatalf("迁移后写新行失败: %v", err)
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM call_logs WHERE upstream_endpoint = '/v1/chat/completions'`).Scan(&n); err != nil {
		t.Fatalf("按出站端点数行失败: %v", err)
	}
	if n != 1 {
		t.Errorf("出站是 /v1/chat/completions 的行有 %d 条, 期望 1", n)
	}
}
