package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// 超时与 ctx 取消这两条分支只能在这里测：真跑出一次连接超时要等满 10s 的拨号上限，
// 而 ctx 取消发生在退避途中，从网关的 HTTP 边界上看不出「有没有再打一次」——两者
// 在主接缝上的代价都不可接受。其余分支（429/5xx/4xx/网络错误）都有行为测试，
// 见 internal/server/retry_test.go。
type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }
func (timeoutErr) Timeout() bool { return true }

// Temporary 已废弃，但仍在 net.Error 接口里；不实现就不是 net.Error。
func (timeoutErr) Temporary() bool { return false }

func TestRetriable(t *testing.T) {
	var _ net.Error = timeoutErr{} // 编译期确认这个替身真的走 net.Error 那条分支

	for _, tc := range []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{name: "429", status: http.StatusTooManyRequests, want: true},
		{name: "500", status: http.StatusInternalServerError, want: true},
		{name: "503", status: http.StatusServiceUnavailable, want: true},
		{name: "401 凭证不对，重试必然同样失败", status: http.StatusUnauthorized},
		{name: "403", status: http.StatusForbidden},
		{name: "400", status: http.StatusBadRequest},
		{name: "200", status: http.StatusOK},
		{name: "连接被拒", err: errors.New("connection refused"), want: true},
		{name: "超时——原地重试大概率再超时，只会让客户端等两倍", err: timeoutErr{}},
		{name: "包在 url.Error 里的超时", err: &url.Error{Op: "Post", Err: timeoutErr{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp *http.Response
			if tc.err == nil {
				resp = &http.Response{StatusCode: tc.status}
			}
			if got := retriable(t.Context(), resp, tc.err); got != tc.want {
				t.Errorf("retriable = %v, 期望 %v", got, tc.want)
			}
		})
	}
}

// 客户端自己走了就别再替他花钱打上游。
func TestRetriableStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if retriable(ctx, &http.Response{StatusCode: http.StatusTooManyRequests}, nil) {
		t.Error("ctx 已取消仍判定可重试")
	}
}

func TestRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
		ok   bool
	}{
		{name: "缺省"},
		{name: "秒数", raw: "30", want: 30 * time.Second, ok: true},
		{name: "0 秒", raw: "0", ok: true},
		{name: "负数当没给", raw: "-5"},
		{name: "看不懂的值当没给", raw: "soon"},
		{name: "已经过期的 HTTP-date 可以立刻重试", raw: "Mon, 02 Jan 2006 15:04:05 GMT", ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if tc.raw != "" {
				resp.Header.Set("Retry-After", tc.raw)
			}
			got, ok := retryAfter(resp)
			if ok != tc.ok {
				t.Fatalf("retryAfter ok = %v, 期望 %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("retryAfter = %v, 期望 %v", got, tc.want)
			}
		})
	}
}

// HTTP-date 形式要按「离现在还有多久」算，不是当成绝对时刻扔掉。
func TestRetryAfterHTTPDateInTheFuture(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))

	got, ok := retryAfter(resp)
	if !ok {
		t.Fatal("HTTP-date 形式的 Retry-After 没被认出来")
	}
	if got < 28*time.Second || got > 31*time.Second {
		t.Errorf("retryAfter = %v, 期望 30s 上下", got)
	}
}

// 退避随尝试次数指数增长、被 MaxDelay 封顶，且抖动只吃掉一半——这样它永远不会
// 短于上游明说的 Retry-After。
func TestDelayForBackoff(t *testing.T) {
	p := RetryPolicy{MaxRetries: 9, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}

	for attempt, want := range []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second, // 封顶
		time.Second,
	} {
		t.Run(fmt.Sprintf("第%d次重试", attempt+1), func(t *testing.T) {
			for range 50 { // 抖动是随机的，多跑几次才盯得住区间
				got, ok := delayFor(p, attempt, nil)
				if !ok {
					t.Fatal("没给 Retry-After 却判定不重试")
				}
				if got < want/2 || got >= want {
					t.Fatalf("delayFor = %v, 期望落在 [%v, %v)", got, want/2, want)
				}
			}
		})
	}
}

// 指数左移到溢出时也得落到封顶，不能变成负数或 0——那会把退避变成忙等。
func TestDelayForSurvivesShiftOverflow(t *testing.T) {
	p := RetryPolicy{MaxRetries: 200, BaseDelay: time.Second, MaxDelay: 10 * time.Second}

	got, ok := delayFor(p, 99, nil)
	if !ok {
		t.Fatal("没给 Retry-After 却判定不重试")
	}
	if got < 5*time.Second || got >= 10*time.Second {
		t.Errorf("delayFor = %v, 期望落在封顶的抖动区间 [5s, 10s)", got)
	}
}

func TestDelayForTakesRetryAfterAsFloor(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Second}
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "2")

	got, ok := delayFor(p, 0, resp)
	if !ok {
		t.Fatal("Retry-After 在 MaxDelay 之内却判定不重试")
	}
	if got != 2*time.Second {
		t.Errorf("delayFor = %v, 期望被 Retry-After 抬到 2s", got)
	}
}

// Retry-After 超过 MaxDelay：不重试，把上游那份响应原样交出去，别把客户端扣住。
func TestDelayForRefusesWhenRetryAfterTooLong(t *testing.T) {
	p := RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second}
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "60")

	if _, ok := delayFor(p, 0, resp); ok {
		t.Error("Retry-After 长过 MaxDelay 仍判定要重试——客户端会被扣一分钟")
	}
}

func TestSleepReturnsEarlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	if sleep(ctx, 5*time.Second) {
		t.Error("ctx 已取消，sleep 仍报「等满了」")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("sleep 等了 %v 才发现 ctx 已取消", elapsed)
	}
}
