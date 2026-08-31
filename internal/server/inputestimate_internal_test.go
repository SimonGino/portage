package server

import (
	"strings"
	"testing"
)

// 本文件测 estimateInputTokens 的换算与识别（口径层 v1.07）：连续标准 base64 字符
// ≥4096 的段按解码字节 ÷512、保底 256 计，段外照旧 ÷4；识别是纯字符类算术，不解析
// 不解码。闸的行为（413、豁免、两条路同一把尺）在 inputlimit_test.go，这里只钉纯函数。

// run 造一个恰好 n 字符的 base64 段。字符取自标准表且不带自然断点。
func run(n int) string { return strings.Repeat("Ab3+/=", n/6) + strings.Repeat("A", n%6) }

func TestEstimatePlainTextIsBytesOver4(t *testing.T) {
	// 中文、空格、标点都不是 base64 字符类，永远走 ÷4——升级前后基线不变。
	body := strings.Repeat("你好 hello, world! ", 100)
	if got, want := estimateInputTokens([]byte(body)), len(body)/4; got != want {
		t.Errorf("纯文本估算 = %d, 期望 len/4 = %d", got, want)
	}
}

func TestEstimateRunBelowThresholdCountsAsPlain(t *testing.T) {
	// 4095 字符差一个不到阈值：整段按 ÷4，不触发换算。
	body := run(base64RunMin - 1)
	if got, want := estimateInputTokens([]byte(body)), len(body)/4; got != want {
		t.Errorf("阈值下段估算 = %d, 期望 len/4 = %d", got, want)
	}
}

func TestEstimateRunAtThresholdGetsFloor(t *testing.T) {
	// 恰到阈值即命中；4096×3/2048=6，被保底 256 兜起——阈值附近两种算法本就同
	// 量级（÷4 是 1024），这正是阈值敢定这么低的原因。
	if got := estimateInputTokens([]byte(run(base64RunMin))); got != 256 {
		t.Errorf("4096 字符段估算 = %d, 期望保底 256", got)
	}
}

func TestEstimateBigRunScalesWithSize(t *testing.T) {
	// 2MB 段：2<<20 × 3/2048 = 3072——换算随大小线性缩放，不是固定值。
	n := 2 << 20
	if got := estimateInputTokens([]byte(run(n))); got != n*3/2048 {
		t.Errorf("2MB 段估算 = %d, 期望 %d", got, n*3/2048)
	}
}

func TestEstimateMixedBodySumsRunsAndPlain(t *testing.T) {
	// 两段大 base64 夹在 JSON 文本里：各段独立换算线性叠加，段外字节 ÷4。引号和
	// 逗号都不在字符类里，天然断段——不需要解析 JSON 就能把载荷和结构分开。
	prefix := `{"model":"m","messages":[{"role":"user","content":[{"type":"image","data":"`
	middle := `"},{"type":"image","data":"`
	suffix := `"}]}]}`
	r1, r2 := 8192, 1<<20
	body := prefix + run(r1) + middle + run(r2) + suffix
	plain := len(prefix) + len(middle) + len(suffix)
	want := max(256, r1*3/2048) + r2*3/2048 + plain/4
	if got := estimateInputTokens([]byte(body)); got != want {
		t.Errorf("混合体估算 = %d, 期望 %d", got, want)
	}
}

func TestEstimateBase64URLDoesNotCount(t *testing.T) {
	// base64url 的 `-` 不在标准表：一段被 `-` 打散成两个 2560 的子段，都不到阈值，
	// 整体照旧 ÷4。JWT（base64url + `.` 分隔）因此天然命不中。
	body := run(2560) + "-" + run(2560)
	if got, want := estimateInputTokens([]byte(body)), len(body)/4; got != want {
		t.Errorf("base64url 形状估算 = %d, 期望 len/4 = %d", got, want)
	}
}

func TestEstimateRunAtBodyTail(t *testing.T) {
	// 段一直顶到 body 末尾也要被收尾结算——扫描器最后一步的 flush 不能丢。
	n := 1 << 20
	body := "x:" + run(n)
	want := n*3/2048 + len("x:")/4
	if got := estimateInputTokens([]byte(body)); got != want {
		t.Errorf("尾段估算 = %d, 期望 %d", got, want)
	}
}
