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

// 两档头候选各按各的头名取，先后由 callRecord 定（三档取值见口径层 v0.74，
// 网关层的用例在 internal/server/requestid_test.go）。
func TestRequestIDs(t *testing.T) {
	cases := []struct {
		name            string
		hdr             http.Header
		official, proxy string
	}{
		{"官方拼写", http.Header{"Request-Id": {"req_018Ee"}}, "req_018Ee", ""},
		{"中转常用的 x- 前缀", http.Header{"X-Request-Id": {"9f2c"}}, "", "9f2c"},
		{"两个都在各归各位", http.Header{"Request-Id": {"req_018Ee"}, "X-Request-Id": {"9f2c"}}, "req_018Ee", "9f2c"},
		{"都没有", http.Header{}, "", ""},
		// 有头但值是空的与没这个头同待遇：调用方判空串，取值自然落到下一档。
		{"有头但值是空的", http.Header{"Request-Id": {""}, "X-Request-Id": {"9f2c"}}, "", "9f2c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			official, proxy := upstream.RequestIDs(tc.hdr)
			if official != tc.official || proxy != tc.proxy {
				t.Errorf("RequestIDs = (%q, %q), 期望 (%q, %q)", official, proxy, tc.official, tc.proxy)
			}
		})
	}
}
