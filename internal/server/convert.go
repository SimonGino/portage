package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/protocol/codecs"
	"github.com/SimonGino/ai-gateway/internal/protocol/taps"
	"github.com/SimonGino/ai-gateway/internal/store"
	"github.com/SimonGino/ai-gateway/internal/upstream"

	"github.com/gin-gonic/gin"
)

// 本文件是转换路径。同协议透传仍走 server.go 的字节复制那条——「透传保真优先」是
// 硬约束，同协议不做 decode→encode 转码。

// conversionOpen 报告「入口端点 → 渠道协议」这一格闸是否已放开。
//
// 判据刻意是**端点**而不是入口协议：/v1/messages 与 /v1/messages/count_tokens 的
// ep.Proto 都是 anthropic，按协议对开闸会把 count_tokens 一起放进转换分支，而 CC
// 那边根本没有对应端点可转。count_tokens 命中非 anthropic 渠道仍回 501。
//
// 每落地一条路径在这里加一格，没落地的仍报「该转换路径尚未实现」。
func conversionOpen(ep protocol.Endpoint, channel protocol.Protocol) bool {
	switch {
	case ep == protocol.EndpointMessages && channel == protocol.OpenAICC:
		return true // A→CC（#11，口径层 §2.1 优先级①上半）
	case ep == protocol.EndpointResponses && channel == protocol.OpenAICC:
		return true // R→CC（#12，优先级①下半）
	}
	return false
}

// relayConverted 跑一条转换路径：入口 codec 解成 canonical，渠道 codec 编出去，
// 响应方向反过来。
//
// 与透传路径共享的口径一条不改：首字节边界即承诺边界（写出去之后不改写、不重发，
// 只能断连）、Tap 挂在**上游原始字节**上（usage 出自上游自己说的数，不是我们编出来
// 的响应）、错误回显不带上游 key 与 base_url。
func (s *Server) relayConverted(c *gin.Context, rec *callRecord, ep protocol.Endpoint, cand store.Candidate, body []byte, stream bool) {
	// 这两个实例要一路带到响应侧，**不能在编码时另 New 一个**：codec 允许携带每请求
	// 状态，而入口 codec 的 DecodeRequest 与 EncodeStream/EncodeFullBody 服务的是同
	// 一次请求。openairesponses 就靠这条把「客户端声明了哪些 custom 工具」从解码侧
	// 传到编码侧（见该包 Codec 的注释）。
	inCodec, outCodec := codecs.New(ep.Proto), codecs.New(cand.Protocol)
	if inCodec == nil || outCodec == nil {
		s.log.Error("转换路径缺 codec", "inbound", ep.Proto, "channel", cand.Protocol)
		ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "转换路径不可用")
		return
	}
	outEp, ok := protocol.UpstreamEndpoint(cand.Protocol)
	if !ok {
		ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "转换路径不可用")
		return
	}

	req, err := inCodec.DecodeRequest(body, stream)
	if err != nil {
		// 解不动入站请求是客户端的问题，不是上游的：回 400，别把它算成 upstream_error。
		s.log.Warn("入站请求解码失败", "inbound", ep.Proto, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "请求体无法解析为 "+string(ep.Proto)+" 请求")
		return
	}
	// 接入点对外模型名 → 纳管模型名。透传路径靠 RewriteModel 做字节级 splice，
	// 转换路径本来就要重编码，改字段即可。
	req.Model = cand.UpstreamModel

	outBody, dropped, err := encodeRequest(outCodec, req, stream)
	if err != nil {
		s.log.Error("出口请求编码失败", "channel", cand.ChannelName, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "请求无法转换为渠道协议")
		return
	}
	if len(dropped) > 0 {
		// 口径层 §2.6：跨协议丢弃要有日志警告，不做伪映射也不静默。codec 只登记，
		// 日志在这里打——codec 是纯函数，不持有 logger。
		s.log.Warn("跨协议转换丢弃字段",
			"inbound", ep.Proto, "channel_protocol", cand.Protocol, "dropped", dropped)
	}

	// rawQuery 不带过去：客户端的查询串是**入口协议**的方言（实测 Claude Code 发
	// /v1/messages?beta=true），照抄到 CC 端点上不是保真是串味。#20 定的「整串照抄」
	// 管的是同协议透传那条路。
	resp, retries, err := s.up.Do(c.Request.Context(), cand, outEp, "", outBody, c.Request.Header, stream)
	rec.retries = retries
	if err != nil {
		rec.outcome = "upstream_error"
		s.log.Error("上游请求失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游渠道 "+cand.ChannelName+" 请求失败")
		return
	}
	defer resp.Body.Close()

	// Tap 与 body 记录挂在上游原始字节上，与透传路径一致：usage 要的是上游自己
	// 报的数，不是网关重编出来的响应。
	var observers []io.Writer
	if tap := taps.New(cand.Protocol, stream); tap != nil {
		observers = append(observers, tap)
		defer func() { rec.summary, rec.haveSummary = tap.Summary(), true }()
	}
	if s.cfg.LogBodies {
		rec.responseBody = &captureWriter{}
		observers = append(observers, rec.responseBody)
	}
	src := io.Reader(resp.Body)
	if len(observers) > 0 {
		src = io.TeeReader(resp.Body, io.MultiWriter(observers...))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rec.outcome = "upstream_error"
		s.writeUpstreamError(c, ep, resp.StatusCode, src)
		return
	}

	if stream {
		s.streamConverted(c, rec, ep, cand, inCodec, outCodec, src)
		return
	}
	s.bufferConverted(c, rec, ep, cand, inCodec, outCodec, src)
}

