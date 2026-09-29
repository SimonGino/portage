package protocol

// MissingToolResultPlaceholder 是「有调用、没结果」时出口侧合成的占位结果正文。
//
// 不变量：assistant 发出的每个工具调用，紧随其后必须有一条配对的结果。DeepSeek V4
// 严格校验这一条，缺了就 400 `Messages with role 'tool' must be a response to a
// preceding message with 'tool_calls'`；Anthropic 同样要求每个 tool_use 在**紧接着
// 的 user 消息里**有对应 tool_result，否则也是 400。而客户端历史确实会缺——取消的
// 轮次、丢掉的 output、Codex 的 bug——上游会拒，客户端又改不了自己的历史，会话就此
// 砖死。合成一条占位是唯一能把这轮救回来的做法。
//
// 文案**明说结果缺失**，不伪装成一次成功或失败的执行（PO 裁定）：模型读到这句会知道
// 这一路没拿到东西，而不是把一段编造的语义当成真的工具输出。
//
// 住在 protocol 而不是两个出口包各写一份：Anthropic 与 CC 的占位必须逐字相同，散开
// 就会漂——漂了之后两条路径的模型行为不一样，而没有任何测试会发现。
//
// 出处：mimo2codex `src/translate/reqToChat.ts` 的 ensureToolCallsHaveOutputs。
const MissingToolResultPlaceholder = "[tool result missing: the client sent no output for this call]"

// ImageOnlyToolResultPlaceholder 是 tool_result 抹平成纯文本后、没有文本只有图片被
// 抬出时补发的占位正文（CC 的 role=tool content、R 的 function_call_output.output
// 都只收字符串，图片本身走各自的抬图逻辑另发到紧随其后的 user 消息/项）。
//
// 不发空串：模型读到空 content 之后紧接着冒出一条不知道属于谁的图片 user 消息，
// 会把两者的因果关系搞错——「工具没返回东西」与「工具返回了图」是两回事。占位就是
// 让模型知道图在下一条消息里（PO 2026-09-28 triage 裁定，成本约 2 个 token）。
//
// 住在 protocol 而不是两个出口包各写一份：理由与 MissingToolResultPlaceholder 相同，
// CC 与 R 的文案必须逐字相同，散开就会漂。
const ImageOnlyToolResultPlaceholder = "[image]"

// EmptyToolResultPlaceholder 是 tool_result 抹平后既没有文本、也没有图片被抬出时
// （压根没有图，或图因 file_id 一类原因被丢、没能抬出）补发的占位正文。
//
// CC 与 R 两个出口对称：不声称有图，也不发空串。
const EmptyToolResultPlaceholder = "(empty)"

// ToolResultOutput 选 tool_result 抹平成纯文本后的正文：有文本用文本；没有文本时
// 按「有没有图真的被抬出去」二选一占位，不发空串。
//
// imageLifted 由调用方拿**实际抬图的结果**判（抬出的 part 数 > 0），不另写一套
// 「图搬不搬得走」的判定——两处各判一次，哪天抬图规则变了，就会出现声称有图、后面
// 却没有图的消息。CC 与 R 两个出口共用这一处，文案与选法都不会漂。
func ToolResultOutput(text string, imageLifted bool) string {
	switch {
	case text != "":
		return text
	case imageLifted:
		return ImageOnlyToolResultPlaceholder
	default:
		return EmptyToolResultPlaceholder
	}
}
