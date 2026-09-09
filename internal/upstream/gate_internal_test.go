package upstream

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 并发闸的本包用例（#55）。行为级的四条（上限压住、队满 429、等超时 429、断连出队）
// 在 internal/server/concurrency_test.go 从 HTTP 边界上测；这里钉的是那几条从边界上
// 看不见、只活在 abandon / release 注释里的不变式——尤其是 moved-slot 那扇竞态窗。

func TestGateAcquireWithinLimitIsImmediate(t *testing.T) {
	g := &gate{}
	for i := 0; i < 3; i++ {
		waited, err := g.acquire(context.Background(), 3, 0, time.Millisecond)
		if err != nil || waited != 0 {
			t.Fatalf("第 %d 次 acquire = (%v, %v)，期望立即成功", i+1, waited, err)
		}
	}
	if g.inflight != 3 {
		t.Fatalf("inflight = %d，期望 3", g.inflight)
	}
	if _, err := g.acquire(context.Background(), 3, 0, time.Millisecond); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("上限已满且队列容量 0 应立即 ErrQueueFull，得到 %v", err)
	}
}

func TestGateHandoffOnRelease(t *testing.T) {
	g := &gate{inflight: 1}
	got := make(chan error, 1)
	go func() {
		_, err := g.acquire(context.Background(), 1, 1, time.Second)
		got <- err
	}()
	// 等到它真排进队列再放坑，否则 release 走的是「没人等、计数减一」那条。
	waitFor(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.waiters) == 1 })
	g.release(1)
	if err := <-got; err != nil {
		t.Fatalf("移交后的等待者应拿到坑，得到 %v", err)
	}
	// 坑换了主人，没有空过：计数不动。
	if g.inflight != 1 {
		t.Fatalf("移交后 inflight = %d，期望 1", g.inflight)
	}
}

func TestGateTimeoutRemovesWaiter(t *testing.T) {
	g := &gate{inflight: 1}
	_, err := g.acquire(context.Background(), 1, 1, 5*time.Millisecond)
	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("err = %v，期望 ErrQueueTimeout", err)
	}
	if len(g.waiters) != 0 || g.inflight != 1 {
		t.Fatalf("超时后应从队列摘走且不动计数：waiters=%d inflight=%d", len(g.waiters), g.inflight)
	}
}

func TestGateAbandonOnContextCancel(t *testing.T) {
	g := &gate{inflight: 1}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := g.acquire(ctx, 1, 1, time.Second)
		got <- err
	}()
	waitFor(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.waiters) == 1 })
	cancel()
	if err := <-got; !errors.Is(err, ErrQueueAbandoned) {
		t.Fatalf("err = %v，期望 ErrQueueAbandoned", err)
	}
	if len(g.waiters) != 0 {
		t.Fatalf("断连后队列应清空，剩 %d", len(g.waiters))
	}
}

// moved-slot 竞态窗：移交方恰好在等待者放弃的同一刻 pop 了它的 chan——此时队列里
// 已经找不到自己，说明坑已归我，必须转手释放，否则坑随请求蒸发、上限从此少一。
// 直接按 abandon 的入参把这一刻摆出来：坑已被 release 移交（chan 已 close）之后再调
// abandon。
func TestGateAbandonAfterHandoffReleasesSlot(t *testing.T) {
	g := &gate{inflight: 1}
	ch := make(chan struct{})
	g.waiters = append(g.waiters, ch)
	g.release(1) // 移交：pop + close，inflight 仍 1（坑归 ch）
	select {
	case <-ch:
	default:
		t.Fatal("release 应把坑移交给队首")
	}
	if err := g.abandon(ch, 1, ErrQueueTimeout); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("abandon 应原样返回 cause，得到 %v", err)
	}
	if g.inflight != 0 {
		t.Fatalf("移交后放弃的坑必须转手还掉：inflight = %d，期望 0", g.inflight)
	}
}

// 拿到移交的坑但 ctx 已断：坑还给下一个活人，自己报 ErrQueueAbandoned。
func TestGateHandoffToCanceledWaiterPassesSlotOn(t *testing.T) {
	g := &gate{inflight: 1}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := g.acquire(ctx, 1, 2, time.Second)
		first <- err
	}()
	waitFor(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.waiters) == 1 })
	second := make(chan error, 1)
	go func() {
		_, err := g.acquire(context.Background(), 1, 2, time.Second)
		second <- err
	}()
	waitFor(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return len(g.waiters) == 2 })
	// 让第一个等待者先断，再移交：它会在 <-ch 与 <-ctx.Done() 之间二选一。两条分支
	// 的结果都必须是「坑最终到第二个人手上」——这正是这条用例要钉的不变式。
	cancel()
	g.release(1)
	if err := <-first; !errors.Is(err, ErrQueueAbandoned) {
		t.Fatalf("断了的等待者 err = %v，期望 ErrQueueAbandoned", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("第二个等待者应拿到坑，得到 %v", err)
	}
	if g.inflight != 1 {
		t.Fatalf("inflight = %d，期望 1（坑在第二个人手上）", g.inflight)
	}
}

// 上限缩小：移交条件不成立，坑被吃掉，直到 in-flight 排空到新上限之下。
func TestGateShrinkingLimitDrainsInsteadOfHandoff(t *testing.T) {
	g := &gate{inflight: 3}
	ch := make(chan struct{})
	g.waiters = append(g.waiters, ch)
	g.release(2) // 3 > 2：不移交，计数减一
	if g.inflight != 2 || len(g.waiters) != 1 {
		t.Fatalf("缩限后 release 应只减计数：inflight=%d waiters=%d", g.inflight, len(g.waiters))
	}
	g.release(2) // 2 <= 2：移交
	if g.inflight != 2 || len(g.waiters) != 0 {
		t.Fatalf("排到新上限之内应移交：inflight=%d waiters=%d", g.inflight, len(g.waiters))
	}
}

func TestGateForIsPerChannel(t *testing.T) {
	c := NewClient(DefaultRetryPolicy())
	if c.gateFor(1) != c.gateFor(1) {
		t.Fatal("同一渠道应拿到同一把闸")
	}
	if c.gateFor(1) == c.gateFor(2) {
		t.Fatal("不同渠道不该共用一把闸")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("等待条件超时")
		}
		time.Sleep(time.Millisecond)
	}
}
