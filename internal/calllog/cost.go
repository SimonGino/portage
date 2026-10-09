package calllog

import (
	"cmp"
	"database/sql"
)

// Prices 是一条纳管条目的四价（口径层 §2.10 计价，#65/#74），单位 USD/百万 token。
//
// 住在本包而不是 store：路由选中候选后它要交给 Recorder 在落库时点算 cost，而
// store 已经 import 本包（写侧行类型的那条依赖，见 Row 的 doc），反着放就成环。
// store.Candidate 直接用这个类型，与 CallLog = calllog.Row 同款别名思路。
//
// 指针的 nil = 这一项未定价。与 0（真免费）必须分开：未定价是「还没记账依据」，
// 免费是「记过了，账是 0」。
type Prices struct {
	Input      *float64
	Output     *float64
	CacheRead  *float64
	CacheWrite *float64
	// CacheWrite1H 是 1 小时 TTL 缓存写的单价（#198），USD/百万 token。nil = 未设，
	// 1h 那部分按 CacheWrite（5 分钟价）计——不自动按 2× input 落库，那是建议价里给
	// 的（定价事实见 docs.anthropic.com prompt-caching#pricing，仅 Claude 系适用）。
	CacheWrite1H *float64

	// 分档价（口径层 v1.49，#185）：TierAbove 非 nil 且毛输入严格大于它时，整笔
	// 五项全按分档价计；分档某价 nil = 沿用基础价。TierAbove nil = 全程一档，
	// 分档价即便有值也不看。
	TierAbove      *int64
	TierInput      *float64
	TierOutput     *float64
	TierCacheRead  *float64
	TierCacheWrite *float64
	// TierCacheWrite1H 同 CacheWrite1H 的分档对应项；nil = 沿用基础 1h 价。
	TierCacheWrite1H *float64
}

// CostUSD 按五价算一次调用的成本（落库时点计价，改价不追溯）。
//
// grossInput 收的是**毛值** input（口径层 v0.71：流水那一列的口径），缓存两项在
// 这里减回去——input 单价只该乘**非缓存**的那部分：上游对缓存读写各收各的折扣价/
// 加成价，毛值直接乘 input 单价会把缓存 token 收两遍钱（§8.2 早记过「不减缓存直接
// 乘单价会系统性高估」，那正是四价分列的全部意义）。净输入 clamp 到 0 是常规路径：
// OpenAI 的缓存读、写两项都是未调整的前缀计数，之和可以大于毛值（#111）；两项照原数
// 全额计价，不按比例压缩（同 protocol.Usage.NetInput）。
//
// cacheWrite1h 是总写入里 1 小时 TTL 那部分（#198），**cacheWrite 的明细**不是另
// 一笔：1h 部分乘 1h 价、其余乘 5 分钟价。未设 1h 价时它按 5 分钟价计（cmp.Or 兜底）
// ——不自动按 2× input。1h 超过总写入的碎上游拆两段钳：5m 部分鏰到 0，1h 部分取
// min(1h, 总写入)，负的 token 数不是合法账。
//
// 判档在最前：毛输入（含缓存读写）> TierAbove 时整笔换成分档价，见 Prices。
//
// 未定价的项按 0 计（#65 ②「有用量但未定价记 0」逐项适用）；reasoning_tokens 是
// output 的明细，不进这里。返回值恒 Valid——「没有用量可计」（NULL）由调用方把关：
// 没有 usage 就压根不该调它。
func (p Prices) CostUSD(grossInput, output, cacheRead, cacheWrite, cacheWrite1h int) sql.NullFloat64 {
	if p.TierAbove != nil && int64(grossInput) > *p.TierAbove {
		p = Prices{
			Input:        cmp.Or(p.TierInput, p.Input),
			Output:       cmp.Or(p.TierOutput, p.Output),
			CacheRead:    cmp.Or(p.TierCacheRead, p.CacheRead),
			CacheWrite:   cmp.Or(p.TierCacheWrite, p.CacheWrite),
			CacheWrite1H: cmp.Or(p.TierCacheWrite1H, p.CacheWrite1H),
		}
	}
	// 1h 是写入的明细：拆成两段各自计价，总和仍是总写入——拆分不改变总量，只改变单价。
	write1h := min(cacheWrite1h, cacheWrite)
	write5m := cacheWrite - write1h
	// 未设 1h 价的那一段退回 5 分钟价，不自动按 2× input（那是建议价的事）。
	price1h := cmp.Or(p.CacheWrite1H, p.CacheWrite)
	net := max(grossInput-cacheRead-cacheWrite, 0)
	cost := mul(net, p.Input) + mul(output, p.Output) +
		mul(cacheRead, p.CacheRead) + mul(write5m, p.CacheWrite) + mul(write1h, price1h)
	return sql.NullFloat64{Float64: cost, Valid: true}
}

func mul(tokens int, pricePerMillion *float64) float64 {
	if pricePerMillion == nil {
		return 0
	}
	return float64(tokens) * *pricePerMillion / 1e6
}
