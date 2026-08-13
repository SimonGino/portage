// Package upstream turns a resolved route plus the client's raw request body
// into a real HTTP call against the channel.
package upstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
)

// Client holds the shared transport.
//
// 刻意不设 http.Client.Timeout——它覆盖整个 body 读取周期，长流必被拦腰掐断
// （docs/MVP设计草案.md §6.1）。超时按 TLS 握手 / 响应头 / 空闲连接分层。
type Client struct {
	http  *http.Client
	retry RetryPolicy

	// Disable 是 401 摘除凭证的挂钩（口径层 v0.38），由 server 接到 store 上。
	// nil 即不摘——用例与探测路径不需要它。
	//
	// 摘除是**副作用**而不是返回值：401 可能发生在中间某一份凭证上，而那次请求
	// 最终是成功的，返回值里没有地方安放「顺带摘了一把」。
	Disable func(cred store.Credential, reason string)

	// Queue 是渠道并发闸的排队参数（口径层 v0.50），由 server 从配置接上，
	// 与 Disable 同一个挂法。零值 Wait 会让排队立即超时，兜底在 config.Load。
	Queue QueuePolicy

	// gates 是渠道并发闸，按渠道 id 一份（见 gate.go），与 cursor 同锁。
	gates map[int64]*gate

	// cursor 是 polling 选取模式的轮询游标，按渠道名一份。
	//
	// 只放内存、不落库（口径层 v0.38 实现口径）：它是「上次从哪把开始」这种纯运行
	// 期偏好，重启从头来一遍没有任何损失，而落库要为每个请求加一次写。放在 Client
	// 上而不是 store 的包级变量，是为了让它跟着进程/用例的生命周期走——渠道 id 在
	// 每个测试库里都从 1 开始，包级 map 会让两个用例共享同一个游标。
	mu     sync.Mutex
	cursor map[string]int
}

func NewClient(retry RetryPolicy) *Client {
	return &Client{retry: retry, cursor: map[string]int{}, http: &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           newDialer().DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 120 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
			MaxIdleConnsPerHost:   16,
		},
	}}
}

// newDialer 给 TCP 拨号本身加上限。
//
// 零值 transport 用的是无超时的 net.Dialer，而 TLSHandshakeTimeout 与
// ResponseHeaderTimeout 都在 TCP 连上之后才起算。渠道地址被黑洞（丢包不回 RST）
// 时，少了这一层请求会一直挂到操作系统放弃（75s 量级），而不是及时回 502。
func newDialer() *net.Dialer {
	return &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
}

// Attempt 记一次 Do 的过程：真正发出去了几次，最后用的是哪份凭证。
type Attempt struct {
	// Sends 是实际发往上游的请求次数，含首次。全局尝试上限 MaxAttempts 封的就是它。
	Sends int
	// Credential 是**最后真正发出请求**的那份凭证名（换过则是最后一份，失败亦然），
	// 进 call_logs.channel_key_name 与日志（口径层 v0.38 的按凭证归因）。
	Credential string
	// QueueWait 是在渠道并发闸上排队的耗时（口径层 v0.52），没排队为 0，
	// 进 call_logs.queue_wait_ms。排队被拒的那两种收场它照样有值。
	QueueWait time.Duration
}

// Retries 是「为拿到这个结果多打了几次」，进 call_logs.retry_count。
//
// 自 v0.38 它数的是**全部**重打，不再只数同一份凭证上的退避重试——换凭证同样会
// 让这次调用变慢，而这一列回答的正是「这次怎么慢了三秒」。
func (a Attempt) Retries() int {
	if a.Sends <= 1 {
		return 0
	}
	return a.Sends - 1
}

