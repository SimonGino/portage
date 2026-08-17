package server_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/gatewaytest"
)

// fastRetry 是行为测试用的策略：退避压到毫秒级，只保留「重试几次」这一个语义。
// 真实取值（500ms 起步）由 TestRetryUsesShippedDefaults 单独跑一遍。
func fastRetry(maxRetries int) config.Retry {
	return config.Retry{
		MaxRetries: maxRetries,
		BaseDelay:  time.Millisecond,
		MaxDelay:   50 * time.Millisecond,
	}
}

// countingUpstream 让假上游每次按第几次被打给出不同的响应体。
//
// 各次响应必须**可区分**：全都回同一份 body 的话，「返回最后一次」和「返回第一次
// 缓存下来的那份」两种实现都能绿。
func countingUpstream(up *gatewaytest.Upstream, status int, header map[string]string) *atomic.Int64 {
	var hits atomic.Int64
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"type":"error","error":{"message":"attempt-%d"}}`, n)
	}
	return &hits
}

func newRetryGateway(t *testing.T, retry config.Retry) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{Retry: retry}), up
}

// 重试耗尽后交出去的必须是**最后一次**上游响应的原字节：状态码、全部响应头、body
// 一个字节都不改。M0 验过的 429 逐字节透传，不能因为加了重试而失效。
func TestRetryReturnsLastUpstreamResponseVerbatim(t *testing.T) {
	gw, up := newRetryGateway(t, fastRetry(2))
	hits := countingUpstream(up, http.StatusTooManyRequests, map[string]string{
		"Content-Type":          "application/json",
		"X-Ratelimit-Remaining": "0",
	})

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if got := hits.Load(); got != 3 {
		t.Errorf("上游被打了 %d 次, 期望 3（首次 + 重试 2 次）", got)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("状态码 = %d, 期望原样 429；body=%s", resp.StatusCode, body)
	}
	if want := `{"type":"error","error":{"message":"attempt-3"}}`; body != want {
		t.Errorf("body = %q, 期望是最后一次上游响应 %q", body, want)
	}
	if v := resp.Header.Get("X-Ratelimit-Remaining"); v != "0" {
		t.Errorf("X-Ratelimit-Remaining = %q, 期望原样回传 0", v)
	}
	assertNoSecrets(t, body, anthropicCredential, up.URL)
}

// 上游明说了 Retry-After 就不许比它更急——抖动只作用在指数退避那部分。
func TestRetryWaitsAtLeastRetryAfter(t *testing.T) {
	const retryAfter = 1100 * time.Millisecond
	gw, up := newRetryGateway(t, config.Retry{
		MaxRetries: 1,
		BaseDelay:  time.Millisecond, // 指数部分远小于 Retry-After，慢下来只能是它起了作用
		MaxDelay:   5 * time.Second,
	})
	hits := countingUpstream(up, http.StatusTooManyRequests, map[string]string{
		"Content-Type": "application/json",
		"Retry-After":  "1",
	})

	start := time.Now()
	gw.Post(t, "/v1/messages", anthropicRequest, nil)
	elapsed := time.Since(start)

	if got := hits.Load(); got != 2 {
		t.Fatalf("上游被打了 %d 次, 期望 2", got)
	}
	// 留 100ms 余量给 Retry-After 秒级取整与调度抖动。
	if elapsed < retryAfter-200*time.Millisecond {
		t.Errorf("整趟耗时 %v, 短于上游要求的 1s——Retry-After 被无视了", elapsed)
	}
}

// Retry-After 长过 MaxDelay 时不重试：照等就是把客户端在这儿扣一分钟，不如把这份
// 429 原样交出去让调用方自己决定。
func TestRetryGivesUpWhenRetryAfterExceedsMaxDelay(t *testing.T) {
	gw, up := newRetryGateway(t, fastRetry(2))
	hits := countingUpstream(up, http.StatusTooManyRequests, map[string]string{
		"Content-Type": "application/json",
		"Retry-After":  "60", // MaxDelay 是 50ms
	})

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if got := hits.Load(); got != 1 {
		t.Errorf("上游被打了 %d 次, 期望 1——不该为了一分钟后的重试把客户端扣住", got)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("状态码 = %d, 期望原样 429", resp.StatusCode)
	}
	if want := `{"type":"error","error":{"message":"attempt-1"}}`; body != want {
		t.Errorf("body = %q, 期望原样透传 %q", body, want)
	}
	if v := resp.Header.Get("Retry-After"); v != "60" {
		t.Errorf("Retry-After = %q, 期望原样回传 60", v)
	}
}

// 5xx 与 429 同档：都是「换个时刻重来有可能成功」。
func TestRetryRecoversFromTransient5xx(t *testing.T) {
	gw, up := newRetryGateway(t, fastRetry(2))
	var hits atomic.Int64
	const ok = `{"id":"msg_01","type":"message","content":[{"type":"text","text":"你好"}]}`
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"type":"error","error":{"message":"boom"}}`)
			return
		}
		_, _ = io.WriteString(w, ok)
	}

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望第三次成功后回 200；body=%s", resp.StatusCode, body)
	}
	if body != ok {
		t.Errorf("body = %q, 期望成功那次的原字节 %q", body, ok)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("上游被打了 %d 次, 期望 3", got)
	}
}

