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

// writeDeadline 是单次向客户端写出的上限。非流式够用；流式在 #3 改为每帧推进。
const writeDeadline = 30 * time.Second

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
	r.Use(gin.Recovery())
	r.GET("/healthz", s.healthz)
	r.POST(protocol.EndpointMessages.Path, s.relay(protocol.EndpointMessages))
	return r
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

// requestHead is the only part of the client body the gateway parses. Everything
// forwarded upstream is still the original bytes — no re-marshal.
type requestHead struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func (s *Server) relay(ep protocol.Endpoint) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "读取请求体失败")
			return
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
			s.log.Error("上游请求失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
			ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游渠道 "+cand.ChannelName+" 请求失败")
			return
		}
		defer resp.Body.Close()

		// 防慢客户端把 handler 永久挂住（§6.1）。#3 的流式拷贝里改为每帧推进。
		if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil {
			s.log.Warn("写超时设置未生效", "err", err)
		}

		upstream.CopyResponseHeaders(c.Writer.Header(), resp.Header)
		c.Writer.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(c.Writer, resp.Body); err != nil {
			// 首字节已写出，格式承诺已生效：只能断连并记日志，不改写、不重发。
			s.log.Warn("首字节写出后透传中断", "channel", cand.ChannelName, "err", upstream.Redact(err))
		}
	}
}
