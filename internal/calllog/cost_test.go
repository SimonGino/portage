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
	got := p.CostUSD(1000, 500, 200, 100, 0)
	want := 700*3/1e6 + 500*15/1e6 + 200*0.3/1e6 + 100*3.75/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %+v，期望 %v", got, want)
	}
}

// cached + write > prompt（OpenAI 两项都是未调整的前缀计数，new-api 92d3c9d18 的取值）：
// 净输入钳到 0，读、写两项照原数全额计价，不按比例压缩（#111，PO 2026-09-28 裁决）。
func TestCostClampsNetInputWhenCacheExceedsGross(t *testing.T) {
	p := calllog.Prices{Input: f(1.25), Output: f(10), CacheRead: f(0.125), CacheWrite: f(1.5625)}
	got := p.CostUSD(3619, 37, 2921, 3616, 0)
	want := 37*10/1e6 + 2921*0.125/1e6 + 3616*1.5625/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %+v，期望 %v（净输入 0）", got, want)
	}
}

// 未定价记 0：四价全 NULL 的条目有用量照记 0，不留 NULL——「没定价」与「没打上游」
// 在这一列上必须分得开。逐项适用：只缺某一项时那一项按 0 计，其余照算。
func TestUnpricedComponentsCountAsZero(t *testing.T) {
	if got := (calllog.Prices{}).CostUSD(1000, 500, 200, 100, 0); !got.Valid || got.Float64 != 0 {
		t.Fatalf("全未定价 CostUSD = %+v，期望 {0 true}", got)
	}
	p := calllog.Prices{Input: f(2), Output: f(10)} // 缓存两项未定价
	got := p.CostUSD(1000, 500, 200, 100, 0)
	want := 700*2/1e6 + 500*10/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("缺缓存价 CostUSD = %+v，期望 %v", got, want)
	}
}

// 真免费（0）与未定价（NULL）算出来都是 0，但那是两种 0：这条只钉住 0 价合法、
// 不会被当成「没有价」处理出别的结果。
func TestZeroPriceIsFreeNotUnpriced(t *testing.T) {
	p := calllog.Prices{Input: f(0), Output: f(0), CacheRead: f(0), CacheWrite: f(0)}
	if got := p.CostUSD(1000, 500, 200, 100, 0); !got.Valid || got.Float64 != 0 {
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

func i64(v int64) *int64 { return &v }

// 分档价（口径层 v1.49）：毛输入严格大于阈值，整笔四项全按分档价——整笔替换
// 不是加成；等于阈值仍按基础价。毛输入含缓存读写，判档不减缓存。
func TestTierAppliesWholeCallAboveThreshold(t *testing.T) {
	p := calllog.Prices{
		Input: f(3), Output: f(15), CacheRead: f(0.3), CacheWrite: f(3.75),
		TierAbove: i64(1000),
		TierInput: f(6), TierOutput: f(22.5), TierCacheRead: f(0.6), TierCacheWrite: f(7.5),
	}
	// 毛 1000 = 阈值：不超过，基础价。
	at := p.CostUSD(1000, 500, 200, 100, 0)
	wantAt := 700*3/1e6 + 500*15/1e6 + 200*0.3/1e6 + 100*3.75/1e6
	if math.Abs(at.Float64-wantAt) > 1e-12 {
		t.Fatalf("阈值处 CostUSD = %v，期望基础价 %v", at.Float64, wantAt)
	}
	// 毛 1001（净只有 1，主要是缓存）：超过，四项全按分档价。
	above := p.CostUSD(1001, 500, 600, 400, 0)
	wantAbove := 1*6/1e6 + 500*22.5/1e6 + 600*0.6/1e6 + 400*7.5/1e6
	if math.Abs(above.Float64-wantAbove) > 1e-12 {
		t.Fatalf("超阈值 CostUSD = %v，期望分档价 %v", above.Float64, wantAbove)
	}
}

// 分档某价空 = 沿用基础价（models.dev 省略语义）；基础价也空的照旧按 0。
func TestTierMissingPriceFallsBackToBase(t *testing.T) {
	p := calllog.Prices{
		Input: f(3), Output: f(15), CacheRead: f(0.3),
		TierAbove: i64(100),
		TierInput: f(6), // 其余三项分档未填
	}
	got := p.CostUSD(1000, 500, 200, 100, 0)
	want := 700*6/1e6 + 500*15/1e6 + 200*0.3/1e6 // cache_write 两边都空记 0
	if math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %v，期望 %v", got.Float64, want)
	}
}

// 阈值空 = 全程一档：分档四价即便有值也不生效（导入或残留数据的防线）。
func TestNilThresholdMeansSingleTier(t *testing.T) {
	p := calllog.Prices{Input: f(3), Output: f(15), TierInput: f(100), TierOutput: f(100)}
	got := p.CostUSD(1_000_000, 500, 0, 0, 0)
	want := 1_000_000*3/1e6 + 500*15/1e6
	if math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %v，期望基础价 %v", got.Float64, want)
	}
}

