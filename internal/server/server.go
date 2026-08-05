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

		route, err := store.Resolve(c.Request.Context(), s.db, head.Model)
		switch {
		case errors.Is(err, store.ErrAccessPointNotFound):
			ep.Proto.WriteError(c.Writer, http.StatusNotFound, "接入点 "+head.Model+" 不存在或已停用")
			return
		case errors.Is(err, store.ErrNoUsableCandidate):
			ep.Proto.WriteError(c.Writer, http.StatusServiceUnavailable, "接入点 "+head.Model+" 没有可用候选")
			return
		case err != nil:
			s.log.Error("resolve access point", "model", head.Model, "err", err)
			ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "路由解析失败")
			return
		}

		// 临时闸：转换路径未实现前，入口协议必须等于命中渠道的协议。
		if route.Protocol != ep.Proto {
			ep.Proto.WriteError(c.Writer, http.StatusNotImplemented,
				"该转换路径尚未实现："+string(ep.Proto)+" → "+string(route.Protocol))
			return
		}

		resp, err := s.up.Do(c.Request.Context(), route, ep, body, c.Request.Header, head.Stream)
		if err != nil {
			// 错误摘要不得携带上游 base_url 与凭证，因此只报渠道名。
			s.log.Error("upstream request failed", "channel", route.ChannelName, "err", err)
			ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游渠道 "+route.ChannelName+" 请求失败")
			return
		}
		defer resp.Body.Close()

		upstream.CopyResponseHeaders(c.Writer.Header(), resp.Header)
		c.Writer.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(c.Writer, resp.Body); err != nil {
			// 首字节已写出，格式承诺已生效：只能断连并记日志，不改写、不重发。
			s.log.Warn("relay interrupted after first byte", "channel", route.ChannelName, "err", err)
		}
	}
}
