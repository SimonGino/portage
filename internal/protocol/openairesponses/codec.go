package openairesponses

import (
	"io"
	"net/http"

	"github.com/SimonGino/portage/internal/protocol"
)

// Codec 是 OpenAI Responses 协议的转换器。
//
// 入口侧（DecodeRequest + EncodeStream/EncodeFullBody）已落地，走的是 R→CC；
// 出口侧（EncodeRequest + DecodeStream/DecodeFullBody）仍是骨架，等 CC→R / A→R
// 那两条路径（口径层 §2.1 优先级③下半与④）。
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
	// customTools 是本次请求声明为 custom 的工具名，由 DecodeRequest 填。
	customTools map[string]bool
}

func NewCodec() *Codec { return &Codec{} }

func (c *Codec) EncodeRequest(req *protocol.Request, stream bool) ([]byte, error) {
	return nil, protocol.ErrNotImplemented
}

func (c *Codec) DecodeStream(r io.Reader) (<-chan protocol.Event, error) {
	return nil, protocol.ErrNotImplemented
}

func (c *Codec) DecodeFullBody(body []byte) ([]protocol.Event, error) {
	return nil, protocol.ErrNotImplemented
}

// EncodeError 直接委托给 M0 就已落地的 protocol.WriteError——错误格式不是转换
// 逻辑，骨架期没有理由让它跟着返回「未实现」。
func (c *Codec) EncodeError(w http.ResponseWriter, status int, msg string) {
	protocol.OpenAIResponses.WriteError(w, status, msg)
}
