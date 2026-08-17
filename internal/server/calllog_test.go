package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol"
)

// reportedModel 刻意不等于 upstreamModel（我们发过去的纳管模型名）：上游把它路由到
// 了别的小版本。两者若取同一个值，把 sum.Model 误接成 cand.UpstreamModel 的实现照样
// 能绿——那正是这条断言要防的回归。
const reportedModel = "claude-sonnet-4-5-20251101"

// anthropicStreamFrames 是一条完整的 Anthropic 流：usage 刻意拆在 message_start
// 与 message_delta 两处，与真实上游一致。
func anthropicStreamFrames() []string {
	return []string{
		sseFrame("message_start", `{"type":"message_start","message":{"id":"msg_01","model":`+
			`"`+reportedModel+`","usage":{"input_tokens":31,"cache_creation_input_tokens":12,`+
			`"cache_read_input_tokens":2048,"output_tokens":1}}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":0,`+
			`"delta":{"type":"text_delta","text":"你好"}}`),
		sseFrame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},`+
			`"usage":{"output_tokens":57}}`),
		sseFrame("message_stop", `{"type":"message_stop"}`),
	}
}

func newLoggingGateway(t *testing.T, opts gatewaytest.Options) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	return gatewaytest.StartWith(t, db, opts), up
}

// 一次流式调用落一行，且 usage 是 Tap 从**转发出去的同一份字节**里旁路解出来的。
func TestCallLogCarriesUsageFromTheRelayedStream(t *testing.T) {
	gw, up := newLoggingGateway(t, gatewaytest.Options{})
	frames := anthropicStreamFrames()
	streamUpstream(t, up, frames...)()

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	// 前提：日志这条路不能把转发字节弄脏。
	if want := strings.Join(frames, ""); body != want {
		t.Fatalf("挂上 Tap 后转发字节变了\n期望: %q\n实得: %q", want, body)
	}

	line := gw.LastCall(t)
	for key, want := range map[string]int64{
		"input_tokens": 31, "output_tokens": 57,
		"cache_read_tokens": 2048, "cache_write_tokens": 12,
		"status": http.StatusOK,
	} {
		if got := line.Int64(key); got != want {
			t.Errorf("%s = %d, 期望 %d", key, got, want)
		}
	}
	for key, want := range map[string]string{
		"endpoint": "/v1/messages", "inbound_protocol": "anthropic",
		"requested_model": accessPointModel, "channel": "test-anthropic",
		"channel_protocol": "anthropic", "upstream_model": upstreamModel,
		"stop_reason": "end_turn", "outcome": "ok",
		// 上游自报的模型名与我们发过去的纳管模型名分开记：两者对不上是排障线索。
		"upstream_reported_model": reportedModel,
	} {
		if got := line.Str(key); got != want {
			t.Errorf("%s = %q, 期望 %q", key, got, want)
		}
	}
	if v, ok := line.Attrs["stream"]; v != true {
		t.Errorf("stream = %v (present=%v), 期望 true", v, ok)
	}
	if _, ok := line.Attrs["ttfb_ms"]; !ok {
		t.Error("缺 ttfb_ms——首字节耗时是判断「上游慢」还是「网关缓冲了」的唯一依据")
	}
	if _, ok := line.Attrs["duration_ms"]; !ok {
		t.Error("缺 duration_ms")
	}
	if v, ok := line.Attrs["tap_degraded"]; ok {
		t.Errorf("正常流被标成了降级: %v", v)
	}
	if len(gw.Lines("call")) != 1 {
		t.Errorf("一次调用落了 %d 行日志", len(gw.Lines("call")))
	}
}

func TestCallLogHandlesNonStreamAndMissingUsage(t *testing.T) {
	t.Run("非流式", func(t *testing.T) {
		gw, up := newLoggingGateway(t, gatewaytest.Options{})
		up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
			`{"id":"msg_1","model":"claude-sonnet-4-5-20250929","stop_reason":"max_tokens",`+
				`"usage":{"input_tokens":8,"output_tokens":1024}}`)

		gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

		line := gw.LastCall(t)
		if line.Int64("input_tokens") != 8 || line.Int64("output_tokens") != 1024 {
			t.Errorf("非流式 usage 没解出来: %+v", line.Attrs)
		}
		if line.Str("stop_reason") != "max_tokens" {
			t.Errorf("stop_reason = %q", line.Str("stop_reason"))
		}
		if v, ok := line.Attrs["stream"]; v != false {
			t.Errorf("stream = %v (present=%v), 期望 false", v, ok)
		}
	})

	t.Run("上游不回 usage", func(t *testing.T) {
		gw, up := newLoggingGateway(t, gatewaytest.Options{})
		up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
			`{"id":"msg_1","model":"claude-sonnet-4-5-20250929"}`)

		gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", anthropicRequest, nil))

		line := gw.LastCall(t)
		// 缺 usage 是合法情形：字段要在、值为零，而不是整行日志没了或标成降级。
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"} {
			v, ok := line.Attrs[key]
			if !ok {
				t.Errorf("缺字段 %s", key)
			}
			if line.Int64(key) != 0 {
				t.Errorf("%s = %v, 期望零值", key, v)
			}
		}
		if _, ok := line.Attrs["tap_degraded"]; ok {
			t.Error("上游不回 usage 不是 Tap 降级")
		}
	})
}

// 被临时闸挡下、以及上游根本连不上的调用，同样要落日志——排障时最想看的就是这两种。
func TestCallLogCoversRejectedAndFailedCalls(t *testing.T) {
	t.Run("闸门拒绝", func(t *testing.T) {
		up := gatewaytest.NewUpstream(t)
		db := gatewaytest.NewDB(t)
		// #80 之后九宫格全开，唯一还会被闸挡下的是 count_tokens——它是 Anthropic
		// 独有端点，另外两个协议根本没有可以转过去的上游端点。载体换了，这条要钉的
		// 「被挡下的调用照样落日志」没变。
		gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL, "gpt-5.6", openaiCredential)
		gw := gatewaytest.StartWith(t, db, gatewaytest.Options{})

		gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages/count_tokens", anthropicRequest, nil))

		line := gw.LastCall(t)
		if line.Int64("status") != http.StatusNotImplemented || line.Str("outcome") != "rejected" {
			t.Errorf("status/outcome = %d/%q, 期望 501/rejected", line.Int64("status"), line.Str("outcome"))
		}
		// 命中了哪个渠道要记下来——不然「为什么被挡」得靠猜。
		if line.Str("channel_protocol") != "openai_responses" {
			t.Errorf("channel_protocol = %q", line.Str("channel_protocol"))
		}
		if _, ok := line.Attrs["input_tokens"]; ok {
			t.Error("没到上游却记了 token 数")
		}
	})

	t.Run("接入点不存在", func(t *testing.T) {
		gw, _ := newLoggingGateway(t, gatewaytest.Options{})

		gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages",
			`{"model":"没配过的接入点","messages":[]}`, nil))

		line := gw.LastCall(t)
		if line.Int64("status") != http.StatusNotFound {
			t.Errorf("status = %d, 期望 404", line.Int64("status"))
		}
		if line.Str("requested_model") != "没配过的接入点" {
			t.Errorf("requested_model = %q，客户端请求的模型名必须记下来", line.Str("requested_model"))
		}
		if _, ok := line.Attrs["channel"]; ok {
			t.Error("没解析出候选却记了渠道")
		}
	})
}

// 首字节之后断流：状态码早已是 200 发出去了，只有 outcome 能看出这次其实没说完。
func TestCallLogMarksStreamAbortedAfterFirstByte(t *testing.T) {
	gw, up := newLoggingGateway(t, gatewaytest.Options{})
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseFrame("message_start",
			`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":5}}}`))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	_, _ = io.ReadAll(resp.Body)

	line := gw.LastCall(t)
	if line.Str("outcome") != "stream_aborted" {
		t.Errorf("outcome = %q, 期望 stream_aborted", line.Str("outcome"))
	}
	if line.Int64("status") != http.StatusOK {
		t.Errorf("status = %d；断流发生在 200 已发出之后", line.Int64("status"))
	}
	// 断流前解出来的部分要留着，那是判断「断在哪」的线索。
	if line.Int64("input_tokens") != 5 {
		t.Errorf("input_tokens = %d, 期望 5", line.Int64("input_tokens"))
	}
}

