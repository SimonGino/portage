package server_test

import (
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件钉的是调用日志那一行的**属性顺序**。
//
// 为什么单开一条 golden：属性顺序是 slog 文本输出里唯一不被逐字段断言覆盖的东西
// ——gatewaytest 的 LogLine 把属性摊成 map，顺序在那一层就没了。#11 把 logCall
// 整体搬进了 calllog.Recorder.LogAttrs，搬家最容易悄悄改的恰恰是 append 的先后。
// 顺序变了，人眼扫日志的肌肉记忆与任何按位置切分的下游（grep/awk 一把梭）都会坏，
// 而所有既有用例照绿。
//
// 钉的是**键的序列**不是整行文本：值里有时间戳、端口、随机 id，钉死会天天红。
//
// 三条 golden 之间的分工：一次跑满的流式调用走满常规分支，一次 401 只剩恒打的那批，
// 一次重试 + 思考 token 覆盖夹在中间的两个条件属性。**剩下四个条件属性没有钉住**，
// 各有各的理由：`queue_wait_ms` 要让并发闸上真排出一个非零毫秒数（钉它等于钉一场
// 时序赛跑，会天天红）；`tap_degraded` 要造一份解不动的 usage；`request_body` 与
// `response_body` 要开 log_bodies，而下面那个正则在开着的时候本就不可靠（见 callLogKeys）。
// 它们各自的**存在与否**仍有既有用例管（retry_test.go、闸拒那几条、TestLogBodiesSwitch），
// 这里漏的只是「顺序」这一维。

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

// 一次重试之后成功的思考调用：把夹在中间的 retries 与排在 usage 之后的
// reasoning_tokens 补进顺序契约里。上面那条跑满的流式调用两个都走不到——它一次
// 打成，上游也没报思考数。
func TestCallLogAttributeOrderWithRetriesAndReasoning(t *testing.T) {
	gw, up := newLoggingGateway(t, gatewaytest.Options{Retry: fastRetry(2)})
	var calls atomic.Int32
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// 503 是可重试的那一档，重试一次之后这一行就有 retries=1。
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("request-id", "req_018EeWyXxfu5pfWkrYcMdjWG")
		w.WriteHeader(http.StatusOK)
		for _, f := range anthropicThinkingStreamFrames() {
			_, _ = w.Write([]byte(f))
			w.(http.Flusher).Flush()
		}
	}

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", streamRequest, nil))
	gw.LastCall(t)

	assertKeyOrder(t, callLogKeys(t, gw.RawLog()), []string{
		"time", "level", "msg",
		"endpoint", "api_key", "inbound_protocol", "requested_model",
		"stream", "status", "outcome", "duration_ms",
		"channel", "channel_protocol", "upstream_model",
		"channel_key", "upstream_endpoint",
		"retries",
		"upstream_request_id", "ttfb_ms",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"stop_reason", "upstream_reported_model",
		"reasoning_tokens",
	})
}

// anthropicThinkingStreamFrames 是同一条流，只是 message_delta 那一帧多报了思考数
// （Anthropic 的载体是 output_tokens_details.**thinking_tokens**，口径层 v0.79）。
func anthropicThinkingStreamFrames() []string {
	frames := anthropicStreamFrames()
	frames[2] = sseFrame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},`+
		`"usage":{"output_tokens":57,"output_tokens_details":{"thinking_tokens":249}}}`)
	return frames
}
