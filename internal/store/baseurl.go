package store

import (
	"strings"

	"github.com/SimonGino/portage/internal/protocol"
)

// BaseURLs 是渠道的「协议 → 出站根地址」映射（口径层 v0.96 ②）：给哪个协议填了
// 地址就是声明了哪个协议，支持协议集从此是推导值（Protocols），不再独立存储。
// 空串 = 未声明该协议。每份地址存的仍是协议子路径之前的前缀，出站拼接规则不变。
//
// 定长三个字段而不是 map：协议就三个且 v0.17 已封第四个（Gemini 走 OpenAI 兼容
// 端点），而字段序就是序列化顺序——声明文件的往返闸要求两次导出字节相等，map 给
// 不了这个确定性。字段序按回退序（protocol.fallbackOrder）排，First 的语义靠它。
type BaseURLs struct {
	OpenAI          string `json:"openai,omitempty" yaml:"openai,omitempty"`
	OpenAIResponses string `json:"openai_responses,omitempty" yaml:"openai_responses,omitempty"`
	Anthropic       string `json:"anthropic,omitempty" yaml:"anthropic,omitempty"`
}

// baseURLOrder 是派生协议集与 First 的固定顺序，与 protocol.Set.Choose 的回退序
// 同一条秩序（口径层 v0.96：fetch-models 按它取第一个已填协议的地址）。
var baseURLOrder = []protocol.Protocol{protocol.OpenAI, protocol.OpenAIResponses, protocol.Anthropic}

// slot 把协议名落到字段上。认不得的协议回 nil，调用方各自决定拒还是忽略。
func (b *BaseURLs) slot(p protocol.Protocol) *string {
	switch protocol.Normalize(p) {
	case protocol.OpenAI:
		return &b.OpenAI
	case protocol.OpenAIResponses:
		return &b.OpenAIResponses
	case protocol.Anthropic:
		return &b.Anthropic
	}
	return nil
}

// Get 取一个协议的出站根地址，未声明为空串。
func (b BaseURLs) Get(p protocol.Protocol) string {
	if s := b.slot(p); s != nil {
		return *s
	}
	return ""
}

// Set 给一个协议填地址。认不得的协议名静默丢弃——写侧都先过了各自的校验。
func (b *BaseURLs) Set(p protocol.Protocol, url string) {
	if s := b.slot(p); s != nil {
		*s = url
	}
}

// Protocols 是推导出的支持协议集：哪些协议填了地址。顺序固定为回退序。
func (b BaseURLs) Protocols() protocol.Set {
	var out protocol.Set
	for _, p := range baseURLOrder {
		if b.Get(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// First 按回退序取第一个已填协议及它的地址；一个都没填时协议为空串。
func (b BaseURLs) First() (protocol.Protocol, string) {
	for _, p := range baseURLOrder {
		if url := b.Get(p); url != "" {
			return p, url
		}
	}
	return "", ""
}

// trimmed 去掉每份地址两端的空白，返回新值。
func (b BaseURLs) trimmed() BaseURLs {
	return BaseURLs{
		OpenAI:          strings.TrimSpace(b.OpenAI),
		OpenAIResponses: strings.TrimSpace(b.OpenAIResponses),
		Anthropic:       strings.TrimSpace(b.Anthropic),
	}
}
