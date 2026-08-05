// Package openaicc holds the OpenAI Chat Completions protocol adapters: the Tap
// (P0, 旁路解析透传流) and later the Codec (P1, 协议转换).
package openaicc

import (
	"encoding/json"

	"github.com/SimonGino/ai-gateway/internal/protocol"
)

// Tap 从 Chat Completions 响应里提取 usage / model / finish_reason。
//
// 流式与非流式共用一套字段：增量 chunk 与完整响应的顶层结构一致（model、choices、
// usage），差别只在 choices 里是 delta 还是 message，而这两个都不看。
type Tap struct {
	protocol.TapCore
}

func NewTap(stream bool) *Tap {
	t := &Tap{}
	t.TapCore = protocol.NewTapCore(stream, observe, observe)
	return t
}

type chunk struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func observe(sum *protocol.Summary, data []byte) {
	var c chunk
	// data: [DONE] 是流的正常收尾，不是 JSON——解不动就跳过，不算降级。
	if json.Unmarshal(data, &c) != nil {
		return
	}
	if c.Model != "" {
		sum.Model = c.Model
	}
	for _, ch := range c.Choices {
		if ch.FinishReason != "" {
			sum.StopReason = ch.FinishReason
		}
	}
	if c.Usage == nil {
		// 流式下 usage 只在带 stream_options.include_usage 时才出现，且在最后
		// 一个 choices 为空的 chunk 里。客户端没开就是没有——降级为零值，不报错。
		return
	}
	if c.Usage.PromptTokens != 0 {
		sum.InputTokens = c.Usage.PromptTokens
	}
	if c.Usage.CompletionTokens != 0 {
		sum.OutputTokens = c.Usage.CompletionTokens
	}
	// CC 只有缓存命中（读）的概念，没有缓存写入，CacheWriteTokens 保持零值。
	if d := c.Usage.PromptTokensDetails; d != nil && d.CachedTokens != 0 {
		sum.CacheReadTokens = d.CachedTokens
	}
}
