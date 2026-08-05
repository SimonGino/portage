package protocol

import (
	"strings"
	"testing"
)

// Tap 挂在 io.TeeReader 上：Write 一旦报错，TeeReader 会把它变成读错误、直接打断
// 转发。所以哪怕字段提取代码炸了，Write 也必须返回 (len(p), nil)。
func TestTapCoreSwallowsPanicFromExtractor(t *testing.T) {
	core := NewTapCore(true, func(*Summary, []byte) { panic("字段提取炸了") }, nil)

	n, err := core.Write([]byte("data: {\"x\":1}\n\n"))

	if n != 15 || err != nil {
		t.Fatalf("Write = (%d, %v)，必须是 (15, nil)", n, err)
	}
	if !core.Summary().Degraded {
		t.Error("panic 被兜住了，但没有标成降级——日志会假装自己是准的")
	}
}

func TestTapCoreSwallowsPanicWhileFinishing(t *testing.T) {
	// 非流式的提取在 Summary() 里才跑，那条路径同样不能把 panic 放出去。
	core := NewTapCore(false, nil, func(*Summary, []byte) { panic("字段提取炸了") })
	if _, err := core.Write([]byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if !core.Summary().Degraded {
		t.Error("Summary() 里的 panic 没有被兜成降级")
	}
}

func TestTapCoreSummaryIsIdempotent(t *testing.T) {
	calls := 0
	core := NewTapCore(false, nil, func(sum *Summary, _ []byte) {
		calls++
		sum.OutputTokens = 7
	})
	_, _ = core.Write([]byte(`{"x":1}`))

	first, second := core.Summary(), core.Summary()

	if calls != 1 {
		t.Errorf("提取跑了 %d 次，Summary 应当幂等", calls)
	}
	if first != second || first.OutputTokens != 7 {
		t.Errorf("两次 Summary 不一致: %+v / %+v", first, second)
	}
}

// 非流式响应大过缓冲上限：停止累积并降级，不能把内存吃满。
func TestTapCoreNonStreamOverLimitDegrades(t *testing.T) {
	called := false
	core := NewTapCore(false, nil, func(*Summary, []byte) { called = true })

	chunk := strings.Repeat("x", 1<<20)
	for range 5 {
		if _, err := core.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}

	sum := core.Summary()
	if !sum.Degraded {
		t.Error("超过上限却没降级")
	}
	if called {
		t.Error("超限后还把残缺的 body 拿去解析了")
	}
}
