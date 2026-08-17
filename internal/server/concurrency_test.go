package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 渠道并发闸（口径层 v0.49~v0.52，展开层 §7.5）的四条验收断言：
// ① 并发打超上限时，假上游观察到的最大同时 in-flight ≤ 上限；
// ② 队满立即 429，error=queue_full；
// ③ 等待超时 429，error=queue_timeout 且 queue_wait_ms ≈ 超时值；
// ④ 排队中断连即出队，不向上游转发。

// newGatedGateway 起一个「单渠道、设了并发上限」的网关。排队参数显式传——这批
// 用例测的就是排队行为，跟随默认值会让「等 30s 超时」这种用例根本跑不动。
func newGatedGateway(t *testing.T, limit int, queue config.Queue) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	chID := gatewaytest.SeedChannel(t, db, "test-anthropic", "anthropic", up.URL, anthropicCredential)
	modelID := gatewaytest.SeedChannelModel(t, db, chID, upstreamModel)
	apID := gatewaytest.SeedAccessPoint(t, db, accessPointModel)
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
	gatewaytest.SetChannelConcurrency(t, db, chID, limit)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{Queue: queue}), up
}

// asyncResp 是后台请求的收场。并发用例的请求不能走 gw.Post：那里面有 t.Fatalf，
// 而 Fatalf 不允许在非测试 goroutine 里调。
type asyncResp struct {
	status int
	body   string
	err    error
}

// postAsync 在后台发一个转发请求，结果从返回的 channel 里取。
func postAsync(gw *gatewaytest.Gateway, ctx context.Context, body string) <-chan asyncResp {
	ch := make(chan asyncResp, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, gw.URL+"/v1/messages", strings.NewReader(body))
		if err != nil {
			ch <- asyncResp{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", gatewaytest.DefaultKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			ch <- asyncResp{err: err}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ch <- asyncResp{status: resp.StatusCode, body: string(b)}
	}()
	return ch
}

// blockingUpstream 让假上游把每个请求都挂住，直到 unblock 被调。unblock 幂等，
// 测试主体调一次、defer 再兜一次都不会炸——不幂等的话提前 Fatal 会让挂着的
// goroutine 泄露到别的用例里。
func blockingUpstream(up *gatewaytest.Upstream) (unblock func()) {
	release := make(chan struct{})
	var once sync.Once
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[]}`))
	}
	return func() { once.Do(func() { close(release) }) }
}

// waitUpstreamCount 等假上游收到第 want 个请求——「占坑的那个已经打到上游」是
// 后续动作的前置条件，不等就是纯赌调度。
func waitUpstreamCount(t *testing.T, up *gatewaytest.Upstream, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for up.Count() < want {
		if time.Now().After(deadline) {
			t.Fatalf("3s 内假上游只收到 %d 个请求，期望 %d", up.Count(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 断言①：上限 2、队列放宽到装得下全部请求，6 个并发全该成功，但假上游任一时刻
// 看到的同时 in-flight 不得超过 2。这是闸的存在性证明——没有闸时 peak 必然到 6。
func TestGateCapsInflightAtLimit(t *testing.T) {
	gw, up := newGatedGateway(t, 2, config.Queue{Factor: 10, Wait: 30 * time.Second, RetryAfter: 10 * time.Second})

	var cur, peak atomic.Int64
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		defer cur.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		// 停一拍让并发真的叠起来：不停的话请求排着队也能挨个瞬间跑完，peak 恒为 1，
		// 断言「≤ 2」就成了永真式，闸拆掉它也绿。
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[]}`))
	}

	const total = 6
	chans := make([]<-chan asyncResp, total)
	for i := range chans {
		chans[i] = postAsync(gw, context.Background(), anthropicRequest)
	}
	for i, ch := range chans {
		got := <-ch
		if got.err != nil {
			t.Fatalf("第 %d 个请求失败: %v", i+1, got.err)
		}
		if got.status != http.StatusOK {
			t.Fatalf("第 %d 个请求 = %d，队列够宽就都该成功", i+1, got.status)
		}
	}

	if up.Count() != total {
		t.Errorf("假上游收到 %d 个请求，期望 %d", up.Count(), total)
	}
	if p := peak.Load(); p > 2 {
		t.Errorf("假上游观察到的最大同时 in-flight = %d，超过上限 2——闸没拦住", p)
	}

	// 观测侧顺带钉死：排过队的成功请求，queue_wait_ms 得是正数（口径层 v0.52）。
	if n := gw.WaitCallRows(t, total); n != total {
		t.Fatalf("call_logs 落了 %d 行，期望 %d", n, total)
	}
	var maxWait int64
	if err := gw.DB.QueryRow(`SELECT MAX(queue_wait_ms) FROM call_logs`).Scan(&maxWait); err != nil {
		t.Fatalf("读 queue_wait_ms 失败: %v", err)
	}
	if maxWait <= 0 {
		t.Errorf("6 个请求挤 2 个坑，却没有任何一行 queue_wait_ms > 0")
	}
}

