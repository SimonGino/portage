package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/exchange"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/codecs"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/upstream"

	"github.com/gin-gonic/gin"
)

// 本文件是转换路径。同协议透传仍走 server.go 的字节复制那条——「透传保真优先」是
// 硬约束，同协议不做 decode→encode 转码。

// conversionOpen 报告「入口端点 → 渠道协议」这一格闸是否已放开。
//
// portage-legacy#80 之后**三协议九宫格全开**（对角线三格走透传，六格跨协议转换在这里列全）。
// count_tokens 曾是这里唯一要挡的入口（没有上游对应端点，回 501），口径层 v0.80
// 之后它在进这道闸**之前**就拆去了本地估算那条路（server.go 的 countTokensLocal，
// #18），于是今天没有能落进 501 的组合。闸保留是给将来新端点兜底——「回 501 是
// 没得做」这句只对**既没有上游对应端点、也没有本地路**的入口成立。判据按端点不按
// 入口协议的教训原样有效：count_tokens 与 /v1/messages 的 ep.Proto 同为 anthropic。
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
		//
		// 两档 400：codec 明确造出来的 RequestError 逐字回显（它带着客户端要照办的
		// 那句指引与 code，换成通用文案等于把这条错误存在的理由抹掉），其余仍回通用
		// 那句——那一档客户端除了「请求体坏了」也读不出别的。
		if reqErr, ok := errors.AsType[*protocol.RequestError](err); ok {
			s.log.Warn("入站请求被拒", "inbound", ep.Proto, "code", reqErr.Code, "param", reqErr.Param)
			ep.Proto.WriteRequestError(c.Writer, reqErr)
			return
		}
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
	if len(dropped) > 0 {
		// 口径层 §2.6：跨协议丢弃要有日志警告，不做伪映射也不静默。codec 只登记，
		// 日志在这里打——codec 是纯函数，不持有 logger。工具类三档带名单（v1.14 ⑨，
		// 渲染在 protocol.Drops.LogValue）。**排在下面的 400 之前**：编码被拒时这条
		// 照打，两条对照才分得出「客户端发空」与「我们丢光」（v1.14 ⑦）。
		s.log.Warn("跨协议转换丢弃字段",
			"inbound", ep.Proto, "channel_protocol", cand.Protocol, "dropped", dropped)
	}
	if err != nil {
		// 转换后请求已不成立（messages 空了、tool_choice 的硬要求落空，口径层 v1.14
		// ⑦⑧）：我们自己回 400，不交上游——交出去渠道会在流水里背「上游拒绝」，归因
		// 反了。收场沿用上面入站解码被拒那一档：不 Dialing、不 Failed，早退缺省
		// rejected，upstream_endpoint 留空（「非空 ⟺ 真的发起过」不变量原样）。
		if reqErr, ok := errors.AsType[*protocol.RequestError](err); ok {
			s.log.Warn("转换后的请求被拒", "inbound", ep.Proto, "channel_protocol", cand.Protocol,
				"code", reqErr.Code, "param", reqErr.Param)
			ep.Proto.WriteRequestError(c.Writer, reqErr)
			return
		}
		s.log.Error("出口请求编码失败", "channel", cand.ChannelName, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "请求无法转换为渠道协议")
		return
	}

	// rawQuery 不带过去：客户端的查询串是**入口协议**的方言（实测 Claude Code 发
	// /v1/messages?beta=true），照抄到 CC 端点上不是保真是串味（portage-legacy#20 的
	//「整串照抄」管的是同协议透传那条路）。错误原文不占旁路坑（TapErrorBody 为假）：
	// 这条路非 2xx 的字节由下面的 writeUpstreamError 读全在手（rec.UpstreamRejected）。
	// Tap 与 body 记录照样挂在**上游原始字节**上：usage 要的是上游自己报的数，
	// 不是网关重编出来的响应。
	res, ok := s.ex.Do(c.Request.Context(), c.Writer, exchange.Request{
		Rec: rec, Inbound: ep.Proto, Cand: cand, Endpoint: outEp,
		Body: outBody, Header: c.Request.Header, Stream: stream,
	})
	if !ok {
		return
	}
	defer res.Close()
	// 转换路径**不**把上游响应头回给客户端（出口协议的头是这边重造的），但流水里
	// 照记了 request-id（exchange 写回）：找上游对账与走的是哪条路无关（口径层
	// v0.56，#2）。三档的取舍仍在 calllog.Recorder.Finish，包括错误体那一档（v0.74）。

	if res.Status < 200 || res.Status >= 300 {
		s.writeUpstreamError(c, rec, ep, res.Status, res.Body)
		return
	}

	if stream {
		s.streamConverted(c, rec, ep, cand, inCodec, outCodec, res)
		return
	}
	s.bufferConverted(c, rec, ep, cand, inCodec, outCodec, res.Body)
}

// encodeRequest 走 RequestEncodeReporter 拿丢弃清单，拿不到就退回普通编码。
func encodeRequest(codec protocol.Codec, req *protocol.Request, stream bool) ([]byte, protocol.Drops, error) {
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
//
// 写出失败的收场序（#8：先断上游 → 排空到关闭 → 才 Summarize）不在本函数里——
// AttachStream 把解码侧挂进 res 之后，relayConverted 那个 defer 的 res.Close()
// 按构造走这条序，panic 展开与正常返回走的是同一个收场。
func (s *Server) streamConverted(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, inCodec, outCodec protocol.Codec, res *exchange.Result) {
	events, err := outCodec.DecodeStream(res.Body)
	if err != nil {
		rec.Failed(calllog.UpstreamError, "")
		s.log.Error("上游响应流解码失败", "channel", cand.ChannelName, "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应流无法解析")
		return
	}
	// 从这一刻起解码 goroutine 在另一条线上读上游字节，收场必须先排空它（#8）。
	res.AttachStream(events)

	// 响应头自己造，不抄上游：body 已经换了协议，上游那套 Content-Type 与
	// Content-Encoding 描述的是另一份字节。
	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	setNoBuffering(h)
	c.Writer.WriteHeader(http.StatusOK)
	rec.Succeeded()

	w := exchange.NewWriter(c.Writer, rec.FirstByte)
	if err := w.Advance(); err != nil {
		rec.Failed(calllog.StreamAborted, "")
		s.log.Warn("转换流写出失败", "channel", cand.ChannelName, "err", err)
		// 收场序在 panic 展开里：relayConverted defer 的 res.Close()（#8）。
		panic(http.ErrAbortHandler)
	}

	if err := inCodec.EncodeStream(w, events); err != nil {
		// 响应头已发出，格式承诺已生效：不改写、不重发，只能断连并记日志（§6）。
		rec.Failed(calllog.StreamAborted, "")
		s.log.Warn("转换流写出失败", "channel", cand.ChannelName, "err", upstream.Redact(err))
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
	// 也走 exchange.Writer：此前这条缓冲路是三份写盘纪律里唯一丢了写超时的那份
	//（#9 点名的病），收成一份之后按构造齐全。首字节上面已亲手记过，回调传 nil。
	if _, err := exchange.NewWriter(c.Writer, nil).Write(out); err != nil {
		rec.Failed(calllog.StreamAborted, "")
		s.log.Warn("响应写出失败", "channel", cand.ChannelName, "err", err)
	}
}
