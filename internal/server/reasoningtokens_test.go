package server_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 思考 token 落流水（口径层 v0.66）。
//
// 三档必须落成三种不同的库值，这是本条口径的全部内容——「已发生的成本不得静默
// 吞没」在流水这一侧的兑现方式，就是让「没思考」与「不知道」在库里分得开。
func TestReasoningTokensLandInCallLogs(t *testing.T) {
	const usage = `"usage":{"prompt_tokens":10,"completion_tokens":100,"total_tokens":110%s}`
	for _, c := range []struct {
		name    string
		details string
		want    *int64 // nil = 期望 NULL
	}{
		{"上游报了数", `,"completion_tokens_details":{"reasoning_tokens":37}`, ptr(int64(37))},
		{"上游报了 0（这次没思考）", `,"completion_tokens_details":{"reasoning_tokens":0}`, ptr(int64(0))},
		{"上游没报（不知道）", ``, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			gw, up := newOpenAIGateway(t, "gw-cc", "openai", "qwen3-max-2025-09-23")
			body := `{"id":"chatcmpl-1","object":"chat.completion","model":"qwen3-max-2025-09-23",` +
				`"choices":[{"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],` +
				fmt.Sprintf(usage, c.details) + `}`
			up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, body)

			resp := gw.Post(t, "/v1/chat/completions", ccRequest, nil)
			gatewaytest.ReadBody(t, resp)

			row := gw.LastCallRow(t)
			if c.want == nil {
				if row.ReasoningTokens.Valid {
					t.Errorf("reasoning_tokens = %d, 上游没报这一格时该是 NULL——"+
						"落 0 等于替上游说「这次没思考」", row.ReasoningTokens.Int64)
				}
				return
			}
			if !row.ReasoningTokens.Valid {
				t.Fatal("reasoning_tokens 是 NULL，上游明明报了")
			}
			if row.ReasoningTokens.Int64 != *c.want {
				t.Errorf("reasoning_tokens = %d, 期望 %d", row.ReasoningTokens.Int64, *c.want)
			}
			// 明细不是加数：output_tokens 仍是上游报的 completion_tokens 原值。
			if row.OutputTokens.Int64 != 100 {
				t.Errorf("output_tokens = %d, 期望 100（思考是它的明细，不从里面减）",
					row.OutputTokens.Int64)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
