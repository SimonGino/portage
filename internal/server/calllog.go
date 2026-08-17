package server

import (
	"context"
	"database/sql"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/upstream"
)

// bodyCaptureLimit 是 log_bodies 打开时单侧 body 的记录上限。开这个开关是为了排障，
// 不是为了留档；一条长流的完整响应进日志只会把日志冲垮。
const bodyCaptureLimit = 64 << 10

// errorDetailLimit 是**落库**的上游错误原文上限（口径层 v0.53）。比 body 那个小两个
// 数量级：错误体是给人读的一段话，2KB 之后基本只剩上游自己的请求快照与堆栈；而
// 这一列每条失败流水都占一份，长期躺在库里。
const errorDetailLimit = 2 << 10

// upstreamErrorLimit 是上游错误原文的**内存**上限：转换路径读到这么多就停，透传路径的
// 旁路收集器收到这么多就停。它防着一个巨大的 HTML 错误页把内存吃满。
//
// 比落库那个上限大两个数量级是口径层 v0.74 要的：`request_id` 在 Anthropic 错误信封里
// 排在 `error` 对象**之后**，超长错误原文按 2KB 截完正好把它截在外面，而取键必须在
// 截断前的字节上做。所以内存里收完整的一段，落库那一刻再截（captureWriter.truncatedTo）。
const upstreamErrorLimit = 64 << 10

// truncationMark 是截断说出口的那句。截断这件事本身要说出来——否则一段被砍掉一半的
// JSON 看起来像上游发了个坏包。
const truncationMark = "…[truncated]"

// captureWriter 是挂在旁路上的定量收集器。和 Tap 一样：永不报错，否则 io.MultiWriter
// 会把错误变成读错误、打断转发。
//
// limit 由构造处给，因为两种用途的合理上限差两个数量级（见 bodyCaptureLimit 与
// errorDetailLimit）。
type captureWriter struct {
	limit     int
	buf       []byte
	truncated bool
}

func newCapture(limit int) *captureWriter { return &captureWriter{limit: limit} }

func (c *captureWriter) Write(p []byte) (int, error) {
	if room := c.limit - len(c.buf); room > 0 {
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
		return string(c.buf) + truncationMark
	}
	return string(c.buf)
}

// collected 是收下的原始字节，未经二次截断。给 request_id 取键用（口径层 v0.74）：
// 那个键排在错误信封末尾，只有完整字节上取得到。
func (c *captureWriter) collected() []byte { return c.buf }

