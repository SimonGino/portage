package calllog

import (
	"database/sql"
	"encoding/json"
)

// Row is one row of call_logs: a single relayed call as it ended.
//
// 定义在这里而不是 store，是依赖方向逼出来的：`internal/upstream` 已经 import 了
// `internal/store`，把写侧行类型留在 store 就会成环（store → calllog → upstream →
// store）。store 用类型别名接回去（`store.CallLog`），SQL 与调用点一个字不动。
// 代价是「存储包依赖日志包」读起来反直觉，接受。
//
// 与读侧的 `store.CallLogRow` **刻意不合并**：字段集有实质差异（读侧多 id 与
// created_at，写侧多 queue_wait_ms）。合并会让写路径带两个永远为零的字段、读路径
// 带一个永远不查的字段，还得再造一种「没有」的表达法（零值 id = 还没落库）。
// 两者收拢的只有可空性规则，那已经在 Outcome.Column / ErrorWord 里收完了。
//
// 可空的字段用 sql.NullInt64 而不是 0：首字节耗时与 token 数「没有」和「是 0」不是
// 一回事——非流式请求本就没有首字节耗时，记成 0 会让「首字延迟」的统计凭空多出一堆
// 满分样本。
//
// **不可空的那几列上，空串就是「没有」**（UpstreamEndpoint / ChannelKeyName /
// UpstreamRequestID）：这几列上「没走到那一步」与「走到了但是空的」在读者眼里是
// 同一件事，不值得为它再开一档 NULL。这条约定只在这里写一次，各字段不再复述。
type Row struct {
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
	// Error 是网关固定词表落库后的形态。可空性规则见 Outcome.Column（写向）与
	// ErrorWord（读向）。
	Error sql.NullString
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

// ErrorBodyRequestID 从上游**错误**响应体里取顶层 `request_id`（口径层 v0.74）。
//
// 这一档是实测撞出来的：应用层中转会把官方响应头裁掉（小写 `request-id` 与
// anthropic-ratelimit-* 一个都不到），只回一个自己编号的 `X-Request-Id`；而官方那个号
// 还在错误信封里——`{"type":"error","error":{…},"request_id":"req_011Ce2…"}`。
//
// 住在这个包而不是 internal/upstream：它唯一的用处是给流水那一列的三档取值供第二
// 档，而三档取值在收尾时才做得了（错误体是边转发边收的）。本包又绝不能反过来
// import upstream——那会成环（store → calllog → upstream → store）。头上那两档
// 仍归 upstream 取（它读的是响应头，那是 upstream 的事）。
//
// 只在失败行调用：2026-08-15 五份真实响应实测，成功响应的体里（流式与非流式）都没有
// 这个字段，成功行不该为它多解一次 body。
//
// 解不出来一律回空串，不报错也不告警：不是 JSON（HTML 错误页）、截断的字节、没有这个
// 键、流式错误帧（本库无样本，形状未知）——这几种情况在对账上是同一件事，都是「没有
// 可用的 id」。
func ErrorBodyRequestID(raw []byte) string {
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return payload.RequestID
}
