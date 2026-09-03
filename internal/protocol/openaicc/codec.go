package openaicc

import (
	"net/http"

	"github.com/SimonGino/portage/internal/protocol"
)

// Codec 是 OpenAI Chat Completions 协议的转换器。
//
// 出口半边（EncodeRequest / DecodeStream / DecodeFullBody）在 portage-legacy#11 落地，
// 入口半边（DecodeRequest / EncodeStream / EncodeFullBody）在 portage-legacy#9 落地
// ——后者打开的是 CC→A，与 openairesponses 的出口半边合起来打开 CC→R。
//
// 编译期断言钉住接口一致性：任一方法漏实现，`go build` 当场红，而不是等 codecs 表
// 在运行时组装才发现。
var _ protocol.Codec = (*Codec)(nil)
var _ protocol.RequestEncodeReporter = (*Codec)(nil)
var _ protocol.StreamReadReporter = (*Codec)(nil)

// Codec 带**每请求状态**（includeUsage），一个实例只能服务一次请求，不可复用、
// 不可并发共享。relayConverted 已经按这条口径把入口 codec 一路带到响应侧。
//
// 与 openairesponses 的 customTools 是同一类问题：响应该怎么编，取决于**请求里
// 怎么说的**，而 EncodeStream 只看得见事件流。CC 的 usage 帧是可选的，发不发由
// 客户端的 stream_options.include_usage 决定，事件流里没有这个信息——它来自入站
// 请求，只能由 DecodeRequest 存下来传给编码侧。
type Codec struct {
	// includeUsage 是本次请求有没有要过流末的 usage 帧，由 DecodeRequest 填。
	includeUsage bool
	// argsSalvaged 记回带历史里入参被救治成 `{}` 的 tool_calls（形如 `名字(call_id)`），
	// 由 DecodeRequest 填，消费方见 ArgsSalvaged。与 openairesponses.Codec 的同名字段
	// 同一个形制：codec 是纯函数、不持有 logger，只登记，日志在 server 层打。
	argsSalvaged []string
	// decodeDrops 记解码入站请求时 canonical 装不下、只能按最近语义折算的字段
	// （形如 `tool_choice.allowed_tools(3 tools)`），由 DecodeRequest 填，消费方见
	// DecodeDrops。与 argsSalvaged 同一个形制：codec 只登记，日志在 server 层打。
	//
	// 与出口侧的 protocol.Drops 不是一回事：那份是「我们编不出去」，这份是
	// 「canonical 收不下」，两条 Warn 分开是为了看日志的人知道该改哪一侧。
	decodeDrops []string
	// StreamReadFlag 记「流式解码途中读上游读断了」，由 DecodeStream 填。
	protocol.StreamReadFlag
}

// ArgsSalvaged 列出解码时入参被救治成 `{}` 的回带 tool_calls（形如 `名字(call_id)`），
// 供调用方打警告日志。server 侧对一个小接口断言，两个入口共用同一句文案。
func (c *Codec) ArgsSalvaged() []string { return c.argsSalvaged }

// DecodeDrops 列出解码时 canonical 收不下、已按最近语义折算的入站字段，供调用方打
// 警告日志。同 ArgsSalvaged：codec 是纯函数、不持有 logger。
func (c *Codec) DecodeDrops() []string { return c.decodeDrops }

func NewCodec() *Codec { return &Codec{} }

// EncodeError 直接委托给 M0 就已落地的 protocol.WriteError——错误格式不是转换
// 逻辑，没有理由跟着走 codec 这条路。
func (c *Codec) EncodeError(w http.ResponseWriter, status int, msg string) {
	protocol.OpenAI.WriteError(w, status, msg)
}
