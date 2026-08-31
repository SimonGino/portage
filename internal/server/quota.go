package server

import (
	"net/http"
	"time"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
)

// quotaGate 是每用户月度 USD 配额闸（口径层 §2.10，#65/#75）。
//
// 位置钉死在全局令牌桶之后、Resolve 之前：令牌桶是防刷账单的第一道，被扫的流量
// 不该先花一次 SUM 查询；而配额判据只要 user_id，不必等 Resolve——省一次路由。
//
// 判据 = 本月（UTC 自然月）流水 SUM(cost) ≥ 限额即拒 429。**不预扣**：判过闸的
// 请求跑出多少账落多少账，最后一笔允许轻微超支（与落库时点计价同精神）。NULL 限额
// 不限（默认），0 = 封停——判据同一条（SUM ≥ 0 恒真），不开分支。调额立即生效：
// 每次现算 SUM，没有计数器可失效。
//
// 三档豁免：无主 key（UserID nil，声明形态不入用户账）、count_tokens（不打生成面，
// 且它正是客户端用来判断「还能不能发」的工具——判端点不判协议，pickLimiter 的同款
// 坑）、以及压根没挂闸的非转发路由。
func (s *Server) quotaGate(ep protocol.Endpoint) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ep == protocol.EndpointCountTokens {
			c.Next()
			return
		}
		key := apiKeyFrom(c)
		if key.UserID == nil {
			c.Next()
			return
		}
		q, err := store.UserQuotaState(c.Request.Context(), s.db, *key.UserID, time.Now())
		if err != nil {
			// 库故障不是配额超限，也不放行：放行等于「库一抖配额闸就消失」。
			s.log.Error("查配额失败", "user_id", *key.UserID, "err", err)
			ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "配额校验失败")
			c.Abort()
			return
		}
		if q.LimitUSD == nil || q.SpentUSD < *q.LimitUSD {
			c.Next()
			return
		}
		recorderFrom(c).Refused(calllog.QuotaExceeded)
		msg := "本月配额已用尽，下月自动恢复；调额请联系管理员"
		if *q.LimitUSD == 0 {
			msg = "配额为 0：该账号的转发已封停，请联系管理员"
		}
		ep.Proto.WriteError(c.Writer, http.StatusTooManyRequests, msg)
		c.Abort()
	}
}
