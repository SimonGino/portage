package server_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件钉的是调用日志那一行的**属性顺序**。
//
// 为什么单开一条 golden：属性顺序是 slog 文本输出里唯一不被逐字段断言覆盖的东西
// ——gatewaytest 的 LogLine 把属性摊成 map，顺序在那一层就没了。而 #11 要把
// logCall 整体搬进 calllog.Recorder，搬家最容易悄悄改的恰恰是 append 的先后。
// 顺序变了，人眼扫日志的肌肉记忆与任何按位置切分的下游（grep/awk 一把梭）都会坏，
// 而所有既有用例照绿。
//
// 钉的是**键的序列**不是整行文本：值里有时间戳、端口、随机 id，钉死会天天红。

var logKey = regexp.MustCompile(`(?:^|\s)([a-z_]+)=`)

// callLogKeys 取出 `msg=call` 那一行的属性键，按出现顺序。
//
// 只在 log_bodies 关着的时候可靠：body 那两个属性的值里什么都可能有，
// 会把正则骗出假的键来。这两条 golden 因此都不开 log_bodies。
func callLogKeys(t *testing.T, raw string) []string {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		if !strings.Contains(line, "msg=call ") {
			continue
		}
		var keys []string
		for _, m := range logKey.FindAllStringSubmatch(line, -1) {
			keys = append(keys, m[1])
		}
		return keys
	}
	t.Fatalf("日志里没有 msg=call 那一行:\n%s", raw)
	return nil
}

func assertKeyOrder(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") == strings.Join(want, " ") {
		return
	}
	t.Errorf("调用日志的属性顺序变了\n实得: %s\n期望: %s",
		strings.Join(got, " "), strings.Join(want, " "))
}

// 一次跑满的流式调用：渠道、凭证名、出站端点、request id、首字节、usage 全都有值，
// 「有值才打」的条件分支因此全部走到。
func TestCallLogAttributeOrderOnAFullStreamingCall(t *testing.T) {
	gw, up := newLoggingGateway(t, gatewaytest.Options{})
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("request-id", "req_018EeWyXxfu5pfWkrYcMdjWG")
		w.WriteHeader(http.StatusOK)
		for _, f := range anthropicStreamFrames() {
			_, _ = w.Write([]byte(f))
			w.(http.Flusher).Flush()
		}
	}

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", streamRequest, nil))
	gw.LastCall(t) // 等那一行落下

	assertKeyOrder(t, callLogKeys(t, gw.RawLog()), []string{
		"time", "level", "msg",
		"endpoint", "api_key", "inbound_protocol", "requested_model",
		"stream", "status", "outcome", "duration_ms",
		"channel", "channel_protocol", "upstream_model",
		"channel_key", "upstream_endpoint",
		"upstream_request_id", "ttfb_ms",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"stop_reason", "upstream_reported_model",
	})
}

// 一次 401：只剩恒打的那一批。这条钉的是「没有值的那些属性整个不出现」——
// 不是打成空串，也不是打成 0。
func TestCallLogAttributeOrderOnAnUnauthorizedCall(t *testing.T) {
	gw, _ := newLoggingGateway(t, gatewaytest.Options{})

	gw.Post(t, "/v1/messages", anthropicRequest, map[string]string{"x-api-key": ""})
	gw.LastCall(t)

	assertKeyOrder(t, callLogKeys(t, gw.RawLog()), []string{
		"time", "level", "msg",
		"endpoint", "api_key", "inbound_protocol", "requested_model",
		"stream", "status", "outcome", "duration_ms",
	})
}
