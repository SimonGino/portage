package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
)

// probeTimeout 短一些：这是保存渠道时同步跑的，人在等着看结果。上游慢到 8 秒还没
// 回一个 400，那本身就是要报出来的信息。
const probeTimeout = 8 * time.Second

// ProbeResult 是一次协议可达性探测的结论（口径层 v0.33 §2.2）。
type ProbeResult struct {
	Protocol protocol.Protocol `json:"protocol"`
	// Reachable=false 只代表「这个子路径看起来不存在」，不代表模型能不能用。
	Reachable bool `json:"reachable"`
	// Status 是上游的 HTTP 状态码，0 表示没连上。
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// Probe 问一个渠道「你到底提供不提供这个协议的子路径」。
//
// 只提示、不做闸（口径层 v0.33）：结果不落库、不参与路由。做它的理由是勾错协议集的
// 后果很隐蔽——一半端点全 404、另一半完全正常，而启动闸看不见（那要发包才知道，
// v0.21 通则只覆盖静态可判定的）；不做成闸的理由是探测结果会过期，把一个可能撒谎的
// 缓存放进请求路径是更坏的失败模式。
//
// 判据是 404/405 与其余状态的区别，**不是 2xx**：发的是一个空 JSON 体，任何真实上游
// 都会拿 400「缺 model」或 401「key 不对」回绝它——而那恰恰证明路由存在。要求 2xx
// 就得发一个真请求，那要花钱，还会把「模型名写错了」混进来当成协议不支持。
//
// 空 body 也是为了不花钱：`{}` 过不了任何上游的参数校验，不会产生一次推理。
func Probe(ctx context.Context, baseURL string, p protocol.Protocol, credential string) ProbeResult {
	res := ProbeResult{Protocol: p}
	ep, ok := protocol.UpstreamEndpoint(p)
	if !ok {
		res.Detail = "没有对应的上游端点"
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		buildURL(baseURL, ep, ""), strings.NewReader("{}"))
	if err != nil {
		res.Detail = "请求构造失败"
		return res
	}
	// 复用转发路径那套认证头，不另写一份：探测要问的正是「按我们发请求的方式打过去
	// 通不通」，换一套头就可能探到一个和真实转发不一样的结论。
	applyHeaders(req.Header, http.Header{}, p, credential, false)

	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err != nil {
		// Redact 摘掉传输错误里内嵌的 URL——这段文案会进管理端页面（CLAUDE.md：
		// 错误回显严禁泄露上游 key 与 base_url）。
		res.Detail = "连不上：" + Redact(err).Error()
		return res
	}
	drain(resp)
	res.Status = resp.StatusCode

	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		res.Detail = "上游没有这个子路径（" + ep.Path + "）"
	default:
		res.Reachable = true
		res.Detail = "子路径存在"
		// 非 2xx 但仍判 reachable 的那些，把状态码翻成一句话带上。判据不变
		// （404/405 之外都算路由存在），但「路由在」与「这把凭证打过去被 401」
		// 是两件事：只报前一句，一个每条子路径都回 401 的渠道在页面上显示的是
		// 「探测通过」。摘要仍走我方固定词表，不带上游原文（口径层 v0.43 ②）。
		if note := subpathNote(resp.StatusCode); note != "" {
			res.Detail += "，但上游回了" + note
		}
	}
	return res
}

// subpathNote 只翻译那些「路由在、但这次打过去不顺」的状态码。
//
// 400 不在其列：探测发的是空 `{}`，被参数校验拒掉正是它预期的成功形态（见 Probe
// 的判据那段），把它报成异常等于每次探测都亮一次假警报。
func subpathNote(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "凭证不对（401）"
	case status == http.StatusForbidden:
		return "拒绝（403）——可能是这把凭证没开通、或来源被限"
	case status == http.StatusTooManyRequests:
		return "限流（429）"
	case status >= 500:
		return "上游错误（" + strconv.Itoa(status) + "）"
	case status > 400:
		return "HTTP " + strconv.Itoa(status)
	}
	return ""
}

// ModelProbeState 是模型级探测一格的三态结论（口径层 v0.43）。
//
// 刻意不是二态：把 429 画成「不通」、把 400 画成「通」都是撒谎，而探测的口径是
// 只提示——提示就得诚实。「说不清」摆出状态码，判断留给人。
type ModelProbeState string

