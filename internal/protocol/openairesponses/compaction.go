package openairesponses

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// 本文件是 Codex remote compaction v2 在转换路径上的**本地合成**（口径层 v0.54 正式
// 修法，#74）。
//
// Codex 的压缩 turn 形态：input 尾部追一个 `{"type":"compaction_trigger"}` 的 item，
// 客户端随后要求响应里**恰好一个** `{"type":"compaction","encrypted_content":…}` 的
// output item；拿到 0 个就 Fatal，且不重试不降级（codex-rs `collect_compaction_output`）。
//
// 官方 OpenAI 那边这个 item 里装的是**上游侧密文**，我们变不出来，上游（Anthropic /
// CC）也没有 compact 端点可转发——所以唯一的路子是本地合成，与 opencodex
// `src/responses/compaction.ts` 同构：把压缩 turn 改写成一次纯总结请求，把上游吐出来的
// 摘要装进一个**自造的透明信封**里当那个 item 发回去；下一轮 Codex 原样回带它时再拆开
// 还原成一条 user 消息。
//
// 三段口径分别落在三处：改写在 decode.go 的 decodeInput，合成在 encode.go 的
// streamEncoder，还原也在 decodeInput（G2）。
//
// 与 opencodex 的唯一有意差异是信封前缀：它用 `ocx1:`，我们用 `ptg1:`。两个网关的信封
// 语义各自独立（谁生成的摘要、按谁的 prompt 生成），撞名只会让混路样本更难判。

// ItemCompactionTrigger 是压缩 turn 那个触发 item 的 type 取值。
const ItemCompactionTrigger = "compaction_trigger"

// 回带侧认的三种 item type。前两个是压缩产物本身，`context_compaction` 是 codex-rs
// **本地**压缩留下的标记——它可以不带 encrypted_content，那种形态里没有任何摘要可还原
// （见 decodeInput）。
const (
	itemCompaction        = "compaction"
	itemCompactionSummary = "compaction_summary"
	itemContextCompaction = "context_compaction"
)

// envelopePrefix 是自造信封的判别前缀。
//
// **长期兼容约束**（展开层 §5 坑清单）：这串一旦发出去就存进了客户端的会话历史，改它
// 等于让所有在途会话的回带摘要解不开、降级成占位。要改只能加新前缀并保留旧前缀的解码。
const envelopePrefix = "ptg1:"

// compactPrompt 是发给上游的总结指令，镜像 codex-rs `core/templates/compact/prompt.md`。
//
// 用 codex-rs 原文而不是自拟：摘要要喂回 Codex 自己的续作流程，它对「摘要里该有什么」
// 的预期就是这份模板定的。
//
// **末尾那个换行是实测补上的**（#73 采样，2026-08-13）：先前照 opencodex 的
// `COMPACT_PROMPT` 抄，两边逐字节一致但都少了行尾换行；拿 Codex CLI 0.147 本地压缩
// 时实发的字节一对，真模板是带的。差一个字节不影响上游怎么总结，但注释写着「镜像
// 模板」就不该有已知偏差——而且现在这份的依据是真客户端转录，不再是二手抄写。
const compactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- Current progress and key decisions made
- Important context, constraints, or user preferences
- What remains to be done (clear next steps)
- Any critical data, examples, or references needed to continue

Be concise, structured, and focused on helping the next LLM seamlessly continue the work.
`

// summaryPrefix 是回带还原时套在摘要外面的引导语，镜像 codex-rs
// `core/templates/compact/summary_prefix.md`。
//
// **必须逐字对齐**：Codex 侧靠这段前缀认出「这条 user 消息是一份摘要而不是用户说的话」。
const summaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"

// opaqueCompactionNote 是解不开的信封的降级占位。
//
// 解不开有两种来路，都不是错误：客户端先经**透传**渠道压缩成功（那是上游侧密文），
// 之后被路由到转换渠道；或者这段历史是别的网关压的。占位不是为了好看——直接丢掉这个
// item，上游看到的历史里会凭空少掉一整段，表现成模型忽然失忆。
const opaqueCompactionNote = "[早前的对话已被压缩，摘要以当前模型读不了的格式存放]"

// HasCompactionTrigger 报告这份 Responses 请求体的 input 里带没带 compaction_trigger。
//
// 只扫 input 一层，逐项单独解：input 允许是字符串（退化成一条 user 消息，那里不可能
// 有 trigger）、数组里也可能混进解不动的元素，任何一处解不动都只让那一项落空，不影响
// 其余项的判定。判不出来一律返回 false——它是**拒绝**的判据（server/compaction.go 的
// 透传半边），宁可漏判让请求照常走，也不能因为解析口味差异把普通请求拒了。
//
// 独立于 DecodeRequest 而不是挂在解码里：透传路径根本不进 codec（透传保真优先），
// 而能力位保护的恰恰是透传那半边。转换路径不用它——那边由 DecodeRequest 顺带认出来
// （见 Codec.CompactionTurn）。
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

// encodeCompactionSummary 把摘要装进自造信封。
//
// base64 而非明文：这个位置在协议上叫 encrypted_content，官方那边装的是密文，客户端与
// 沿途的日志都不该拿它当可读正文渲染。它**不是加密**——信封是透明的，谁都能解，安全性
// 不在这里做（摘要本来就是这次会话自己的内容）。
func encodeCompactionSummary(summary string) string {
	return envelopePrefix + base64.StdEncoding.EncodeToString([]byte(summary))
}

// decodeCompactionSummary 拆自造信封。ok=false 表示这不是我们发的信封（真密文、别家
// 信封、或者被截断过），调用方按不透明处理。
func decodeCompactionSummary(encrypted string) (string, bool) {
	rest, found := strings.CutPrefix(encrypted, envelopePrefix)
	if !found {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// compactionItemText 把一个回带的 compaction item 渲染成给上游看的明文。
//
// 解得开就还原成 `summaryPrefix + 摘要`，解不开退到占位。
func compactionItemText(encrypted string) (text string, restored bool) {
	if summary, ok := decodeCompactionSummary(encrypted); ok {
		return summaryPrefix + "\n\n" + summary, true
	}
	return opaqueCompactionNote, false
}
