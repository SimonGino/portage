// Package server wires the inbound HTTP endpoints to the passthrough relay.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/SimonGino/ai-gateway/internal/config"
	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/store"
	"github.com/SimonGino/ai-gateway/internal/upstream"

	"github.com/gin-gonic/gin"
)

const (
	// writeDeadline 是单次向客户端写出的上限，每写一块推进一次。它约束的是「客户端
	// 收得多慢」，不是「流总共多长」——所以长流不会被它掐断，挂死的慢客户端会。
	writeDeadline = 30 * time.Second

	// copyBufferSize 是透传的读写块大小。按字节块复制、永不按帧切分：透传路径对 SSE
	// 帧边界一无所知，因此并行工具调用那种远超缓冲区的大参数帧也不会被截断。
	copyBufferSize = 32 * 1024
)

// relayBody 把上游响应按字节块复制给客户端，每块 flush 一次。
//
// 不用 io.Copy：它不 flush，SSE 帧会攒在 net/http 的缓冲里，客户端要等攒满或流结束
// 才看得到——正是「逐字输出」失效的成因。也不用 bufio.Scanner 按行读再重组：那会引入
// 换行/空行的重写风险，且 Scanner 的 token 上限会变成透传路径的截断上限。
func relayBody(w gin.ResponseWriter, body io.Reader, onFirstByte func()) error {
	rc := http.NewResponseController(w)
	// 先兜住响应头本身与空 body 的情形，之后每写一块再推进一次。
	if err := advanceWriteDeadline(rc); err != nil {
		return err
	}
	buf := make([]byte, copyBufferSize)
	first := true
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if first {
				first = false
				onFirstByte()
			}
			if err := advanceWriteDeadline(rc); err != nil {
				return err
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// advanceWriteDeadline 把「这一次写出」的截止时间往后推。ErrNotSupported 说明底层
// writer 不支持 deadline（本项目的 gin ResponseWriter 支持），不该因此中断透传。
func advanceWriteDeadline(rc *http.ResponseController) error {
	if err := rc.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

type Server struct {
	cfg config.Config
	db  *sql.DB
	up  *upstream.Client
	log *slog.Logger
}

func New(cfg config.Config, db *sql.DB, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, db: db, up: upstream.NewClient(), log: log}
}

func (s *Server) Engine() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(s.recovery())
	r.GET("/healthz", s.healthz)
	r.GET("/v1/models", s.models)
	for _, ep := range []protocol.Endpoint{
		protocol.EndpointMessages,
		protocol.EndpointCountTokens,
		protocol.EndpointChatCompletions,
		protocol.EndpointResponses,
	} {
		r.POST(ep.Path, s.relay(ep))
	}
	return r
}

// recovery 与 gin.Recovery 只差一处：http.ErrAbortHandler 原样再抛给 net/http。
//
// gin 把它归进 broken pipe 分支 recover 掉，于是响应被正常收尾——chunked 的终止块
// 照发，客户端看到的是一个「干净结束」的流。而透传中途失败时我们要的恰恰相反：
// 连接必须异常终止，客户端才能区分「上游说完了」和「上游死了」。net/http 自己的
// recover 认得 ErrAbortHandler，会静默断连，正是这个语义。
func (s *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			s.log.Error("handler panic", "path", c.Request.URL.Path, "panic", rec)
			if !c.Writer.Written() {
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			c.Abort()
		}()
		c.Next()
	}
}

