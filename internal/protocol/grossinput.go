package protocol

// 本文件是「毛值 input」那条规则（CONTEXT.md 词条，口径层 v0.54 ④ / v0.71）的家：
// canonical 与流水两条路都从这里取，算术只写这一遍。

// GrossInput 是毛值 input 的唯一算术定义处：净值 + 缓存读 + 缓存写。
//
// Anthropic 的 usage 契约里 input_tokens 与缓存两项**互不相交**（净值），毛值口径
// 把三者归到一个数上；缓存两项照旧留作明细。canonical 解码侧
// （anthropic/decode_response.go 的 usagePayload.canonical）与流水归一侧
// （GrossSummaryInput）共用它——两处各写一遍无名加法，正是 v0.49 给出口减法收进
// Usage.NetInput 时要防的形态。
func GrossInput(net, cacheRead, cacheWrite int) int {
	return net + cacheRead + cacheWrite
}

// GrossSummaryInput 按渠道协议把 Tap Summary 的 input 归一为毛值（#6，口径层 v0.71）。
//
// Tap 的 Summary 刻意保留上游原始语义（v0.71 ② 不复议）：Anthropic Tap 存的是上游
// 原样的**净值**，CC / Responses 存的本就是毛值。于是跨协议可加的数不能直接取
// Summary——落流水那一列前要经这里归一，否则用量页的 SUM(input_tokens) 在把两种
// 单位相加，缓存命中重的 Anthropic 渠道被系统性低估。
//
// 只有这一处按协议分派：调用方（calllog.Recorder.Row）不该知道哪家是净值。
func GrossSummaryInput(channel Protocol, sum Summary) int {
	if channel == Anthropic {
		return GrossInput(sum.InputTokens, sum.CacheReadTokens, sum.CacheWriteTokens)
	}
	return sum.InputTokens
}
