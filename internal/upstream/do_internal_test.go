package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
)

// Do 的本包用例（#55）：key 层内环、跨凭证预算、轮询与闸的持有区间，此前只能在
// server 包从 HTTP + SQLite 外面测。这里直接造 Route 打假上游。

type fakeUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	auth []string // 每次收到的 Authorization
}

func newFakeUpstream(t *testing.T, handler func(n int, w http.ResponseWriter, r *http.Request)) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		n := len(f.auth)
		f.mu.Unlock()
		handler(n, w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeUpstream) route(creds ...string) Route {
	rt := Route{ChannelID: 1, ChannelName: "ch", Protocol: protocol.OpenAI, BaseURL: f.URL}
	for i, c := range creds {
		rt.Credentials = append(rt.Credentials, Credential{Name: "k" + string(rune('1'+i)), Value: c})
	}
	return rt
}

func fastClient(retry RetryPolicy) *Client {
	c := NewClient(retry)
	c.retry.BaseDelay, c.retry.MaxDelay = time.Millisecond, 5*time.Millisecond
	return c
}

func TestDoSwitchesCredentialOn401(t *testing.T) {
	up := newFakeUpstream(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	c := fastClient(RetryPolicy{MaxRetries: 2, MaxAttempts: 6})
	resp, at, err := c.Do(context.Background(), up.route("a", "b"), protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || at.Sends != 2 || at.Credential != "k2" || at.Retries() != 1 {
		t.Fatalf("status=%d attempt=%+v，期望 401 后换第二把一次成功", resp.StatusCode, at)
	}
	if up.auth[0] != "Bearer a" || up.auth[1] != "Bearer b" {
		t.Fatalf("凭证顺序 = %v", up.auth)
	}
}

func TestDoDoesNotSwitchCredentialOn5xx(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	c := fastClient(RetryPolicy{MaxRetries: 1, MaxAttempts: 6})
	resp, at, err := c.Do(context.Background(), up.route("a", "b"), protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// 同一把上退避 1 次即止，不换凭证：5xx 是渠道的问题。
	if at.Sends != 2 || at.Credential != "k1" || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("attempt=%+v status=%d，期望同一把两次后原样交出 502", at, resp.StatusCode)
	}
	for _, a := range up.auth {
		if a != "Bearer a" {
			t.Fatalf("5xx 不该换凭证，收到 %v", up.auth)
		}
	}
}

func TestDoGlobalBudgetCapsAcrossCredentials(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	// 每把最多 1+2 次、三把共 9 次，全局预算封在 4。
	c := fastClient(RetryPolicy{MaxRetries: 2, MaxAttempts: 4})
	resp, at, err := c.Do(context.Background(), up.route("a", "b", "c"), protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if at.Sends != 4 || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt=%+v status=%d，期望预算 4 次用完后原样交出最后一次 429", at, resp.StatusCode)
	}
}

func TestDoPollingRotatesStartCredential(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	c := fastClient(RetryPolicy{})
	rt := up.route("a", "b")
	rt.KeyMode = store.KeyModePolling
	for _, want := range []string{"Bearer a", "Bearer b", "Bearer a"} {
		resp, _, err := c.Do(context.Background(), rt, protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := up.auth[len(up.auth)-1]; got != want {
			t.Fatalf("轮询第 %d 次用了 %q，期望 %q", len(up.auth), got, want)
		}
	}
}

func TestDoWithoutCredentialsFailsBeforeSending(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	_, at, err := fastClient(RetryPolicy{}).Do(context.Background(), up.route(), protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
	if err == nil || at.Sends != 0 || len(up.auth) != 0 {
		t.Fatalf("零凭证应报错且一个请求都不发：err=%v attempt=%+v sent=%d", err, at, len(up.auth))
	}
}

// 闸的持有区间是「一次 Do 到响应体 Close」：响应还没读完时坑仍占着，Close 才还。
func TestDoHoldsGateSlotUntilBodyClosed(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "hello")
	})
	c := fastClient(RetryPolicy{})
	c.Queue = QueuePolicy{Factor: 0, Wait: time.Second}
	rt := up.route("a")
	rt.MaxConcurrency = 1
	resp, _, err := c.Do(context.Background(), rt, protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
	if err != nil {
		t.Fatal(err)
	}
	// 第二次：坑被占、队列容量 0 → 立即 ErrQueueFull，且一个字节不打上游。
	if _, _, err := c.Do(context.Background(), rt, protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false); err != ErrQueueFull {
		t.Fatalf("坑占着时第二次 Do err = %v，期望 ErrQueueFull", err)
	}
	if len(up.auth) != 1 {
		t.Fatalf("被闸拒的请求不该到上游，收到 %d 次", len(up.auth))
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "hello") {
		t.Fatalf("body = %q", b)
	}
	resp.Body.Close()
	resp.Body.Close() // 重复 Close 不能把同一个坑还两次
	resp2, _, err := c.Do(context.Background(), rt, protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
	if err != nil {
		t.Fatalf("Close 之后坑应已还回：%v", err)
	}
	resp2.Body.Close()
	if g := c.gateFor(rt.ChannelID); g.inflight != 0 {
		t.Fatalf("全部 Close 后 inflight = %d，期望 0", g.inflight)
	}
}

// 闸拒的那两种收场没有响应，但 Attempt.QueueWait 照样有值。
func TestDoQueueTimeoutReportsWait(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	c := fastClient(RetryPolicy{})
	c.Queue = QueuePolicy{Factor: 1, Wait: 10 * time.Millisecond}
	rt := up.route("a")
	rt.MaxConcurrency = 1
	resp, _, err := c.Do(context.Background(), rt, protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, at, err := c.Do(context.Background(), rt, protocol.EndpointChatCompletions, "", []byte(`{}`), http.Header{}, false)
	if err != ErrQueueTimeout || at.QueueWait < 10*time.Millisecond {
		t.Fatalf("err=%v attempt=%+v，期望 ErrQueueTimeout 且 QueueWait ≥ 10ms", err, at)
	}
}
