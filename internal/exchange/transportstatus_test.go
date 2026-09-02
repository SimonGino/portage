package exchange

import (
	"errors"
	"net"
	"net/http"
	"testing"
)

// timeoutErr 是这一档唯一要的信息：net.Error 且 Timeout() 为真。真超时错误长什么
// 样由 upstream.TestTimeoutRecognizesRealTransportTimeout 钉，这里钉的是分档本身。
type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }
func (timeoutErr) Timeout() bool { return true }

// Temporary 已废弃，但 net.Error 的方法集还带着它。
func (timeoutErr) Temporary() bool { return false }

var _ net.Error = timeoutErr{}

// TestTransportStatusSplitsTimeoutFrom502 钉口径层 v1.16：等超时 504、链路坏 502，
// 且文案跟着状态码走——只有码没有话，光看响应体的人分不出这两件事。
func TestTransportStatusSplitsTimeoutFrom502(t *testing.T) {
	if status, msg := transportStatus(timeoutErr{}, "glm53"); status != http.StatusGatewayTimeout || msg != "上游渠道 glm53 响应超时" {
		t.Errorf("超时 → (%d, %q), 期望 (504, 上游渠道 glm53 响应超时)", status, msg)
	}
	if status, msg := transportStatus(errors.New("connection refused"), "glm53"); status != http.StatusBadGateway || msg != "上游渠道 glm53 请求失败" {
		t.Errorf("连不上 → (%d, %q), 期望 (502, 上游渠道 glm53 请求失败)", status, msg)
	}
}
