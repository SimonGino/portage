// Package protocol names the three wire protocols the gateway speaks and owns
// each one's native error representation.
//
// Tap（透传旁路 usage 提取）与 Codec（跨协议编解码）按 docs/MVP设计草案.md §3
// 落在本包的子包里，分别属于 M0-4 与 M2。
package protocol

import (
	"encoding/json"
	"net/http"
)

type Protocol string

// 三个协议的线上取值。这套名字是**对外**的：落库、写配置、进日志、显示在管理端，
// 全是这三个字符串，所以改一次要付迁移的代价，v0.36 改过一次就到此为止。
//
// OpenAI 指的是 Chat Completions。它不叫 `openai_cc`：这个字段回答的是「上游说哪套
// 协议」，而 `/v1/chat/completions` 就是 OpenAI 协议的缺省形态——第三方兼容端点（百炼、
// Vertex）说的都是它，`openai_responses` 才是需要额外限定的那个。Go 侧的包名与
// golden 目录名仍是 `openaicc` / `cc-*`：那些是内部标识，跟着改只会搅动全部 import
// 而换不来任何对外收益。
const (
	Anthropic       Protocol = "anthropic"
	OpenAI          Protocol = "openai"
	OpenAIResponses Protocol = "openai_responses"
)

// legacyNames 是 v0.36 改名前写进库/配置里的旧取值。
//
// 读的时候永远收，写的时候永远不吐——库里的存量行由 store.migrate 一次性改写，这张
// 表兜的是迁移跑之前就被读到的值、以及用户手写的配置和 GOLDENREC_PROTOCOL 之类的
// 环境变量。别给它设「兼容期」：一个只在读侧生效、写侧永不产出的别名不会漂移，留着
// 的成本是一行 map，删掉的代价是某个人的旧配置突然启动不了。
var legacyNames = map[Protocol]Protocol{
	"openai_cc": OpenAI,
}

// Normalize 把旧取值折成现名，现名与未知值原样返回（未知值交给 Valid 去拒）。
func Normalize(p Protocol) Protocol {
	if now, ok := legacyNames[p]; ok {
		return now
	}
	return p
}

func (p Protocol) Valid() bool {
	switch p {
	case Anthropic, OpenAI, OpenAIResponses:
		return true
	}
	return false
}

// Endpoint is one inbound 端点（口径层 §2.1 用词），and the protocol it speaks.
//
// Path doubles as the suffix appended to a 渠道 base_url: base_url 存「协议子路径
// 之前」的前缀，入站与出站子路径同形（docs/MVP设计草案.md §6.1）。
type Endpoint struct {
	Path  string
	Proto Protocol
}

// 入口协议由路径决定，不猜、不嗅探请求体。
var (
	EndpointMessages = Endpoint{"/v1/messages", Anthropic}
	// count_tokens 是 Anthropic 独有端点；命中非 anthropic 渠道时由请求时临时闸
	// 回 501，不做估算（估算属 M2）。
	EndpointCountTokens     = Endpoint{"/v1/messages/count_tokens", Anthropic}
	EndpointChatCompletions = Endpoint{"/v1/chat/completions", OpenAI}
	// Responses 的有状态子路径（GET /v1/responses/{id}、cancel）不做：
	// 口径层已定 v1 只支持无状态用法。
	EndpointResponses = Endpoint{"/v1/responses", OpenAIResponses}
)

// UpstreamEndpoint 给出「向这个协议的渠道发请求时打哪个子路径」。
//
// 转换路径必须用**出口**协议的端点，不能沿用入口的：Anthropic 入口进来的请求转成
// CC 之后要打 /v1/chat/completions，照抄 /v1/messages 会打到一个不存在的路径。
// 同协议透传不经过这里——那条路上入口即出口。
//
// count_tokens 没有对应物，故不出现在这张表里：它是 Anthropic 独有端点，命中非
// anthropic 渠道时按口径回 501（估算属 M2 后续批次）。
func UpstreamEndpoint(p Protocol) (Endpoint, bool) {
	switch p {
	case Anthropic:
		return EndpointMessages, true
	case OpenAI:
		return EndpointChatCompletions, true
	case OpenAIResponses:
		return EndpointResponses, true
	}
	return Endpoint{}, false
}

// RequestError 是「入站请求本身不合法」这一类**可逐字回显**的错误。
//
// 它存在的理由只有一个：codec 的 DecodeRequest 认出的某些问题，客户端有既定的降级
// 动作可做（第一例是 previous_response_id → 重发完整 input），而那个动作是由错误体
// 里的 code 触发的。普通的解码失败仍然走裸 error——那一档客户端除了「请求体坏了」
// 之外读不出别的，多一个 code 也没人认。
//
// 承载它的状态码恒为 400：这个类型的定义就是「客户端的问题」。真要一个非 400 的
// 可回显错误时，先想清楚它是不是同一件事，不要顺手给这个结构加 Status。
//
// Message 与 WriteError 的 msg 受同一条硬约束：**不许带上游 key 与 base_url**。
type RequestError struct {
	// Message 是给人读的那句话，要带可执行指引（「改成什么就能过」）。
	Message string
	// Code 是给程序读的那一位，落进 OpenAI 系错误体的 `code`。取值要挑客户端**已经
	// 认得**的那个——自造一个新词等于回到只有文案可读。
	Code string
	// Param 是出问题的字段名，落进 OpenAI 系错误体的 `param`。
	Param string
}

func (e *RequestError) Error() string { return e.Message }

// WriteError renders msg in the protocol's own error shape so a harness can
// parse it. Callers must never pass upstream credentials or base_url in msg.
func (p Protocol) WriteError(w http.ResponseWriter, status int, msg string) {
	p.writeError(w, status, msg, "", "")
}

// WriteRequestError 回一条带 code/param 的 400。
//
// 与 WriteError 分成两个动词而不是加两个参数：带 code 的错误是**契约**（客户端按
// code 决定怎么降级），而绝大多数调用点回的是一句给人读的话，让它们统统多写两个空串
// 只会让「这两位有没有人认」变得看不出来。
func (p Protocol) WriteRequestError(w http.ResponseWriter, e *RequestError) {
	p.writeError(w, http.StatusBadRequest, e.Message, e.Code, e.Param)
}

func (p Protocol) writeError(w http.ResponseWriter, status int, msg, code, param string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p.errorBody(status, msg, code, param))
}

// errorBody 造出错误体。
//
// code/param 只有 OpenAI 系（Chat Completions 与 Responses 共用同一个 error 对象）
// 有位置放。Anthropic 的 error 对象只有 type 与 message 两键，**不给它塞** ——那两个
// 键没有任何 Anthropic 客户端会读，凭空多出来的字段只会让「回显与官方同形」这条不再
// 成立。今天这不构成损失：带 code 的那条错误只从 Responses 入口发出（有状态续链是
// Responses 独有的），而入口协议决定回显形状。
func (p Protocol) errorBody(status int, msg, code, param string) any {
	if p == Anthropic {
		return map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    anthropicErrorType(status),
				"message": msg,
			},
		}
	}
	return map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    openaiErrorType(status),
			"param":   nilIfEmpty(param),
			"code":    nilIfEmpty(code),
		},
	}
}

// nilIfEmpty 让没填的那位落成 JSON null 而不是空串：这两键的官方形态就是「要么有
// 值、要么 null」，空串会让按真值判断的客户端把「没有 code」读成一个空 code。
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func openaiErrorType(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}
