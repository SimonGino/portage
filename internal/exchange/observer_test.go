package exchange

import (
	"io"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

// 本文件钉 ResponseObserver 的收场序（#8 的构造化）：convertrace_test.go 的拓扑
// 用例靠手工重建生产拓扑（NewTap + io.Pipe + TeeReader + DecodeStream）来摆出
// 「先断上游 → 排空到关闭 → 才 Summarize」，这里钉的是**观察者自己的 Close 就是
// 那条序**——不需要任何调用方记得次序，-race 下必须全绿，再报 DATA RACE 即回归。

// TestObserverCloseRunsAbortOrderingByConstruction 缩微写出失败的收场：解码
// goroutine 在另一条线上经 TeeReader 读上游、往 Tap 写；handler 侧正常消费几条
// 事件后不再读（对应 EncodeStream 中途写客户端失败），然后只调一个 obs.Close()。
// 修复前的等价形态（Summary 与解码侧无 happens-before 边）-race 三连跑三报。
func TestObserverCloseRunsAbortOrderingByConstruction(t *testing.T) {
	tap := openaicc.NewTap(true)
	pr, pw := io.Pipe()
	obs := &ResponseObserver{
		Body:     io.TeeReader(pr, tap),
		upstream: pr,
		rec:      calllog.Detached(),
		tap:      tap,
	}

	events, err := (&openaicc.Codec{}).DecodeStream(obs.Body)
	if err != nil {
		t.Fatal(err)
	}
	obs.AttachStream(events)

	frame := []byte(`data: {"choices":[{"index":0,"delta":{"content":"xxxx"}}]}` + "\n\n")
	stop := make(chan struct{})
	go func() { // 上游持续灌帧，直到读端被关
		defer close(stop)
		for {
			if _, err := pw.Write(frame); err != nil {
				return
			}
		}
	}()

	for i := 0; i < 5; i++ {
		<-events // handler 正常消费的前半段，建立解码侧→本侧的接收边
	}
	obs.Close() // 收场序全在构造里：断上游 → 排空到关闭 → Summarize

	pw.Close()
	<-stop
}

// TestObserverCloseWithoutStreamDoesNotBlock 钉住没挂解码侧时 Close 不阻塞——
// nil 通道的 range 会永久阻塞，那个守卫是正确性不是优化。透传、缓冲、错误早退
// 三条路走的都是这一支。
func TestObserverCloseWithoutStreamDoesNotBlock(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	obs := &ResponseObserver{Body: pr, upstream: pr, rec: calllog.Detached()}

	done := make(chan struct{})
	go func() {
		obs.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("没挂解码侧的 Close 阻塞住了——nil 通道守卫回归")
	}
}
