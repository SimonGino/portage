package store

import (
	"context"
	"database/sql"
)

// CallLog is one row of call_logs: a single relayed call as it ended.
//
// 可空的字段用 sql.NullInt64 而不是 0：首字节耗时与 token 数「没有」和「是 0」不是
// 一回事——非流式请求本就没有首字节耗时，记成 0 会让「首字延迟」的统计凭空多出一堆
// 满分样本。
type CallLog struct {
	APIKeyName       string
	ClientProtocol   string
	UpstreamProtocol string
	ModelRequested   string
	ModelUpstream    string
	ChannelName      string
	// ChannelKeyName 是本次真正发请求的那份凭证名（口径层 v0.38）。快照文本而不是
	// 外键：删凭证是常事，存 id 会把历史 join 空。没走到上游时是空串。
	ChannelKeyName string
	Status         int
	RetryCount     int
	TTFTMs         sql.NullInt64
	TotalMs        int64
	// QueueWaitMs 是并发闸排队耗时（口径层 v0.52）。**不是** NullInt64：没排队就是
	// 0，这一列上「没排」与「排了 0ms」没有语义差别，不像 ttft 那样要区分「没有」。
	QueueWaitMs      int64
	InputTokens      sql.NullInt64
	OutputTokens     sql.NullInt64
	CacheReadTokens  sql.NullInt64
	CacheWriteTokens sql.NullInt64
	Error            sql.NullString
}

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
			api_key_name, client_protocol, upstream_protocol,
			model_requested, model_upstream, channel_name, channel_key_name,
			status, retry_count, ttft_ms, total_ms, queue_wait_ms,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.APIKeyName, l.ClientProtocol, l.UpstreamProtocol,
		l.ModelRequested, l.ModelUpstream, l.ChannelName, l.ChannelKeyName,
		l.Status, l.RetryCount, l.TTFTMs, l.TotalMs, l.QueueWaitMs,
		l.InputTokens, l.OutputTokens, l.CacheReadTokens, l.CacheWriteTokens, l.Error)
	return err
}