// Do 把 body 原样透传给候选所在渠道，返回实时响应与这次的尝试过程。
// 调用方负责 Close resp.Body。
//
// 两层重试（口径层 v0.19 + v0.38）：内层对**同一份凭证**按 RetryPolicy 退避重试，
// 外层是 key 层内环——401/403/429 换渠道内下一份启用凭证再来，其中**只有 401 顺带
// 摘除那份凭证**（403 换而不摘：它在上游还可能是「这把没开通这个模型」，摘掉的却是
// 渠道级资源）。5xx/网络错误不换凭证，直接跳出交给候选间转移（M4）。
//
// 全部重试都发生在向客户端写出首字节之前——Do 返回之后 relay 才开始写响应头，所以
// 「首字节边界即承诺边界」这条约束不受影响。
//
// 无论是重试耗尽、凭证耗尽还是撞上全局尝试上限，返回的都是**最后一次**上游响应的
// 原字节，网关不改写不吞：M0 已验证的 429 逐字节透传，不能因为加了这两层而失效。
//
// rawQuery 是客户端 URL 上的查询串，整串照抄给上游（见 buildURL）。
//
// 渠道并发闸（口径层 v0.49/v0.50）拦在最外面：设了上限的渠道先占坑，闸满有界
// 排队，队满/等超时返回 ErrQueueFull / ErrQueueTimeout（不带响应，由 server 译成
// 429）。**一次 Do 只占一个坑**——里面的退避重试与换凭证全在同一个坑里发生，坑
// 一直占到响应体读完（Close）才还，上游还在生成流时并发就是还占着的。
func (c *Client) Do(ctx context.Context, cand store.Candidate, ep protocol.Endpoint, rawQuery string, body []byte, clientHdr http.Header, stream bool) (*http.Response, Attempt, error) {
	if cand.MaxConcurrency <= 0 {
		return c.do(ctx, cand, ep, rawQuery, body, clientHdr, stream)
	}
	g, limit := c.gateFor(cand.ChannelID), cand.MaxConcurrency
	waited, err := g.acquire(ctx, limit, limit*c.Queue.Factor, c.Queue.Wait)
	if err != nil {
		return nil, Attempt{QueueWait: waited}, err
	}
	resp, at, err := c.do(ctx, cand, ep, rawQuery, body, clientHdr, stream)
	at.QueueWait = waited
	if resp == nil {
		// 没有响应体可挂，坑当场还掉——包括 err != nil 与「凭证为空」两种收场。
		g.release(limit)
		return resp, at, err
	}
	resp.Body = &releasingBody{ReadCloser: resp.Body, release: func() { g.release(limit) }}
	return resp, at, err
}

// do 是闸内的主体：凭证外环 + 同凭证退避内环。
func (c *Client) do(ctx context.Context, cand store.Candidate, ep protocol.Endpoint, rawQuery string, body []byte, clientHdr http.Header, stream bool) (*http.Response, Attempt, error) {
	creds := c.order(cand)
	var at Attempt
	for i, cred := range creds {
		at.Credential = cred.Name
		resp, err := c.send(ctx, cand, cred, ep, rawQuery, body, clientHdr, stream, &at)
		if resp != nil && resp.StatusCode == http.StatusUnauthorized && c.Disable != nil {
			// 摘除在这里而不是循环外：401 可能发生在中间某一份凭证上，而这次请求
			// 最终由后面某一份跑成了——那把坏凭证照样得摘。
			c.Disable(cred, "上游回 401")
		}
		if !switchCredential(err, resp) || i == len(creds)-1 || c.budgetOut(at) {
			return resp, at, err
		}
		drain(resp)
	}
	// creds 为空。Resolve 保证不会（零凭证在那一层就报 ErrNoUsableCandidate），
	// 真走到这儿说明调用方自己拼了个候选，报错比发一个不带凭证的请求强。
	return nil, at, errors.New("候选没有可用凭证")
}

// send 在**同一份凭证**上跑退避重试（口径层 v0.19），并把每次真实发送记进 at。
func (c *Client) send(ctx context.Context, cand store.Candidate, cred store.Credential, ep protocol.Endpoint, rawQuery string, body []byte, clientHdr http.Header, stream bool, at *Attempt) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildURL(cand.BaseURL, ep, rawQuery), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(body))
		applyHeaders(req.Header, clientHdr, cand.Protocol, cred.Value, stream)

		resp, err := c.http.Do(req)
		at.Sends++
		if attempt >= c.retry.MaxRetries || c.budgetOut(*at) || !retriable(ctx, resp, err) {
			return resp, err
		}
		// Retry-After 长过 MaxDelay：不等了，把这份响应原样交给客户端自己决定。
		// 注意它必须在 drain 之前返回——drain 会把 body 读空。
		d, ok := delayFor(c.retry, attempt, resp)
		if !ok {
			return resp, err
		}
		drain(resp)
		if !sleep(ctx, d) {
			// 退避途中客户端走了。此时 resp 的 body 已被读空关掉，不能再交出去。
			if err == nil {
				err = ctx.Err()
			}
			return nil, err
		}
	}
}

// switchCredential 判这次失败该不该换渠道内的下一份凭证。
//
// 只有 401/403/429 换：前两者是这**一把**凭证的问题（无效 / 没权限），换一把有可能
// 成功；429 是这把的配额被打满，换一把同样有可能成功。5xx 与网络错误是渠道或链路
// 的问题，换凭证毫无意义——那是候选间转移要处理的（M4），在这里换只会把同一个故障
// 乘以凭证数。其余 4xx 是请求本身有问题，换什么都一样。
func switchCredential(err error, resp *http.Response) bool {
	if err != nil || resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return false
}

// budgetOut 判全局尝试上限（口径层 v0.38 的外层预算）是否已经用完。MaxAttempts
// 为 0 即不封顶。
func (c *Client) budgetOut(at Attempt) bool {
	return c.retry.MaxAttempts > 0 && at.Sends >= c.retry.MaxAttempts
}

