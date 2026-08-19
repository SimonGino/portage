package server

import (
	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
)

// 本文件是 Responses 有状态续链（previous_response_id）的**透传半边闸**（口径层
// v0.88）。转换半边在 codec 里就地拒（openairesponses/stateful.go），两边的口径与
// 错误形状是同一份。
//
// 与 compaction 那一位的形状同构，默认值**相反**：这一位默认取是。两次的立论都是代价
// 不对称，只是不对称的方向反过来了——
//
//	compaction 位错成是：Codex 收到 0 个 compaction item，静默 Fatal 砖死长会话，
//	  客户端不重试不降级，人只看得见「Codex 崩了」。
//	本位错成是：上游自己回一句明确的 unsupported / not_found，客户端看得见、也知道
//	  该重发完整 input。反过来错成否，会把一条本来正常工作的续链当场打断。
//
// 所以这一位只兜「上游明确说了自己不做有状态」的那一格，缺省不挡路。
//
// 拦在**发上游之前**、用普通 400，同 compaction 闸：判据在读完请求体那一刻就成立，
// 响应头还没发，流式与非流式共用同一条路。

// statefulChannelMsg 的文案不带 base_url 与上游 key（口径层 §2.7），只报渠道名。
// 补救动作写全两头：客户端能做的（重发完整 input）与运维能做的（把位勾回来）。
const statefulChannelMsg = "未声明支持 Responses 有状态续链（previous_response_id）：" +
	openairesponses.PreviousResponseGuidance +
	"。上游确实支持的话，去管理端渠道页把「支持 Responses 有状态续链」勾上。"

// rejectStatefulResponses 判这次请求要不要按「渠道不支持有状态续链」拒掉，要拒就地
// 写完响应并返回 true。
//
// 只对 Responses 入口的**透传**渠道生效：previous_response_id 是 Responses 独有的顶层
// 字段，别的入口的请求体里不会有它；而转换路径无条件拒，与渠道能力位无关。
func (s *Server) rejectStatefulResponses(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, body []byte) bool {
	if ep != protocol.EndpointResponses {
		return false
	}
	passthrough := cand.Protocol == ep.Proto
	if !passthrough || cand.SupportsStatefulResponses {
		return false
	}
	// 扫描放在能力位之后：能力位为是的渠道（默认值，也是 Responses 透传的常态）
	// 一个字节都不用扫。
	if openairesponses.PreviousResponseID(body) == "" {
		return false
	}

	// 流水词沿用 rejected（早退分支的缺省词，见 calllog/outcome.go）：这一档一个字节
	// 都没到上游。没给它单开一个词——outcome 词表是口径层 v0.70 逐词钉死的对外契约，
	// 加第 11 个词要 PO 先裁。
	rec.Refused(calllog.Rejected)
	s.log.Warn("拒绝 Responses 有状态续链：透传渠道未声明支持 previous_response_id",
		"channel", cand.ChannelName, "channel_protocol", cand.Protocol)
	ep.Proto.WriteRequestError(c.Writer, &protocol.RequestError{
		Message: "渠道 " + cand.ChannelName + " " + statefulChannelMsg,
		Code:    openairesponses.CodePreviousResponseNotFound,
		Param:   openairesponses.ParamPreviousResponseID,
	})
	return true
}
