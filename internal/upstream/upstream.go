// Package upstream turns a resolved route plus the client's raw request body
// into a real HTTP call against the channel.
package upstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/store"
)

// Client holds the shared transport.
//
// 刻意不设 http.Client.Timeout——它覆盖整个 body 读取周期，长流必被拦腰掐断
// （docs/MVP设计草案.md §6.1）。超时按 TLS 握手 / 响应头 / 空闲连接分层。
type Client struct {
	http  *http.Client
	retry RetryPolicy
}

func NewClient(retry RetryPolicy) *Client {
	return &Client{retry: retry, http: &http.Client{
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

// Do 把 body 原样透传给候选所在渠道，返回实时响应与「为拿到它重试了几次」。
// 调用方负责 Close resp.Body。
//
// 失败按 RetryPolicy 原地退避重试（同候选同凭证，口径层 v0.19）。重试全部发生在
// 向客户端写出首字节之前——Do 返回之后 relay 才开始写响应头，所以「首字节边界即
// 承诺边界」这条约束不受影响。
//
// 重试耗尽时返回的是**最后一次**上游响应的原字节，网关不改写不吞：M0 已验证的
// 429 逐字节透传，不能因为加了重试而失效。
//
// retries 在 err != nil 时同样有效——重试若干次仍拨不通，日志得看得出来。
//
// rawQuery 是客户端 URL 上的查询串，整串照抄给上游（见 buildURL）。
func (c *Client) Do(ctx context.Context, cand store.Candidate, ep protocol.Endpoint, rawQuery string, body []byte, clientHdr http.Header, stream bool) (resp *http.Response, retries int, err error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildURL(cand.BaseURL, ep, rawQuery), bytes.NewReader(body))
		if err != nil {
			return nil, attempt, err
		}
		req.ContentLength = int64(len(body))
		applyHeaders(req.Header, clientHdr, cand, stream)

		resp, err := c.http.Do(req)
		if attempt >= c.retry.MaxRetries || !retriable(ctx, resp, err) {
			return resp, attempt, err
		}
		// Retry-After 长过 MaxDelay：不等了，把这份响应原样交给客户端自己决定。
		// 注意它必须在 drain 之前返回——drain 会把 body 读空。
		d, ok := delayFor(c.retry, attempt, resp)
		if !ok {
			return resp, attempt, err
		}
		drain(resp)
		if !sleep(ctx, d) {
			// 退避途中客户端走了。此时 resp 的 body 已被读空关掉，不能再交出去。
			if err == nil {
				err = ctx.Err()
			}
			return nil, attempt, err
		}
	}
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
func applyHeaders(dst, client http.Header, cand store.Candidate, stream bool) {
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

	switch cand.Protocol {
	case protocol.Anthropic:
		dst.Set("x-api-key", cand.Credential)
		version := client.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		dst.Set("anthropic-version", version)
		if beta := client.Get("anthropic-beta"); beta != "" {
			dst.Set("anthropic-beta", beta)
		}
	default:
		dst.Set("Authorization", "Bearer "+cand.Credential)
	}
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