// 断言②：上限 1、系数 1（队列也只装 1 个），第三个请求该**立即**吃 429，
// error=queue_full，带可配置的 Retry-After，body 是入口协议原生格式。
func TestGateFullQueueRejectsImmediately(t *testing.T) {
	gw, up := newGatedGateway(t, 1, config.Queue{Factor: 1, Wait: 30 * time.Second, RetryAfter: 7 * time.Second})

	unblock := blockingUpstream(up)
	defer unblock()

	a := postAsync(gw, context.Background(), anthropicRequest) // 占坑
	waitUpstreamCount(t, up, 1)
	b := postAsync(gw, context.Background(), anthropicRequest) // 进队列
	// 队列长度从外面观察不到，只能给 B 一点时间真正排进去。太短的话 C 会抢在
	// B 之前入队，测的就成了另一个请求的 429。
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil) // C：队满，立即拒
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("队满时 = %d, 期望 429", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("队满耗时 %v 才拒——「立即」失守", elapsed)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "7" {
		t.Errorf("Retry-After = %q, 期望配置的 7", ra)
	}
	// 协议原生格式（口径层 v0.50）：回 gin 默认 JSON 的话 harness 认不出来。
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(gatewaytest.ReadBody(t, resp)), &body); err != nil {
		t.Fatalf("429 的 body 不是合法 JSON: %v", err)
	}
	if body.Type != "error" || body.Error.Type != "rate_limit_error" {
		t.Errorf("body = %+v, 期望 Anthropic 原生的 rate_limit_error", body)
	}

	// A、B 还挂在上游/队列里，此刻唯一落库的行就是 C 的。
	if n := gw.WaitCallRows(t, 1); n != 1 {
		t.Fatalf("call_logs 落了 %d 行，期望 1", n)
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "queue_full" {
		t.Errorf("error = %q, 期望 queue_full", row.Error.String)
	}
	if row.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, 期望 429", row.Status)
	}
	// 队满是在闸上回绝的，一个字节都没拨到上游，出站端点因此为空（#20）——同 401 /
	// 429 / 501 那批。入站那条照旧有值：它由最外层中间件写死。
	if row.UpstreamEndpoint != "" {
		t.Errorf("upstream_endpoint = %q, 期望空串——队满从没打过上游", row.UpstreamEndpoint)
	}
	if row.Endpoint != "/v1/messages" {
		t.Errorf("endpoint = %q, 期望 /v1/messages", row.Endpoint)
	}

	// 收尾：放开上游，A 与排着的 B 都该正常完成——被拒的只有 C。
	unblock()
	for name, ch := range map[string]<-chan asyncResp{"A": a, "B": b} {
		got := <-ch
		if got.err != nil || got.status != http.StatusOK {
			t.Errorf("%s = %d/%v, 期望 200——队满只该拒 C", name, got.status, got.err)
		}
	}
	if up.Count() != 2 {
		t.Errorf("假上游收到 %d 个请求，期望 2（C 不该被转发）", up.Count())
	}
}

