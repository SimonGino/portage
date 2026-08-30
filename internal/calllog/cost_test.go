package calllog_test

import (
	"math"
	"testing"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件对照展开层 §7.10.1 的 cost 条目：四项求和、未定价记 0、无用量 NULL、
// 落库时点（改价不追溯——由「cost 在 Row() 里按当时的 Prices 算死」这个形状保证，
// 库里没有第二次计算点，无从追溯）。

func f(v float64) *float64 { return &v }

// 四项求和：净 input、output、cache_read、cache_write 各乘各的单价 ÷ 1e6。
// grossInput 是毛值（口径层 v0.71），缓存两项要减回去——不减就是把缓存 token
// 按 input 全价收两遍钱（§8.2 记过的那个系统性高估）。
func TestCostSumsFourComponentsOnNetInput(t *testing.T) {
	p := calllog.Prices{Input: f(3), Output: f(15), CacheRead: f(0.3), CacheWrite: f(3.75)}
	// 毛值 1000 = 净 700 + 缓存读 200 + 缓存写 100。
	got := p.CostUSD(1000, 500, 200, 100)
	want := 700*3/1e6 + 500*15/1e6 + 200*0.3/1e6 + 100*3.75/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %+v，期望 %v", got, want)
	}
}

// 未定价记 0：四价全 NULL 的条目有用量照记 0，不留 NULL——「没定价」与「没打上游」
// 在这一列上必须分得开。逐项适用：只缺某一项时那一项按 0 计，其余照算。
func TestUnpricedComponentsCountAsZero(t *testing.T) {
	if got := (calllog.Prices{}).CostUSD(1000, 500, 200, 100); !got.Valid || got.Float64 != 0 {
		t.Fatalf("全未定价 CostUSD = %+v，期望 {0 true}", got)
	}
	p := calllog.Prices{Input: f(2), Output: f(10)} // 缓存两项未定价
	got := p.CostUSD(1000, 500, 200, 100)
	want := 700*2/1e6 + 500*10/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("缺缓存价 CostUSD = %+v，期望 %v", got, want)
	}
}

// 真免费（0）与未定价（NULL）算出来都是 0，但那是两种 0：这条只钉住 0 价合法、
// 不会被当成「没有价」处理出别的结果。
func TestZeroPriceIsFreeNotUnpriced(t *testing.T) {
	p := calllog.Prices{Input: f(0), Output: f(0), CacheRead: f(0), CacheWrite: f(0)}
	if got := p.CostUSD(1000, 500, 200, 100); !got.Valid || got.Float64 != 0 {
		t.Fatalf("真免费 CostUSD = %+v，期望 {0 true}", got)
	}
}

// 无用量 NULL：没有 summary 的行（没到上游、鉴权失败）cost 与 token 五列同判据，
// 一起留 NULL。走 Recorder 全链路验，别只验算术函数。
func TestCostIsNullWithoutUsage(t *testing.T) {
	h := newHarness()
	h.rec.RequestParsed("m", false)
	h.rec.Routed("ch", protocol.OpenAI, "gpt-4o",
		calllog.Prices{Input: f(3), Output: f(15)})
	// 没有 Summarized——比如上游拨不通。
	h.rec.Failed(calllog.UpstreamError, "拨不通")
	if row := h.row(t, 502); row.Cost.Valid {
		t.Fatalf("无用量的行 cost = %+v，期望 NULL", row.Cost)
	}
}

// 有用量的行按 Routed 交来的四价算——Anthropic 渠道的 Summary 存的是净值 input，
// 落库前归一成毛值再在计价里减回去，净值口径不变（两步互逆是刻意的：流水列要毛值、
// 记账要净值，各自的理由见 GrossSummaryInput 与 Prices.CostUSD）。
func TestCostUsesRoutedPricesWithAnthropicNetInput(t *testing.T) {
	h := newHarness()
	h.rec.RequestParsed("m", false)
	h.rec.Routed("ch", protocol.Anthropic, "claude-sonnet-4",
		calllog.Prices{Input: f(3), Output: f(15), CacheRead: f(0.3), CacheWrite: f(3.75)})
	h.rec.Summarized(protocol.Summary{
		InputTokens: 700, OutputTokens: 500, CacheReadTokens: 200, CacheWriteTokens: 100,
	})
	h.rec.Succeeded()
	row := h.row(t, 200)
	want := 700*3/1e6 + 500*15/1e6 + 200*0.3/1e6 + 100*3.75/1e6
	if !row.Cost.Valid || math.Abs(row.Cost.Float64-want) > 1e-12 {
		t.Fatalf("cost = %+v，期望 %v", row.Cost, want)
	}
	// 毛值列照旧是 1000：计价的净值处理不许漂到 token 列上。
	if row.InputTokens.Int64 != 1000 {
		t.Fatalf("input_tokens = %v，期望 1000（毛值）", row.InputTokens)
	}
}
