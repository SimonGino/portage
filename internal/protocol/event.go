package protocol

// 本文件是响应侧的 canonical 事件模型（docs/MVP设计草案.md §4）。
//
// 非流式响应当作「完整事件序列一次性回放」，上下游代码不分流式两套。
//
// 事实来源：Anthropic 侧取自本仓 testdata/golden/raw/anthropic-* 的五份真实上游
// SSE 转录；CC 侧取自 M0 语料 testdata/golden/cc-stream-*；Responses 侧取自
// testdata/golden/raw/resp-{text,tool,parallel} 三份真实上游转录。
//
// Responses 那三份是 M2-3 补的，销掉了这里原先记的一笔待办（「没有真实上游转录，
// 事件名以 sub2api 为准，拿到真实上游流后须复核」）。复核结论：**事件名与 sub2api
// 一致，无出入**；线格式上的三条实测细节记在 openairesponses/encode.go 头部
// （帧序、全流连号的 sequence_number、不发 [DONE]）。

// EventType 是事件判别式。
type EventType int

const (
	// EvMessageStart：一次响应开始。取 ID / Model。
	EvMessageStart EventType = iota
	// EvTextDelta：正文增量。取 Text。
	//
	// 块边界在此丢失：Anthropic 的 content_block_start/stop 把正文切成多块，
	// canonical 只留拼接后的增量流，回编码到 Anthropic 时会合成单块。这是
	// **显式接受的丢弃**——多块与单块对客户端渲染等价，而为了保边界要在事件流里
	// 引入一对纯结构事件，三个协议里只有一个用得上。
	EvTextDelta
	// EvThinkingDelta：推理增量。取 Text 与 Channel。
	EvThinkingDelta
	// EvToolCallStart：一次工具调用开始。取 Index / ToolID / ToolName。
	EvToolCallStart
	// EvToolArgsDelta：工具入参增量。取 Index / Text。
	//
	// 原草案这里叫 JSONFragment，被样本证伪：Codex custom 工具的入参是 JS 源码，
	// 分片拼起来也不是 JSON（见 ToolCall.Args）。字段改叫 Text，是不是 JSON 由
	// 起始事件的 ArgsIsJSON 决定。
	EvToolArgsDelta
	// EvToolCallEnd：一次工具调用参数收尾。取 Index。
	EvToolCallEnd
	// EvUsage：token 计数。取 Usage。
	//
	// 可在一条流里出现多次：Anthropic 在 message_start 给 input_tokens、在
	// message_delta 给 output_tokens。语义是**累计快照**，后来者的非零字段覆盖
	// 先前值，消费方不做加法——用 Usage.MergeSnapshot 兑现，别写整结构体赋值。
	EvUsage
	// EvDone：响应结束。取 StopReason（保留映射后的 canonical 取值）。
	EvDone
	// EvError：上游错误的流内表达。取 Status / Message。
	EvError
)

// ThinkingChannel 区分推理增量的三条通道。
//
// 需要显式判别式而非塞进 Extras：Responses 同时有 response.reasoning_text.delta
// 与 response.reasoning_summary_text.delta 两条流，语义不同（前者是推理正文，
// 后者是面向展示的摘要），codec 必须分支处理。藏在 map 里等于每个 codec 都写一次
// 魔法键查找。
type ThinkingChannel string

const (
	// ThinkingBody：推理正文。
	ThinkingBody ThinkingChannel = ""
	// ThinkingSummary：面向展示的推理摘要（Responses 独有）。
	ThinkingSummary ThinkingChannel = "summary"
	// ThinkingSignature：Anthropic thinking 块的 signature_delta。它不是给人看的
	// 文本，而是回带给同一上游时的完整性凭据；跨协议必然丢弃。
	ThinkingSignature ThinkingChannel = "signature"
)

// OutboundThinkingText 判定一条 EvThinkingDelta 在**跨协议出口**上要不要合成，返回
// 要写出去的文本。三个出口共用这一处判定（口径层 v0.62），流式与非流式也共用它。
//
// 丢弃条件下沉到 Channel == ThinkingSignature，不再是整类 EvThinkingDelta 一刀切：
// 签名是一段 base64 凭据，当正文写给客户端就是把密文当散文渲染；正文与摘要则必须
// 写出去——「已发生的成本不得静默吞没」。ThinkingBody 与 ThinkingSummary 在三个出口
// 上的落点完全相同（口径层 §2.6 的通道×出口矩阵），所以这里不区分它们：R→A 是摘要
// 落到 thinking 块的唯一实例，§9.4 已明确不算「摘要冒充正文」。
//
// 出口侧**不设开关**：thinking.display / reasoning.summary 只是请求侧参数，登记后
// 即丢，不回来影响这里该不该写（口径层 v0.62 ③）。
//
// 空文本一并跳过，与 EvTextDelta 的处理一致：只带 output_config.effort 的 Anthropic
// 上游会发正文整段为空、只有 signature 的 thinking 块（实测见
// testdata/golden/anthropic-stream-thinking），照写会凭空开出一个空推理块。
func OutboundThinkingText(e Event) (string, bool) {
	if e.Channel == ThinkingSignature || e.Text == "" {
		return "", false
	}
	return e.Text, true
}

