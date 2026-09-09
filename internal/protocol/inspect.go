package protocol

// 本文件是 Codec 的第三个可选扩展：**透传请求检查**（#54，架构评审卡 6）。
//
// 透传路径按硬约束不进 codec——字节原样复制，不 decode 不 encode。但有两件事只有
// 认得那套协议的人才判得出来：一份 Responses 请求体带没带 compaction_trigger、带没带
// previous_response_id。它们此前各写成 server 里的一个闸，两个闸结构同构、都直连
// openairesponses 包——「协议 → Codec 只有一张表」在那两处漏了。收进 codec 之后
// server 不再认得任何具体协议包，加一套协议时它的检查逻辑就地跟着 codec 走。
//
// 与 RequestEncodeReporter 同一分工：codec 判并给出拒绝形状，**日志与流水由调用方
// 写**——codec 是纯函数、不持有 logger、不认 calllog（calllog 反过来 import 本包）。

// ChannelCapabilities 是选中的渠道声明的能力位，透传检查只看这几位。
//
// 不传整个 store.Candidate：protocol 包不认 store，而检查只需要这几个布尔。默认值
// 各位自己定（compaction 默认否、stateful 默认是，理由见各自的实现），这里只是搬运。
type ChannelCapabilities struct {
	// Compaction：渠道认得 Codex 的 compaction_trigger（channels.supports_compaction）。
	Compaction bool
	// StatefulResponses：渠道支持 Responses 有状态续链 previous_response_id
	// （channels.supports_stateful_responses）。
	StatefulResponses bool
}

// RejectReason 是拒绝的分类，调用方据此选流水词与日志文案。
//
// 是一套独立的词而不是直接用 calllog 的 outcome：outcome 词表是口径层 v0.70 钉死的
// 对外契约，由 calllog 独占；这里的词只在 codec 与 server 之间走一趟。
type RejectReason string

const (
	// RejectCompaction：透传渠道未声明认得 compaction_trigger（口径层 v0.54 ⑨）。
	RejectCompaction RejectReason = "compaction"
	// RejectStateful：透传渠道未声明支持 previous_response_id（口径层 v0.88）。
	RejectStateful RejectReason = "stateful"
)

// Rejection 是一次透传检查判出的拒绝。
type Rejection struct {
	Reason RejectReason
	// Err 是回给客户端的 400 形状。Message 不含渠道名——codec 不知道选了哪个渠道，
	// 调用方自己拼前缀；code/param 按各协议的既定信号填（Anthropic 形状会丢掉这两键，
	// 见 errorBody）。
	Err *RequestError
}

// RequestInspector 是 Codec 的可选扩展：在**透传**之前看一眼请求体，判它在这个渠道
// 上能不能原样发出去。
//
// 只对透传半边负责：转换半边进 DecodeRequest，同样的判据在那里就地处置（拒或改写），
// 调用方不该对转换路径再调一次。判不出来一律放行——它是拒绝的判据，宁可漏判让请求
// 照常走（上游自己会回一句明确的错误），也不能因为解析口味差异把一次本来能用的请求
// 拒了。
type RequestInspector interface {
	// InspectPassthrough 返回 nil 表示放行。实现应先看能力位再扫字节：能力位为是的
	// 渠道（各位的常态）一个字节都不用扫。
	InspectPassthrough(body []byte, caps ChannelCapabilities) *Rejection
}