// Tap 的缓冲上限只许影响日志字段，绝不许碰转发字节。这条得在主接缝上验：单测只
// 证明了 Tap 自己会降级，证明不了「客户端收到的还是原样」。
func TestOversizedFrameDegradesLogButNotRelayedBytes(t *testing.T) {
	gw, up := newLoggingGateway(t, gatewaytest.Options{})
	// 一个超出 Tap 缓冲上限的巨帧，夹在正常帧之间——并行工具调用的大参数帧就长这样。
	huge := sseFrame("content_block_delta", `{"type":"content_block_delta","index":0,`+
		`"delta":{"type":"input_json_delta","partial_json":"`+
		strings.Repeat("A", protocol.BufferLimit+4096)+`"}}`)
	frames := []string{anthropicStreamFrames()[0], huge, anthropicStreamFrames()[2]}
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			w.(http.Flusher).Flush()
		}
	}

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if want := strings.Join(frames, ""); body != want {
		t.Errorf("巨帧把转发字节改了：收到 %d 字节，期望 %d", len(body), len(want))
	}
	line := gw.LastCall(t)
	if v, _ := line.Attrs["tap_degraded"].(bool); !v {
		t.Error("丢了帧却没标 tap_degraded，日志会假装自己是准的")
	}
	// 丢的只是那一帧：巨帧之后的 message_delta 仍要解出来。
	if line.Int64("output_tokens") != 57 || line.Str("stop_reason") != "end_turn" {
		t.Errorf("巨帧之后的 usage 被带走了: output_tokens=%d stop_reason=%q",
			line.Int64("output_tokens"), line.Str("stop_reason"))
	}
}

