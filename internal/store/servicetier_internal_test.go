package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 老库补 call_logs.service_tier / speed（#103）：存量行落空串，重跑幂等。
func TestMigrateAddsServiceTierSpeed(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE call_logs (id INTEGER PRIMARY KEY, api_key_name TEXT NOT NULL)`); err != nil {
		t.Fatalf("建老表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name) VALUES ('k')`); err != nil {
		t.Fatalf("种存量流水失败: %v", err)
	}
	for range 2 { // 网关每次启动都会跑一遍
		if err := addServiceTierSpeed(db); err != nil {
			t.Fatalf("迁移失败: %v", err)
		}
	}
	var tier, speed string
	if err := db.QueryRow(`SELECT service_tier, speed FROM call_logs WHERE id = 1`).Scan(&tier, &speed); err != nil {
		t.Fatalf("读新列失败: %v", err)
	}
	if tier != "" || speed != "" {
		t.Errorf("存量流水 service_tier/speed = %q/%q, 期望空串", tier, speed)
	}
}

// 写进去的两格原样回到管理端接口的行上。
func TestCallLogServiceTierSpeedRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := InsertCallLog(ctx, db, CallLog{
		APIKeyName: "k", ClientProtocol: "anthropic", UpstreamProtocol: "anthropic",
		ModelRequested: "m", ModelUpstream: "mu", ChannelName: "ch", Status: 200, TotalMs: 1,
		ServiceTier: "priority", Speed: "fast",
	}); err != nil {
		t.Fatalf("InsertCallLog 失败: %v", err)
	}
	rows, err := ListCallLogs(ctx, db, CallLogFilter{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCallLogs = %d 行, err = %v", len(rows), err)
	}
	if r := rows[0]; r.ServiceTier != "priority" || r.Speed != "fast" {
		t.Errorf("service_tier/speed = %q/%q, 期望 priority/fast", r.ServiceTier, r.Speed)
	}
}
