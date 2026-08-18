package server

import (
	"net/http"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// retryAfterSeconds 是超限时回给客户端的 Retry-After，固定 1 秒。
//
// 不按剩余亏空算：Retry-After 的单位是整秒，而默认 10 QPS 下一个令牌 100ms 就回来，
// 算出来的真值一律不足 1 秒、只能向上取整成 1。真去用 rate.Reserve() 拿精确延迟还得
// 记得 Cancel() 把令牌还回去（漏了就等于每次被拒都再扣一个），为一个恒等于 1 的
// 结果背这个风险不划算。
const retryAfterSeconds = "1"

// newLimiter 按配置造全局令牌桶，qps <= 0 时回 nil 表示不限流。
//
// 口径层 §2.7（v0.15 定，v0.81 修订桶数）：全局令牌桶，默认 10 QPS / 突发 20，不分
// key/IP 维度。目的只有一个——key 泄露或被扫时钳制上游账单损失，不是公平调度。所以
// 桶必须是进程级的、且少：无脑按端点各造一只就是 4 只、配 10 实收 40 QPS，按 key 造
// 更是泄露了哪把 key 就白配了哪把桶。
//
// v0.81（#16）挖开的**唯一**端点级例外：count_tokens 另有一只，见 pickLimiter。桶数
// 从此刻意停在 2，两只都用同一份 rate_limit_qps/burst 造，最坏总速率 2×10 QPS。
func newLimiter(qps, burst int) *rate.Limiter {
	if qps <= 0 {
		return nil
	}
	if burst <= 0 {
		// 只写了 qps 时兜底，否则桶容量为 0，Allow 永远为假、整个转发面直接瘫掉。
		burst = 20
	}
	return rate.NewLimiter(rate.Limit(qps), burst)
}

// rateLimit 是全局限流中间件。
//
// **挂在鉴权之后**（实现口径，见 docs/MVP设计草案.md §7.2）：没过鉴权的请求根本到不了
// 上游、进不了账单，让它们消耗令牌等于给扫描流量一把饿死合法请求的刀。代价是被扫时
// 网关自己仍要为每个请求查一次 key——那是 SQLite 的一次索引命中，与「刷爆上游账单」
// 不是一个量级的损失。
//
// 只挂转发面那四个 POST：`/healthz` 是探活（限它等于让监控在忙时先报警），
// `/v1/models` 不打上游，`/admin` 走的是另一套凭证、把自己限出管理端毫无意义。
func (s *Server) rateLimit(ep protocol.Endpoint) gin.HandlerFunc {
	lim := s.pickLimiter(ep)
	return func(c *gin.Context) {
		if lim == nil || lim.Allow() {
			c.Next()
			return
		}
		callRecordFrom(c).Refused(calllog.RateLimited)
		// Retry-After 要赶在 WriteError 之前设：那里面就 WriteHeader 了，
		// 之后再往 Header() 里写什么都不会发出去。
		c.Writer.Header().Set("Retry-After", retryAfterSeconds)
		ep.Proto.WriteError(c.Writer, http.StatusTooManyRequests, "网关限流，请稍后重试")
		c.Abort()
	}
}

// pickLimiter 选这个端点该用哪只桶：count_tokens 一只，生成面那三个共用另一只。
//
// 为什么单拎 count_tokens（#16，PO 裁定 jinpenga 2026-08-17）：Claude Code 一开场
// 连打二十几次 count_tokens，默认 10/20 挡不住，桶被打空、紧随其后的 /v1/messages
// 被 429 掉（真机 26 次 count_tokens → 11 条 429）。而 count_tokens 命中非 Anthropic
// 渠道时就地回绝、一个字节都不打上游——桶是为「防账单」立的，却被一批到不了上游的
// 请求消耗光，饿死了真正要打上游的那些。
//
// 不是整个豁免：count_tokens 在 Anthropic 出口确实打上游，所以它自己那只桶照样限。
// 也不用 Reserve()+Cancel() 把就地回绝的令牌还回去——那要把限流从「中间件里 Allow
// 完就走」改成跨中间件持有 reservation，且 count_tokens 改成本地估算回 200 之后
// （#18）那机制就不起作用了，这一票的病根是共用桶本身。
//
// 判据是**端点**不是入口协议：count_tokens 与 /v1/messages 的 ep.Proto 同为
// anthropic，按协议判会把 /v1/messages 一起分到这只桶里（同 conversionOpen 的坑）。
func (s *Server) pickLimiter(ep protocol.Endpoint) *rate.Limiter {
	if ep == protocol.EndpointCountTokens {
		return s.countTokensLim
	}
	return s.genLim
}
