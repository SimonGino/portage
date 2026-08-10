package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/store"
)

// bodyCaptureLimit 是 log_bodies 打开时单侧 body 的记录上限。开这个开关是为了排障，
// 不是为了留档；一条长流的完整响应进日志只会把日志冲垮。
const bodyCaptureLimit = 64 << 10

// captureWriter 是 log_bodies 打开时挂在旁路上的定量收集器。和 Tap 一样：永不报错，
// 否则 io.MultiWriter 会把错误变成读错误、打断转发。
type captureWriter struct {
	buf       []byte
	truncated bool
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if room := bodyCaptureLimit - len(c.buf); room > 0 {
		if len(p) > room {
			c.buf = append(c.buf, p[:room]...)
			c.truncated = true
		} else {
			c.buf = append(c.buf, p...)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *captureWriter) String() string {
	if c.truncated {
		return string(c.buf) + "…[truncated]"
	}
	return string(c.buf)
}

// callRecord 攒够一次调用的日志字段，由 callLog 中间件的 defer 统一落一行 slog
// 加一行 call_logs。放在中间件而不是 relay 里，是因为鉴权失败的请求也要留痕。
//
// 里面**没有**渠道 base_url 与凭证，也不该被加进来：日志是最容易被复制粘贴出去的
// 东西，上游密钥不进日志是硬约束。渠道用 name 指代已经够定位。
type callRecord struct {
	start     time.Time
	firstByte time.Time

	endpoint string
	// apiKeyName 是网关 key 的**名字**，不是 key 本身，也不是它的 hash：
	// 日志是最容易被复制粘贴出去的东西，凭证材料一概不进。
	apiKeyName     string
	inboundProto   protocol.Protocol
	requestedModel string
	channel        string
	channelProto   protocol.Protocol
	upstreamModel  string
	stream         bool
	status         int
	// outcome 区分「同样是 200」的几种收场，尤其是首字节之后断流那种——
	// 状态码已经发出去了，只有这里能看出它其实没说完。
	outcome string
	// retries 是这次调用向同一候选重打了几次。不含首次尝试，0 是常态所以不打进
	// 日志——否则每一行都要背一个恒为 0 的字段。非 0 才说明发生过退避重试，
	// 「这次怎么慢了三秒」有据可查。
	retries int

	summary     protocol.Summary
	haveSummary bool

	requestBody  *captureWriter
	responseBody *captureWriter
}

func (s *Server) logCall(rec *callRecord) {
	attrs := []any{
		"endpoint", rec.endpoint,
		"api_key", rec.apiKeyName,
		"inbound_protocol", string(rec.inboundProto),
		"requested_model", rec.requestedModel,
		"stream", rec.stream,
		"status", rec.status,
		"outcome", rec.outcome,
		"duration_ms", time.Since(rec.start).Milliseconds(),
	}
	if rec.channel != "" {
		attrs = append(attrs, "channel", rec.channel,
			"channel_protocol", string(rec.channelProto),
			"upstream_model", rec.upstreamModel)
	}
	if rec.retries > 0 {
		attrs = append(attrs, "retries", rec.retries)
	}
	if !rec.firstByte.IsZero() {
		attrs = append(attrs, "ttfb_ms", rec.firstByte.Sub(rec.start).Milliseconds())
	}
	if rec.haveSummary {
		sum := rec.summary
		attrs = append(attrs,
			"input_tokens", sum.InputTokens,
			"output_tokens", sum.OutputTokens,
			"cache_read_tokens", sum.CacheReadTokens,
			"cache_write_tokens", sum.CacheWriteTokens,
			"stop_reason", sum.StopReason,
			// upstream_reported_model 是上游自报的模型名，和 upstream_model
			// 对不上时说明上游把别名解析成了别的版本。
			"upstream_reported_model", sum.Model,
		)
		if sum.Degraded {
			attrs = append(attrs, "tap_degraded", true)
		}
	}
	if rec.requestBody != nil {
		attrs = append(attrs, "request_body", rec.requestBody.String())
	}
	if rec.responseBody != nil {
		attrs = append(attrs, "response_body", rec.responseBody.String())
	}
	s.log.Info("call", attrs...)
	s.persistCall(rec)
}

// persistCall 把同一条流水写进 call_logs。
//
// 落库失败**不得影响请求**：这里跑的时候响应早已写出去了，改写不了也中断不了，
// 只能记一条 slog error。口径层 §2.5 要的是「每请求一行落库」，但一次 SQLite 抖动
// 不该把一次成功的转发变成客户端眼里的失败。
func (s *Server) persistCall(rec *callRecord) {
	// 不用请求 ctx：客户端断开时它已经 canceled，拿它来写等于「一断线就不记账」，
	// 而被打断恰恰是最需要留痕的时候。超时给得短——它只是兜住库卡死。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	row := store.CallLog{
		APIKeyName:       rec.apiKeyName,
		ClientProtocol:   string(rec.inboundProto),
		UpstreamProtocol: string(rec.channelProto),
		ModelRequested:   rec.requestedModel,
		ModelUpstream:    rec.upstreamModel,
		ChannelName:      rec.channel,
		Status:           rec.status,
		RetryCount:       rec.retries,
		TotalMs:          time.Since(rec.start).Milliseconds(),
	}
	// 只记流式（展开层 §7 该列原文「首字节耗时（流式）」）。非流式也填的话它约等于
	// 总耗时，混合流量下「平均首字延迟」就成了一个没有意义的数。非流式的首字节耗时
	// 仍在 slog 的 ttfb_ms 里，没有丢。
	if rec.stream && !rec.firstByte.IsZero() {
		row.TTFTMs = sql.NullInt64{Int64: rec.firstByte.Sub(rec.start).Milliseconds(), Valid: true}
	}
	if rec.haveSummary {
		row.InputTokens = nullInt(rec.summary.InputTokens)
		row.OutputTokens = nullInt(rec.summary.OutputTokens)
		row.CacheReadTokens = nullInt(rec.summary.CacheReadTokens)
		row.CacheWriteTokens = nullInt(rec.summary.CacheWriteTokens)
	}
	// 表里没有 outcome 列（#22：不动表结构），而「这行为什么不是一次干净的成功」
	// 正是 error 列该承载的。写的是我们自己的固定词表（upstream_error /
	// stream_aborted / unauthorized / rejected），不是上游原文——上游错误文案里
	// 可能带 base_url。
	if rec.outcome != "ok" {
		row.Error = sql.NullString{String: rec.outcome, Valid: true}
	}

	if err := store.InsertCallLog(ctx, s.db, row); err != nil {
		s.log.Error("调用流水落库失败", "err", err)
	}
}

func nullInt(v int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(v), Valid: true}
}
