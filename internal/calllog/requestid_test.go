package calllog_test

import (
	"testing"

	"github.com/SimonGino/portage/internal/calllog"
)

// request_id 的第二档解析。从 internal/upstream 搬来（#11 C11）：依赖方向不允许
// 本包 import upstream，而这个函数唯一的用处就是给流水那一列的三档取值供第二档。
// 断言一字未改。

// 第二档（口径层 v0.74）：官方那个号在错误信封的体里，而中转把响应头裁了。
func TestErrorBodyRequestID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"Anthropic 错误信封",
			`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"},` +
				`"request_id":"req_011Ce2vfd1LHuZQWqwDRc5mm"}`,
			"req_011Ce2vfd1LHuZQWqwDRc5mm",
		},
		// 键排在 error 之后是常态，但排在前面也一样取得到——JSON 里键序无意义。
		{
			"键排在前面",
			`{"request_id":"req_first","type":"error","error":{"message":"x"}}`,
			"req_first",
		},
		{"没有这个键", `{"type":"error","error":{"message":"boom"}}`, ""},
		{"HTML 错误页", "<html><body>502 Bad Gateway</body></html>", ""},
		// 截断的字节（错误原文超过 upstreamErrorLimit）解不成 JSON，落空串。
		{"截断的 JSON", `{"type":"error","error":{"message":"boo`, ""},
		// 流式错误帧本库无样本、形状未知：SSE 那层不解，整段字节不是 JSON，落空串。
		{"SSE 错误帧", "event: error\ndata: {\"type\":\"error\",\"request_id\":\"req_x\"}\n\n", ""},
		{"空体", "", ""},
		{"键在但值是空的", `{"request_id":"","error":{"message":"x"}}`, ""},
		// 只认顶层：嵌在别处的同名键不是官方那个号的位置。
		{"嵌在 error 里的同名键", `{"error":{"request_id":"req_nested"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := calllog.ErrorBodyRequestID([]byte(tc.body)); got != tc.want {
				t.Errorf("ErrorBodyRequestID = %q, 期望 %q", got, tc.want)
			}
		})
	}
}