func (s *Server) healthz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// models 按 OpenAI models 列表格式列出全部未停用接入点——harness 启动时会拉它。
// 对外暴露的是接入点名，不是纳管模型名：纳管模型是渠道的内部事实，不该泄露给客户端。
func (s *Server) models(c *gin.Context) {
	points, err := store.ListAccessPoints(c.Request.Context(), s.db)
	if err != nil {
		s.log.Error("列接入点失败", "err", err)
		protocol.OpenAICC.WriteError(c.Writer, http.StatusInternalServerError, "接入点列表读取失败")
		return
	}

	data := make([]gin.H, 0, len(points))
	for _, ap := range points {
		data = append(data, gin.H{
			"id":       ap.Model,
			"object":   "model",
			"created":  ap.CreatedAt,
			"owned_by": "ai-gateway",
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// requestHead is the only part of the client body the gateway parses. Everything
// forwarded upstream is still the original bytes — no re-marshal.
type requestHead struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func (s *Server) relay(ep protocol.Endpoint) gin.HandlerFunc {
	return func(c *gin.Context) {
		rec := &callRecord{
			start:        time.Now(),
			endpoint:     ep.Path,
			inboundProto: ep.Proto,
			outcome:      "rejected",
		}
		// 一次调用一行日志，无论走到哪个分支收场——包括首字节后断流那条
		// panic 路径（defer 在 panic 展开时照常执行）。
		defer func() {
			rec.status = c.Writer.Status()
			s.logCall(rec)
		}()

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "读取请求体失败")
			return
		}
		if s.cfg.LogBodies {
			rec.requestBody = &captureWriter{}
			_, _ = rec.requestBody.Write(body)
		}

		var head requestHead
		if err := json.Unmarshal(body, &head); err != nil {
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		if head.Model == "" {
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "请求体缺少 model 字段")
			return
		}
		rec.accessPoint, rec.stream = head.Model, head.Stream

		cand, err := store.Resolve(c.Request.Context(), s.db, head.Model)
		switch {
		case errors.Is(err, store.ErrAccessPointNotFound):
			ep.Proto.WriteError(c.Writer, http.StatusNotFound, "接入点 "+head.Model+" 不存在或已停用")
			return
		case errors.Is(err, store.ErrNoUsableCandidate):
			ep.Proto.WriteError(c.Writer, http.StatusServiceUnavailable, "接入点 "+head.Model+" 没有可用候选")
			return
		case err != nil:
			s.log.Error("接入点解析失败", "model", head.Model, "err", err)
			ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "接入点解析失败")
			return
		}

		rec.channel, rec.channelProto, rec.upstreamModel = cand.ChannelName, cand.Protocol, cand.UpstreamModel

		// 临时闸：转换路径未实现前，入口协议必须等于命中候选所在渠道的协议。
		if cand.Protocol != ep.Proto {
			ep.Proto.WriteError(c.Writer, http.StatusNotImplemented,
				"该转换路径尚未实现："+string(ep.Proto)+" → "+string(cand.Protocol))
			return
		}

		// 接入点对外模型名 → 纳管模型名（口径层 §2.3）。字节级 splice，不整体重编码。
		forward, err := protocol.RewriteModel(body, cand.UpstreamModel)
		if err != nil {
			s.log.Error("改写 model 字段失败", "channel", cand.ChannelName, "err", err)
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "请求体 model 字段无法改写")
			return
		}

		resp, err := s.up.Do(c.Request.Context(), cand, ep, forward, c.Request.Header, head.Stream)
		if err != nil {
			// 只报渠道名；Redact 摘掉传输错误里内嵌的 base_url。
			rec.outcome = "upstream_error"
			s.log.Error("上游请求失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
			ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游渠道 "+cand.ChannelName+" 请求失败")
			return
		}
		defer resp.Body.Close()

		// Tap 与 body 记录都挂旁路：拿到的是与转发**同一份**字节，且都写不坏
		// 转发——它们的 Write 恒不报错，io.MultiWriter 因此也不会。
		var observers []io.Writer
		if tap := newTap(cand.Protocol, head.Stream); tap != nil {
			observers = append(observers, tap)
			defer func() {
				rec.summary, rec.haveSummary = tap.Summary(), true
			}()
		}
		if s.cfg.LogBodies {
			rec.responseBody = &captureWriter{}
			observers = append(observers, rec.responseBody)
		}
		src := io.Reader(resp.Body)
		if len(observers) > 0 {
			src = io.TeeReader(resp.Body, io.MultiWriter(observers...))
		}

		upstream.CopyResponseHeaders(c.Writer.Header(), resp.Header)
		c.Writer.WriteHeader(resp.StatusCode)
		rec.outcome = "ok"
		if err := relayBody(c.Writer, src, func() { rec.firstByte = time.Now() }); err != nil {
			// 响应头已发出，格式承诺已生效：不改写、不重发，只能断连并记日志（§6）。
			rec.outcome = "stream_aborted"
			s.log.Warn("首字节写出后透传中断", "channel", cand.ChannelName, "err", upstream.Redact(err))
			panic(http.ErrAbortHandler)
		}
	}
}
