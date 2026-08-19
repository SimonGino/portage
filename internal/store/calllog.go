package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/SimonGino/portage/internal/calllog"
)

// CallLog 是写侧行类型，定义在 internal/calllog（依赖方向所迫，见那里的 doc）。
//
// 别名而不是重新定义：InsertCallLog 的签名、SQL、全部调用点因此一个字都不用动，
// 而「一行流水长什么样」只有一个定义处。
type CallLog = calllog.Row

// CountAPIKeys 数启用中的网关 key。启动时用它警告空表——那种库下每个转发请求都
// 会回 401，而症状（全都 401）看起来像 key 填错了，不像根本没有 key。
func CountAPIKeys(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE disabled = 0`).Scan(&n)
	return n, err
}

// InsertCallLog 落一行调用流水。
//
// ctx 必须是**不随请求取消**的：这行是在响应收尾之后写的，客户端断开时请求 ctx 已经
// 是 canceled，拿它来写等于「一断线就不记账」——而被刷、被打断恰恰是最需要留痕的时候。
func InsertCallLog(ctx context.Context, db *sql.DB, l CallLog) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO call_logs (
			api_key_name, endpoint, upstream_endpoint, client_protocol, upstream_protocol,
			model_requested, model_upstream, channel_name, channel_key_name,
			status, retry_count, is_stream, ttft_ms, total_ms, queue_wait_ms,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			reasoning_tokens, error, error_detail,
			upstream_request_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.APIKeyName, l.Endpoint, l.UpstreamEndpoint, l.ClientProtocol, l.UpstreamProtocol,
		l.ModelRequested, l.ModelUpstream, l.ChannelName, l.ChannelKeyName,
		l.Status, l.RetryCount, l.IsStream, l.TTFTMs, l.TotalMs, l.QueueWaitMs,
		l.InputTokens, l.OutputTokens, l.CacheReadTokens, l.CacheWriteTokens,
		l.ReasoningTokens, l.Error, l.ErrorDetail,
		l.UpstreamRequestID)
	return err
}

// DeleteCallLogsBefore 删掉 cutoff 之前的流水，返回删了多少行（口径层 v0.93，#35）。
//
// **刻意不跟 VACUUM**（v0.93 ③ 钉死）：删掉的页被 SQLite 复用，库文件不缩但停止
// 增长，这是设计行为不是漏了。同一条立论也管着这里的形状——**分批删、每批各自
// 成事务**：本库单写连接（SetMaxOpenConns(1)），一条无上限的 DELETE 对着存量大库
// 会把写连接占到删完为止，转发请求全排在它后面，裁掉 VACUUM 防的正是这件事。
// 批间放行其他写入，代价只是清理慢一点，而它本来就不赶时间。
//
// 条件是 <（恰好等于 cutoff 的行留下），比较走 sqlTime 折成的 UTC 串——与用量聚合
// 同一条纪律：不在 SQL 里对 created_at 套函数，否则 idx_call_logs_created_at 废掉，
// 这条 DELETE 就要全表扫。
func DeleteCallLogsBefore(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	return deleteCallLogsBefore(ctx, db, cutoff, 10_000)
}

func deleteCallLogsBefore(ctx context.Context, db *sql.DB, cutoff time.Time, batch int) (int64, error) {
	var total int64
	for {
		res, err := db.ExecContext(ctx, `DELETE FROM call_logs WHERE id IN
			(SELECT id FROM call_logs WHERE created_at < ? LIMIT ?)`, sqlTime(cutoff), batch)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batch) {
			return total, nil
		}
	}
}
