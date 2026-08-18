package calllog

import (
	"io"
	"strings"
	"testing"
)

// captureWriter 的两条硬约束在这里钉住：永不报错，收到上限就停。
//
// 是包内测试（package calllog 而不是 calllog_test）：这个类型不导出，也不该为了
// 测试导出——它是 Recorder 的零件，外面唯一看得见的是 TapResponseBody 那两个
// io.Writer。

// 永不报错是它挂进 io.MultiWriter 的前提：MultiWriter 会把任何一路的写错误变成
// 整体的写错误，而这一路是**旁路**——为了记一份日志把正在转发的响应打断，方向
// 完全反了。
func TestCaptureNeverErrorsEvenPastItsLimit(t *testing.T) {
	c := newCapture(8)
	for _, p := range [][]byte{[]byte("12345678"), []byte("溢出的那些"), nil} {
		n, err := c.Write(p)
		if err != nil {
			t.Fatalf("Write(%q) 报错了: %v", p, err)
		}
		// 报的是「全收下了」而不是实际留下的字节数：报少了 io.MultiWriter 会
		// 判成 io.ErrShortWrite，一样打断转发。
		if n != len(p) {
			t.Errorf("Write(%q) = %d，期望 %d", p, n, len(p))
		}
	}
}

// 截断这件事要说出口——否则一段被砍掉一半的 JSON 看起来像上游发了个坏包。
func TestCaptureAnnouncesTruncation(t *testing.T) {
	c := newCapture(8)
	_, _ = io.WriteString(c, "12345678")
	if got := c.String(); got != "12345678" {
		t.Errorf("正好填满时 String() = %q，不该报截断", got)
	}

	_, _ = io.WriteString(c, "9")
	if got := c.String(); got != "12345678"+truncationMark {
		t.Errorf("String() = %q", got)
	}
	// collected 交的是原始字节，不带那句话：request_id 取键在它上面做（v0.74），
	// 混进一句中文会让本来解得动的 JSON 解不动。
	if got := string(c.collected()); got != "12345678" {
		t.Errorf("collected() = %q", got)
	}
}

// truncatedTo 是「内存里收完整的一段、落库那一刻再截」这条口径的执行者
// （v0.74 + v0.53）。两个上限差两个数量级，所以二次截断必须是独立的一步。
func TestTruncatedToCutsAgainAtTheSmallerLimit(t *testing.T) {
	c := newCapture(upstreamErrorLimit)
	_, _ = io.WriteString(c, strings.Repeat("x", 3<<10))

	got := c.truncatedTo(errorDetailLimit)
	if !strings.HasSuffix(got, truncationMark) || len(got) != errorDetailLimit+len(truncationMark) {
		t.Errorf("truncatedTo 长 %d，期望 %d + 截断标记", len(got), errorDetailLimit)
	}
	// 没超过小上限时原样交出，不该凭空多一句截断。
	short := newCapture(upstreamErrorLimit)
	_, _ = io.WriteString(short, "短的")
	if got := short.truncatedTo(errorDetailLimit); got != "短的" {
		t.Errorf("truncatedTo = %q", got)
	}
}
