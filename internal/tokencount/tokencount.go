// Package tokencount 是 count_tokens 的本地估算器（#18，口径层 v0.80）：非 Anthropic
// 出口没有原生 count_tokens 端点可转发，网关自己算一个数回 200。
//
// 承诺边界三条，一条都不能越（口径层 v0.80）：**不承诺与上游一致**（分词器不同、
// 系统提示注入不同、工具序列化不同，偏差必然存在，用途只是让 harness 判断何时该
// 压缩）；**不进计费、不写 call_logs 的 usage 列**（它不是一次上游调用）；**边界
// 锁死在这个端点、这一个字段**——这是全项目唯一一处网关把自己算的数当响应交给
// 客户端，不得据此外扩。
//
// 分词器取 O200kBase（tiktoken-go/tokenizer，词表内嵌、离线可用）：Anthropic 的
// 分词器不公开，这里要的是「同一个数量级、随文本长度单调」，CLIProxyAPI 的本地
// 估算同款（claude_executor_tokens.go）。**为了更准去打上游是禁手**——那会把一个
// 本地端点变成一次真实调用，直接违反边界二。
package tokencount

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/tiktoken-go/tokenizer"
)

// encoder 懒加载并缓存 O200kBase 编码器：词表构建有一次性成本，Claude Code 开场
// 会连打二十几次（#18 立论里的实测），逐请求构建就是逐请求白付。
var encoder = sync.OnceValues(func() (tokenizer.Codec, error) {
	return tokenizer.Get(tokenizer.O200kBase)
})

// Estimate 对一份 canonical 请求估算 input token 数，恒 ≥ 1。
//
// 口径是「取正文段落拼起来数一遍」：system / messages（role + 各块）/ tools
// （name + description + schema 原文）/ tool_choice。段落间以换行相接。不数
// 协议信封的 JSON 语法字节——上游注入的系统提示这类必然的低估已经被「不承诺
// 一致」盖住，再把括号引号数进去只是往另一个方向偏。
func Estimate(req *protocol.Request) (int, error) {
	e, err := encoder()
	if err != nil {
		return 0, err
	}
	var segs []string
	attachment := 0
	collectBlocks(req.System, &segs, &attachment)
	for _, m := range req.Messages {
		appendSeg(&segs, string(m.Role))
		collectBlocks(m.Content, &segs, &attachment)
	}
	for _, t := range req.Tools {
		appendSeg(&segs, t.Name)
		appendSeg(&segs, t.Description)
		if len(t.Schema) > 0 {
			appendSeg(&segs, string(t.Schema))
		}
		// custom 工具的文法约束（format）住在 Extras 里，可能比 schema 还大，
		// 不数它的话声明了 exec 工具的 Codex 请求会整段消失。
		appendExtras(&segs, t.Extras)
	}
	appendSeg(&segs, req.ToolChoice.Mode)
	appendSeg(&segs, req.ToolChoice.Name)

	n := 0
	if len(segs) > 0 {
		c, err := e.Count(strings.Join(segs, "\n"))
		if err != nil {
			return 0, err
		}
		n = c
	}
	n += attachment
	// 声明了工具的请求补一笔固定量：Anthropic 对带工具的请求注入 tool-use 系统
	// 提示（官方文档记 Sonnet 约 294~346 token）。#18 落地时拿 golden 转录实测过：
	// 单工具小请求不补时估算 51、真实 580，缺口几乎全是这一笔。300 是取中的近似，
	// 在 Claude Code 那种几万 token 的真实请求上它本来就淹没在正文里。
	if len(req.Tools) > 0 {
		n += 300
	}
	// Math.max(1, …) 同 opencodex：0 会被一些客户端读成「压根没算」，而空请求
	// 也至少要付信封的钱。
	if n < 1 {
		n = 1
	}
	return n, nil
}

// collectBlocks 把一串内容块的可数正文追加进 segs，图片折算另记在 attachment 上。
func collectBlocks(blocks []protocol.Block, segs *[]string, attachment *int) {
	for _, b := range blocks {
		switch b.Kind {
		case protocol.BlockText, protocol.BlockThinking:
			appendSeg(segs, b.Text)
		case protocol.BlockToolUse:
			if b.ToolCall != nil {
				appendSeg(segs, b.ToolCall.ID)
				appendSeg(segs, b.ToolCall.Name)
				appendSeg(segs, b.ToolCall.Args)
			}
		case protocol.BlockToolResult:
			if b.ToolResult != nil {
				appendSeg(segs, b.ToolResult.ToolCallID)
				collectBlocks(b.ToolResult.Content, segs, attachment)
			}
		case protocol.BlockImage:
			if b.Image != nil {
				*attachment += imageTokens(b.Image)
				// URL / FileID 是句柄不是载荷，按短文本数进去即可。
				appendSeg(segs, b.Image.URL)
				appendSeg(segs, b.Image.FileID)
			}
		}
	}
}

// imageTokens 是每张 base64 图片的折算：解码后字节数 / 512，下限 256。
//
// 系数照 opencodex 的 estimateBase64AttachmentTokens 兜底档（尺寸嗅探那半不做，
// 系数先摆在这一处，将来要调只改这里）。**绝不能把 base64 当正文数**：一张 2MB
// 截图约 2.7M 个 base64 字符，按文本数出来是几十万 token，真实成本一千六上下——
// 这正是验收里「带附件不爆量」那条要防的。
func imageTokens(img *protocol.Image) int {
	if img.Data == "" {
		return 0
	}
	decoded := len(img.Data) * 3 / 4
	if t := decoded / 512; t > 256 {
		return t
	}
	return 256
}

func appendSeg(segs *[]string, s string) {
	if trimmed := strings.TrimSpace(s); trimmed != "" {
		*segs = append(*segs, trimmed)
	}
}

// appendExtras 把块外字段按 JSON 原样数进去。序列化失败就跳过——估算器对任何
// 输入都不该报错到「回不出数」的程度。
func appendExtras(segs *[]string, extras map[string]any) {
	if len(extras) == 0 {
		return
	}
	if raw, err := json.Marshal(extras); err == nil {
		appendSeg(segs, string(raw))
	}
}
