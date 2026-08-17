package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/protocol"

	"github.com/gin-gonic/gin"
)

const (
	// ctxCallRecord 是调用日志记录在 gin 上下文里的键。
	ctxCallRecord = "portage.call_record"
	// ctxAPIKey 是本次请求认出来的网关 key。
	//
	// 只放 auth.Key（名字 + 白名单），不放明文也不放 hash：上下文里的东西迟早会被
	// 顺手打进日志。
	ctxAPIKey = "portage.api_key"
)

// callLog 建这次调用的日志记录并保证它**恰好**落一行，无论后面在哪个中间件收场。
//
// 从 relay 里提出来单独成一层，是因为鉴权失败的请求也要落一行（#22）：401 时
// relay 根本不会执行，日志逻辑留在里面就等于「被刷的时候日志里什么都看不到」。
func (s *Server) callLog(ep protocol.Endpoint) gin.HandlerFunc {
	return func(c *gin.Context) {
		rec := &callRecord{
			start:        time.Now(),
			endpoint:     ep.Path,
			inboundProto: ep.Proto,
			outcome:      "rejected",
		}
		c.Set(ctxCallRecord, rec)
		// defer 在 panic 展开时照常执行——首字节后断流那条路径也落得下日志。
		defer func() {
			rec.status = c.Writer.Status()
			s.logCall(rec)
		}()
		c.Next()
	}
}

// callRecordFrom 取出本次调用的日志记录。
//
// 三层中间件是在 Engine() 里一起注册的，取不到只可能是路由被改坏了。这里回一个
// 临时记录而不是 panic：链路挂了不该顺带把请求也打成 500，日志少一行由
// TestEveryRelayEndpointLogsOnce 那类用例去逮。
func callRecordFrom(c *gin.Context) *callRecord {
	if v, ok := c.Get(ctxCallRecord); ok {
		if rec, ok := v.(*callRecord); ok {
			return rec
		}
	}
	return &callRecord{start: time.Now()}
}

// authRelay 是四个转发端点的 key 鉴权。
//
// 认 `api_keys`，与管理端认 session 彻底分离（口径层 §2.7）——两套凭证互不可用。
func (s *Server) authRelay(ep protocol.Endpoint) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, err := auth.Resolve(c.Request.Context(), s.db, c.Request.Header)
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			rec := callRecordFrom(c)
			rec.outcome = "unauthorized"
			// 回显里没有 key 本身，也不说是「不存在」还是「已停用」。
			// 走协议原生格式：回 gin 默认 JSON 的话 harness 认不出来，
			// 表现成解析失败而不是「key 不对」。
			ep.Proto.WriteError(c.Writer, http.StatusUnauthorized, "API key 无效")
			c.Abort()
			return
		case err != nil:
			// 库故障不是鉴权失败。当成 401 会让一次 SQLite 抖动看起来像
			// 「你的 key 突然不认了」，把人引到完全错误的排查方向上。
			s.log.Error("key 鉴权查询失败", "err", err)
			ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "鉴权失败")
			c.Abort()
			return
		}
		callRecordFrom(c).apiKeyName = key.Name
		// allowed_models 的校验**不在这里**：这一层还没读请求体，不知道客户端要哪个
		// 接入点。把 key 传下去，由 relay 在解析出 head.Model 之后判（M3）。
		c.Set(ctxAPIKey, key)
		c.Next()
	}
}

// apiKeyFrom 取出本次请求认出来的网关 key。
//
// 取不到时回零值 auth.Key——它的 AllowedModels 是空串，Allows 一律放行。这是刻意的：
// 唯一能走到「没有 key 却进了 relay」的路径是路由被改坏（authRelay 没挂上），那时
// 请求根本没鉴权过，白名单再严也没意义，不该在这里假装还有一道防线。
func apiKeyFrom(c *gin.Context) auth.Key {
	if v, ok := c.Get(ctxAPIKey); ok {
		if k, ok := v.(auth.Key); ok {
			return k
		}
	}
	return auth.Key{}
}

// authModels 是 /v1/models 的鉴权。
//
// 它也要认：harness 启动时就拉这个表，不认就等于把接入点清单对全网段公开（#22）。
// 不建 callRecord——`call_logs` 每行描述的是一次**转发调用**（模型、渠道、上游协议、
// token），而这个端点一样都没有，塞一行进去只会是一条除端点外全空的记录。加了
// endpoint 列（#17）也不改这一条：那一列是为了把四个转发端点分开，不是为了把非转发
// 路径也收进流水。它的 401 走结构化日志。
func (s *Server) authModels() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, err := auth.Resolve(c.Request.Context(), s.db, c.Request.Header)
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			s.log.Warn("拒绝未鉴权的模型列表请求", "path", c.Request.URL.Path)
			// /v1/models 本身就是 OpenAI 公开格式，错误也照 OpenAI 的形状回，
			// 与 models handler 里的 500 分支一致。
			protocol.OpenAI.WriteError(c.Writer, http.StatusUnauthorized, "API key 无效")
			c.Abort()
			return
		case err != nil:
			s.log.Error("key 鉴权查询失败", "err", err)
			protocol.OpenAI.WriteError(c.Writer, http.StatusInternalServerError, "鉴权失败")
			c.Abort()
			return
		}
		c.Next()
	}
}
