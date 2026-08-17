package openairesponses

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 思考 token 要如实写出去（口径层 v0.66，#97）。这一格此前是硬编码的 0——
// canonical 装下这个数之后，那就成了「把上游报的思考成本抹成零」。
//
// 键**恒在**（与 CC 出口「有数才写」相反）：Responses 的 usage 契约里
// output_tokens_details 是必有项，真实转录里非推理轮也带 reasoning_tokens:0。
func TestUsageBodyCarriesReasoningTokens(t *testing.T) {
	for _, c := range []struct {
		name string
		u    protocol.Usage
		want int
	}{
		{"有数就如实写", protocol.Usage{InputTokens: 10, OutputTokens: 100, ReasoningTokens: 37}, 37},
		{"没数落回 0", protocol.Usage{InputTokens: 10, OutputTokens: 100}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, ok := usageBody(c.u)["output_tokens_details"].(map[string]any)
			if !ok {
				t.Fatal("output_tokens_details 必须恒在")
			}
			if d["reasoning_tokens"] != c.want {
				t.Errorf("reasoning_tokens = %v, 期望 %d", d["reasoning_tokens"], c.want)
			}
			// 明细不是加数：total 仍是 input + output。
			if got := usageBody(c.u)["total_tokens"]; got != 110 {
				t.Errorf("total_tokens = %v, 期望 110（思考不另外相加）", got)
			}
		})
	}
}
