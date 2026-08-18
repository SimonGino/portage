package server

import (
	"net/http"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
)

// 本文件是 Codex remote compaction 的透传半边闸（口径层 v0.54，portage-legacy#71 止血 + portage-legacy#74 收口）。
//
// Codex 发压缩 turn 的形态是 input 尾部一个 `compaction_trigger` item，它要求响应里
// **恰好一个** compaction item，收到 0 个就 Fatal 且不重试不降级。网关原本有两条路会让
// 它收到 0 个，而且两条都表现成「一次成功的普通转发」：
//
//	① 转换路径（R→A / R→CC）：trigger 落在 decodeInput 的未知 item 分支被跳过，
//	   请求照常打给一个根本不知道要压缩的上游。
//	② 透传路径配错渠道：Responses 形状的 wire 不等于支持压缩，一个不认 trigger 的
//	   兼容网关会把它当成无关字段忽略掉，照样回 0 个 item。
//
// ① 已经由本地合成治掉：转换路径认得 trigger、把那一轮改写成 summarizer、自己
// 合成那个 item，所以它不再是拒绝的理由。留下的只有 ②——上游认不认 trigger 网关探不
// 出来，只能看渠道的 compaction 能力位（默认取否，口径层 v0.54 ⑨）。
//
// 拦在**发上游之前**、用普通 400，而不是流内 response.failed：trigger 在读完请求体
// 时就认得出来，响应头还没发，流式与非流式共用同一条路；等到开了流再报错，反而要在
// 一个已经承诺 200 的流里塞失败事件。

// 文案不带 base_url 与上游 key（口径层 §2.7），只报渠道名——渠道名本来就出现在别的
// 转发错误里。补救动作直接写进去：勾一下就好。
const compactionChannelMsg = "不支持 Codex 压缩（remote compaction）：该渠道未声明认得 compaction_trigger。" +
	"上游确实支持的话，去管理端渠道页把「支持 Codex 压缩」勾上。"

// rejectCompaction 判这次请求要不要按「不支持压缩」拒掉，要拒就地写完响应并返回 true。
//
// 只对 Responses 入口的**透传**渠道生效：compaction_trigger 是 Responses 独有的 item，
// 别的入口的请求体里不会有它；而转换路径自己会合成（openairesponses 的 compaction.go）。
func (s *Server) rejectCompaction(c *gin.Context, rec *callRecord, ep protocol.Endpoint, cand store.Candidate, body []byte) bool {
	if ep != protocol.EndpointResponses {
		return false
	}
	// 与 relay 里那个分岔判据用同一种写法（server.go 的 `cand.Protocol != ep.Proto`）：
	// 这里 ep 已经确定是 Responses，两种写法等价，但同一个概念不该有两种拼法。
	passthrough := cand.Protocol == ep.Proto
	if !passthrough || cand.SupportsCompaction {
		return false
	}
	// 扫描放在能力位之后：能力位为是的渠道（Responses 透传的常态）一个字节都不用扫。
	if !openairesponses.HasCompactionTrigger(body) {
		return false
	}

	// 流水 error 列里的固定词（同 queue_full 那批，见 calllog.go 的词表注释）。
	rec.outcome = calllog.CompactionUnsupported
	// 这条日志就是口径要的「drop 日志」：以前 trigger 是被静默丢掉的，现在丢不丢都
	// 有一行说得清是哪个渠道。
	s.log.Warn("拒绝 Codex 压缩 turn：透传渠道未声明支持 compaction",
		"channel", cand.ChannelName, "channel_protocol", cand.Protocol)
	ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "渠道 "+cand.ChannelName+" "+compactionChannelMsg)
	return true
}

// compactUnsupported 应付 legacy 的 `POST /v1/responses/compact`（口径层 v0.54 裁定
// 501，不实现）。
//
// 之前它落在 NoRoute 上，被管理端的 SPA fallback 接走，客户端拿到的是一页 HTML 或一个
// 裸 404——两样都读不出「这个网关不做这件事」。501 是**明确拒绝**，与「路径打错了」
// 分得开。
//
// 不挂鉴权与流水中间件：它无条件回同一句话，既不碰上游也不碰库，鉴权只会让一个想
// 弄明白「这个端点到底有没有」的人先撞 401。
func compactUnsupported(c *gin.Context) {
	protocol.OpenAIResponses.WriteError(c.Writer, http.StatusNotImplemented,
		"网关不支持 v1 compact：请用 Codex 自带的压缩流程（input 带 compaction_trigger 的请求），并确认渠道已声明支持 Codex 压缩")
}
