package openairesponses

import "encoding/json"

// 本文件只做一件事：认出「这是一次 Codex 压缩 turn」。
//
// Codex remote compaction v2 的形态是：input 尾部追一个 `{"type":"compaction_trigger"}`
// 的 item，客户端随后要求响应里**恰好一个** compaction item；拿到 0 个就 Fatal，
// 且不重试不降级（codex-rs `collect_compaction_output`，opencodex
// `src/responses/compaction.ts` 有同款记述）。网关这边只要静默把 trigger 吃掉——
// 转换路径的 decodeInput 落在未知 item 那一支，或透传给一个不认 trigger 的
// Responses 兼容上游——客户端看到的就是「一次成功的普通转发」之后长会话砖死。
//
// 所以在真正动手转发之前先认出它，明确拒绝（口径层 v0.54 止血档，#71）。真让压缩
// 可用的本地合成是 #74。

// ItemCompactionTrigger 是那个 item 的 type 取值。
const ItemCompactionTrigger = "compaction_trigger"

// HasCompactionTrigger 报告这份 Responses 请求体的 input 里带没带 compaction_trigger。
//
// 只扫 input 一层，逐项单独解：input 允许是字符串（退化成一条 user 消息，那里不可能
// 有 trigger）、数组里也可能混进解不动的元素，任何一处解不动都只让那一项落空，不影响
// 其余项的判定。判不出来一律返回 false——这个函数是**拒绝**的判据，宁可漏判让请求照
// 常走（回到今天的行为），也不能因为解析口味差异把普通请求拒了。
//
// 独立于 DecodeRequest 而不是挂在解码里：透传路径根本不进 codec（透传保真优先），
// 而能力位保护的恰恰是透传那半边。
func HasCompactionTrigger(body []byte) bool {
	var root struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &root); err != nil || len(root.Input) == 0 {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(root.Input, &items); err != nil {
		return false
	}
	for _, raw := range items {
		var item struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		if item.Type == ItemCompactionTrigger {
			return true
		}
	}
	return false
}