// encodeRequest 走 RequestEncodeReporter 拿丢弃清单，拿不到就退回普通编码。
func encodeRequest(codec protocol.Codec, req *protocol.Request, stream bool) ([]byte, []string, error) {
	if reporter, ok := codec.(protocol.RequestEncodeReporter); ok {
		return reporter.EncodeRequestReport(req, stream)
	}
	body, err := codec.EncodeRequest(req, stream)
	return body, nil, err
}

// upstreamErrorLimit 是上游错误体的读取上限。错误体应当很小；设上限是防着一个巨大
// 的 HTML 错误页把内存吃满。
const upstreamErrorLimit = 64 << 10

// writeUpstreamError 把上游的错误按**入口协议**的原生形态回给客户端。
//
// 转换路径不能像透传那样把上游字节原样递出去：客户端等的是 Anthropic 形状的错误，
// 收到一个 OpenAI 形状的 error 对象会解不动。状态码原样保留——它是客户端退避与
// 重试决策的依据。
func (s *Server) writeUpstreamError(c *gin.Context, ep protocol.Endpoint, status int, body io.Reader) {
	raw, _ := io.ReadAll(io.LimitReader(body, upstreamErrorLimit))
	msg := upstreamErrorMessage(raw)
	if msg == "" {
		msg = "上游返回 " + http.StatusText(status)
	}
	ep.Proto.WriteError(c.Writer, status, msg)
}

// upstreamErrorMessage 从上游错误体里取出可读的说明。
//
// 只取 error.message 一个字段，不整体转发：错误体的其余部分（headers 回显、请求
// 快照一类）是上游自己的实现细节，转发它等于把一段不受控的内容塞进我们的错误契约。
func upstreamErrorMessage(raw []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return payload.Error.Message
}