// Usage 是 token 计数。这里**归一**各协议的口径，与 Summary（保留上游原始语义的
// 排障线索）分工不同：
//
//   - InputTokens 定死为**毛值**——含缓存命中与缓存写入，即「这一轮上游一共读了
//     多少输入 token」。OpenAI 的 prompt_tokens / Responses 的 input_tokens 本就是
//     毛值直映；Anthropic 的 input_tokens 是净值，解码时加回缓存两项、编码时减回去。
//   - CacheReadTokens / CacheWriteTokens 是毛值的**明细**而非另外两笔加数，不许再
//     加到 InputTokens 上。
//   - ReasoningTokens 同理是 **OutputTokens 的明细**（口径层 v0.66）：真实字节
//     里 total 恒等于 input + output，思考 token 一次都没进这个加法。
//
// 归一放在 canonical 而不是各出口：不归一的话 total_tokens（= input + output）在
// 缓存非零时低估，Codex 按它判压缩触发点，会被推后到先撞上游 400（portage-legacy#72）。
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	// ReasoningTokens 是思考 token 数（口径层 v0.66），OutputTokens 的明细。
	//
	// 这里**零值即「上游没报」**，不像 Tap 的 Summary 那样另立一个 Has 标志：
	// canonical 侧本就有这条约定（MergeSnapshot 把零值当「这一帧没报」），而两侧要答
	// 的问题不同——Summary 答「上游到底说了什么」（落流水，要区分没报与报 0），
	// canonical 答「往下游写什么」，而这两种情况写出去的字节一模一样（都不写）。
	//
	// 三个出口都有承接它的位置，键名各不同形（见 Summary.ReasoningTokens 那份对照）。
	// CC 与 Anthropic 两侧**有数才写**——零值在这里等于「上游没报」，写成 0 是把没报
	// 说成确凿的零思考成本；Responses 那侧的键恒在（契约必有项，理由记在
	// openairesponses/encode.go 的 usageBody 上），于是那条路上「没报」只能落回 0。
	// 成本可见性一律由流水那一列
	// 兜底，那一列才分得开三档（口径层 v0.66 ⑤）。
	ReasoningTokens int
}

// NetInput 是毛值减掉缓存两项后的**净输入**，即 Anthropic 线上的 `input_tokens`
// 口径（它与两项缓存互不相交，客户端自己相加）。A 出口编码时用它减回去，与
// anthropic 解码侧那个加回缓存的加法互为逆向。
//
// 钳到 0：上游报的缓存数大于毛值（口径不一致的兼容上游）时，负的 input_tokens 不是
// 合法 Anthropic 响应，客户端多半直接算崩。
//
// 钳零**不登记日志**，与 codec 的丢块登记（openaicc 的 DropVendorContent）不同档：
// 那边是我们主动丢掉了客户端本该看到的东西，这边没丢任何信息——毛值与两项明细都
// 照原样在别的字段里。而且两个解码侧都构造性地保证毛值 ≥ 缓存之和，真钳到说明上游
// 自己报的数就自相矛盾，那是排障要看 Tap.Summary（保留上游原样）的场景。
func (u Usage) NetInput() int {
	return max(0, u.InputTokens-u.CacheReadTokens-u.CacheWriteTokens)
}

// MergeSnapshot 把一份新的累计快照并进 u：**非零字段覆盖**，零值当「这一帧没报」
// 而非「上游说是 0」（EvUsage 的约定）。
//
// 必须逐字段而非整结构体赋值：部分兼容上游的末帧只带 output_tokens，整体覆盖会把
// 先前那份 input 与缓存明细一起清零。
func (u *Usage) MergeSnapshot(next Usage) {
	if next.InputTokens != 0 {
		u.InputTokens = next.InputTokens
	}
	if next.OutputTokens != 0 {
		u.OutputTokens = next.OutputTokens
	}
	if next.CacheReadTokens != 0 {
		u.CacheReadTokens = next.CacheReadTokens
	}
	if next.CacheWriteTokens != 0 {
		u.CacheWriteTokens = next.CacheWriteTokens
	}
	if next.ReasoningTokens != 0 {
		u.ReasoningTokens = next.ReasoningTokens
	}
}

// Event 是一个 canonical 事件。
//
// 取扁平 struct 而非 union/接口：事件在 channel 里流动，接口会给每个事件加一次
// 堆分配与一次断言，而字段数少到扁平化的代价可以忽略。**按 Type 取用对应字段**，
// 其余字段是零值，不承诺有意义。
type Event struct {
	Type EventType

	// EvMessageStart
	ID    string
	Model string

	// EvTextDelta / EvThinkingDelta：正文增量。
	// EvToolArgsDelta：入参分片。
	Text string

	// EvThinkingDelta
	Channel ThinkingChannel

	// EvToolCallStart / EvToolArgsDelta / EvToolCallEnd
	//
	// Index 为序，ID 为稳定标识——并行调用下 CC 的分片按 index 交错到达，必须按
	// Index 缓存再按序输出。Anthropic 用 content block index，Responses 用
	// output_index，语义对齐。
	Index      int
	ToolID     string
	ToolName   string
	ArgsIsJSON bool // EvToolCallStart：后续 EvToolArgsDelta 拼出来的是不是 JSON

	// EvUsage
	Usage *Usage

	// EvDone：canonical 取值 stop / tool_calls / length / content_filter，
	// 未知一律 stop（Anthropic 非流式不接受空 stop_reason，见 §5 坑清单）。
	StopReason string

	// EvDone：这个收尾是**解码侧兜底合成**的——上游流断在它声明 stop reason 之前
	// （Anthropic 的 message_delta、CC 的 finish_reason 都没等到）。
	//
	// 单开一位而不是让 StopReason 留空：留空这条路被上面那个兜底堵死了，而「上游
	// 说它停了」与「上游没说话就没了」在压缩合成上是生死之别——半截摘要会被 Codex
	// 当作**替换历史**装回去（openairesponses 的 finishCompaction）。普通路径不读
	// 这一位，读它的今天只有压缩。
	Truncated bool

	// EvError
	Status  int
	Message string

	// Extras 存协议独有、跨协议无处安放的事件级字段（如 Responses 的
	// encrypted_content）。同协议路径不经过本模型，所以这里的住户远少于 Request。
	Extras map[string]any
}
