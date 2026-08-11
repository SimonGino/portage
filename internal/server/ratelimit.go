package server

import (
	"net/http"

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
// 口径层 §2.7（v0.15 定）：**单个**全局令牌桶，默认 10 QPS / 突发 20，不分 key/IP
// 维度。目的只有一个——key 泄露或被扫时钳制上游账单损失，不是公平调度。所以它必须
// 是进程级的一只桶：按端点各造一只会变成 4×10 QPS，按 key 造更是泄露了哪把 key 就
// 白配了哪把桶。
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
	return func(c *gin.Context) {
		if s.lim == nil || s.lim.Allow() {
			c.Next()
			return
		}
		callRecordFrom(c).outcome = "rate_limited"
		// Retry-After 要赶在 WriteError 之前设：那里面就 WriteHeader 了，
		// 之后再往 Header() 里写什么都不会发出去。
		c.Writer.Header().Set("Retry-After", retryAfterSeconds)
		ep.Proto.WriteError(c.Writer, http.StatusTooManyRequests, "网关限流，请稍后重试")
		c.Abort()
	}
}