// order 按渠道的选取模式排出这次要依次尝试的凭证顺序（口径层 v0.11/v0.38）。
//
// polling 是轮转而不是「永远从第一把开始」：多凭证的意义之一就是把量摊开，每次都
// 从头开始等于第一把跑满、其余当备胎。random 直接洗牌。
func (c *Client) order(cand store.Candidate) []store.Credential {
	if len(cand.Credentials) < 2 {
		return cand.Credentials
	}
	out := make([]store.Credential, len(cand.Credentials))
	copy(out, cand.Credentials)
	if cand.KeyMode == store.KeyModeRandom {
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}
	start := c.nextCursor(cand.ChannelName, len(out))
	rotated := make([]store.Credential, 0, len(out))
	rotated = append(rotated, out[start:]...)
	return append(rotated, out[:start]...)
}

// nextCursor 取这个渠道的下一个起始下标并推进游标。
//
// 取模放在读的时候而不是写的时候：凭证数会变（加一把、摘一把），把计数器本身按当时
// 的 n 归一，下次 n 变了就会跳号。
func (c *Client) nextCursor(channel string, n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cursor == nil {
		c.cursor = map[string]int{}
	}
	i := c.cursor[channel]
	c.cursor[channel] = i + 1
	return i % n
}

// drain 丢掉这次不要的响应体。
//
// 不读完就 Close 会让连接无法复用（Go 只在 body 读到 EOF 后才把连接放回池子），
// 重试场景下这等于每次都新建一条连接。上限是防着上游拿一个巨大的错误体拖死我们。
func drain(resp *http.Response) {
	if resp == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// buildURL appends the endpoint's fixed suffix to the 渠道 base_url, which stores
// everything *before* the protocol sub-path. 百炼这类自带路径前缀的兼容端点因此要
// 填到 .../compatible-mode 为止。
//
// 客户端的查询串**整串照抄**接在后面，不过滤（#20，PO 裁定 jinpenga）。实测
// Claude Code 发的是 `POST /v1/messages?beta=true`，丢掉之后上游收到的是另一个
// 请求，而丢没丢不看日志根本发现不了。不做白名单是因为这里没有可枚举的对象——
// 各家 harness 的私有参数不可穷举，而查询参数不像请求头那样天然带客户端指纹。
//
// 顺序只能是 base + path + "?" + query：store 的启动校验已拦掉带查询串的
// base_url（internal/store/store.go:295），所以这里不会拼出两个 "?"。
// rawQuery 为空时不产生裸 "?"——`/v1/messages?` 与 `/v1/messages` 语义相同，
// 凭空多一个问号只会让日志与样本对不上。
func buildURL(baseURL string, ep protocol.Endpoint, rawQuery string) string {
	u := strings.TrimRight(baseURL, "/") + ep.Path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// Redact strips the request URL out of a transport error so the 渠道 base_url does
// not reach a log line or, later, call_logs.error.
func Redact(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// applyHeaders rebuilds the upstream headers from a whitelist rather than
// copying the client's. Anything not named here never reaches the upstream —
// including the client's own Authorization / x-api-key, which from M1 onward
// carries the gateway key.
//
// credential 是**这次尝试**用的那份凭证值（key 层内环会在同一次请求里换，所以它
// 是参数而不是从候选上读）。
func applyHeaders(dst, client http.Header, p protocol.Protocol, credential string, stream bool) {
	if ct := client.Get("Content-Type"); ct != "" {
		dst.Set("Content-Type", ct)
	} else {
		dst.Set("Content-Type", "application/json")
	}

	switch accept := client.Get("Accept"); {
	case accept != "":
		dst.Set("Accept", accept)
	case stream:
		dst.Set("Accept", "text/event-stream")
	}

	// 流式下显式 identity：上游压缩会引入分块缓冲，拖长首字延迟。
	if stream {
		dst.Set("Accept-Encoding", "identity")
	}

	switch p {
	case protocol.Anthropic:
		dst.Set("x-api-key", credential)
		version := client.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		dst.Set("anthropic-version", version)
		if beta := client.Get("anthropic-beta"); beta != "" {
			dst.Set("anthropic-beta", beta)
		}
	default:
		dst.Set("Authorization", "Bearer "+credential)
	}
}

// RequestID 取上游响应里的请求 id，用于事后找上游对账（口径层 v0.56，#37）。
//
// 头名以 `request-id` 为准：Anthropic 官方文档「Request ID」一节的原文就是这个拼写
// （值形如 req_018Ee…），错误响应体里的 `request_id` 字段是同一个值。兜底
// `x-request-id` 是给中转与自建上游留的——它们多半按通用惯例发这个名字，两个都不给
// 就是没有，回空串。
//
// 只读不改：这个头本身照 CopyResponseHeaders 原样回给客户端，这里取一份是为了落库。
func RequestID(h http.Header) string {
	if v := h.Get("request-id"); v != "" {
		return v
	}
	return h.Get("x-request-id")
}

// CopyResponseHeaders mirrors the upstream response headers onto the client
// response. Content-Length is dropped: it is meaningless once a stream starts,
// and net/http recomputes it for buffered writes.
func CopyResponseHeaders(dst http.Header, src http.Header) {
	for name, values := range src {
		if strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}
