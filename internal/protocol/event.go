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
	// 先前值，消费方不做加法。
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

// Usage 是 token 计数。语义与 Summary 一致：**保留各协议原始语义，不在此归一**
// （Anthropic 的 input_tokens 不含缓存命中，OpenAI 的 prompt_tokens 含）。
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
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

	// EvError
	Status  int
	Message string

	// Extras 存协议独有、跨协议无处安放的事件级字段（如 Responses 的
	// encrypted_content）。同协议路径不经过本模型，所以这里的住户远少于 Request。
	Extras map[string]any
}
