package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 老库补 call_logs.reasoning_tokens（口径层 v0.66）。
//
// 与 upstream_request_id 那条的关键差别：这一列**可空**，存量行落在 NULL 上。
// NULL 不是「还没回填」，是「那时候网关不认这一格，上游报没报都无从知道」——与新行
// 里「上游不报这个数」（Anthropic 一路）同一档，恰恰不能记成 0：记 0 会让那些调用
// 的思考成本显示为确凿的零。
func TestMigrateAddsReasoningTokens(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE call_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		api_key_name TEXT NOT NULL, client_protocol TEXT NOT NULL,
		upstream_protocol TEXT NOT NULL, model_requested TEXT NOT NULL,
		model_upstream TEXT NOT NULL, channel_name TEXT NOT NULL,
		status INTEGER NOT NULL, total_ms INTEGER NOT NULL,
		output_tokens INTEGER)`); err != nil {
		t.Fatalf("建老表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name, client_protocol,
		upstream_protocol, model_requested, model_upstream, channel_name, status, total_ms, output_tokens)
		VALUES ('k', 'openai', 'openai', 'm', 'mu', 'ch', 200, 1, 350)`); err != nil {
		t.Fatalf("种存量流水失败: %v", err)
	}

	if err := addReasoningTokens(db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 幂等：网关每次启动都会跑一遍。
	if err := addReasoningTokens(db); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}

	var got sql.NullInt64
	if err := db.QueryRow(`SELECT reasoning_tokens FROM call_logs WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("读新列失败: %v", err)
	}
	if got.Valid {
		t.Errorf("存量流水的 reasoning_tokens = %d, 期望 NULL", got.Int64)
	}
}
