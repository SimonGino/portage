package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

// 本文件是 #8 的回归（两条用例合起来才看住整件事）：
//
// 竞态的结构性成因在 convert.go 的 streamConverted——outCodec.DecodeStream 在解码
// goroutine 上读 TeeReader，于是 tap.Write（与 LogBodies 的 body 收集器）跑在那条
// 线上；修复前写出失败分支走 `go drainEvents + panic`，panic 展开里 defer 的
// rec.Summarized(tap.Summary()) 先于 resp.Body.Close() 执行，两条线在 TapCore 的
// sum/finished/scanner 上无同步重叠（复现史与报警栈见展开层 v1.08）。
//
// 修复（方案 B，PO 2026-08-20 裁定）是收场次序重排：abortDecode 先关上游 body、
// 再把事件通道排空**到关闭**——通道关闭给出解码侧全部写入对 handler 侧的
// happens-before 边，之后才 panic，Summary 在 defer 里自然有序。
// 两条用例都要在 -race 下全绿；拓扑用例若再报 DATA RACE 即回归。

// 拓扑用例：把 streamConverted 写出失败的收场用生产类型原样缩微——TeeReader 挂
// Tap、DecodeStream 起解码 goroutine、handler 先正常收几条（对应 EncodeStream 的
// 前半段）、然后按 abortDecode 的次序收场：先断上游读端（≙ resp.Body.Close()）、
// 排空事件通道到关闭、才调 Summary（≙ panic 展开里的 rec.Summarized）。
//
// 修复前这里的收场是 `go drain + Summary`，-race 三连跑三报，报警栈就是生产栈
// （TapCore.Summary/finish 对 teeReader.Read → TapCore.Write/consume）；改成现在
// 这个次序后必须全绿——它钉的正是「通道关闭在 Summary 之前」这条 happens-before 边。
func TestConvertStreamTapAbortOrderingHasNoRace(t *testing.T) {
	tap := openaicc.NewTap(true)
	pr, pw := io.Pipe()
	src := io.TeeReader(pr, tap)

	events, err := (&openaicc.Codec{}).DecodeStream(src)
	if err != nil {
		t.Fatal(err)
	}

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

	for range 5 {
		<-events // EncodeStream 的前半段：正常消费，建立解码侧→handler 的接收边
	}
	// abortDecode 的收场次序，逐步对应：
	pr.Close() // ① 先断上游——解码 goroutine 的下一次 Read 立刻出错收摊
	for range events {
	} // ② 排空到通道关闭——拿到解码侧全部写入的 happens-before 边
	_ = tap.Summary() // ③ 此后 Summary（panic 展开里 defer 的 rec.Summarized）才安全

	pw.Close()
	<-stop
}

// 整链用例：证明写出失败的 panic 路径在完整网关上真实可达，且修复后收场干净。
//
// 触发形态挑的是**客户端停读但不断连**：断连（RST/ctx 取消）会同时杀掉上游请求
// （s.up.Do 挂在 c.Request.Context() 上），解码 goroutine 几乎立刻收摊；停读则请求
// ctx 还活着、上游继续灌、编码侧堵在给客户端的 Write 上直到 30s writeDeadline 到期
// 才报错进 panic 路径——这正是生产里的真实形态（客户端挂死、网络路径僵住）。
// 断言钉在「这一行流水以 stream_aborted 收场」上：走到这里就意味着 abortDecode
// 的收场次序真的跑过一遍；-race 下必须全绿（修复前这里绿不绿是调度彩票，检出
// 靠上面的拓扑用例，可达性由这条钉住——两条的分工没变）。
//
// 耗时约 35s（等写超时），是全套里最慢的一条；它看住的是只有整链才摆得出来的
// 触发时机，慢是这个形态本身的价格。
func TestConvertStreamStalledClientReachesThePanicPath(t *testing.T) {
	gw, up := newConvertGateway(t)
	// 上游无限发大帧：帧越大，每次 Read 里的帧越少（解码 goroutine 醒来即回 Read）、
	// tap 侧的 json 解析越慢（重叠窗口越宽）。handler 收场时上游 body 被关，这里跟着退。
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
		t.Fatalf("error 列 = %q，期望 stream_aborted——没走到写出失败的 panic 路径", row.Error.String)
	}
}
