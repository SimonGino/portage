package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/exchange"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/codecs"
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
	// 订阅渠道（*_account，#213）：出站按 OpenAI 文档 D6 改写、恒为 SSE；客户端要
	// 非流式时由下面的聚合路径收完再编。判据是 credential_type，不按地址判（口径层
	// §2.2 v1.52）。入口半边同带这一位：previous_response_id 的 400 换订阅渠道的文案。
	sub := store.IsSubscriptionCredentialType(cand.CredentialType)
	codecOpts := codecs.Options{DefaultMaxTokens: s.cfg.DefaultMaxTokens, Subscription: sub}
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
	// 函数、不持有 logger，同「转换路径丢弃字段」那条的分工。
	// 断言小接口而不是具体 codec 类型（同下面 ArgsSalvaged 那条，#54）：server 不认
	// 任何具体协议包，「协议 → Codec 只有一张表」在 codecs.New。
	if rc, ok := inCodec.(interface{ CompactionTurn() bool }); ok && rc.CompactionTurn() {
		s.log.Info("Codex 压缩 turn 本地合成",
			"channel", cand.ChannelName, "channel_protocol", cand.Protocol)
	}
	// 丢弃日志**不能**罩在压缩 turn 里面：回带解不开发生在压缩之后的**普通**请求上
	// （那一轮没有 trigger，CompactionTurn 为假——见 decode 侧的还原用例），而混路
	// 场景恰恰是它要诊断的头一次。罩着的话最该归因的那次静默无声。
	if rc, ok := inCodec.(interface{ CompactionDrops() protocol.NameList }); ok {
		if drops := rc.CompactionDrops(); !drops.Empty() {
			// 回带的压缩摘要解不开、降级成了占位：这一段历史对上游是失忆的，
			// 「模型好像忘了前半段」这类反馈只能靠这行日志归因。
			s.log.Warn("回带的压缩摘要解不开，已降级为占位",
				"channel", cand.ChannelName, "items", drops)
		}
	}
	// 残缺入参的登记**不**限于 Responses 入口：CC 入口同规救治，所以这里断言一个小
	// 接口而不是具体 codec 类型，两个入口共用同一句文案。同理不罩在压缩 turn 里——
	// 残缺入参是上一轮流被截断留下的，之后每一轮普通请求都带着它。不救治的话严格
	// 上游逐次回 400（CC→R 出口）或我们自己 500（CC→A 出口 Marshal 报错），会话
	// 不新开就再也走不通。
	if r, ok := inCodec.(interface{ ArgsSalvaged() protocol.NameList }); ok {
		if salvaged := r.ArgsSalvaged(); !salvaged.Empty() {
			s.log.Warn("回带历史里有残缺的工具入参，已替换成 {}，模型看不到那次调用的原始入参，会话得以继续",
				"channel", cand.ChannelName, "calls", salvaged)
		}
	}
	// 入站请求里 canonical 收不下的形态（CC 的 tool_choice.allowed_tools 白名单）：
	// 同上一条的分工，codec 只登记、日志在这里打。与出口侧的「转换路径丢弃字段」
	// 分成两条是因为归因不同——那条丢的是**我们编不出去**的，这条丢的是**canonical
	// 装不下**的，看日志的人据此决定该改哪一侧。
	if r, ok := inCodec.(interface{ DecodeDrops() protocol.NameList }); ok {
		if drops := r.DecodeDrops(); !drops.Empty() {
			s.log.Warn("入站请求里有转换路径收不下的字段，已按最近语义折算",
				"inbound", ep.Proto, "channel", cand.ChannelName, "fields", drops)
		}
	}

	outBody, dropped, err := encodeRequest(outCodec, req, stream)
	if len(dropped) > 0 {
		// 口径层 §2.6：跨协议丢弃要有日志警告，不做伪映射也不静默。codec 只登记，
		// 日志在这里打——codec 是纯函数，不持有 logger。工具类三档带名单（v1.14 ⑨，
		// 渲染在 protocol.Drops.LogValue）。**排在下面的 400 之前**：编码被拒时这条
		// 照打，两条对照才分得出「客户端发空」与「我们丢光」（v1.14 ⑦）。订阅渠道
		// 同协议也走这条路（#213），文案不再限定「跨协议」。
		s.log.Warn("转换路径丢弃字段",
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
	dumpFrom(c).write("out.json", outBody)
	// 出站恒为 SSE：Tap 按 SSE 解 usage，出站头按流式写。客户端的非流式由聚合路径
	// 兜（下面 response 侧的分岔），出站这半边一个字不省。
	xreq := exchange.Request{
		Rec: rec, Inbound: ep.Proto, Route: routeOf(cand), Endpoint: outEp,
		Body: outBody, Header: c.Request.Header, Stream: stream || sub,
	}
	res, ok := s.ex.Do(c.Request.Context(), c.Writer, xreq)
	if !ok {
		return
	}
	if res.Status == http.StatusBadRequest {
		res, ok = s.retryTokenFloor(c, cand, outCodec, req, stream || sub, xreq, res)
		if !ok {
			return
		}
	}
	defer func() {
		res.Close()
		// 流中撞限改判（#215 集成项，接缝假设照两份 handoff 落地）：上游 200 开流、
		// 中途 response.failed 带撞限码——错误帧已写给客户端、流正常收场，流水词还是
		// OK，按词筛（口径层 §2.5）筛不到这一档。Close 先把 Summarize 落进流水（#8
		// 收场序），之后才判得了。判据与 HTTP 层 writeUpstreamError 同一份 planLimitCode，
		// 不另立第二处。Failed 传空串：流内形态没有 2KB 错误体那一档，原文已随错误帧
		// 写给客户端，不重复截。同族其余码不改判，照旧 OK（#213 钉住的带内语义）。
		if res.UpstreamErrorCode() == planLimitCode {
			rec.Failed(calllog.PlanLimitExceeded, "")
		}
	}()
	// 转换路径**不**把上游响应头回给客户端（出口协议的头是这边重造的），但流水里
	// 照记了 request-id（exchange 写回）：找上游对账与走的是哪条路无关（口径层
	// v0.56，#2）。三档的取舍仍在 calllog.Recorder.Finish，包括错误体那一档（v0.74）。

	if res.Status < 200 || res.Status >= 300 {
		s.writeUpstreamError(c, rec, ep, res.Status, res.Body)
		return
	}

	if stream {
		s.streamConverted(c, rec, ep, cand, inCodec, outCodec, res)
		s.warnResponseDrops(cand, outCodec)
		return
	}
	if sub {
		// 客户端要非流式而上游恒为 SSE（#213，§7.13）：不拒请求，收完聚出完整响应体。
		s.aggregateConverted(c, rec, ep, cand, inCodec, outCodec, res)
		s.warnResponseDrops(cand, outCodec)
		return
	}
	s.bufferConverted(c, rec, ep, cand, inCodec, outCodec, res.Body)
	s.warnResponseDrops(cand, outCodec)
}

// retryTokenFloor 处理「上游嫌 max_tokens 太小」这一种 400（口径层 v1.29）：从报错里
// 读出它要的下限，按下限重编请求再发**一次**。健康检查一类的客户端只要一两个 token，
// 个别上游回 400「max_tokens must be greater than 2」，改一个数就能过的事不该让
// 客户端吃一个错。
//
// 只在转换路径做：请求体本来就是我们重编的，改 canonical 的 MaxTokens 等于换个数
// 编出去；透传路径改请求体违背透传保真。这时首字节还没写出，重发不破承诺边界。
// 不是这一种 400 的，把读掉的字节接回去，原样交给下游的 writeUpstreamError。
func (s *Server) retryTokenFloor(c *gin.Context, cand store.Candidate, outCodec protocol.Codec,
	req *protocol.Request, stream bool, xreq exchange.Request, res *exchange.Result) (*exchange.Result, bool) {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	floor := tokenFloor(raw)
	if req.MaxTokens <= 0 || floor <= req.MaxTokens {
		res.Body = io.MultiReader(bytes.NewReader(raw), res.Body)
		return res, true
	}
	retried := *req
	retried.MaxTokens = floor
	body, _, err := encodeRequest(outCodec, &retried, stream)
	if err != nil {
		res.Body = io.MultiReader(bytes.NewReader(raw), res.Body)
		return res, true
	}
	res.Close()
	s.log.Info("上游嫌 max_tokens 太小，按它给的下限重发一次",
		"channel", cand.ChannelName, "from", req.MaxTokens, "to", floor)
	// 重发的 ledger 记法（Resent、闸拒时端点补回）归 exchange 管，这里只声明「这是重发」。
	xreq.Body, xreq.Resend = body, true
	return s.ex.Do(c.Request.Context(), c.Writer, xreq)
}

// tokenFloorRe 读上游报的 max_tokens 下限，词表取自 magpie `tooFewTokens`（9e78539）。
var tokenFloorRe = regexp.MustCompile(`(?i)max_(?:completion_|output_)?tokens.{0,60}?(greater than|more than|larger than|at least|>=|>)\s*(\d+)`)

// tokenFloor 是上游说它至少要多少个 token；报错说的不是这件事时为 0。上限 1024：
// 更大的数说的是别的（「max_tokens 300000 > 128000」那类上限报错）。
func tokenFloor(raw []byte) int {
	m := tokenFloorRe.FindSubmatch(raw)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(string(m[2]))
	if err != nil || n > 1024 {
		return 0
	}
	switch strings.ToLower(string(m[1])) {
	case "at least", ">=":
		return n
	}
	return n + 1
}

// warnResponseDrops 打「上游响应里有转换路径放不出去的项」这一条。
//
// 与请求侧那两条同一个分工：codec 是纯函数、不持有 logger，只登记，日志在 server
// 层打。登记内容只有类型与 id，不含 item 正文——搜索结果原文是内容，不该进日志。
//
// **只登记不合成、不计费**（PO 2026-09-03 裁定）：上游自带搜索时钱已经花了，客户端
// 那边什么也看不到，这行 Warn 是唯一的留痕。
//
// 并发：流式下这份登记由 DecodeStream 的解码 goroutine 写，本函数在 streamConverted
// 返回之后读。中间的 happens-before 由**事件通道的关闭**给出——EncodeStream 只在
// 通道关闭后返回，而通道关闭排在解码 goroutine 最后一次写之后。与
// protocol.StreamReadReporter 那一位同理，两处靠的是同一条边。
func (s *Server) warnResponseDrops(cand store.Candidate, outCodec protocol.Codec) {
	r, ok := outCodec.(interface{ ResponseDrops() protocol.NameList })
	if !ok {
		return
	}
	drops := r.ResponseDrops()
	if drops.Empty() {
		return
	}
	s.log.Warn("上游响应里有转换路径放不出去的项，已丢弃；若是服务端工具，上游成本已经发生",
		"channel", cand.ChannelName, "items", drops)
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
//
// 唯一的例外是「上下文超长」：各家上游说法不一，客户端只认自家那句，认出来才会压缩
// 再重试，认不出就停在那里。所以这一档改说成入口协议的原话——Anthropic 是 message
// 以 `prompt is too long` 开头，OpenAI 系是 code `context_length_exceeded`——状态码
// 统一 400（Anthropic 与 OpenAI 自己都用 400 说这件事；413 会被 Codex 当传输失败重发）。
func (s *Server) writeUpstreamError(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, status int, body io.Reader) {
	// 收场与原文一次记完：「上游说不行」与「它说了什么」本来就是同一件事，
	// 分两处写只是因为它们以前落在两个函数里。读多少由流水那一侧定。
	raw := rec.UpstreamRejected(body)
	// 订阅额度撞限（#215，口径层 §2.2 v1.52）：这一档落第 14 词，按词筛得到转换路径上的
	// 全部撞限请求；同族其余码（user_not_eligible / usage_unavailable / 认不得的）照旧
	// upstream_error。判码不判状态码也不判渠道类型——撞限码语义自足（只有订阅后端会
	// 发），谁回的、带什么状态码都落同一个词。透传路径不走这里：那条路不解析错误体，
	// error 列按 v0.28 纪律留空（订阅渠道已全量改道转换，#213）。
	// 流中那一档（200 开流、response.failed 带码）在 relayConverted 的收场处改判，
	// 判据同一份 planLimitCode。
	// 原文已由 UpstreamRejected 记全，这里只改词——Failed 传空串即不碰原文（后写覆盖
	// 先写，一档一个词）。key 层内环照旧：429 换凭证不摘不冷却，全池撞限时这份 429
	// 状态码原样交回客户端（上面的 status），只有流水那一格换词。
	if refusalCode(raw) == planLimitCode {
		rec.Failed(calllog.PlanLimitExceeded, "")
	}
	msg, code := upstreamErrorMessage(raw)
	if msg == "" {
		msg = "上游返回 " + http.StatusText(status)
	}
	if contextTooLong(status, msg, code) {
		if ep.Proto == protocol.Anthropic && !strings.HasPrefix(strings.ToLower(msg), "prompt is too long") {
			msg = "prompt is too long: " + msg
		}
		ep.Proto.WriteRequestError(c.Writer, &protocol.RequestError{Message: msg, Code: "context_length_exceeded"})
		return
	}
	ep.Proto.WriteError(c.Writer, status, msg)
}

// upstreamErrorMessage 从上游错误体里取出可读的说明与 code。
//
// 只取 error.message 一个字段回显，不整体转发：错误体的其余部分（headers 回显、请求
// 快照一类）是上游自己的实现细节，转发它等于把一段不受控的内容塞进我们的错误契约。
// code 只拿来判档，不回显。
func upstreamErrorMessage(raw []byte) (msg, code string) {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"` // 有的上游写数字
		} `json:"error"`
		// detail 是 FastAPI 一族的错误形态：`{"detail":"<句子>"}`，没有 error 对象
		// （订阅渠道 SIWC 的真机拒绝形态全是它，见 testdata/golden/responses-siwc-
		// evidence/README.md；中转上游也常见）。不取它的话客户端只能看到
		// 「上游返回 Bad Request」，上游明明说清了是哪个参数。
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return "", ""
	}
	code, _ = payload.Error.Code.(string)
	if payload.Error.Message != "" {
		return payload.Error.Message, code
	}
	return payload.Detail, code
}

