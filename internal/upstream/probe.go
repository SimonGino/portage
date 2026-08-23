package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
)

// probeTimeout 短一些：检测是人在弹层里等着看结果的一次采样。上游慢到 8 秒还没
// 回话，那本身就是要报出来的信息。
const probeTimeout = 8 * time.Second

// ProbeState 是一格检测的三态结论（模型级 v0.43 立的口径）。
//
// 刻意不是二态：把 429 画成「不通」、把 400 画成「通」、把超时画成「不存在」都是
// 撒谎，而检测的口径是只提示——提示就得诚实。「说不清」摆出状态码，判断留给人。
type ProbeState string

const (
	// ProbeOK：上游 2xx 真回了话。
	ProbeOK ProbeState = "ok"
	// ProbeMissing：404/405。模型不存在与子路径不存在合并——对使用者是同一个
	// 结论：这一格当下不能用。
	ProbeMissing ProbeState = "missing"
	// ProbeUnclear：没拿到响应（超时、握手失败、我方压根没发出去），以及那些
	// 既不能算通也不能算不通的状态码（400/401/403/429/5xx）。
	ProbeUnclear ProbeState = "unclear"
)

// ModelProbeResult 是「这个模型在这一侧通不通」的一格答案（口径层 v0.43，
// v0.96 起是检测仅剩的一层——朝子路径发空 `{}` 的可达性探测已拆除：端点可达
// 证明不了真实可用，空体反过来还会把可用渠道画成不可用）。
type ModelProbeResult struct {
	Protocol protocol.Protocol `json:"protocol"`
	State    ProbeState        `json:"state"`
	// Status 是上游的 HTTP 状态码，0 表示没连上。
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// ProbeModel 拿一个真实的最小请求问上游「这个模型在这条子路径上存在吗」。
//
// 带模型名就得发真请求，所以这一层会花一点点钱（`max_tokens` 压到最小），也因此
// 只由人手点、保存后什么都不跑（口径层 v0.96 ①）。
//
// 结论沿 v0.33 血统：只提示、不落库、不进路由——检测结果会过期，把一个可能撒谎
// 的缓存放进请求路径是更坏的失败模式。
func ProbeModel(ctx context.Context, baseURL string, p protocol.Protocol, credential, model string) ModelProbeResult {
	res := ModelProbeResult{Protocol: p, State: ProbeUnclear}
	ep, ok := protocol.UpstreamEndpoint(p)
	if !ok {
		res.Detail = "本次检测失败：没有对应的上游端点"
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		buildURL(baseURL, ep, ""), strings.NewReader(modelProbeBody(p, model)))
	if err != nil {
		res.Detail = "本次检测失败：请求构造失败"
		return res
	}
	// 复用转发路径那套认证头，不另写一份：检测要问的正是「按我们发请求的方式打过去
	// 通不通」，换一套头就可能测出一个和真实转发不一样的结论。
	applyHeaders(req.Header, http.Header{}, p, credential, false)

	resp, err := (&http.Client{Timeout: probeTimeout}).Do(req)
	if err != nil {
		// 措辞是「本次检测失败」，不定性「不支持」（口径层 v0.96 ③）：检测是一次
		// 采样，限流、上游抖动都会让它失败，定性交给人。
		//
		// Redact 摘掉传输错误里内嵌的 URL——这段文案会进管理端页面（CLAUDE.md：
		// 错误回显严禁泄露上游 key 与 base_url）。
		res.Detail = "本次检测失败：连不上（" + Redact(err).Error() + "）"
		return res
	}
	drain(resp)
	res.Status = resp.StatusCode

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.State = ProbeOK
		res.Detail = "通"
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusMethodNotAllowed:
		res.State = ProbeMissing
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
