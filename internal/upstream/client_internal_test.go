package upstream

import (
	"net/http"
	"testing"
	"time"
)

// 这是全项目唯一的白盒测试，理由：§6.1 的「不设 http.Client.Timeout」是一条**配置**
// 约束，而它的破坏后果——长对话在中途被掐断——只有跑满那个超时才看得见。行为测试
// 要复现就得让流真的活过 30s/60s/120s，代价不可接受，于是只能直接钉字段。
//
// 与之配套的行为测试（TestStreamSurvivesLongSilence）只覆盖秒级的短超时，别指望它。
func TestClientHasNoOverallTimeout(t *testing.T) {
	c := NewClient(DefaultRetryPolicy())

	if c.http.Timeout != 0 {
		t.Errorf("http.Client.Timeout = %v, 必须为 0——它覆盖整个 body 读取周期，"+
			"设上之后长 SSE 流会被拦腰掐断（§6.1）", c.http.Timeout)
	}
}

// 不设整体超时不等于没有超时：三层分别兜住握手、上游迟迟不给响应头、空闲连接。
func TestClientSetsLayeredTimeouts(t *testing.T) {
	tr, ok := NewClient(DefaultRetryPolicy()).http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型 = %T, 期望 *http.Transport", NewClient(DefaultRetryPolicy()).http.Transport)
	}

	for _, tc := range []struct {
		name string
		got  time.Duration
	}{
		{"TLSHandshakeTimeout", tr.TLSHandshakeTimeout},
		{"ResponseHeaderTimeout", tr.ResponseHeaderTimeout},
		{"IdleConnTimeout", tr.IdleConnTimeout},
		{"DialContext 的 net.Dialer.Timeout", newDialer().Timeout},
	} {
		if tc.got <= 0 {
			t.Errorf("%s = %v, 未设置", tc.name, tc.got)
		}
	}

	// 上面那条只证明 dialer 本身有上限，还得证明 transport 真的用了它——零值
	// transport 会退回无超时的 net.Dial，而那正是这条约束要拦的情况。
	if tr.DialContext == nil {
		t.Error("Transport.DialContext = nil，零值 transport 用的是无超时的 net.Dialer；" +
			"上面两个超时都在 TCP 连上之后才起算，地址被黑洞时请求会挂到操作系统放弃")
	}
}
