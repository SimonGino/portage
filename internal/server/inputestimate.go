package server

// 输入上限的 token 估算（口径层 v0.99 ②，base64 大段换算见 v1.07）。
//
// 基线是字节数 ÷ 4——透传路径不解析 body 是硬约束，字节估算是唯一让透传与转换
// 同一把尺的算法。例外是 base64 大段：一张 1MB 图的 base64 按 ÷4 会估出 35 万
// token，真实开销只有千把，误差两个数量级，足以把限内的请求误拦（v1.07 的起因
// 样本）。所以对连续标准 base64 字符 ≥ base64RunMin 的段改按解码字节 ÷ 512 计
// （常数沿 opencodex 的兜底路径），保底 256/段，段外字节照旧 ÷ 4。
//
// 识别是纯字符类算术：不解析 JSON、不解码内容、不嗅媒体类型。不认 base64url 的
// `-_`（三协议图片字段与 data URI 都是标准表），也不用 `;base64,` 锚点——
// Anthropic 的 source.data 没有 data URI 头，锚点会漏。由此有两条已知边界
// （v1.07 写明不防）：离群视觉模型会被低估；纯 base64 字符的长文本可借换算绕过
// 上限——防它等于全量解码验证真伪，违背「不解析」。

// base64RunMin 是判定「大段」的最小连续长度（字符数）。换算之下小段误判几乎
// 无代价（1KB 段两种算法都 ≈250 token），阈值只为省扫描开销、并让长 hash 与
// JWT（base64url，本就命不中）天然落在线外。
const base64RunMin = 4096

// estimateInputTokens 对入站原始请求体做输入上限闸的 token 估算。
func estimateInputTokens(body []byte) int {
	plain := len(body)
	tokens := 0
	run := 0
	for _, b := range body {
		if isBase64Byte(b) {
			run++
			continue
		}
		if run >= base64RunMin {
			plain -= run
			tokens += base64RunTokens(run)
		}
		run = 0
	}
	if run >= base64RunMin {
		plain -= run
		tokens += base64RunTokens(run)
	}
	return tokens + plain/4
}

// base64RunTokens 把一个 base64 大段换算成 token：解码字节（字符数 × 3/4）÷ 512，
// 保底 256。
func base64RunTokens(n int) int {
	return max(256, n*3/2048)
}

func isBase64Byte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' ||
		b == '+' || b == '/' || b == '='
}