// 1h 缓存写（#198）：cacheWrite1h 是总写入 cacheWrite 的**明细**——1h 部分乘 1h 价、
// 其余乘 5 分钟价，两项之和仍是总写入乘各自单价的账，总量不变。
func TestCostSplitsCacheWriteByTTL(t *testing.T) {
	p := calllog.Prices{Input: f(3), Output: f(15), CacheRead: f(0.3), CacheWrite: f(3.75), CacheWrite1H: f(6)}
	// 毛 1000 = 净 700 + 读 200 + 写 100（其中 1h 40、5m 60）。
	got := p.CostUSD(1000, 500, 200, 100, 40)
	want := 700*3/1e6 + 500*15/1e6 + 200*0.3/1e6 + 60*3.75/1e6 + 40*6/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %+v，期望 %v", got, want)
	}
}

// 未设 1h 价：1h 那部分按 5 分钟价计，不自动按 2× input 落库（口径层 v1.49——
// 「Claude 1h = 2× input」只进建议价）。
func TestCostWithout1hPriceFallsBackTo5mPrice(t *testing.T) {
	p := calllog.Prices{Input: f(3), CacheWrite: f(3.75)}
	got := p.CostUSD(140, 0, 0, 100, 40)
	want := 40*3/1e6 + 60*3.75/1e6 + 40*3.75/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %+v，期望 %v（1h 部分也按 3.75 计）", got, want)
	}
}

// 碎上游把 1h 明细报到比总写入还大：5m 部分钳到 0、1h 部分取 min——负的 token 数
// 不是合法账（同 NetInput 钳零那道闸，只钳差不压原数）。
func TestCostClampsBroken1hBreakdown(t *testing.T) {
	p := calllog.Prices{Input: f(3), CacheWrite: f(3.75), CacheWrite1H: f(6)}
	got := p.CostUSD(150, 0, 0, 100, 130)
	want := 50*3/1e6 + 100*6/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %+v，期望 %v", got, want)
	}
}

// 分档价连 1h 那格一起整笔换（#198 + 口径层 v1.49）：分档 1h 价空 = 沿用基础 1h 价。
func TestTierReplacesAndFallsBackFor1hPrice(t *testing.T) {
	p := calllog.Prices{
		Input: f(3), CacheWrite: f(3.75), CacheWrite1H: f(6),
		TierAbove: i64(1000), TierInput: f(6), TierCacheWrite: f(7.5), // 分档 1h 价不填
	}
	got := p.CostUSD(1001, 0, 0, 100, 40) // 毛 1001 超阈值，整笔按分档
	want := 901*6/1e6 + 60*7.5/1e6 + 40*6/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %+v，期望 %v（1h 空沿用基础 6）", got, want)
	}
	p.TierCacheWrite1H = f(12)
	got = p.CostUSD(1001, 0, 0, 100, 40)
	want = 901*6/1e6 + 60*7.5/1e6 + 40*12/1e6
	if !got.Valid || math.Abs(got.Float64-want) > 1e-12 {
		t.Fatalf("CostUSD = %+v，期望 %v（1h 按分档 12）", got, want)
	}
}

// 1h 明细落流水（#198）：有 summary 就有这一列（含 0），cost 按 1h 拆两段计。
func TestRowLandsCacheWrite1hAndSplitsCost(t *testing.T) {
	h := newHarness()
	h.rec.RequestParsed("m", false)
	h.rec.Routed("ch", protocol.Anthropic, "claude-sonnet-5",
		calllog.Prices{Input: f(3), CacheWrite: f(3.75), CacheWrite1H: f(6)})
	h.rec.Summarized(protocol.Summary{
		InputTokens: 700, OutputTokens: 500, CacheReadTokens: 200,
		CacheWriteTokens: 100, CacheWrite1hTokens: 40,
	})
	h.rec.Succeeded()
	row := h.row(t, 200)
	if !row.CacheWrite1hTokens.Valid || row.CacheWrite1hTokens.Int64 != 40 {
		t.Fatalf("cache_write_1h_tokens = %+v，期望 40", row.CacheWrite1hTokens)
	}
	// 毛值 1000（净值 700 加回缓存两项）；output 与缓读未定价按 0，1h 那部分按 6 计、
	// 其余写入按 3.75。
	want := 700*3/1e6 + 60*3.75/1e6 + 40*6/1e6
	if !row.Cost.Valid || math.Abs(row.Cost.Float64-want) > 1e-12 {
		t.Fatalf("cost = %+v，期望 %v", row.Cost, want)
	}
	if row.InputTokens.Int64 != 1000 {
		t.Fatalf("input_tokens = %d，期望 1000（毛值不变——1h 是明细不是加数）", row.InputTokens.Int64)
	}
}
