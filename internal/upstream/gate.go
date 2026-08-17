package upstream

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// 渠道并发闸（口径层 v0.49/v0.50）：按渠道限 in-flight 并发，闸满有界排队，
// 队满/等超时回给调用方哨兵错误，由 server 层译成入口协议的 429。
//
// 状态只在内存（口径层 v0.49 实现口径）：in-flight 是纯运行期事实，重启后一个
// 都不剩，落库反而会把「上次进程的幽灵占坑」带进新进程。代价是重启瞬间不设防，
// 与轮询游标丢失同一量级，可接受。

// ErrQueueFull / ErrQueueTimeout 是闸的两种拒绝（口径层 v0.50），文案不直接回
// 客户端——server 按入口协议译写，这里的消息只进网关自己的日志。
var (
	ErrQueueFull    = errors.New("渠道并发已满且队列已满")
	ErrQueueTimeout = errors.New("渠道并发排队等待超时")
	// ErrQueueAbandoned 是排队途中客户端自己断开。单列一个哨兵而不是透传
	// ctx.Err()：server 要把它与「打到上游后失败」区分开——这种请求一个字节都
	// 没碰过上游，按 upstream_error 记账是冤枉渠道。
	ErrQueueAbandoned = errors.New("渠道并发排队途中客户端断开")
)

// QueuePolicy 是有界排队的两个全局参数（口径层 v0.50：全局项、不进渠道表），
// 由 server 从配置接上，像 Disable 一样。零值 Wait 会让排队立即超时——配置层
// （config.Load）负责兜底，这里不重复藏一份默认值。
type QueuePolicy struct {
	// Factor 是队列上限系数：队列上限 = 渠道并发上限 × Factor。<=0 即不排队。
	Factor int
	// Wait 是排队等待超时。
	Wait time.Duration
}

// gate 是单个渠道的并发闸：计数 + 等待队列，一把锁保护。
//
// 不用带权 semaphore 库而手写 handoff（释放时把坑**直接移交**队首等待者），是
// 因为上限是每次 Acquire 时从渠道配置带进来的——改配置不用重启，缩小上限时靠
// 「移交前先查新上限」自然排空，通用库的固定容量做不到这一点。
type gate struct {
	mu       sync.Mutex
	inflight int
	// waiters 按到达序排队，先到先得。每个等待者一只自己的 chan，被移交时 close；
	// 等超时/断连的自己把 chan 从队列里摘走（见 abandon 的竞态说明）。
	waiters []chan struct{}
}

// acquire 占一个坑：有空位立即成功；满了且队列有位就排队，等到移交、等到超时
// （ErrQueueTimeout）或客户端断连（ErrQueueAbandoned）为止；队列也满了立即
// ErrQueueFull。
// 返回的时长是排队耗时，进 call_logs.queue_wait_ms（口径层 v0.52）。
func (g *gate) acquire(ctx context.Context, limit, queueCap int, wait time.Duration) (time.Duration, error) {
	g.mu.Lock()
	if g.inflight < limit {
		g.inflight++
		g.mu.Unlock()
		return 0, nil
	}
	if len(g.waiters) >= queueCap {
		g.mu.Unlock()
		return 0, ErrQueueFull
	}
	ch := make(chan struct{})
	g.waiters = append(g.waiters, ch)
	g.mu.Unlock()

	start := time.Now()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ch:
		// 拿到移交的坑。但若客户端在等的途中已经断了，这个坑给一个死请求毫无
		// 意义（http.Client 见到 canceled ctx 根本不会发）——还回去给下一个活人。
		if ctx.Err() != nil {
			g.release(limit)
			return time.Since(start), ErrQueueAbandoned
		}
		return time.Since(start), nil
	case <-ctx.Done():
		return time.Since(start), g.abandon(ch, limit, ErrQueueAbandoned)
	case <-timer.C:
		return time.Since(start), g.abandon(ch, limit, ErrQueueTimeout)
	}
}

// abandon 把自己从等待队列里摘走。竞态窗口：移交方可能恰好在此刻 pop 了我们的
// chan 并 close——那时队列里已经找不到自己，说明坑其实已经归我们了，得转手释放，
// 否则这个坑随请求一起蒸发、上限从此少一。
func (g *gate) abandon(ch chan struct{}, limit int, cause error) error {
	g.mu.Lock()
	for i, w := range g.waiters {
		if w == ch {
			g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
			g.mu.Unlock()
			return cause
		}
	}
	g.mu.Unlock()
	g.release(limit)
	return cause
}

// release 还坑：队列有人且不超当前上限就直接移交（inflight 不动——坑换了主人，
// 没有空过），否则计数减一。上限缩小时移交条件不成立，坑就这样被吃掉，直到
// in-flight 排空到新上限之下。
func (g *gate) release(limit int) {
	g.mu.Lock()
	if len(g.waiters) > 0 && g.inflight <= limit {
		ch := g.waiters[0]
		g.waiters = g.waiters[1:]
		g.mu.Unlock()
		close(ch)
		return
	}
	g.inflight--
	g.mu.Unlock()
}

// gateFor 取渠道对应的闸，按渠道 id 一份、懒建、永不删：渠道数是个位数量级，
// 删渠道后残留一个空 gate 结构不值得为它做生命周期管理。复用 Client.mu（它同时
// 护着轮询游标）——两处都是纳秒级临界区，分两把锁只是多一个字段。
func (c *Client) gateFor(channelID int64) *gate {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gates == nil {
		c.gates = map[int64]*gate{}
	}
	g := c.gates[channelID]
	if g == nil {
		g = &gate{}
		c.gates[channelID] = g
	}
	return g
}

// releasingBody 把「坑还回去」挂在响应体的 Close 上：闸的持有区间是「一次 Do 到
// 响应体读完」（口径层 v0.50——上游还在生成流时并发就是还占着的）。Once 防重复
// Close 把同一个坑还两次。
type releasingBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *releasingBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}