// 网络层失败同样重试，且最终 502 里不许漏 base_url 或凭证。
func TestRetryRetriesTransportFailures(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	deadURL := up.URL
	up.Close() // 端口随即空出，网关将连不上

	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", deadURL, upstreamModel, anthropicCredential)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{Retry: fastRetry(2)})

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, 期望 502；body=%s", resp.StatusCode, body)
	}
	assertNoSecrets(t, body, anthropicCredential, deadURL)
	// 连不上时也得看得出重试过——否则「这次 502 怎么等了这么久」查无实据。
	if n := gw.LastCall(t).Int64("retries"); n != 2 {
		t.Errorf("日志 retries = %d, 期望 2", n)
	}
}

// 确定性失败重试一万次也一样，只会把客户端的等待翻倍。401/403 尤其不归这里管：
// 渠道内换 key 是 M4 的 key 层内环。
func TestNoRetryOnDeterministicFailures(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusUnprocessableEntity,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			gw, up := newRetryGateway(t, fastRetry(2))
			hits := countingUpstream(up, status, map[string]string{"Content-Type": "application/json"})

			resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
			body := gatewaytest.ReadBody(t, resp)

			if got := hits.Load(); got != 1 {
				t.Errorf("上游被打了 %d 次, 期望 1——%d 重试多少次都一样", got, status)
			}
			if resp.StatusCode != status {
				t.Errorf("状态码 = %d, 期望原样 %d", resp.StatusCode, status)
			}
			if want := `{"type":"error","error":{"message":"attempt-1"}}`; body != want {
				t.Errorf("body = %q, 期望原样透传 %q", body, want)
			}
			if _, logged := gw.LastCall(t).Attrs["retries"]; logged {
				t.Error("没重试却记了 retries 字段")
			}
		})
	}
}

// 写出首字节之后上游断流：格式承诺已经生效，只能断连记日志，绝不重打一次——
// 重打的字节会接在已发出的半截流后面，客户端拿到的是两条流拼起来的怪东西。
func TestNoRetryAfterFirstByteWritten(t *testing.T) {
	gw, up := newRetryGateway(t, fastRetry(2))
	var hits atomic.Int64
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseFrame("message_start",
			`{"type":"message_start","message":{"model":"m","usage":{"input_tokens":5}}}`))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	_, _ = io.ReadAll(resp.Body)

	if got := hits.Load(); got != 1 {
		t.Errorf("上游被打了 %d 次, 期望 1——首字节已写出，这时候重试等于把两条流拼起来", got)
	}
	if line := gw.LastCall(t); line.Str("outcome") != "stream_aborted" {
		t.Errorf("outcome = %q, 期望沿用 M0 的 stream_aborted", line.Str("outcome"))
	}
}

