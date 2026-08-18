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
	APIKeyName string
	// Endpoint 是这次打的转发端点路径（#17），取值就是 protocol.Endpoint 那四条之一。
	// 与 ClientProtocol 不是一回事：`/v1/messages` 与 `/v1/messages/count_tokens` 的
	// 入站协议同为 anthropic，只看协议这两者在流水里结构上不可分。
	Endpoint string
	// UpstreamEndpoint 是这次**打到上游**的那条路径（#20），与 Endpoint 成对。跨协议
	// 时两者不等（A 入口落 openai 渠道，出站是 /v1/chat/completions），同协议透传时
	// 相等。**没发到上游的行是空串**——401、429、count_tokens 撞非 anthropic 渠道的
	// 501、并发闸队满/超时都从没拨过上游，空就是事实，不从 UpstreamProtocol 推导。
	UpstreamEndpoint string
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
	// IsStream 记这次是同步还是流式。可空：stream 是解析请求体才知道的，鉴权失败
	// 那类行没走到那一步——「不知道」与「同步」不是一回事。
	IsStream sql.NullBool
	TTFTMs   sql.NullInt64
	TotalMs  int64
	// QueueWaitMs 是并发闸排队耗时（口径层 v0.52）。**不是** NullInt64：没排队就是
	// 0，这一列上「没排」与「排了 0ms」没有语义差别，不像 ttft 那样要区分「没有」。
	QueueWaitMs      int64
	InputTokens      sql.NullInt64
	OutputTokens     sql.NullInt64
	CacheReadTokens  sql.NullInt64
	CacheWriteTokens sql.NullInt64
	// ReasoningTokens 是思考 token（口径层 v0.66），output_tokens 的**明细**
	// 而非另一笔。可空的理由与上面几个不同：这一列上 NULL 是「上游这一轮不报这个数」
	// （整个 details 容器都不发——老式兼容上游、中转裁剪、CC 挂非推理模型），0 是
	// 「上游报了，这次没思考」——抹成一个，那些调用的思考成本就显示为确凿的零。
	ReasoningTokens sql.NullInt64
	Error           sql.NullString
	// UpstreamRequestID 是上游响应头 request-id 的原样快照（口径层 v0.56，#2），
	// 拿去找上游对账用。没走到上游、或上游没回这个头时是空串——这一列上两者同档。
	UpstreamRequestID string
	// ErrorDetail 是上游错误原文的前 2KB（口径层 v0.53），只在失败时有值。
	//
	// 与 Error 分列：Error 是网关的固定词表（可枚举、可 group by），这一列是上游
	// 自己说的那段话，不可控、只给人看。可空是为了分开「没存」与「上游回了 4xx 但
	// 体是空的」——后者本身就是排障信息。
	ErrorDetail sql.NullString
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
