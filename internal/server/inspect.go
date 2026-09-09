package server

import (
	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/codecs"
	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
)

// 本文件是透传半边的**请求检查闸**（#54）：Codex 压缩闸（口径层 v0.54）与 Responses
// 有状态续链闸（口径层 v0.88）原先是 server 里两个同构的函数、各自直连 openairesponses
// 包；现在判据与拒绝形状都收进 codec 的 protocol.RequestInspector，这里只剩三件
// server 才做得了的事：认出这是透传、拼上渠道名、把结果记进流水与日志。
//
// 位置在 relay 选完渠道之后、透传/转换分岔之前——判据要同时用到「渠道说哪个协议」
// 与渠道能力位，而两条判据一个字节都没打上游，所以都在 rec.Dialing 之前。

// rejectPassthrough 判这次请求要不要在透传之前拒掉，要拒就地写完响应并返回 true。
//
// 只对**透传**渠道生效：转换路径进 DecodeRequest，同样的判据在 codec 里就地处置
// （previous_response_id 无条件拒；compaction_trigger 走本地合成）。入口协议的 codec
// 没实现 RequestInspector 就没有可检查的东西——今天只有 Responses 有。
func (s *Server) rejectPassthrough(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, body []byte) bool {
	// 与 relay 里那个分岔判据用同一种写法（server.go 的 `cand.Protocol != ep.Proto`）：
	// 同一个概念不该有两种拼法。
	if cand.Protocol != ep.Proto {
		return false
	}
	insp, ok := codecs.New(ep.Proto, codecs.Options{DefaultMaxTokens: s.cfg.DefaultMaxTokens}).(protocol.RequestInspector)
	if !ok {
		return false
	}
	rej := insp.InspectPassthrough(body, protocol.ChannelCapabilities{
		Compaction:        cand.SupportsCompaction,
		StatefulResponses: cand.SupportsStatefulResponses,
	})
	if rej == nil {
		return false
	}

	// 流水词与日志文案按 Reason 挑：outcome 词表是口径层 v0.70 钉死的对外契约，由
	// calllog 独占，codec 不认它。
	switch rej.Reason {
	case protocol.RejectCompaction:
		rec.Refused(calllog.CompactionUnsupported)
		// 这条日志就是口径要的「drop 日志」：以前 trigger 是被静默丢掉的，现在丢不丢
		// 都有一行说得清是哪个渠道。
		s.log.Warn("拒绝 Codex 压缩 turn：透传渠道未声明支持 compaction",
			"channel", cand.ChannelName, "channel_protocol", cand.Protocol)
	case protocol.RejectStateful:
		// 沿用 rejected（早退分支的缺省词）：没给它单开一个词——加第 11 个词要 PO 先裁。
		rec.Refused(calllog.Rejected)
		s.log.Warn("拒绝 Responses 有状态续链：透传渠道未声明支持 previous_response_id",
			"channel", cand.ChannelName, "channel_protocol", cand.Protocol)
	default:
		rec.Refused(calllog.Rejected)
		s.log.Warn("拒绝透传请求：渠道能力位不满足",
			"reason", rej.Reason, "channel", cand.ChannelName, "channel_protocol", cand.Protocol)
	}
	// 文案只报渠道名，不带 base_url 与上游 key（口径层 §2.7）——渠道名本来就出现在
	// 别的转发错误里。
	e := *rej.Err
	e.Message = "渠道 " + cand.ChannelName + " " + e.Message
	ep.Proto.WriteRequestError(c.Writer, &e)
	return true
}
