package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/codecs"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
	"github.com/SimonGino/portage/internal/protocol/taps"
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/upstream"

	"github.com/gin-gonic/gin"
)

// 本文件是转换路径。同协议透传仍走 server.go 的字节复制那条——「透传保真优先」是
// 硬约束，同协议不做 decode→encode 转码。

// conversionOpen 报告「入口端点 → 渠道协议」这一格闸是否已放开。
//
// portage-legacy#80 之后**三协议九宫格全开**（对角线三格走透传，六格跨协议转换在这里列全）。
// 于是这个函数的职责只剩一件：挡住**没有上游对应端点**的入口端点。
//
// 那就是 /v1/messages/count_tokens。它与 /v1/messages 的 ep.Proto 同为 anthropic，
// 所以判据必须是**端点**而不是入口协议——按协议判会把 count_tokens 一起放进转换
// 分支，而 CC 与 Responses 两边根本没有可以转过去的端点。它命中非 anthropic 渠道
// 一律回 501，这不是「还没做」，是**没得做**。
func conversionOpen(ep protocol.Endpoint, channel protocol.Protocol) bool {
	switch {
	case ep == protocol.EndpointMessages && channel == protocol.OpenAI:
		return true // A→CC（口径层 §2.1 优先级①上半）
	case ep == protocol.EndpointResponses && channel == protocol.OpenAI:
		return true // R→CC（优先级①下半）
	case ep == protocol.EndpointResponses && channel == protocol.Anthropic:
		return true // R→A（优先级②：Codex 挂 Claude）
	case ep == protocol.EndpointChatCompletions && channel == protocol.Anthropic:
		return true // CC→A（优先级③上半）
	case ep == protocol.EndpointChatCompletions && channel == protocol.OpenAIResponses:
		return true // CC→R（优先级③下半）
	case ep == protocol.EndpointMessages && channel == protocol.OpenAIResponses:
		return true // A→R（优先级④）
	}
	return false
}

