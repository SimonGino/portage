package store

import (
	"context"
	"database/sql"

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
