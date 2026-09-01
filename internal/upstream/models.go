package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
)

// listModelsTimeout 比探测宽一点：这是人点了按钮在等的一次拉取，而模型列表在聚合型
// 中转站上能有几百条，序列化本身就比一个 400 错误体慢。
const listModelsTimeout = 12 * time.Second

// listModelsLimit 封顶响应体。中转站返回上千个模型是常事，但真到几 MB 的就不是模型
// 列表了，没必要为它把管理端的内存吃掉。
const listModelsLimit = 2 << 20

// modelsPath 是两家共用的模型列表子路径。
//
// OpenAI 与 Anthropic 在这一点上恰好同形：都是 GET {base_url}/v1/models、都回
// `{"data":[{"id":"..."}]}`（Anthropic 侧见 litellm llms/anthropic/common_utils.py）。
// 所以这里既不需要 UpstreamEndpoint 那张表，也不做各家 URL 特判——new-api 为
// `/compatible-mode/v1/models`、`/api/paas/v4/models` 这些变体养了一张渠道类型表
// （controller/channel_upstream_update.go），那正是 MVP §6.1 明确不取的复杂度。
const modelsPath = "/v1/models"

// ModelListResult 是朝一个协议侧拉模型列表的结果（口径层 v0.40）。
//
// **这东西不落库、不参与路由**，与协议可达性探测（§2.2）同档：它拉回来的东西只进
// 管理端的表单做预勾选，人确认之后落库的是人的配置。理由与 v0.33 那条一字不差——
// 中转站的 `/v1/models` 撒谎是常态（返回一份写死的大列表，含它实际并不支持的模型），
// 把一份可能撒谎且会过期的缓存放进请求路径是比手工填错更坏的失败模式。
type ModelListResult struct {
	// Protocols 是这份列表适用的协议。openai 与 openai_responses 共用同一次拉取，
	// 所以这里是数组而不是单值——分两次打同一个 URL、拿同一份结果，只是白费一趟。
	Protocols []protocol.Protocol `json:"protocols"`
	// Models 是上游自称提供的模型名，原样保留、不做归一化：它要拿去跟纳管模型名
	// 逐字比对，大小写和前缀都是语义的一部分。
	Models []string `json:"models"`
	// Status 是上游的 HTTP 状态码，0 表示没连上。
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// ListModels 朝一个渠道的一个协议侧拉模型列表。
//
// 认证头复用转发路径那套（applyHeaders），理由同 Probe：要问的正是「按我们发请求的
// 方式打过去，上游认为我们能看见什么」，换一套头就可能拉到一份和真实转发不一样的表。
//
// 这也是它能区分协议的原理：同一个 base_url 下 `/v1/models` 只有一条，但带 openai
// 的 Bearer 头和带 anthropic 的 x-api-key 头打过去，聚合型中转站会回各自视角的列表。
// `gpt-4o` 出现在前者不出现在后者，就是「它只走 openai」的依据。
func ListModels(ctx context.Context, baseURL string, p protocol.Protocol, scheme, credential string) ModelListResult {
	res := ModelListResult{Protocols: sameModelsEndpoint(p)}

	ctx, cancel := context.WithTimeout(ctx, listModelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		buildURL(baseURL, protocol.Endpoint{Path: modelsPath, Proto: p}, ""), nil)
	if err != nil {
		res.Detail = "请求构造失败"
		return res
	}
	applyHeaders(req.Header, http.Header{}, p, scheme, credential, false)

	resp, err := (&http.Client{Timeout: listModelsTimeout}).Do(req)
	if err != nil {
		// Redact 摘掉传输错误里内嵌的 URL：这段文案会进管理端页面
		// （CLAUDE.md：错误回显严禁泄露上游 key 与 base_url）。
		res.Detail = "连不上：" + Redact(err).Error()
		return res
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		// 不回显响应体：上游的错误体里可能原样带回我们发过去的 key。
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			res.Detail = "上游没有 " + modelsPath + " 这个子路径，只能手工填"
		case http.StatusUnauthorized, http.StatusForbidden:
			res.Detail = "凭证不被接受，换一把再拉"
		default:
			res.Detail = "上游拒绝了这次拉取"
		}
		return res
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, listModelsLimit)).Decode(&payload); err != nil {
		res.Detail = "上游返回的不是模型列表的形状"
		return res
	}
	for _, m := range payload.Data {
		if m.ID != "" {
			res.Models = append(res.Models, m.ID)
		}
	}
	if len(res.Models) == 0 {
		res.Detail = "上游回了一张空列表"
		return res
	}
	res.Detail = "拉到 " + strconv.Itoa(len(res.Models)) + " 个模型"
	return res
}

// ListModelsFor 按渠道声明的每个协议拉模型列表，**各协议打各的出站根地址**（口径层
// v0.96 ②，#49——此前收单个 baseURL，把回退序第一个地址打给了全部协议，多协议渠道
// 的 anthropic 列表会拉到 openai 的根上；ProbeModel 那边一直是对的）。
//
// 串行而不是并发：拉的是同一个上游同一把凭证，两三次请求并发过去省不下多少，却容易
// 在限流严的中转站上把两次都撞成 429。这跟 Probe 逐凭证串行探是同一个取舍。
func ListModelsFor(ctx context.Context, urls store.BaseURLs, scheme, credential string) []ModelListResult {
	set := urls.Protocols()
	out := []ModelListResult{}
	done := map[protocol.Protocol]bool{}
	for _, p := range set {
		if done[p] {
			continue
		}
		base := urls.Get(p)
		res := ListModels(ctx, base, p, scheme, credential)
		// 一份结果只对「同组**且同地址**」的协议成立：openai 拉过之后
		// openai_responses 不用再打一次的前提是两者挂在同一个根下，v0.96 起它们
		// 可以各挂各的，地址不同就是两份答案、各拉各的。顺带把回给前端的
		// Protocols 收窄到渠道真声明了的那些——渠道只勾了 openai_responses 时，
		// 说这份列表对 openai 也成立会让表单勾出一个渠道根本不支持的协议。
		group := res.Protocols
		res.Protocols = nil
		for _, q := range group {
			if !set.Has(q) || urls.Get(q) != base {
				continue
			}
			done[q] = true
			res.Protocols = append(res.Protocols, q)
		}
		out = append(out, res)
	}
	return out
}

// sameModelsEndpoint 给出与 p 共用同一次模型列表拉取的全部协议。
//
// openai 与 openai_responses 打的是同一个 URL、带的是同一套头，因此拉一次就够，
// 结果对两者都成立。把这条知识留在协议层附近，别让调用方自己去猜哪些能合并。
func sameModelsEndpoint(p protocol.Protocol) []protocol.Protocol {
	switch p {
	case protocol.OpenAI, protocol.OpenAIResponses:
		return []protocol.Protocol{protocol.OpenAI, protocol.OpenAIResponses}
	default:
		return []protocol.Protocol{p}
	}
}
