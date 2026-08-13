package upstream_test

import (
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/upstream"
)

// 整套头名的出处与它验不到什么，见 gatewaytest.AnthropicResponseHeaders 的注释。
func TestCopyResponseHeadersPassesTheDocumentedAnthropicSet(t *testing.T) {
	src := http.Header{}
	for k, v := range gatewaytest.AnthropicResponseHeaders {
		src.Set(k, v)
	}
	dst := http.Header{}
	upstream.CopyResponseHeaders(dst, src)

	for k, want := range gatewaytest.AnthropicResponseHeaders {
		if got := dst.Get(k); got != want {
			t.Errorf("%s = %q, 期望原样 %q", k, got, want)
		}
	}
}

// Content-Length 是唯一的例外（展开层 §6.1）：转发出去的字节数由 net/http 按实际
// 写入量重设，照抄上游那个数会在任何一处改写下把响应截断。
func TestCopyResponseHeadersDropsContentLength(t *testing.T) {
	src := http.Header{"Content-Length": {"512"}, "Content-Type": {"application/json"}}
	dst := http.Header{}
	upstream.CopyResponseHeaders(dst, src)

	if got := dst.Values("Content-Length"); len(got) != 0 {
		t.Errorf("Content-Length = %v, 期望被丢掉", got)
	}
	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, 期望原样过去", got)
	}
}

// 多值头按值逐条过去，不合并成一条：Set-Cookie 那类头合并了就不再等价。
func TestCopyResponseHeadersKeepsMultipleValues(t *testing.T) {
	src := http.Header{"Set-Cookie": {"a=1", "b=2"}}
	dst := http.Header{}
	upstream.CopyResponseHeaders(dst, src)

	got := dst.Values("Set-Cookie")
	if len(got) != 2 || got[0] != "a=1" || got[1] != "b=2" {
		t.Errorf("Set-Cookie = %v, 期望 [a=1 b=2]", got)
	}
}

func TestRequestID(t *testing.T) {
	cases := []struct {
		name string
		hdr  http.Header
		want string
	}{
		{"官方拼写", http.Header{"Request-Id": {"req_018Ee"}}, "req_018Ee"},
		{"中转常用的 x- 前缀兜底", http.Header{"X-Request-Id": {"9f2c"}}, "9f2c"},
		// 两个都在时以官方那个为准：中转会给自己也编一个号，对账要找的是上游的。
		{"两个都在取官方的", http.Header{"Request-Id": {"req_018Ee"}, "X-Request-Id": {"9f2c"}}, "req_018Ee"},
		{"都没有", http.Header{}, ""},
		{"有头但值是空的", http.Header{"Request-Id": {""}, "X-Request-Id": {"9f2c"}}, "9f2c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstream.RequestID(tc.hdr); got != tc.want {
				t.Errorf("RequestID = %q, 期望 %q", got, tc.want)
			}
		})
	}
}
