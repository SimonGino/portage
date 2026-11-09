package store

import (
	"fmt"
	"strings"

	"github.com/SimonGino/portage/internal/protocol"
)

// PathOverrides 是渠道的「协议 → 消息端点路径覆盖」映射（#175）：给某个协议填了
// 完整子路径，出站拼接就用它替换固定的 /v1/<protocol>。为消息端点不在
// /v1/<protocol> 形状的上游而设（ChatGPT Codex 后端，如 base …/backend-api/codex +
// /responses）：base_url 只填到子路径之前那一段，子路径放在这一侧。
//
// 只覆盖三个消息端点（messages / chat/completions / responses）；共用的 GET
// /v1/models 与 Anthropic 的 count_tokens 不覆盖——它们的路径形状不是问题（#175 是
// 消息端点的票）。空串 = 未覆盖，拼接与既有行为逐字节一致。
//
// 与 BaseURLs 同为定长三字段：字段序就是序列化顺序——声明文件的往返闸要求两次导出
// 字节相等，map 给不了这个确定性。
type PathOverrides struct {
	OpenAI          string `json:"openai,omitempty" yaml:"openai,omitempty"`
	OpenAIResponses string `json:"openai_responses,omitempty" yaml:"openai_responses,omitempty"`
	Anthropic       string `json:"anthropic,omitempty" yaml:"anthropic,omitempty"`
}

// slot 把协议名落到字段上。约定同 BaseURLs.slot：认不得的协议回 nil。
func (o *PathOverrides) slot(p protocol.Protocol) *string {
	switch protocol.Normalize(p) {
	case protocol.OpenAI:
		return &o.OpenAI
	case protocol.OpenAIResponses:
		return &o.OpenAIResponses
	case protocol.Anthropic:
		return &o.Anthropic
	}
	return nil
}

// Get 取一个协议的路径覆盖，未填为空串。
func (o PathOverrides) Get(p protocol.Protocol) string {
	if s := o.slot(p); s != nil {
		return *s
	}
	return ""
}

// Set 给一个协议填路径覆盖。认不得的协议名静默丢弃——写侧都先过了各自的校验。
func (o *PathOverrides) Set(p protocol.Protocol, path string) {
	if s := o.slot(p); s != nil {
		*s = path
	}
}

// Protocols 是推导出的「哪些协议声明了覆盖」：固定顺序同 BaseURLs.Protocols。
func (o PathOverrides) Protocols() []protocol.Protocol {
	var out []protocol.Protocol
	for _, p := range baseURLOrder {
		if o.Get(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// trimmed 去掉每份值两端的空白，返回新值。
func (o PathOverrides) trimmed() PathOverrides {
	return PathOverrides{
		OpenAI:          strings.TrimSpace(o.OpenAI),
		OpenAIResponses: strings.TrimSpace(o.OpenAIResponses),
		Anthropic:       strings.TrimSpace(o.Anthropic),
	}
}

// ValidatePathOverrides 是路径覆盖的写侧闸（writer 与声明文件 selfCheck 共用一份）：
// 每份非空值必须以 `/` 开头，且不带尾随 `/`、不带查询串。
//
// 它是子路径：TrimRight(base, "/") 之后直接进 URL。漏了首斜杠，两段路径直接黏成
// 一段；带尾斜杠，URL 里多出一个 `/`；带查询串，与 buildURL 在路径之后拼上的客户端
// 查询串相撞（两个 `?`，后者赢、前者静默消失）。
func ValidatePathOverrides(o PathOverrides) error {
	for _, f := range []struct {
		name string
		val  string
	}{
		{"openai", o.OpenAI},
		{"openai_responses", o.OpenAIResponses},
		{"anthropic", o.Anthropic},
	} {
		if f.val == "" {
			continue
		}
		switch {
		case !strings.HasPrefix(f.val, "/"):
			return InvalidInput{Reason: fmt.Sprintf("path_override.%s %q 缺少开头的 /", f.name, f.val)}
		case strings.HasSuffix(f.val, "/"):
			return InvalidInput{Reason: fmt.Sprintf("path_override.%s %q 带了结尾的 /", f.name, f.val)}
		case strings.Contains(f.val, "?"):
			return InvalidInput{Reason: fmt.Sprintf("path_override.%s %q 带了查询串", f.name, f.val)}
		}
	}
	return nil
}
