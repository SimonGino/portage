package upstream

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

// RetryPolicy 描述同候选退避重试（口径层 v0.19）。
//
// 这条口径推翻了 v0.9 的「无同候选重试，重试留给 harness」——那个决策的依据是
// harness 会自行退避，而 M0 验收实测（portage-legacy#6）证伪了一半：Codex 0.144.1 对 5xx 退避
// 重试，对 429 一次即弃（带不带 Retry-After、把 request_max_retries 调到 4 都只打
// 一次）。单候选单上游被限流时没有人自愈，网关必须自己补这一环。
//
// 它不依赖多候选，所以能在临时闸不放开的前提下提前到 M2；候选间转移仍在 M4。
type RetryPolicy struct {
	// MaxRetries 是**重试**次数，不含首次尝试。0 即关闭，行为与 M0 完全一致。
	MaxRetries int
	// MaxAttempts 是一次请求的全局上游尝试上限，跨凭证累计（口径层 v0.38）；
	// 0 即不封顶。它与 MaxRetries 分两层：内层管同一份凭证上的抖动，外层管
	// 客户端最多等多久——只留内层的话，最坏耗时会随凭证数线性增长。
	MaxAttempts int
	// BaseDelay 是第一次重试前的退避基准，随尝试次数指数增长。
	BaseDelay time.Duration
	// MaxDelay 封顶单次退避。上游 Retry-After 超过它就干脆不重试——见 delayFor。
	MaxDelay time.Duration
}

// DefaultRetryPolicy 的取值参照 M0 实测：Codex 对 5xx 的退避是 0.22→0.45→0.84→1.62s，
// 量级相仿；重试 2 次是「够救瞬时限流、又不至于让客户端干等太久」的折中，真实
// 429 通常带 Retry-After，那时以它为准。
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 2, MaxAttempts: 6, BaseDelay: 500 * time.Millisecond, MaxDelay: 10 * time.Second}
}

// retriable 判断这次失败值不值得原地再打一次。
//
// 判据只有一条：**这个错法换个时刻重来有没有可能成功**。
//   - 429 / 5xx / 网络错误：上游容量或链路的瞬时问题，值得。
//   - 401 / 403：这**一把**凭证不对，原地重试必然同样失败——换一把才有意义，
//     那是 key 层内环的事（Do 的外层，口径层 v0.38），不归这里。
//   - 其余 4xx：请求本身有问题，重试一万次也一样。
//   - 超时：刚超时的对端原地重试大概率再超时，只会把客户端的等待翻倍。
//   - ctx 已取消：客户端自己走了，再打一次纯属替他花钱。
//
// 「未向 client 写出首字节」这条前提在这一层是结构性成立的——Do 返回之前 relay
// 还没写响应头。写出首字节之后的断流由 relayBody 处理，那里一律不重试不改写。
func retriable(ctx context.Context, resp *http.Response, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
			return false
		}
		return true
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
}

// delayFor 算这次重试前该等多久；返回 false 表示别重试了。
//
// 退避是指数增长 + 抖动，抖动只作用在指数那部分（半区间抖动，落在 [d/2, d)），
// 这样退避永远不会短于上游明说的 Retry-After。
//
// Retry-After 超过 MaxDelay 时不重试而不是照等：上游说「一分钟后再来」，网关照做
// 就是把客户端扣在这儿一分钟，还不如把这份 429 原样交出去，让调用方自己决定。
func delayFor(p RetryPolicy, attempt int, resp *http.Response) (time.Duration, bool) {
	d := p.BaseDelay << attempt
	if d <= 0 || d > p.MaxDelay { // <<= 溢出成负数时同样落到封顶
		d = p.MaxDelay
	}
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1))

	if after, ok := retryAfter(resp); ok {
		if after > p.MaxDelay {
			return 0, false
		}
		d = max(d, after)
	}
	return d, true
}

// retryAfter 读 Retry-After 头。RFC 9110 允许两种写法：延迟秒数，或 HTTP-date。
func retryAfter(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	raw := resp.Header.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	d := time.Until(when)
	if d < 0 {
		return 0, true // 已经过期：可以立刻重试，但仍算「上游给了下界」
	}
	return d, true
}

// sleep 等 d，客户端中途走人就提前返回 false。
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
