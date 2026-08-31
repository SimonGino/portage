package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// seedCostRow 种一行带归属与成本的流水。userID 0 = 无主（NULL）。
func seedCostRow(t *testing.T, db *sql.DB, userID int64, keyName string, at time.Time, cost any) {
	t.Helper()
	var uid any
	if userID != 0 {
		uid = userID
	}
	_, err := db.Exec(`INSERT INTO call_logs
		(created_at, api_key_name, user_id, client_protocol, upstream_protocol,
		 model_requested, model_upstream, channel_name, status, total_ms, cost)
		VALUES (?, ?, ?, 'anthropic', 'anthropic', 'm', 'm', 'ch', 200, 1, ?)`,
		at.UTC().Format("2006-01-02 15:04:05"), keyName, uid, cost)
	if err != nil {
		t.Fatalf("种流水失败: %v", err)
	}
}

// 月度已用只数本 UTC 自然月：上月最后一秒的行不算，月首第一秒的行算。cost 为 NULL
// 的行（没有用量可计）天然不计。默认限额是 NULL = 不限。
func TestUserQuotaStateCountsOnlyThisUTCMonth(t *testing.T) {
	db := openTestDB(t)
	uid := seedUser(t, db, "bob@x", RoleUser)
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	monthStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	seedCostRow(t, db, uid, "k", monthStart.Add(-time.Second), 100.0) // 上月，不算
	seedCostRow(t, db, uid, "k", monthStart, 1.5)                     // 月首第一秒，算
	seedCostRow(t, db, uid, "k", now.Add(-time.Hour), 2.25)           // 本月，算
	seedCostRow(t, db, uid, "k", now.Add(-time.Minute), nil)          // 本月但 cost NULL，不算
	seedCostRow(t, db, 0, "k", now.Add(-time.Minute), 50.0)           // 无主行，不入任何人的账

	q, err := UserQuotaState(context.Background(), db, uid, now)
	if err != nil {
		t.Fatalf("查配额失败: %v", err)
	}
	if q.LimitUSD != nil {
		t.Errorf("默认限额该是 NULL（不限），得到 %v", *q.LimitUSD)
	}
	if q.SpentUSD != 3.75 {
		t.Errorf("本月已用 = %v, 期望 3.75（只数本月归属本人的行）", q.SpentUSD)
	}
}

// 用户不存在回 ErrNotFound，不静默当「不限额」——key 指着的用户没了是数据损坏。
func TestUserQuotaStateMissingUserIsNotFound(t *testing.T) {
	db := openTestDB(t)
	if _, err := UserQuotaState(context.Background(), db, 999, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, 期望 ErrNotFound", err)
	}
}

// SetUserQuota 三态：正数 = 限额、0 = 封停、nil = 清回不限；负数 400、没人 404。
func TestSetUserQuota(t *testing.T) {
	db := openTestDB(t)
	uid := seedUser(t, db, "bob@x", RoleUser)
	ctx := context.Background()

	five := 5.0
	if err := SetUserQuota(ctx, db, uid, &five); err != nil {
		t.Fatalf("设限额失败: %v", err)
	}
	q, err := UserQuotaState(ctx, db, uid, time.Now())
	if err != nil || q.LimitUSD == nil || *q.LimitUSD != 5.0 {
		t.Fatalf("限额没落库: %+v err=%v", q, err)
	}
	if err := SetUserQuota(ctx, db, uid, nil); err != nil {
		t.Fatalf("清限额失败: %v", err)
	}
	if q, _ = UserQuotaState(ctx, db, uid, time.Now()); q.LimitUSD != nil {
		t.Errorf("清限额后该回 NULL，得到 %v", *q.LimitUSD)
	}
	neg := -1.0
	if err := SetUserQuota(ctx, db, uid, &neg); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("负限额 err = %v, 期望 ErrInvalidInput", err)
	}
	if err := SetUserQuota(ctx, db, 999, &five); !errors.Is(err, ErrNotFound) {
		t.Errorf("没这个人 err = %v, 期望 ErrNotFound", err)
	}
}

// 回填三态（展开层 §7.10 ⑤）：有 key 名的无主老行归第一个 admin，未鉴权行（key 名
// 空串）留 NULL，已有归属的行不动；重跑幂等。
func TestBackfillCallLogUsersThreeStates(t *testing.T) {
	db := openTestDB(t)
	adminID := seedUser(t, db, "admin@x", RoleAdmin)
	bobID := seedUser(t, db, "bob@x", RoleUser)
	now := time.Now()

	seedCostRow(t, db, 0, "old-key", now, 1.0)  // 老行：有 key 名、无归属 → admin
	seedCostRow(t, db, 0, "", now, nil)         // 未鉴权行 → 留 NULL
	seedCostRow(t, db, bobID, "bobs", now, 1.0) // 已有归属 → 不动

	for round := 0; round < 2; round++ { // 跑两遍：第二遍是幂等断言
		if err := backfillCallLogUsers(db); err != nil {
			t.Fatalf("回填失败（第 %d 遍）: %v", round+1, err)
		}
		var got sql.NullInt64
		if err := db.QueryRow(`SELECT user_id FROM call_logs WHERE api_key_name = 'old-key'`).Scan(&got); err != nil {
			t.Fatalf("读老行失败: %v", err)
		}
		if !got.Valid || got.Int64 != adminID {
			t.Errorf("老行该归第一个 admin(%d)，得到 %+v", adminID, got)
		}
		if err := db.QueryRow(`SELECT user_id FROM call_logs WHERE api_key_name = ''`).Scan(&got); err != nil {
			t.Fatalf("读未鉴权行失败: %v", err)
		}
		if got.Valid {
			t.Errorf("未鉴权行该留 NULL，得到 %d", got.Int64)
		}
		if err := db.QueryRow(`SELECT user_id FROM call_logs WHERE api_key_name = 'bobs'`).Scan(&got); err != nil {
			t.Fatalf("读 bob 行失败: %v", err)
		}
		if !got.Valid || got.Int64 != bobID {
			t.Errorf("已有归属的行不该被抢走：期望 %d，得到 %+v", bobID, got)
		}
	}
}

// 无 admin 的库（纯转发/声明形态）回填整步空转，不报错也不动行。
func TestBackfillCallLogUsersNoopWithoutAdmin(t *testing.T) {
	db := openTestDB(t)
	seedCostRow(t, db, 0, "old-key", time.Now(), 1.0)

	if err := backfillCallLogUsers(db); err != nil {
		t.Fatalf("无 admin 时回填该空转: %v", err)
	}
	var got sql.NullInt64
	if err := db.QueryRow(`SELECT user_id FROM call_logs WHERE api_key_name = 'old-key'`).Scan(&got); err != nil {
		t.Fatalf("读行失败: %v", err)
	}
	if got.Valid {
		t.Errorf("无 admin 时不该回填，得到 %d", got.Int64)
	}
}
