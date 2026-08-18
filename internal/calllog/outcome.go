// Package calllog 是「一次调用怎么变成一行流水」这件事的家。
//
// 它管的是从鉴权那一刻到收尾落库之间攒下来的全部账：调用打到哪个端点、命中哪条
// 渠道、上游报了多少 token、这次是怎么收场的。对外只有一组**有名字的生命周期
// 事件**，没有裸字段可写——词表拼错、可空列的判据被换掉、顺序不变量被绕过，这些
// 从「读代码才看得出」变成「编译不过」或「包内单测会红」。
//
// 里面**没有**渠道 base_url 与上游凭证，也不该被加进来：日志是最容易被复制粘贴
// 出去的东西，上游密钥不进日志是硬约束。
package calllog

import "database/sql"

// Outcome 是流水 `error` 列的那份固定词表（CONTEXT.md「outcome 词表」，口径层
// v0.70）：**10 个词**，加一个不落库的哨兵 `ok`。
//
// 封闭类型而不是裸 string，是因为这份词表的读者（`group by error` 的人）没有第二个
// 信源：字面量写错一个字母能编译、能上线，只在有人按词聚合时才发现，而那时错的
// 那批行已经躺在库里了。类型一收窄，拼错就是编译不过。
//
// 一档一个词，不叠加：一次调用只落一个。后写覆盖先写——收尾分支比早退分支知道得多。
type Outcome string

// 词表全集。分两半列出只是给人读的，判半区的那份数据在下面的 halves 表里——
// 加第 11 个词时「它属于哪一半」是必答题，漏填的词两个谓词都不认，会被
// outcome_test.go 的全覆盖断言逮住。
const (
	// OK 是**哨兵，不落库**：库里留 NULL，NULL 即「这行没有错误词」。它既不是
	// 拒绝也不是失败，两个谓词都回假。
	OK Outcome = "ok"

	// —— 回绝这一半：网关自己回绝，一个字节都没到上游 ——

	// Unauthorized 是网关 key 不认（鉴权层）。
	Unauthorized Outcome = "unauthorized"
	// Rejected 是构造时的默认词：走到收尾还没有任何分支覆盖过它，说明这次调用
	// 在某个早退分支上停下了。今天唯一还在用它的载体是 count_tokens 撞非
	// Anthropic 渠道那条 501。
	Rejected Outcome = "rejected"
	// ModelNotAllowed 是 key 的 allowed_models 白名单挡下的（口径层 v0.32）。
	ModelNotAllowed Outcome = "model_not_allowed"
	// RateLimited 是全局令牌桶挡下的（口径层 §2.7）。
	RateLimited Outcome = "rate_limited"
	// CompactionUnsupported 是透传渠道未声明认得 compaction_trigger（口径层 v0.54）。
	CompactionUnsupported Outcome = "compaction_unsupported"
	// QueueFull 是渠道并发闸队满（口径层 v0.50/v0.52）。
	QueueFull Outcome = "queue_full"
	// QueueTimeout 是渠道并发闸排队超时。
	QueueTimeout Outcome = "queue_timeout"
	// QueueAbandoned 是客户端在排队途中自己断了。归在这一半而不是失败那半：
	// 它同样一个字节都没碰过上游，与「打过上游之后死掉」不是一类事故。
	QueueAbandoned Outcome = "queue_abandoned"

	// —— 失败这一半：真的向上游发起过之后出的事 ——

	// UpstreamError 是打上游失败（拨不通、握手失败、读超时），或转换路径上
	// 上游回了非 2xx。**透传路径的上游 4xx 不算**：那是一次成功的透传，
	// error 列留空（口径层 v0.28 纪律）。
	UpstreamError Outcome = "upstream_error"
	// StreamAborted 是首字节写出之后断流。状态码早已发出去了，只有这一格
	// 能看出这次其实没说完。
	StreamAborted Outcome = "stream_aborted"
)

// String 是这个词的字面形态，也就是落进 `error` 列与 slog `outcome` 属性的那串字节。
func (o Outcome) String() string { return string(o) }

// Column 是这个词落进 `error` 列时的形态——**写向唯一的定义处**。
//
// 规则一句话：`ok` 落 NULL，其余落词本身。表里没有 outcome 列（不动表结构），
// 而「这行为什么不是一次干净的成功」正是 `error` 列该承载的；成功那一档没有词可
// 写，NULL 就是「这行没有错误词」。
//
// 不落空串：空串与 NULL 在这一列上不能是两态（那样「没有错误」就有了两种写法，
// 按词聚合时得记着两个都判）。读侧统一走 ErrorWord 抹平。
func (o Outcome) Column() sql.NullString {
	if o == OK {
		return sql.NullString{}
	}
	return sql.NullString{String: string(o), Valid: true}
}

// ErrorWord 是 `error` 列的读向：NULL → 空串，有值 → 词本身。与 Column 互为一对。
//
// 接口层对外给空串而不是 null（同 upstream_request_id 的成例，口径层 v0.67 ⑤）：
// 「这行没有错误词」在读者眼里就是「没有」，不必让每个前端各判一次 null。
//
// 是包级函数而不是 Outcome 的方法：读出来的那一列不保证是词表里的词（v0.70 之前
// 的老行、手工改过的行都可能有别的字节），把它硬转成 Outcome 等于在读侧假装
// 类型收窄成立。
func ErrorWord(col sql.NullString) string {
	if !col.Valid {
		return ""
	}
	return col.String
}

// half 是词表的两个半区。零值 halfUnclassified 是**故意**留给「新加的词还没分
// 半区」这一档的：漏进 halves 表的词两个谓词都回假，outcome_test.go 的全覆盖
// 断言随即变红。
type half int

const (
	halfUnclassified half = iota
	halfRefusal
	halfFailure
)

// halves 是「哪个词属于哪一半」的**唯一定义处**。
//
// 一张表而不是两个 switch：分半区这件事是一份数据，写成两处判断就得两处一起改，
// 而「只改了一处」的表现是某个词同时不属于任何一半——那正是加第 11 个词时最容易
// 犯的错。`ok` 不在表里：它是哨兵，既不是回绝也不是失败。
var halves = map[Outcome]half{
	Unauthorized:          halfRefusal,
	Rejected:              halfRefusal,
	ModelNotAllowed:       halfRefusal,
	RateLimited:           halfRefusal,
	CompactionUnsupported: halfRefusal,
	QueueFull:             halfRefusal,
	QueueTimeout:          halfRefusal,
	QueueAbandoned:        halfRefusal,

	UpstreamError: halfFailure,
	StreamAborted: halfFailure,
}

// Refusal 报告这个词是不是「网关自己回绝、一个字节都没到上游」那一半。
//
// 这条线与 `upstream_endpoint` 列的不变量是同一条：回绝的行那一列必然是空串。
func (o Outcome) Refusal() bool { return halves[o] == halfRefusal }

// Failure 报告这个词是不是「真的向上游发起过之后出的事」那一半。
//
// 含拨不通、读超时——那些行有 error_detail，恰恰要知道打的是哪条路径。
func (o Outcome) Failure() bool { return halves[o] == halfFailure }