const (
	// ModelOK：上游 2xx，这个模型在这一侧真实回了话。
	ModelOK ModelProbeState = "ok"
	// ModelMissing：404/405。模型不存在与子路径不存在合并——对使用者是同一个
	// 结论：这一格当下不能用。
	ModelMissing ModelProbeState = "missing"
	// ModelUnclear：其余一切（400/401/403/429/5xx/连不上）。
	ModelUnclear ModelProbeState = "unclear"
)

// ModelProbeResult 是「这个模型在这一侧通不通」的一格答案。
type ModelProbeResult struct {
	Protocol protocol.Protocol `json:"protocol"`
	State    ModelProbeState   `json:"state"`
	// Status 是上游的 HTTP 状态码，0 表示没连上。
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// ProbeModel 拿一个真实的最小请求问上游「这个模型在这条子路径上存在吗」。
//
// 空 `{}` 的 Probe 答不了这个问题——v0.40 记录的坑正是「渠道级探测全通、请求照样
// 404」：聚合型中转在同一前缀下提供两套子路径，但它转的 gpt-4o 未必在 Anthropic
// 那一侧列出。带模型名就得发真请求，所以这一层会花一点点钱（`max_tokens` 压到
// 最小），也因此只用一把凭证、只由人手点，不逐把凭证轰全矩阵。
//
// 结论沿 v0.33 血统：只提示、不落库、不进路由。
func ProbeModel(ctx context.Context, baseURL string, p protocol.Protocol, credential, model string) ModelProbeResult {
	res := ModelProbeResult{Protocol: p, State: ModelUnclear}
	ep, ok := protocol.UpstreamEndpoint(p)
	if !ok {
		res.Detail = "没有对应的上游端点"
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		buildURL(baseURL, ep, ""), strings.NewReader(modelProbeBody(p, model)))
	if err != nil {
		res.Detail = "请求构造失败"
		return res
	}
	applyHeaders(req.Header, http.Header{}, p, credential, false)

	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err != nil {
		res.Detail = "连不上：" + Redact(err).Error()
		return res
	}
	drain(resp)
	res.Status = resp.StatusCode

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.State = ModelOK
		res.Detail = "通"
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusMethodNotAllowed:
		res.State = ModelMissing
		res.Detail = "这一侧没有这个模型（或子路径不存在）"
	default:
		// 摘要用我们自己的固定词表，**不带上游原文**——上游错误文案里可能带
		// base_url，与 call_logs.error 的处理是同一条纪律。
		res.Detail = unclearDetail(resp.StatusCode)
	}
	return res
}

// unclearDetail 把「说不清」的状态码翻成一句人话。判断不替人下：400 多半意味着
// 模型其实存在（路由和模型都认了，拒的是请求形状），429 说明模型八成存在只是限流，
// 但「八成」不该被画成通。
func unclearDetail(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "参数被拒（模型多半存在，是请求形状问题）"
	case status == http.StatusUnauthorized:
		return "凭证不对（401）"
	case status == http.StatusForbidden:
		return "被拒（403）——可能是这把凭证没开通这个模型"
	case status == http.StatusTooManyRequests:
		return "限流（429）"
	case status >= 500:
		return "上游错误"
	default:
		return "说不清"
	}
}

// modelProbeBody 给出各协议的最小合法请求体。
//
//   - CC 与 Anthropic 用 `max_tokens: 1`——两边的通用最小参数。注意 OpenAI 官方的
//     推理系模型（o 系、gpt-5 系）拒收 max_tokens、只认 max_completion_tokens，
//     那会落成 400 →「说不清」；不迁就它，因为兼容型上游对不认识的字段各有脾气，
//     而 400 的固定词表已经写明「模型多半存在」。
//   - Responses 用 `max_output_tokens: 16`——OpenAI 给这个字段定了 16 的下限。
func modelProbeBody(p protocol.Protocol, model string) string {
	m, _ := json.Marshal(model) // 模型名来自库，仍然按 JSON 字符串正经编码
	switch p {
	case protocol.Anthropic, protocol.OpenAI:
		return `{"model":` + string(m) + `,"max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	case protocol.OpenAIResponses:
		return `{"model":` + string(m) + `,"input":"hi","max_output_tokens":16}`
	}
	return `{"model":` + string(m) + `}`
}