// truncatedTo 按一个更小的上限二次截断。错误原文用得到它：内存里要留完整字节给
// request_id 取键（v0.74），落库只留 2KB（v0.53）。
func (c *captureWriter) truncatedTo(limit int) string {
	if len(c.buf) > limit {
		return string(c.buf[:limit]) + truncationMark
	}
	return c.String()
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
	// channelKey 是本次**真正发出请求**的那份上游凭证的名字（换过凭证时是最后一
	// 份，失败亦然）。名字不是凭证值——网关不从凭证值派生任何显示字符（口径层
	// v0.38），日志里出现的永远只是人自己写的那个名字。
	channelKey    string
	channelProto  protocol.Protocol
	upstreamModel string
	stream        bool
	status        int
	// outcome 区分「同样是 200」的几种收场，尤其是首字节之后断流那种——
	// 状态码已经发出去了，只有这里能看出它其实没说完。
	outcome string
	// retries 是这次调用向上游重打了几次（含换凭证之后的那些，口径层 v0.38）。
	// 不含首次尝试，0 是常态所以不打进日志——否则每一行都要背一个恒为 0 的字段。
	// 非 0 才说明发生过退避重试或换过凭证，「这次怎么慢了三秒」有据可查。
	retries int
	// queueWait 是在渠道并发闸上排队的耗时（口径层 v0.52），没排队为 0。
	// 与 retries 同理，非 0 才进 slog；落库则恒落（列不可空，0 就是 0）。
	queueWait time.Duration

	// upstreamRequestID 是最终落库与进 slog 的那个上游请求 id（口径层 v0.56，#37）。
	// 它同时原样回传给了客户端（透传路径）；留一份在流水里，是为了事后不用翻客户端
	// 日志就能对账。
	//
	// 不是凭证材料，进日志无碍：这是上游自己给这次调用编的号，报障时官方文档就要它。
	//
	// 值由 resolveUpstreamRequestID 在收尾时从下面两档候选加错误体里那一档定下来
	// （口径层 v0.74），别在别处直接赋值。
	upstreamRequestID string
	// officialRequestID 是第一档：响应头里官方拼写的 `request-id`，Anthropic 自己写的。
	officialRequestID string
	// proxyRequestID 是第三档：响应头里的 `x-request-id`。排最后是因为它多半是中间层
	// 给自己编的号——2026-08-15 实测那条链路上它**总是**有值，插在前面就等于永远只
	// 记中转的流水号，拿去问官方什么都查不到，比空着更误导。
	proxyRequestID string

	summary     protocol.Summary
	haveSummary bool

	requestBody  *captureWriter
	responseBody *captureWriter

	// errorDetail 是上游错误原文（口径层 v0.53），只在失败时有值。收到
	// upstreamErrorLimit，落库那一刻截到 errorDetailLimit。
	//
	// 是 *captureWriter 而不是 string，因为透传路径上它得挂在旁路上边转发边收——
	// 那条链路不允许为了记一份错误体把响应先读进内存再转出去。另外两个来源（转换
	// 路径已经读到的原始字节、传输错误的 Redact 文本）用 setErrorDetail 写进来。
	//
	// 它同时是 request_id 第二档的字节来源（口径层 v0.74）：那一档「复用 error_detail
	// 已经读进来的字节」，不新开一次 body 读，因此不违反透传路径不 decode→encode。
	errorDetail *captureWriter
}

// resolveUpstreamRequestID 按三档定下这次调用的上游请求 id（口径层 v0.74）：
// `request-id`（头）→ 错误响应体里的 `request_id` → `x-request-id`（头）。
//
// 前两档都是 Anthropic 自己写的，第三档是中间层给自己编的号，所以新档插在中间。
//
// 在收尾时才做而不是拿到响应头就做，是因为第二档要等错误体收完——透传路径上那份字节
// 是边转发边攒的，最后一个字节到这一刻才齐。
//
// 第二档只有失败行有货：rec.errorDetail 只在失败时才被挂上（透传路径按 status >= 400
// 挂旁路，转换路径在写错误时 setErrorDetail），成功行走不到这里的 json 解析。
func (r *callRecord) resolveUpstreamRequestID() string {
	if r.officialRequestID != "" {
		return r.officialRequestID
	}
	if r.errorDetail != nil {
		if id := upstream.ErrorBodyRequestID(r.errorDetail.collected()); id != "" {
			return id
		}
	}
	return r.proxyRequestID
}

// setErrorDetail 记下一段已经拿在手里的错误原文。**不覆盖已有的**：透传路径的旁路
// 收集器先挂上，之后的收尾分支不该把它抹成一句概括。
func (r *callRecord) setErrorDetail(s string) {
	if r.errorDetail != nil || s == "" {
		return
	}
	r.errorDetail = newCapture(upstreamErrorLimit)
	_, _ = r.errorDetail.Write([]byte(s))
}

