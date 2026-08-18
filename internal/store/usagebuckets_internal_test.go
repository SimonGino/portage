package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// 补齐序列的钟点钉死在 15:30，**日期仍取今天**：SQL 里的窗口下界走 datetime('now')，
// 补齐序列走这个 now，两者必须落在同一天上（见 UsageBuckets 的注释）。
func fixedNow(t *testing.T) time.Time {
	t.Helper()
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 15, 30, 0, 0, n.Location())
}

// seedCall 按**本地时刻**种一行流水。created_at 存 UTC（CURRENT_TIMESTAMP），
// 所以这里也折成 UTC 再写，与生产写入同一形态。
func seedCall(t *testing.T, db *sql.DB, at time.Time, status int, in, out, cr, cw int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO call_logs
		(created_at, api_key_name, client_protocol, upstream_protocol,
		 model_requested, model_upstream, channel_name, status, total_ms,
		 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
		VALUES (?, 'k', 'anthropic', 'anthropic', 'model-a', 'model-a', 'ch', ?, 1, ?, ?, ?, ?)`,
		at.UTC().Format("2006-01-02 15:04:05"), status, in, out, cr, cw)
	if err != nil {
		t.Fatalf("种流水失败: %v", err)
	}
}

func bucketOf(rows []BucketUsage, key string) (BucketUsage, bool) {
	for _, r := range rows {
		if r.Bucket == key {
			return r, true
		}
	}
	return BucketUsage{}, false
}

// 未到的区间不给（口径层 v0.86 ③）：15:30 看「今天」，回的是 00:00~15:00 这 16 格，
// 不是满格的 24。吐满 24 格的话，前端要么自己再算一次「哪些是未来」，要么就把活跃
// 区间的分母算错——那张图在一天过完之前会永远低估。
func TestUsageBucketsStopsAtNow(t *testing.T) {
	db, now := openTestDB(t), fixedNow(t)
	rows, err := UsageBuckets(context.Background(), db, 1, BucketHour, now)
	if err != nil {
		t.Fatalf("分桶失败: %v", err)
	}
	if len(rows) != 16 {
		t.Fatalf("15:30 看今天回了 %d 格, 期望 00:00~15:00 共 16 格", len(rows))
	}
	if want := now.Format("2006-01-02") + " 00:00"; rows[0].Bucket != want {
		t.Errorf("第一格是 %q, 期望 %q", rows[0].Bucket, want)
	}
	if want := now.Format("2006-01-02") + " 15:00"; rows[15].Bucket != want {
		t.Errorf("最后一格是 %q, 期望 %q", rows[15].Bucket, want)
	}
}

// 空桶补零：一天里只调用一次，回的仍是从 00:00 到现在的整点数，不是 1。
//
// 顺带钉住 Go 侧补齐用的桶名与 SQL 那个 strftime 逐字节一致——对不上的话，有行的那格
// 会被当成「GROUP BY 没吐出来的空桶」，整张图恒为零而且不报错。
func TestUsageBucketsFillsEmptyHours(t *testing.T) {
	db, now := openTestDB(t), fixedNow(t)
	at := time.Date(now.Year(), now.Month(), now.Day(), 8, 15, 0, 0, now.Location())
	seedCall(t, db, at, 200, 10, 20, 30, 40)

	rows, err := UsageBuckets(context.Background(), db, 1, BucketHour, now)
	if err != nil {
		t.Fatalf("分桶失败: %v", err)
	}
	if len(rows) != 16 {
		t.Fatalf("回了 %d 格, 一次调用也该占满 00:00~15:00 这 16 格", len(rows))
	}
	hit, ok := bucketOf(rows, at.Format("2006-01-02 15:00"))
	if !ok {
		t.Fatalf("没有 %s 这一格, 回的是 %+v", at.Format("2006-01-02 15:00"), rows)
	}
	if hit.Calls != 1 || hit.InputTokens != 10 || hit.OutputTokens != 20 ||
		hit.CacheRead != 30 || hit.CacheWrite != 40 {
		t.Errorf("08:00 这一格是 %+v, 期望 1 次调用与 10/20/30/40 四列 token", hit)
	}
	for _, r := range rows {
		if r.Bucket != hit.Bucket && r.Calls != 0 {
			t.Errorf("%s 这一格有 %d 次调用, 本该是空的", r.Bucket, r.Calls)
		}
	}
}

// errors 列：切片井的 tooltip 要报失败数，status >= 400 的行算一次（含上游自己回的
// 4xx——人问的是「有多少次没拿到东西」）。
func TestUsageBucketsCountsErrors(t *testing.T) {
	db, now := openTestDB(t), fixedNow(t)
	at := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	seedCall(t, db, at, 200, 1, 1, 0, 0)
	seedCall(t, db, at, 429, 0, 0, 0, 0)
	seedCall(t, db, at, 500, 0, 0, 0, 0)

	rows, err := UsageBuckets(context.Background(), db, 1, BucketHour, now)
	if err != nil {
		t.Fatalf("分桶失败: %v", err)
	}
	hit, ok := bucketOf(rows, at.Format("2006-01-02 15:00"))
	if !ok {
		t.Fatalf("没有 09:00 这一格, 回的是 %+v", rows)
	}
	if hit.Calls != 3 || hit.Errors != 2 {
		t.Errorf("09:00 这一格 = %d 次调用 / %d 次失败, 期望 3 / 2", hit.Calls, hit.Errors)
	}
}

// 小时桶跨自然日边界：昨天 23:30 与今天 00:30 落进两个不同的桶，而 days=1（今天算
// 一天，口径层 v0.55）的窗口里只该有后者——桶按本地日历切，而 created_at 存 UTC，
// 照 UTC 分桶的话 UTC+8 下「今天」要到本地早上八点才开始。
func TestUsageBucketsCrossesMidnight(t *testing.T) {
	db, now := openTestDB(t), fixedNow(t)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	late := today.Add(-30 * time.Minute) // 昨天 23:30
	early := today.Add(30 * time.Minute) // 今天 00:30
	seedCall(t, db, late, 200, 0, 0, 0, 0)
	seedCall(t, db, early, 200, 0, 0, 0, 0)

	one, err := UsageBuckets(context.Background(), db, 1, BucketHour, now)
	if err != nil {
		t.Fatalf("分桶失败: %v", err)
	}
	var total int64
	for _, r := range one {
		total += r.Calls
	}
	if total != 1 {
		t.Errorf("days=1 里共 %d 次调用, 昨天 23:30 那行不该进来", total)
	}
	if b, ok := bucketOf(one, early.Format("2006-01-02 15:00")); !ok || b.Calls != 1 {
		t.Errorf("今天 00:00 这一格 = %+v (存在: %v), 期望 1 次调用", b, ok)
	}

	two, err := UsageBuckets(context.Background(), db, 2, BucketHour, now)
	if err != nil {
		t.Fatalf("分桶失败: %v", err)
	}
	lateB, lateOK := bucketOf(two, late.Format("2006-01-02 15:00"))
	earlyB, earlyOK := bucketOf(two, early.Format("2006-01-02 15:00"))
	if !lateOK || !earlyOK || lateB.Calls != 1 || earlyB.Calls != 1 {
		t.Errorf("days=2 里 23:00 格 = %+v (存在 %v), 00:00 格 = %+v (存在 %v), 期望各 1 次",
			lateB, lateOK, earlyB, earlyOK)
	}
}

// unit=day 与老的 UsageDaily 同口径：恒 days 行、最后一行是本地时区的今天。
func TestUsageBucketsByDay(t *testing.T) {
	db, now := openTestDB(t), fixedNow(t)
	seedCall(t, db, now.Add(-time.Hour), 200, 0, 0, 0, 0)

	rows, err := UsageBuckets(context.Background(), db, 7, BucketDay, now)
	if err != nil {
		t.Fatalf("分桶失败: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("回了 %d 行, 「7 天」恒是 7 行", len(rows))
	}
	if want := now.Format("2006-01-02"); rows[6].Bucket != want {
		t.Errorf("最后一行是 %q, 期望本地时区的今天 %q", rows[6].Bucket, want)
	}
	if rows[6].Calls != 1 {
		t.Errorf("今天这一桶 = %d 次调用, 期望 1", rows[6].Calls)
	}
	for _, r := range rows[:6] {
		if r.Calls != 0 {
			t.Errorf("%s 这一桶有 %d 次调用, 这几天本该是空的", r.Bucket, r.Calls)
		}
	}
}

// from/to 与 days 那条路径口径一致：取同一段时间，两条路径的 SUM 必须相等。
//
// 走 by=model 的默认档，于是标签那个 ? 与范围那两个 ? 一起下推——参数顺序错了这里
// 就会当场炸（SELECT 里那个先于 WHERE 里的两个出现）。
func TestUsageByRangeMatchesDays(t *testing.T) {
	db, now := openTestDB(t), fixedNow(t)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	seedCall(t, db, today.Add(-time.Hour), 200, 5, 5, 5, 5) // 昨天，两条路径都不该数
	seedCall(t, db, today.Add(time.Hour), 200, 10, 20, 30, 40)
	seedCall(t, db, today.Add(2*time.Hour), 500, 1, 2, 3, 4)

	byDays, err := UsageBy(context.Background(), db, UsageRange{Days: 1}, UsageByModel)
	if err != nil {
		t.Fatalf("按 days 汇总失败: %v", err)
	}
	byRange, err := UsageBy(context.Background(), db,
		UsageRange{Days: 1, From: today, To: today.AddDate(0, 0, 1)}, UsageByModel)
	if err != nil {
		t.Fatalf("按 from/to 汇总失败: %v", err)
	}
	if len(byDays) != 1 || len(byRange) != 1 {
		t.Fatalf("行数不一致: days %d 行 / from-to %d 行", len(byDays), len(byRange))
	}
	if byDays[0] != byRange[0] {
		t.Errorf("同一段时间两条路径不等: days %+v / from-to %+v", byDays[0], byRange[0])
	}
	if byRange[0].Calls != 2 || byRange[0].Errors != 1 || byRange[0].InputTokens != 11 {
		t.Errorf("这一段的合计是 %+v, 期望 2 次调用 / 1 次失败 / 11 个输入 token", byRange[0])
	}
}

// 半开区间 [from, to)：右端点那一刻的行不算进来，否则相邻两格会把它数两遍。
func TestUsageByRangeIsHalfOpen(t *testing.T) {
	db, now := openTestDB(t), fixedNow(t)
	at := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	seedCall(t, db, at, 200, 0, 0, 0, 0)

	rows, err := UsageBy(context.Background(), db,
		UsageRange{From: at.Add(-time.Hour), To: at}, UsageByModel)
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("右端点那一刻的行被数进来了: %+v", rows)
	}
	rows, err = UsageBy(context.Background(), db,
		UsageRange{From: at, To: at.Add(time.Hour)}, UsageByModel)
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}
	if len(rows) != 1 || rows[0].Calls != 1 {
		t.Errorf("左端点那一刻的行没被数进来: %+v", rows)
	}
}
