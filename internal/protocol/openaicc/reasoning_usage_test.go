package openaicc

import (
	"encoding/json"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 编码侧：有数才写 completion_tokens_details。
//
// 与同一个函数里 prompt_tokens_details.cached_tokens「恒写出来哪怕是 0」相反——
// 那边 0 是真的「一次都没命中」，这边 0 会被读成「这次没思考」，而 canonical 侧
// 零值即「上游没报」（没开思考的响应连 details 容器都不发）。宁可不说，不能瞎说。
func TestUsageBodyOmitsReasoningWhenAbsent(t *testing.T) {
	for _, c := range []struct {
		name string
		u    protocol.Usage
		want any // nil = 整个 details 不该出现
	}{
		{"有数就写", protocol.Usage{InputTokens: 10, OutputTokens: 100, ReasoningTokens: 37}, float64(37)},
		{"没数就不写", protocol.Usage{InputTokens: 10, OutputTokens: 100}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(usageBody(c.u))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			d, ok := got["completion_tokens_details"].(map[string]any)
			if c.want == nil {
				if ok {
					t.Errorf("不该出现 completion_tokens_details，实得 %v", d)
				}
				return
			}
			if !ok {
				t.Fatalf("缺 completion_tokens_details，实得 %v", got)
			}
			if d["reasoning_tokens"] != c.want {
				t.Errorf("reasoning_tokens = %v, 期望 %v", d["reasoning_tokens"], c.want)
			}
		})
	}
}