// streamConverted 跑流式转换：上游 SSE → canonical 事件 → 入口协议 SSE。
func (s *Server) streamConverted(c *gin.Context, rec *callRecord, ep protocol.Endpoint, cand store.Candidate, inCodec, outCodec protocol.Codec, src io.Reader) {
	events, err := outCodec.DecodeStream(src)
	if err != nil {
		rec.outcome = "upstream_error"
		s.log.Error("上游响应流解码失败", "channel", cand.ChannelName, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应流无法解析")
		return
	}

	// 响应头自己造，不抄上游：body 已经换了协议，上游那套 Content-Type 与
	// Content-Encoding 描述的是另一份字节。
	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	setNoBuffering(h)
	c.Writer.WriteHeader(http.StatusOK)
	rec.outcome = "ok"

	w := &clientStream{w: c.Writer, rc: http.NewResponseController(c.Writer), onFirstByte: func() {
		rec.firstByte = time.Now()
	}}
	if err := w.advance(); err != nil {
		rec.outcome = "stream_aborted"
		s.log.Warn("转换流写出失败", "channel", cand.ChannelName, "err", err)
		panic(http.ErrAbortHandler)
	}

	if err := inCodec.EncodeStream(w, events); err != nil {
		// 响应头已发出，格式承诺已生效：不改写、不重发，只能断连并记日志（§6）。
		rec.outcome = "stream_aborted"
		s.log.Warn("转换流写出失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
		// 上游流还没读完时 EncodeStream 提前返回，会把解码 goroutine 卡在发送上。
		// 后台读空事件通道让它能写完退出；真正让它停下来的是 relayConverted 里
		// defer 的 resp.Body.Close()——panic 展开时它照常执行。就地同步排空不行：
		// 上游可能还在源源不断地发，那会把这次调用挂在一条已经断了的连接上。
		go drainEvents(events)
		panic(http.ErrAbortHandler)
	}
}

// drainEvents 把剩余事件读空，好让解码侧的 goroutine 能写完退出，不泄漏。
func drainEvents(events <-chan protocol.Event) {
	for range events {
	}
}

// bufferConverted 跑非流式转换：上游完整响应体 → canonical 事件 → 入口协议响应体。
func (s *Server) bufferConverted(c *gin.Context, rec *callRecord, ep protocol.Endpoint, cand store.Candidate, inCodec, outCodec protocol.Codec, src io.Reader) {
	raw, err := io.ReadAll(src)
	if err != nil {
		rec.outcome = "upstream_error"
		s.log.Error("读上游响应失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应读取失败")
		return
	}
	events, err := outCodec.DecodeFullBody(raw)
	if err != nil {
		rec.outcome = "upstream_error"
		s.log.Error("上游响应解码失败", "channel", cand.ChannelName, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应无法解析")
		return
	}
	out, err := inCodec.EncodeFullBody(events)
	if err != nil {
		rec.outcome = "upstream_error"
		s.log.Error("响应编码失败", "inbound", ep.Proto, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应无法转换")
		return
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	rec.outcome = "ok"
	rec.firstByte = time.Now()
	if _, err := c.Writer.Write(out); err != nil {
		rec.outcome = "stream_aborted"
		s.log.Warn("响应写出失败", "channel", cand.ChannelName, "err", err)
	}
}

// clientStream 是 EncodeStream 写下行 SSE 用的 writer。
//
// 它把透传路径 relayBody 里那三件事搬到转换路径上：首字节回调（ttfb 日志）、每写
// 一块推进写超时、写完 flush。少任何一件，要么日志缺 ttfb，要么慢客户端挂死连接，
// 要么「逐字输出」变成攒完一次性吐出。
type clientStream struct {
	w           gin.ResponseWriter
	rc          *http.ResponseController
	onFirstByte func()
	first       bool
}

func (s *clientStream) Write(p []byte) (int, error) {
	if !s.first {
		s.first = true
		if s.onFirstByte != nil {
			s.onFirstByte()
		}
	}
	if err := s.advance(); err != nil {
		return 0, err
	}
	return s.w.Write(p)
}

// Flush 满足 anthropic.Flusher：每帧之后被调一次。
func (s *clientStream) Flush() {
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		// Flush 失败说明连接已经坏了，下一次 Write 会拿到同样的错误并把它带上来。
		// 这里不能返回错误（Flusher 没有返回值），静默是唯一选择。
		return
	}
}

func (s *clientStream) advance() error {
	if err := s.rc.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}