// portage-legacy#13 的回归护栏：max_retries 配 0 时行为必须与 M0 完全一致——打一次，原样透回。
func TestRetriesDisabledBehavesLikeM0(t *testing.T) {
	gw, up := newRetryGateway(t, config.Retry{MaxRetries: 0, BaseDelay: time.Second, MaxDelay: time.Minute})
	hits := countingUpstream(up, http.StatusTooManyRequests, map[string]string{
		"Content-Type": "application/json",
		"Retry-After":  "1",
	})

	start := time.Now()
	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	elapsed := time.Since(start)

	if got := hits.Load(); got != 1 {
		t.Errorf("上游被打了 %d 次, 期望 1", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("耗时 %v——关了重试还在退避", elapsed)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("状态码 = %d, 期望原样 429", resp.StatusCode)
	}
	if _, logged := gw.LastCall(t).Attrs["retries"]; logged {
		t.Error("关了重试却记了 retries 字段")
	}
}

// 退避途中客户端走人：不再打上游，也不许把那份已经被读空的响应当成结果交出去
// （那会变成一个 body 为空的 429）。
func TestRetryStopsWhenClientLeavesDuringBackoff(t *testing.T) {
	gw, up := newRetryGateway(t, config.Retry{
		MaxRetries: 2,
		BaseDelay:  2 * time.Second, // 退避远长于下面的取消时刻
		MaxDelay:   5 * time.Second,
	})
	hits := countingUpstream(up, http.StatusTooManyRequests, map[string]string{"Content-Type": "application/json"})

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gw.URL+"/v1/messages",
		strings.NewReader(anthropicRequest))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// 这个用例自己造请求（要一个可取消的 ctx），走不到 Post 自动补 key 那条路。
	req.Header.Set("x-api-key", gatewaytest.DefaultKey)
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("请求应当因客户端取消而失败")
	}

	line := gw.LastCall(t)
	if got := hits.Load(); got != 1 {
		t.Errorf("上游被打了 %d 次, 期望 1——客户端都走了还替他花钱", got)
	}
	if line.Str("outcome") != "upstream_error" {
		t.Errorf("outcome = %q, 期望 upstream_error；取消发生在退避途中，那份 429 的 body "+
			"已经被读空，不能当结果交出去", line.Str("outcome"))
	}
}

// 调用日志要能看出这次重试了几次。
func TestCallLogRecordsRetryCount(t *testing.T) {
	gw, up := newRetryGateway(t, fastRetry(2))
	countingUpstream(up, http.StatusTooManyRequests, map[string]string{"Content-Type": "application/json"})

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	line := gw.LastCall(t)
	if n := line.Int64("retries"); n != 2 {
		t.Errorf("retries = %d, 期望 2；整行: %v", n, line.Attrs)
	}
	if line.Int64("status") != http.StatusTooManyRequests {
		t.Errorf("status = %d, 期望 429", line.Int64("status"))
	}
}

// 出厂默认值本身也得跑一遍：上面那些用例都把退避压到了毫秒级，config.Default() 的
// max_retries 若被谁改回 0，只有这条会红。
func TestRetryUsesShippedDefaults(t *testing.T) {
	gw, up := newRetryGateway(t, config.Default().Retry)
	hits := countingUpstream(up, http.StatusServiceUnavailable, map[string]string{"Content-Type": "application/json"})

	gw.Post(t, "/v1/messages", anthropicRequest, nil)

	if got := hits.Load(); got != 3 {
		t.Errorf("上游被打了 %d 次, 期望 3（出厂默认重试 2 次）", got)
	}
}