// relayConverted 跑一条转换路径：入口 codec 解成 canonical，渠道 codec 编出去，
// 响应方向反过来。
//
// 与透传路径共享的口径一条不改：首字节边界即承诺边界（写出去之后不改写、不重发，
// 只能断连）、Tap 挂在**上游原始字节**上（usage 出自上游自己说的数，不是我们编出来
// 的响应）、错误回显不带上游 key 与 base_url。
func (s *Server) relayConverted(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, body []byte, stream bool) {
	// 这两个实例要一路带到响应侧，**不能在编码时另 New 一个**：codec 允许携带每请求
	// 状态，而入口 codec 的 DecodeRequest 与 EncodeStream/EncodeFullBody 服务的是同
	// 一次请求。openairesponses 就靠这条把「客户端声明了哪些 custom 工具」从解码侧
	// 传到编码侧（见该包 Codec 的注释）。
	codecOpts := codecs.Options{DefaultMaxTokens: s.cfg.DefaultMaxTokens}
	inCodec, outCodec := codecs.New(ep.Proto, codecOpts), codecs.New(cand.Protocol, codecOpts)
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

	// Codex 压缩 turn 走本地合成（portage-legacy#74）。日志在这里打而不是在 codec 里：codec 是纯
	// 函数、不持有 logger，同「跨协议转换丢弃字段」那条的分工。
	if rc, ok := inCodec.(*openairesponses.Codec); ok {
		if rc.CompactionTurn() {
			s.log.Info("Codex 压缩 turn 本地合成",
				"channel", cand.ChannelName, "channel_protocol", cand.Protocol)
		}
		// 丢弃日志**不能**罩在压缩 turn 里面：回带解不开发生在压缩之后的**普通**请求上
		// （那一轮没有 trigger，CompactionTurn 为假——见 decode 侧的还原用例），而混路
		// 场景恰恰是它要诊断的头一次。罩着的话最该归因的那次静默无声。
		if drops := rc.CompactionDrops(); len(drops) > 0 {
			// 回带的压缩摘要解不开、降级成了占位：这一段历史对上游是失忆的，
			// 「模型好像忘了前半段」这类反馈只能靠这行日志归因。
			s.log.Warn("回带的压缩摘要解不开，已降级为占位",
				"channel", cand.ChannelName, "items", drops)
		}
	}

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

	// 出站端点在这一刻才记（#20），不在上面算出 outEp 的地方：中间还夹着解码 400、
	// 编码 500 两条早退，那些行同样没打上游，记了就是在说一件没发生的事。
	rec.Dialing(outEp.Path)
	// rawQuery 不带过去：客户端的查询串是**入口协议**的方言（实测 Claude Code 发
	// /v1/messages?beta=true），照抄到 CC 端点上不是保真是串味。portage-legacy#20 定的
	//「整串照抄」管的是同协议透传那条路（那是拆库前的编号，与上一段的 #20 无关）。
	resp, at, err := s.up.Do(c.Request.Context(), cand, outEp, "", outBody, c.Request.Header, stream)
	rec.Attempted(at.Retries(), at.Credential, at.QueueWait)
	if err != nil {
		if s.writeQueueReject(c, rec, ep, cand.ChannelName, err) {
			return
		}
		// 与透传路径同：这一支没有响应体，落库的原文就是传输错误本身（v0.53）。
		rec.Failed(calllog.UpstreamError, upstream.Redact(err).Error())
		s.log.Error("上游请求失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游渠道 "+cand.ChannelName+" 请求失败")
		return
	}
	defer resp.Body.Close()
	// 转换路径**不**把上游响应头回给客户端（出口协议的头是这边重造的），但流水里
	// 照记这个 id：找上游对账与走的是哪条路无关（口径层 v0.56，#2）。三档的取舍与
	// 透传路径同一处（calllog.Recorder.Finish），包括错误体那一档（v0.74）。
	rec.RequestIDs(upstream.RequestIDs(resp.Header))

	// Tap 与 body 记录挂在上游原始字节上，与透传路径一致：usage 要的是上游自己
	// 报的数，不是网关重编出来的响应。
	var observers []io.Writer
	if tap := taps.New(cand.Protocol, stream); tap != nil {
		observers = append(observers, tap)
		defer func() { rec.Summarized(tap.Summary()) }()
	}
	if s.cfg.LogBodies {
		observers = append(observers, rec.TapResponseBody())
	}
	src := io.Reader(resp.Body)
	if len(observers) > 0 {
		src = io.TeeReader(resp.Body, io.MultiWriter(observers...))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.writeUpstreamError(c, rec, ep, resp.StatusCode, src)
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

// writeUpstreamError 把上游的错误按**入口协议**的原生形态回给客户端。
//
// 转换路径不能像透传那样把上游字节原样递出去：客户端等的是 Anthropic 形状的错误，
// 收到一个 OpenAI 形状的 error 对象会解不动。状态码原样保留——它是客户端退避与
// 重试决策的依据。
// 回给客户端的只有 error.message 一句，但**落库落全**（截到 2KB，口径层 v0.53）：
// 客户端拿到的是我们的错误契约，排障要的是上游到底说了什么，两者不该是同一份文本。
func (s *Server) writeUpstreamError(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, status int, body io.Reader) {
	// 收场与原文一次记完：「上游说不行」与「它说了什么」本来就是同一件事，
	// 分两处写只是因为它们以前落在两个函数里。读多少由流水那一侧定。
	raw := rec.UpstreamRejected(body)
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
func (s *Server) streamConverted(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, inCodec, outCodec protocol.Codec, src io.Reader) {
	events, err := outCodec.DecodeStream(src)
	if err != nil {
		rec.Failed(calllog.UpstreamError, "")
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
	rec.Succeeded()

	w := &clientStream{w: c.Writer, rc: http.NewResponseController(c.Writer), onFirstByte: rec.FirstByte}
	if err := w.advance(); err != nil {
		rec.Failed(calllog.StreamAborted, "")
		s.log.Warn("转换流写出失败", "channel", cand.ChannelName, "err", err)
		panic(http.ErrAbortHandler)
	}

	if err := inCodec.EncodeStream(w, events); err != nil {
		// 响应头已发出，格式承诺已生效：不改写、不重发，只能断连并记日志（§6）。
		rec.Failed(calllog.StreamAborted, "")
		s.log.Warn("转换流写出失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
		// 上游流还没读完时 EncodeStream 提前返回，会把解码 goroutine 卡在发送上。
		// 后台读空事件通道让它能写完退出；真正让它停下来的是 relayConverted 里
		// defer 的 resp.Body.Close()——panic 展开时它照常执行。就地同步排空不行：
		// 上游可能还在源源不断地发，那会把这次调用挂在一条已经断了的连接上。
		go drainEvents(events)
		panic(http.ErrAbortHandler)
	}

	// EncodeStream 正常返回不等于这次说完了：上游读断是**带内**传下来的（解码侧放
	// 一条 EvError 就收摊，编码侧把错误帧写给客户端后照常收场），返回值里看不出来。
	// 不在这里补一刀，客户端中途断开这种最常见的断流就会记成干净的 200/ok，而透传
	// 路径上同一件事记的是 stream_aborted。
	if r, ok := outCodec.(protocol.StreamReadReporter); ok {
		if err := r.StreamReadError(); err != nil {
			// 与上游传输错误那一支同源：落库的原文就是脱敏后的错误本身（v0.53）。
			rec.Failed(calllog.StreamAborted, upstream.Redact(err).Error())
			s.log.Warn("上游响应流中断", "channel", cand.ChannelName, "err", upstream.Redact(err))
		}
	}
}

// drainEvents 把剩余事件读空，好让解码侧的 goroutine 能写完退出，不泄漏。
func drainEvents(events <-chan protocol.Event) {
	for range events {
	}
}

// bufferConverted 跑非流式转换：上游完整响应体 → canonical 事件 → 入口协议响应体。
func (s *Server) bufferConverted(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, inCodec, outCodec protocol.Codec, src io.Reader) {
	raw, err := io.ReadAll(src)
	if err != nil {
		rec.Failed(calllog.UpstreamError, "")
		s.log.Error("读上游响应失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应读取失败")
		return
	}
	events, err := outCodec.DecodeFullBody(raw)
	if err != nil {
		rec.Failed(calllog.UpstreamError, "")
		s.log.Error("上游响应解码失败", "channel", cand.ChannelName, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应无法解析")
		return
	}
	out, err := inCodec.EncodeFullBody(events)
	if err != nil {
		rec.Failed(calllog.UpstreamError, "")
		s.log.Error("响应编码失败", "inbound", ep.Proto, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应无法转换")
		return
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	rec.Succeeded()
	rec.FirstByte()
	if _, err := c.Writer.Write(out); err != nil {
		rec.Failed(calllog.StreamAborted, "")
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
