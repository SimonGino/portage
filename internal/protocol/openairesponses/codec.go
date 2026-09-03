package openairesponses

import (
	"net/http"

	"github.com/SimonGino/portage/internal/protocol"
)

// Codec 是 OpenAI Responses 协议的转换器。
//
// 入口侧（DecodeRequest + EncodeStream/EncodeFullBody，走 R→CC / R→A）与出口侧
// （EncodeRequest + DecodeStream/DecodeFullBody，走 CC→R / A→R，portage-legacy#80）都已落地，
// 九宫格自此全开。
//
// 出口半边**不碰** customTools / namespaceTools / compaction 三份状态：那是 DecodeRequest 在入口侧
// 填的、描述「本次客户端怎么声明工具」的知识；出口侧的请求来自另外两个协议的解码
// 结果，客户端根本没说过 Responses 的话，读它等于拿上一条路径的状态判这一条。同理
// 出口侧一律不合成 compaction item。
//
// 编译期断言钉住接口一致性：漏实现哪个方法 `go build` 当场红，而不是等 codecs 表
// 在运行时组装才发现。
var _ protocol.Codec = (*Codec)(nil)

// Codec 带**每请求状态**，一个实例只能服务一次请求，不可复用、不可并发共享。
//
// 这是三个 codec 里唯一有状态的一个，代价是实打实的：Responses 的响应形态取决于
// **请求里怎么声明的工具**——同一个上游 function-call 回来，声明成 custom 的要发
// custom_tool_call + 自由文本入参，声明成 function 的要发 function_call + JSON 入参。
// 而 Codec 接口的 EncodeStream(w, events) 只看得见事件流，事件是 CC 上游解出来的，
// 那边根本不知道客户端当初声明了什么。所以这份知识只能由 DecodeRequest 存下来传给
// 编码侧。
//
// 备选方案都试过了，都不行：
//   - 塞进 Event：CC 解码侧无从得知，它看到的 arguments 一律是 JSON。
//   - 按形状猜（拆到 `{"input":"…"}` 就当是包装）：一个真的只收 input 字符串参数的
//     JSON 工具会被误拆。形状不足以区分意图。
//   - 改 Codec 接口多传一个 *Request：六条路径里只有 R 出口用得上，等于让另外两个
//     codec 各背一个永远为 nil 的参数。
//
// sub2api 遇到的是同一个问题，解法同构（ResponsesClientToolMapping.CustomTools 从
// 请求里抽出来，显式传给响应侧转换）；差别只在我们的接口固定，状态改挂实例上。
type Codec struct {
	// StreamReadFlag 让本 codec 兑现 protocol.StreamReadReporter：转换路径上
	// 「上游传输断了」与「上游回了个错误对象」在事件流里都是 EvError，收场判不出
	// 来，靠这一位分开（见 DecodeStream 与 server 侧 streamConverted）。
	protocol.StreamReadFlag

	// customTools 是本次请求声明为 custom 的工具名（摊平名），由 DecodeRequest 填。
	customTools map[string]bool

	// namespaceTools 是本次请求里非默认命名空间子工具的映射表：摊平名 → {命名空间名，
	// 裸名}，由 DecodeRequest 填（namespace.go）。与 customTools 同一个理由挂在实例上：
	// 回程要把模型对摊平名的调用还原成 namespace 字段 + 裸名，而命名空间名自身可含
	// `__`（ADE 的 mcp__ade_asset_knowledge），按分隔符拆串必拆错——**只能查表**。
	// 默认命名空间与顶层工具不在表里：它们对外就是裸名，回程照旧不带字段。
	namespaceTools map[string]NamespaceTool

	// compaction 记「本次是 Codex 压缩 turn」，compactionDrops 记回带时没能还原的
	// 压缩 item。两个都由 DecodeRequest 填，消费方见 CompactionTurn / CompactionDrops。
	compaction      bool
	compactionDrops []string

	// argsSalvaged 记回带历史里入参被救治成 `{}` 的 function_call（形如 `名字(call_id)`），
	// 由 DecodeRequest 填，消费方见 ArgsSalvaged。与 compactionDrops 同一个形制：codec
	// 只登记，日志在 server 层打。
	argsSalvaged []string
}

func NewCodec() *Codec { return &Codec{} }

// NamespaceTools 是本次请求的 namespace 映射表（摊平名 → 来处），没有非默认命名空间
// 子工具时为 nil。只读：调用方拿去查，不改。
func (c *Codec) NamespaceTools() map[string]NamespaceTool { return c.namespaceTools }

// EncodeError 直接委托给 M0 就已落地的 protocol.WriteError——错误格式不是转换
// 逻辑，骨架期没有理由让它跟着返回「未实现」。
func (c *Codec) EncodeError(w http.ResponseWriter, status int, msg string) {
	protocol.OpenAIResponses.WriteError(w, status, msg)
}
