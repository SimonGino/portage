package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/SimonGino/ai-gateway/internal/auth"
	"github.com/SimonGino/ai-gateway/internal/protocol"

	"github.com/gin-gonic/gin"
)

// ctxCallRecord 是调用日志记录在 gin 上下文里的键。
const ctxCallRecord = "aig.call_record"

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
		c.Next()
	}
}

// authModels 是 /v1/models 的鉴权。
//
// 它也要认：harness 启动时就拉这个表，不认就等于把接入点清单对全网段公开（#22）。
// 不建 callRecord——`call_logs` 每行描述的是一次**转发调用**（入站/上游协议、模型、
// 渠道），表里没有 endpoint 列，塞一行进去只会是分不清来路的空记录。它的 401 走
// 结构化日志。
func (s *Server) authModels() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, err := auth.Resolve(c.Request.Context(), s.db, c.Request.Header)
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			s.log.Warn("拒绝未鉴权的模型列表请求", "path", c.Request.URL.Path)
			// /v1/models 本身就是 OpenAI 公开格式，错误也照 OpenAI 的形状回，
			// 与 models handler 里的 500 分支一致。
			protocol.OpenAICC.WriteError(c.Writer, http.StatusUnauthorized, "API key 无效")
			c.Abort()
			return
		case err != nil:
			s.log.Error("key 鉴权查询失败", "err", err)
			protocol.OpenAICC.WriteError(c.Writer, http.StatusInternalServerError, "鉴权失败")
			c.Abort()
			return
		}
		c.Next()
	}
}
