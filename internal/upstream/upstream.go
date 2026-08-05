// Package upstream turns a resolved route plus the client's raw request body
// into a real HTTP call against the channel.
package upstream

import (
	"bytes"
	"context"
	"errors"
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
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 120 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: time.Second,
			MaxIdleConnsPerHost:   16,
		},
	}}
}

// Do 把 body 原样透传给候选所在渠道，返回实时响应。调用方负责 Close resp.Body。
func (c *Client) Do(ctx context.Context, cand store.Candidate, ep protocol.Endpoint, body []byte, clientHdr http.Header, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildURL(cand.BaseURL, ep), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	applyHeaders(req.Header, clientHdr, cand, stream)
	return c.http.Do(req)
}

// buildURL appends the endpoint's fixed suffix to the 渠道 base_url, which stores
// everything *before* the protocol sub-path. 百炼这类自带路径前缀的兼容端点因此要
// 填到 .../compatible-mode 为止。
func buildURL(baseURL string, ep protocol.Endpoint) string {
	return strings.TrimRight(baseURL, "/") + ep.Path
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
