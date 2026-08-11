package upstream

import (
	"context"
	"net/http"
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
	}
	return res
}
