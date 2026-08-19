package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 保留期清理只看 created_at 一列（#35）：cutoff 之前的行删掉，之后的原样留下。
// 边界行为要钉死——恰好等于 cutoff 的行**不删**（条件是 <，不是 <=），否则
// 「保留 90 天」在边界那一秒少留一行，没人查得出来。批大小取 2、超期行种 5 行，
// 让分批循环真的转起来（一批删不完、最后一批不满）。
func TestDeleteCallLogsBefore(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "prune.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -90)
	seed := func(name string, at time.Time) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO call_logs (created_at, api_key_name,
			client_protocol, upstream_protocol, model_requested, model_upstream,
			channel_name, status, total_ms)
			VALUES (?, 'k', 'anthropic', 'anthropic', ?, 'mu', 'ch', 200, 1)`,
			sqlTime(at), name); err != nil {
			t.Fatalf("种流水 %s 失败: %v", name, err)
		}
	}
	for i := 0; i < 5; i++ {
		seed("old", cutoff.Add(-time.Duration(i+1)*time.Hour))
	}
	seed("boundary", cutoff)
	seed("fresh", now)

	deleted, err := deleteCallLogsBefore(context.Background(), db, cutoff, 2)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if deleted != 5 {
		t.Errorf("deleted = %d, 期望删掉 5 行 old", deleted)
	}

	rows, err := db.Query(`SELECT model_requested FROM call_logs ORDER BY model_requested`)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	defer rows.Close()
	var kept []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, m)
	}
	if len(kept) != 2 || kept[0] != "boundary" || kept[1] != "fresh" {
		t.Errorf("剩余行 = %v, 期望 [boundary fresh]", kept)
	}

	// 幂等：每天都会跑一遍，没得删就是 0，不是错。
	deleted, err = DeleteCallLogsBefore(context.Background(), db, cutoff)
	if err != nil {
		t.Fatalf("重复清理失败: %v", err)
	}
	if deleted != 0 {
		t.Errorf("重复清理 deleted = %d, 期望 0", deleted)
	}
}

// 真实流水的 created_at 是 DDL 默认值 CURRENT_TIMESTAMP 写的，不经过 sqlTime——
// 上面那条用例两侧都走 sqlTime，格式真不一致它照样绿。这条种一行真实路径
// （InsertCallLog，吃默认时间戳），拿贴着当下的 cutoff（±1h）从两个方向逼近：
// 同一 UTC 日期下比较会一路比到时间那几位，sqlTime 的格式要是与 CURRENT_TIMESTAMP
// 对不上（比如折成带 T 的 RFC3339），「过去的 cutoff 不该删它」那一半就会红。
func TestPruneMatchesCurrentTimestampFormat(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fmt.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := InsertCallLog(ctx, db, CallLog{
		APIKeyName: "k", ClientProtocol: "anthropic", UpstreamProtocol: "anthropic",
		ModelRequested: "m", ModelUpstream: "mu", ChannelName: "ch",
		Status: 200, TotalMs: 1,
	}); err != nil {
		t.Fatalf("InsertCallLog 失败: %v", err)
	}

	if n, err := DeleteCallLogsBefore(ctx, db, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Errorf("cutoff 在过去: deleted = %d, err = %v, 刚写的行不该被删", n, err)
	}
	if n, err := DeleteCallLogsBefore(ctx, db, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("cutoff 在未来: deleted = %d, err = %v, 期望恰好删掉那一行", n, err)
	}
}