func TestLogBodiesSwitch(t *testing.T) {
	const marker = "这段内容只有开了 log_bodies 才该出现"
	request := `{"model":"` + accessPointModel + `","messages":[{"role":"user","content":"` + marker + `"}]}`
	const responseMarker = "上游回的私密内容"
	response := `{"id":"msg_1","model":"m","content":[{"type":"text","text":"` + responseMarker + `"}]}`

	for _, tc := range []struct {
		name string
		on   bool
	}{{"默认关闭", false}, {"打开", true}} {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := newLoggingGateway(t, gatewaytest.Options{LogBodies: tc.on})
			up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, response)

			gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", request, nil))

			raw := gw.RawLog()
			if got := strings.Contains(raw, marker); got != tc.on {
				t.Errorf("请求体出现在日志里 = %v, 期望 %v", got, tc.on)
			}
			if got := strings.Contains(raw, responseMarker); got != tc.on {
				t.Errorf("响应体出现在日志里 = %v, 期望 %v", got, tc.on)
			}
		})
	}
}

// 日志是最容易被整段复制出去的东西。凭证与 base_url 一个字节都不许进去——
// 包括 log_bodies 打开这种「什么都记」的情形。
func TestCallLogNeverLeaksCredentialOrBaseURL(t *testing.T) {
	gw, up := newLoggingGateway(t, gatewaytest.Options{LogBodies: true})
	frames := anthropicStreamFrames()
	streamUpstream(t, up, frames...)()

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", streamRequest, nil))
	// 再来一次连不上的：传输错误里内嵌 base_url，是最容易漏出去的一处。
	gw2, up2 := newLoggingGateway(t, gatewaytest.Options{LogBodies: true})
	dead := up2.URL
	up2.Close()
	gatewaytest.ReadBody(t, gw2.Post(t, "/v1/messages", anthropicRequest, nil))

	for _, tc := range []struct {
		raw     string
		baseURL string
	}{{gw.RawLog(), up.URL}, {gw2.RawLog(), dead}} {
		if tc.raw == "" {
			t.Fatal("没有采到任何日志，这条断言等于没做")
		}
		if strings.Contains(tc.raw, anthropicCredential) {
			t.Errorf("日志泄漏了上游凭证:\n%s", tc.raw)
		}
		if strings.Contains(tc.raw, tc.baseURL) {
			t.Errorf("日志泄漏了渠道 base_url (%s):\n%s", tc.baseURL, tc.raw)
		}
	}
}