// 断言③：上限 1、等待超时 300ms，排队的请求到点该吃 429，error=queue_timeout，
// queue_wait_ms ≈ 超时值。
func TestGateQueueTimeoutReturns429(t *testing.T) {
	gw, up := newGatedGateway(t, 1, config.Queue{Factor: 1, Wait: 300 * time.Millisecond, RetryAfter: 10 * time.Second})

	unblock := blockingUpstream(up)
	defer unblock()

	a := postAsync(gw, context.Background(), anthropicRequest) // 占坑
	waitUpstreamCount(t, up, 1)

	start := time.Now()
	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil) // B：排队到超时
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("排队超时 = %d, 期望 429", resp.StatusCode)
	}
	if elapsed < 300*time.Millisecond {
		t.Errorf("%v 就回来了，没等满 300ms——超时不是从排队起算的？", elapsed)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "10" {
		t.Errorf("Retry-After = %q, 期望 10", ra)
	}

	if n := gw.WaitCallRows(t, 1); n != 1 {
		t.Fatalf("call_logs 落了 %d 行，期望 1（A 还挂着）", n)
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "queue_timeout" {
		t.Errorf("error = %q, 期望 queue_timeout", row.Error.String)
	}
	// ≈ 超时值：下界是配置的 300ms（提早出队才会小于它），上界放宽到 3s——这一列
	// 记的是真实等待，慢机器上多出几百毫秒正常，翻十倍就是把总耗时错记进来了。
	if row.QueueWaitMs < 300 || row.QueueWaitMs > 3000 {
		t.Errorf("queue_wait_ms = %d, 期望 ≈ 300", row.QueueWaitMs)
	}

	unblock()
	if got := <-a; got.err != nil || got.status != http.StatusOK {
		t.Errorf("占坑的 A = %d/%v, 期望 200", got.status, got.err)
	}
}

// 断言④：排队中客户端断连要**出队**——不向上游转发，坑也不能跟着蒸发。
func TestGateDequeuesOnClientDisconnect(t *testing.T) {
	gw, up := newGatedGateway(t, 1, config.Queue{Factor: 2, Wait: 30 * time.Second, RetryAfter: 10 * time.Second})

	unblock := blockingUpstream(up)
	defer unblock()

	a := postAsync(gw, context.Background(), anthropicRequest) // 占坑
	waitUpstreamCount(t, up, 1)

	ctxB, cancelB := context.WithCancel(context.Background())
	b := postAsync(gw, ctxB, anthropicRequest) // 进队列
	time.Sleep(200 * time.Millisecond)         // 给 B 时间真正排进去（理由见断言②）
	cancelB()
	if got := <-b; got.err == nil {
		t.Fatalf("断连的请求拿到了响应 %d——它不该有任何收场", got.status)
	}

	// B 的流水行是此刻唯一落库的（A 还挂在上游），归因该是 queue_abandoned 而不是
	// upstream_error——它一个字节都没碰过上游，记后者是冤枉渠道。
	if n := gw.WaitCallRows(t, 1); n != 1 {
		t.Fatalf("call_logs 落了 %d 行，期望 1", n)
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "queue_abandoned" {
		t.Errorf("error = %q, 期望 queue_abandoned", row.Error.String)
	}
	// 499 是 nginx 的 client closed request 惯例码：客户端已不在，写什么都没人收，
	// 这个码只为流水有个诚实的状态、别跟 200/429/502 任何一种真实收场混在一起。
	if row.Status != 499 {
		t.Errorf("status = %d, 期望 499", row.Status)
	}

	// 放开 A，再补一个 C：C 能成说明 B 出队时没把坑带走，也没把队列位占死。
	unblock()
	if got := <-a; got.err != nil || got.status != http.StatusOK {
		t.Errorf("占坑的 A = %d/%v, 期望 200", got.status, got.err)
	}
	if resp := gw.Post(t, "/v1/messages", anthropicRequest, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("断连之后的新请求 = %d, 期望 200——坑被断连请求带走了？", resp.StatusCode)
	}
	if up.Count() != 2 {
		t.Errorf("假上游收到 %d 个请求，期望 2（断连的 B 不该被转发）", up.Count())
	}
}
