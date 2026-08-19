package server_test

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

// 本文件是 #8 的复现（两条用例合起来才是完整论证，处置未定前都默认跳过）：
//
// 竞态的结构性成因在 convert.go 的 streamConverted——outCodec.DecodeStream 在解码
// goroutine 上读 TeeReader，于是 tap.Write 跑在那条线上；EncodeStream 写出失败时
// handler goroutine 走 `go drainEvents + panic(http.ErrAbortHandler)`，panic 展开里
// relayConverted 的 defer 按 LIFO **先**跑 rec.Summarized(tap.Summary())、**后**跑
// resp.Body.Close()。Summary 被调的那一刻上游连接还开着，drainEvents 又保证解码
// goroutine 没有卡在发事件上——两条线在 TapCore 的 sum/finished/scanner 上重叠，
// 而 TapCore 完全无同步。cfg.LogBodies 开着时 rec.TapResponseBody() 那个收集器
// 同构（也在解码 goroutine 上被写、在 handler 侧被读）。
//
// 复现方式：
//
//	PORTAGE_RACE_REPRO=1 go test -race -run TestConvertStream -count=1 ./internal/server/
//
// 拓扑用例**必报 DATA RACE**（报了即复现成功）；整链用例证明对齐这个拓扑的触发
// 路径真实可达，但 race 本身是微秒级调度彩票，它自己报不报警看运气。修复落地后
// 这两条去掉跳过，转正成回归用例（届时 -race 下应全绿）。

// 拓扑用例：把 streamConverted 的 goroutine 拓扑用生产类型原样缩微——TeeReader 挂
// Tap、DecodeStream 起解码 goroutine、handler 先正常收几条（对应 EncodeStream 的
// 前半段）、然后 go drain + Summary（对应写出失败后的 panic 展开）。-race 下必报。
//
// 它绕开的只有「写出失败」的对齐时机（由下面整链用例证明可达），竞争的双方、
// 数据结构、调用栈与生产一字不差。
func TestConvertStreamTapTopologyRaces(t *testing.T) {
	if os.Getenv("PORTAGE_RACE_REPRO") == "" {
		t.Skip("竞态复现用例（#8）：设 PORTAGE_RACE_REPRO=1 并带 -race 跑，报 DATA RACE 即复现")
	}

	tap := openaicc.NewTap(true)
	pr, pw := io.Pipe()
	src := io.TeeReader(pr, tap)

	events, err := (&openaicc.Codec{}).DecodeStream(src)
	if err != nil {
		t.Fatal(err)
	}

	frame := []byte(`data: {"choices":[{"index":0,"delta":{"content":"xxxx"}}]}` + "\n\n")
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		for {
			if _, err := pw.Write(frame); err != nil {
				return
			}
		}
	}()

	for i := 0; i < 5; i++ {
		<-events // EncodeStream 的前半段：正常消费，建立解码侧→handler 的接收边
	}
	go func() { // 写出失败分支的 go drainEvents(events)
		for range events {
		}
	}()
	_ = tap.Summary() // panic 展开里的 rec.Summarized(tap.Summary())

	pr.Close()
	pw.Close()
	<-stop
}

// 整链用例：证明上面那个拓扑的触发路径在完整网关上真实可达。
//
// 触发形态挑的是**客户端停读但不断连**：断连（RST/ctx 取消）会同时杀掉上游请求
// （s.up.Do 挂在 c.Request.Context() 上），解码 goroutine 几乎立刻收摊；停读则请求
// ctx 还活着、上游继续灌、编码侧堵在给客户端的 Write 上直到 30s writeDeadline 到期
// 才报错进 panic 路径——这正是生产里的真实形态（客户端挂死、网络路径僵住）。
// 断言钉在「这一行流水以 stream_aborted 收场」上：走到这里就意味着 panic 展开里
// Summary 先于 Body.Close 跑过了一遍，与解码 goroutine 的重叠窗口打开过。
//
// race 报不报警取决于解码 goroutine 被 drainEvents 放行后能不能在 Body.Close 之前
// 抢到下一次 Read——本机实测多为 Close 先到，所以这条经常绿着结束；它的价值是
// 钉住可达性，检出交给上面的拓扑用例。
func TestConvertStreamStalledClientReachesThePanicPath(t *testing.T) {
	if os.Getenv("PORTAGE_RACE_REPRO") == "" {
		t.Skip("竞态复现用例（#8）：设 PORTAGE_RACE_REPRO=1 并带 -race 跑，耗时约 35s（等写超时）")
	}

	gw, up := newConvertGateway(t)
	// 上游无限发大帧：帧越大，每次 Read 里的帧越少（解码 goroutine 醒来即回 Read）、
	// tap 侧的 json 解析越慢（重叠窗口越宽）。handler 收场时请求 ctx 取消，这里跟着退。
	frame := `data: {"choices":[{"index":0,"delta":{"content":"` +
		strings.Repeat("x", 24<<10) + `"}}]}` + "\n\n"
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		for {
			if _, err := io.WriteString(w, frame); err != nil {
				return
			}
			f.Flush()
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}

	resp := gw.PostCtx(t, t.Context(), "/v1/messages", convertRequest, nil)
	// 读到首帧确认流已经立起来，然后**停读**：连接保持打开，TCP 窗口收满之后
	// 网关往下行的 Write 堵住，30s 后 writeDeadline 到期报错，进 panic 路径。
	gatewaytest.ReadSome(t, resp.Body, 2*time.Second)

	// 等那一行流水落库（rec.Finish 在 panic 展开的 defer 里同步插行）。上限放宽到
	// 45s：30s 是写超时本身，剩下的留给调度与落库。
	deadline := time.Now().Add(45 * time.Second)
	for gw.CountCallRows(t) < 1 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "stream_aborted" {
		t.Fatalf("error 列 = %q，期望 stream_aborted——没走到写出失败的 panic 路径，这一轮没构成复现", row.Error.String)
	}
}