// planLimitCode 是「订阅额度撞限」的上游错误码（口径层 §2.2 v1.52，ChatGPT）。
// Copilot premium requests 耗尽的码随其实现票并入同一个词，届时在判词处加一行。
const planLimitCode = "subscription_sharing_usage_limit_exceeded"

// refusalCode 读上游拒绝体里点名的错误码，三种形态都认（magpie siwcErrorCode 同读法，
// MIT 参考；真实 429 形态未采到，#205 只采到 400 的 {"detail":…}，构造样本以它的
// 读法为准）：标准 error 信封的 error.code、detail 对象的 code 键、detail 句子里嵌
// 的 subscription_sharing_* 码（句子形态按词界截取，不做子串匹配——撞限码不是任何
// 已知码的前缀，但截取把「将来出现更长码」的误报也一并堵死）。
// error.code 按字符串读（与 magpie 同）：撞限码只有字符串形态，numeric code 的上游
// 会让整个 unmarshal 报错、在这里拿到空串、落回 upstream_error——安全默认，不另兼容。
func refusalCode(raw []byte) string {
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	if payload.Error.Code != "" {
		return payload.Error.Code
	}
	var detailObj struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(payload.Detail, &detailObj) == nil && detailObj.Code != "" {
		return detailObj.Code
	}
	var detailStr string
	if json.Unmarshal(payload.Detail, &detailStr) != nil {
		return ""
	}
	const family = "subscription_sharing_"
	i := strings.Index(detailStr, family)
	if i < 0 {
		return ""
	}
	end := strings.IndexFunc(detailStr[i:], func(r rune) bool {
		return !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	if end < 0 {
		return detailStr[i:]
	}
	return detailStr[i : i+end]
}

// contextTooLongRe 是各家说「输入超过模型上下文」的写法：OpenAI 的 maximum context
// length、Anthropic 的 prompt is too long、火山的 Input exceeds the context limit、
// Kimi 的 exceeded model token limit、中文上游的「超过上下文」。词表取自 magpie
// `tooLongRe`（40812f1）与 mimo2codex `contextOverflow.ts`，去掉了会撞上限流报错的
// too many tokens。
var contextTooLongRe = regexp.MustCompile(`(?i)context_length_exceeded|prompt is too long|input is too long|maximum context length|` +
	`(exceeds?|exceeded|over|beyond)( the)?( model'?s?)?( maximum)? (context|token limit)|context (length|limit|window) (is )?exceeded|` +
	`上下文(长度)?(超|过长)|超(过|出)了?(模型)?的?(最大)?上下文`)

// contextTooLong 报告上游这个错误是不是在说「对话装不下了」。
//
// 只认 400 / 413：429 的「token 超限」说的是限流。提到 max_tokens 一类的不认——那是
// 回复额度太大，压缩对话救不了。
func contextTooLong(status int, msg, code string) bool {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge {
		return false
	}
	if code == "context_length_exceeded" {
		return true
	}
	m := strings.ToLower(msg)
	if strings.Contains(m, "max_tokens") || strings.Contains(m, "max_output_tokens") || strings.Contains(m, "max_completion_tokens") {
		return false
	}
	return contextTooLongRe.MatchString(msg)
}

// streamConverted 跑流式转换：上游 SSE → canonical 事件 → 入口协议 SSE。
//
// 写出失败的收场序（#8：先断上游 → 排空到关闭 → 才 Summarize）不在本函数里——
// AttachStream 把解码侧挂进 res 之后，relayConverted 那个 defer 的 res.Close()
// 按构造走这条序，panic 展开与正常返回走的是同一个收场。
func (s *Server) streamConverted(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, inCodec, outCodec protocol.Codec, res *exchange.Result) {
	events, err := outCodec.DecodeStream(res.Body)
	if err != nil {
		// 与 bufferConverted 各失败支同一口径（#163 的兄弟路径，
		// issue #170）：落库的原文就是脱敏后的错误本身（v0.53），不传空串。
		detail := upstream.Redact(err)
		rec.Failed(calllog.UpstreamError, detail.Error())
		s.log.Error("上游响应流解码失败", "channel", cand.ChannelName, "err", detail)
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
	// 收场记账（Succeeded / 首字节 / stream_aborted）在 Writer 里按构造走，这里只管断连。
	w := exchange.NewWriter(c.Writer, rec)
	if err := w.WriteHeader(http.StatusOK); err != nil {
		s.log.Warn("转换流写出失败", "channel", cand.ChannelName, "err", err)
		// 收场序在 panic 展开里：relayConverted defer 的 res.Close()（#8）。
		panic(http.ErrAbortHandler)
	}

	if err := inCodec.EncodeStream(w, events); err != nil {
		// 响应头已发出，格式承诺已生效：不改写、不重发，只能断连并记日志（§6）。
		// 断在 Writer 自己的写失败上时它已经记过，这一句是给编码器出错兜底的。
		w.Abort(err)
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
			w.Abort(err)
			s.log.Warn("上游响应流中断", "channel", cand.ChannelName, "err", upstream.Redact(err))
		}
	}
}

// aggregateConverted 跑订阅渠道的非流式聚合（#213，展开层 §7.13）：上游恒为 SSE
// （出口被强制 stream:true），客户端要非流式时把事件收完，再按入口协议编码完整响应体。
//
// 与 bufferConverted 只差输入半边：那边上游回整包 JSON（DecodeFullBody），这边上游
// 是 SSE（DecodeStream）；输出半边同为 EncodeFullBody，收场与记账同构——usage 出自
// 挂在**上游原始字节**上的 Tap（SSE 模式，usage 在 response.completed 里），不由事件
// 流回灌；三个入口协议各编各的完整响应体，不拒非流式请求。
func (s *Server) aggregateConverted(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, inCodec, outCodec protocol.Codec, res *exchange.Result) {
	events, err := outCodec.DecodeStream(res.Body)
	if err != nil {
		// 接口防御：今天三个 codec 的 DecodeStream 都恒返 nil（读断走带内 EvError
		// 给到 EncodeFullBody），但 Codec 契约有这一格；真到那一天，失败收场与
		// streamConverted 同一口径（#163）：落库的原文就是脱敏后的错误本身。
		detail := upstream.Redact(err)
		rec.Failed(calllog.UpstreamError, detail.Error())
		s.log.Error("上游响应流解码失败", "channel", cand.ChannelName, "err", detail)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应流无法解析")
		return
	}
	// 挂进收场序再收事件：EncodeFullBody 早退时（事件里带了 EvError）解码 goroutine
	// 还在往通道里塞，Close 的排空步骤就是给它收摊的（#8）。
	res.AttachStream(events)
	collected := make([]protocol.Event, 0, 64)
	for ev := range events {
		collected = append(collected, ev)
	}
	out, err := inCodec.EncodeFullBody(collected)
	if err != nil {
		// 上游 200 开了流、中途 response.failed（事件里带 EvError），或聚合不出
		// 响应体：非流式客户端等的是完整 JSON，改写不了只能按上游错误收场。
		detail := upstream.Redact(err)
		rec.Failed(calllog.UpstreamError, detail.Error())
		s.log.Error("上游响应聚合失败", "inbound", ep.Proto, "channel", cand.ChannelName, "err", detail)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应无法聚合成完整响应体")
		return
	}
	// 上游 200 开了流却没给终态（截断，或回的压根不是 SSE 字节）：不把空壳聚给
	// 客户端——一个 200 的空响应体是查不出因的静默失败，与 bufferConverted 对非法
	// JSON 回 502 同档。判据与 streamConverted 收尾那道同源（StreamReadReporter）；
	// 带 EvError 的断流已在上面 EncodeFullBody 报错过，能走到这一步的只剩截断。
	if r, ok := outCodec.(protocol.StreamReadReporter); ok {
		if err := r.StreamReadError(); err != nil {
			detail := upstream.Redact(err)
			rec.Failed(calllog.UpstreamError, detail.Error())
			s.log.Warn("上游响应流中断", "channel", cand.ChannelName, "err", detail)
			ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应流中断")
			return
		}
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	w := exchange.NewWriter(c.Writer, rec)
	if err := w.WriteHeader(http.StatusOK); err != nil {
		s.log.Warn("响应写出失败", "channel", cand.ChannelName, "err", err)
		return
	}
	if _, err := w.Write(out); err != nil {
		s.log.Warn("响应写出失败", "channel", cand.ChannelName, "err", err)
	}
}

// bufferConverted 跑非流式转换：上游完整响应体 → canonical 事件 → 入口协议响应体。
func (s *Server) bufferConverted(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, inCodec, outCodec protocol.Codec, src io.Reader) {
	raw, err := io.ReadAll(src)
	if err != nil {
		// 与流式路径同源（v0.53）：落库的原文就是脱敏后的错误本身。这一支此前
		// 传空串，#105 的 idle timeout 落这条路时流水里完全看不出是空闲超时还是
		// 断连（issue #163）。
		detail := upstream.Redact(err)
		rec.Failed(calllog.UpstreamError, detail.Error())
		s.log.Error("读上游响应失败", "channel", cand.ChannelName, "err", detail)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应读取失败")
		return
	}
	events, err := outCodec.DecodeFullBody(raw)
	if err != nil {
		// 同一原则：不带上游 key / base_url。Redact 只认传输错误的外壳，解码错误
		// 原样透过，这里统一走一遍单纯是不必对每个失败分支各判一次。
		detail := upstream.Redact(err)
		rec.Failed(calllog.UpstreamError, detail.Error())
		s.log.Error("上游响应解码失败", "channel", cand.ChannelName, "err", detail)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应无法解析")
		return
	}
	out, err := inCodec.EncodeFullBody(events)
	if err != nil {
		detail := upstream.Redact(err)
		rec.Failed(calllog.UpstreamError, detail.Error())
		s.log.Error("响应编码失败", "inbound", ep.Proto, "err", detail)
		ep.Proto.WriteError(c.Writer, http.StatusBadGateway, "上游响应无法转换")
		return
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	// 也走 exchange.Writer：此前这条缓冲路是三份写盘纪律里唯一丢了写超时的那份
	//（#9 点名的病），收成一份之后按构造齐全；收场记账同样在 Writer 里。
	w := exchange.NewWriter(c.Writer, rec)
	if err := w.WriteHeader(http.StatusOK); err != nil {
		s.log.Warn("响应写出失败", "channel", cand.ChannelName, "err", err)
		return
	}
	if _, err := w.Write(out); err != nil {
		s.log.Warn("响应写出失败", "channel", cand.ChannelName, "err", err)
	}
}
