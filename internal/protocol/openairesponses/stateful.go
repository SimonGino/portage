package openairesponses

import (
	"encoding/json"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Responses **有状态续链**（previous_response_id）在网关这一层的处置。
//
// 口径：网关不做本地会话历史存储，所以「上一次的 response 还在」这件事只有上游自己
// 知道。于是这个字段只有两种归宿——
//
//	① 转换路径（R→A / R→CC）：**无条件拒**。有状态语义在这条路上物理不成立：那个 id
//	   是某个 Responses 上游发的句柄，Anthropic / CC 上游根本不认；而我们也没有历史可
//	   以展开成完整 input。
//	② 同协议透传：由渠道能力位 supports_stateful_responses 定。位为是就字节直传（认不
//	   认由上游自己回答），为否则同样拒。
//
// 拒而不是**静默丢弃**（v0.87 之前的做法）是本次口径变更的全部内容。静默丢弃的失败
// 模式最恶劣：客户端以为历史生效、实际每轮都是单轮，产出静默劣化且无从归因——没有
// 任何一条日志或错误能让人把「模型好像失忆」联系到这个被丢掉的字段上。

// 回给客户端的 code/param。
//
// 挑 previous_response_not_found 而不是 unsupported_parameter：前者是客户端**已经
// 认得**的信号——OpenAI 侧这个 code 的既定含义就是「那个 response 我这儿没有」，
// 客户端对它的既定降级动作正是我们要的那个：重发完整 input。后者没有任何既定降级
// 语义，客户端拿到多半只会把整次请求当失败上报。
const (
	CodePreviousResponseNotFound = "previous_response_not_found"
	ParamPreviousResponseID      = "previous_response_id"
)

// PreviousResponseGuidance 是两条路共用的那半句可执行指引。
//
// 共用一份是有意的：客户端要做的动作与「为什么拒」无关，两处各写一句迟早会漂成
// 两种说法，而这半句正是错误文案里唯一有用的部分。
const PreviousResponseGuidance = "请把完整对话历史放进 input 重发，不要带 previous_response_id"

// convertPathMsg 是转换路径那一条的完整文案。不提渠道名与能力位：这条路上拒绝与
// 渠道怎么配无关，指人去勾一个改不了结果的开关只会浪费一次往返。
const convertPathMsg = "该请求要转换成另一套协议发往上游，Responses 的有状态续链" +
	"（previous_response_id）在转换路径上不成立：" + PreviousResponseGuidance + "。"

// PreviousResponseRejection 造转换路径那条 400。
//
// 每次新建一个而不是暴露一个包级 var：*RequestError 是指针，共享一份等于给每个调用点
// 一个能改掉所有人错误文案的把手。
func PreviousResponseRejection() *protocol.RequestError {
	return &protocol.RequestError{
		Message: convertPathMsg,
		Code:    CodePreviousResponseNotFound,
		Param:   ParamPreviousResponseID,
	}
}

// PreviousResponseID 取出这份 Responses 请求体里的 previous_response_id，没有、是
// null、或不是字符串都返回空串。
//
// 独立于 DecodeRequest，理由同 HasCompactionTrigger：透传路径根本不进 codec（透传保真
// 优先），而能力位保护的恰恰是透传那半边。判不出来一律当「没带」——它是**拒绝**的
// 判据，宁可漏判让请求照常走（上游自己会回一句明确的 not_found），也不能因为解析口味
// 差异把一次本来能用的续链拒了。
func PreviousResponseID(body []byte) string {
	var root struct {
		PreviousResponseID string `json:"previous_response_id"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return ""
	}
	return root.PreviousResponseID
}