func (s *Server) logCall(rec *callRecord) {
	// 三档取值到这一刻才定得下来（口径层 v0.74）：中间那档在错误体里，而错误体是边
	// 转发边收的。定一次写回 rec，下面的 slog 与 persistCall 因此同源，两条转发路径
	// （透传 / 转换）也都只经过这一处——对账与走哪条路无关（v0.56 原则）。
	rec.upstreamRequestID = rec.resolveUpstreamRequestID()

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
	if rec.channelKey != "" {
		attrs = append(attrs, "channel_key", rec.channelKey)
	}
	if rec.retries > 0 {
		attrs = append(attrs, "retries", rec.retries)
	}
	if rec.queueWait > 0 {
		attrs = append(attrs, "queue_wait_ms", rec.queueWait.Milliseconds())
	}
	// 与 retries 同理，只在有值时进 slog：上游不回这个头的部署（自建、部分中转）
	// 每一行都背一个空字段没有意义。落库则恒落（列不可空，空串就是「没有」）。
	if rec.upstreamRequestID != "" {
		attrs = append(attrs, "upstream_request_id", rec.upstreamRequestID)
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
		// 只在上游真报了这一格时进 slog——没报的时候多一个 reasoning_tokens=0
		// 会被读成「这次没思考」，与流水那一列留 NULL 的理由相同。
		if sum.HasReasoningTokens {
			attrs = append(attrs, "reasoning_tokens", sum.ReasoningTokens)
		}
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
		ChannelKeyName:   rec.channelKey,
		Status:           rec.status,
		RetryCount:       rec.retries,
		// 三档都落空、或根本没走到上游时是空串（口径层 v0.56、v0.74）。
		UpstreamRequestID: rec.upstreamRequestID,
		TotalMs:           time.Since(rec.start).Milliseconds(),
		QueueWaitMs:       rec.queueWait.Milliseconds(),
	}
	// stream 是解析请求体那一步才知道的（server.go 里与 requestedModel 同一行赋值），
	// 没走到那一步的行（鉴权失败、body 不是合法 JSON）留 NULL——落一个 false 会把
	// 「不知道」说成「同步」。判据借 requestedModel：两者同源，空串即没解析到。
	if rec.requestedModel != "" {
		row.IsStream = sql.NullBool{Bool: rec.stream, Valid: true}
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
		// 思考 token 单独判（口径层 v0.66，#97）：上面四个是「有 summary 就一定有
		// 数」，这一格不是——Anthropic 协议里根本没有它，CC 上游挂非推理模型时也
		// 整个 details 都不发。没报就留 NULL，别落 0：落 0 是在说「上游报了，这次
		// 没思考」，而我们其实什么都不知道。
		if rec.summary.HasReasoningTokens {
			row.ReasoningTokens = nullInt(rec.summary.ReasoningTokens)
		}
	}
	// 表里没有 outcome 列（#22：不动表结构），而「这行为什么不是一次干净的成功」
	// 正是 error 列该承载的。写的是我们自己的固定词表（upstream_error /
	// stream_aborted / unauthorized / rejected，并发闸批加 queue_full /
	// queue_timeout / queue_abandoned，口径层 v0.52；压缩止血批加
	// compaction_unsupported，口径层 v0.54），不是上游原文——上游错误
	// 文案里可能带 base_url。
	if rec.outcome != "ok" {
		row.Error = sql.NullString{String: rec.outcome, Valid: true}
	}
	// 上游原文另落一列（口径层 v0.53）。它与 error 列是两件事，也不同步出现：上游
	// 透传 4xx 的 error 列是空的（透传成功不算网关侧错误，v0.28 纪律），detail 却有
	// 值——「可展开」的判据因此是 status >= 400，不是 error 非空。
	// 落库这一刻才截到 2KB：内存里收的是完整的一段（upstreamErrorLimit），因为
	// request_id 取键要在截断前的字节上做（v0.74）。
	if rec.errorDetail != nil {
		row.ErrorDetail = sql.NullString{String: rec.errorDetail.truncatedTo(errorDetailLimit), Valid: true}
	}

	if err := store.InsertCallLog(ctx, s.db, row); err != nil {
		s.log.Error("调用流水落库失败", "err", err)
	}
}

func nullInt(v int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(v), Valid: true}
}
