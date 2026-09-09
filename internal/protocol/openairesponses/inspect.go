package openairesponses

import "github.com/SimonGino/portage/internal/protocol"

// 本文件是 Responses 透传半边的请求检查（protocol.RequestInspector 的实现，#54）。
// 两条判据各自的口径、判据函数与转换半边的处置分别在 compaction.go 与 stateful.go，
// 这里只把它们按能力位串起来。
//
// 为什么拦在**发上游之前**、用普通 400，而不是流内 response.failed：两条判据在读完
// 请求体那一刻就成立，响应头还没发，流式与非流式共用同一条路；等到开了流再报错，
// 反而要在一个已经承诺 200 的流里塞失败事件。
//
// 两位默认值**相反**，立论都是代价不对称、只是方向反过来——
//
//	compaction 位默认否。错成是：Codex 收到 0 个 compaction item，静默 Fatal 砖死长
//	  会话，客户端不重试不降级，人只看得见「Codex 崩了」。
//	stateful 位默认是。错成是：上游自己回一句明确的 unsupported / not_found，客户端
//	  看得见、也知道该重发完整 input。反过来错成否，会把一条本来正常工作的续链当场
//	  打断。所以它只兜「上游明确说了自己不做有状态」的那一格，缺省不挡路。

// 文案不带 base_url 与上游 key（口径层 §2.7），渠道名由调用方拼在前面——codec 不
// 知道选了哪个渠道。补救动作直接写进去：勾一下就好。
const compactionChannelMsg = "不支持 Codex 压缩（remote compaction）：该渠道未声明认得 compaction_trigger。" +
	"上游确实支持的话，去管理端渠道页把「支持 Codex 压缩」勾上。"

// statefulChannelMsg 补救动作写全两头：客户端能做的（重发完整 input）与运维能做的
// （把位勾回来）。
const statefulChannelMsg = "未声明支持 Responses 有状态续链（previous_response_id）：" +
	PreviousResponseGuidance +
	"。上游确实支持的话，去管理端渠道页把「支持 Responses 有状态续链」勾上。"

// InspectPassthrough 实现 protocol.RequestInspector。
//
// 顺序先 compaction 后 stateful，与两个闸原先在 relay 里的先后一致；一份同时带两样
// 且两位都为否的请求体只报前者。两条都把能力位放在扫描之前：位为是的渠道一个字节
// 都不扫。
func (c *Codec) InspectPassthrough(body []byte, caps protocol.ChannelCapabilities) *protocol.Rejection {
	if !caps.Compaction && HasCompactionTrigger(body) {
		return &protocol.Rejection{
			Reason: protocol.RejectCompaction,
			// 不带 code/param：OpenAI 侧没有一个客户端认得的既定信号对应「网关不做
			// 压缩」，硬填一个反而会触发某种不相干的降级。
			Err: &protocol.RequestError{Message: compactionChannelMsg},
		}
	}
	if !caps.StatefulResponses && PreviousResponseID(body) != "" {
		return &protocol.Rejection{
			Reason: protocol.RejectStateful,
			Err: &protocol.RequestError{
				Message: statefulChannelMsg,
				Code:    CodePreviousResponseNotFound,
				Param:   ParamPreviousResponseID,
			},
		}
	}
	return nil
}
