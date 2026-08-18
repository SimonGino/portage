package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

// 同一条口径在 Anthropic 渠道上（#5）。单开一个用例而不是并进上面那张表，是因为
// 它钉的两件事 CC 那侧没有：
//
//   - **键名不同形**：Anthropic 是 output_tokens_details.**thinking_tokens**，容器名
//     跟 Responses 一样、字段名跟 CC 一样，抄任何一边都对不上。
//   - **流式的落点只有 message_delta**：message_start 那帧连这个容器都没有。Tap 对
//     其余 usage 项是「只覆盖非零值」，思考这一格若跟着走，`thinking_tokens:0` 那一档
//     就被吃掉、落库退回 NULL——三档又变回两档。
func TestAnthropicReasoningTokensLandInCallLogs(t *testing.T) {
	for _, c := range []struct {
		name    string
		details string
		want    *int64 // nil = 期望 NULL
	}{
		{"上游报了数", `,"output_tokens_details":{"thinking_tokens":249}`, ptr(int64(249))},
		{"上游报了 0（这次没思考）", `,"output_tokens_details":{"thinking_tokens":0}`, ptr(int64(0))},
		{"上游根本不报（老模型 / 中转裁剪）", ``, nil},
	} {
		for _, stream := range []bool{false, true} {
			mode := "非流式"
			if stream {
				mode = "流式"
			}
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				gw, up := newAnthropicGateway(t)
				req := anthropicRequest
				if stream {
					req = strings.Replace(req, `"stream":false`, `"stream":true`, 1)
					up.RespondWith(http.StatusOK,
						map[string]string{"Content-Type": "text/event-stream"},
						anthropicThinkingStream(c.details))
				} else {
					up.RespondWith(http.StatusOK,
						map[string]string{"Content-Type": "application/json"},
						anthropicThinkingBody(c.details))
				}

				gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", req, nil))

				row := gw.LastCallRow(t)
				if c.want == nil {
					if row.ReasoningTokens.Valid {
						t.Errorf("reasoning_tokens = %d, 上游没报这一格时该是 NULL",
							row.ReasoningTokens.Int64)
					}
					return
				}
				if !row.ReasoningTokens.Valid {
					t.Fatal("reasoning_tokens 是 NULL，上游明明报了——这一列在 A 渠道上不再恒 NULL")
				}
				if row.ReasoningTokens.Int64 != *c.want {
					t.Errorf("reasoning_tokens = %d, 期望 %d", row.ReasoningTokens.Int64, *c.want)
				}
				// 明细不是加数：output_tokens 仍是上游报的原值。
				if row.OutputTokens.Int64 != 875 {
					t.Errorf("output_tokens = %d, 期望 875（思考是它的明细，不从里面减）",
						row.OutputTokens.Int64)
				}
			})
		}
	}
}

// 照 testdata/golden/anthropic-stream-thinking 的形态：思考数只在 message_delta，
// message_start 那帧没有 output_tokens_details。
func anthropicThinkingStream(details string) string {
	return `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_r","usage":{"input_tokens":564,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":3}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":564,"output_tokens":875` + details + `}}

event: message_stop
data: {"type":"message_stop"}

`
}

func anthropicThinkingBody(details string) string {
	return `{"id":"msg_r","type":"message","role":"assistant","model":"claude-sonnet-5",` +
		`"content":[{"type":"text","text":"你好"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":564,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,` +
		`"output_tokens":875` + details + `}}`
}

// 转换路径的**出口**那半边：Anthropic 上游报的思考数要一路到客户端手上的 usage 里。
//
// 这半边与上面那半边不是一回事——上面钉的是流水（Tap 旁路），这里钉的是转换出口
// （decode → canonical → encode）。丢了的话 Responses 出口写出来的是 `reasoning_tokens: 0`，
// 那比 NULL 更糟：NULL 是「不知道」，0 是一个确凿的假数。
func TestAnthropicThinkingTokensReachConvertedOutlets(t *testing.T) {
	// 上游是 Anthropic 且开了思考：310 在顶层 usage 的 output_tokens_details 里。
	const upstreamBody = `{"id":"msg_1","model":"claude-sonnet-5","type":"message",` +
		`"role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":564,"output_tokens":1110,` +
		`"output_tokens_details":{"thinking_tokens":310}}}`

	t.Run("A→CC 出口", func(t *testing.T) {
		gw, up := newCC2AGateway(t)
		up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

		resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", false), nil)
		raw := gatewaytest.ReadBody(t, resp)

		var out struct {
			Usage struct {
				CompletionTokens        int `json:"completion_tokens"`
				CompletionTokensDetails *struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("回给客户端的不是 CC 响应: %v\n%s", err, raw)
		}
		if out.Usage.CompletionTokensDetails == nil {
			t.Fatalf("completion_tokens_details 不见了，上游报的思考数在出口丢了:\n%s", raw)
		}
		if got := out.Usage.CompletionTokensDetails.ReasoningTokens; got != 310 {
			t.Errorf("reasoning_tokens = %d, 期望 310（上游报的数）", got)
		}
		// 明细不是加数：completion_tokens 仍是上游报的 output_tokens 原值。
		if out.Usage.CompletionTokens != 1110 {
			t.Errorf("completion_tokens = %d, 期望 1110", out.Usage.CompletionTokens)
		}
	})

	t.Run("A→R 出口", func(t *testing.T) {
		gw, up := newR2AGateway(t)
		up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

		resp := gw.Post(t, "/v1/responses",
			strings.Replace(convertResponsesRequest, `"stream":true`, `"stream":false`, 1), nil)
		raw := gatewaytest.ReadBody(t, resp)

		var out struct {
			Usage struct {
				OutputTokens        int `json:"output_tokens"`
				OutputTokensDetails struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("回给客户端的不是 Responses 响应: %v\n%s", err, raw)
		}
		if got := out.Usage.OutputTokensDetails.ReasoningTokens; got != 310 {
			t.Errorf("reasoning_tokens = %d, 期望 310——这一格恒写，丢了就是个确凿的假数 0", got)
		}
		if out.Usage.OutputTokens != 1110 {
			t.Errorf("output_tokens = %d, 期望 1110", out.Usage.OutputTokens)
		}
	})

	// 流式那半边单独钉：上游只在 message_delta 那一帧带思考数，转换出口的 usage
	// 帧却是自己另攒的——非流式过了不代表这条过。
	t.Run("A→CC 出口/流式", func(t *testing.T) {
		gw, up := newCC2AGateway(t)
		streamUpstream(t, up, anthropicThinkingStream(`,"output_tokens_details":{"thinking_tokens":310}`))()

		resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", true), nil)
		body := gatewaytest.ReadBody(t, resp)

		if !strings.Contains(body, `"reasoning_tokens":310`) {
			t.Errorf("usage 帧里没有上游报的思考数:\n%s", body)
		}
	})

	t.Run("A→R 出口/流式", func(t *testing.T) {
		gw, up := newR2AGateway(t)
		streamUpstream(t, up, anthropicThinkingStream(`,"output_tokens_details":{"thinking_tokens":310}`))()

		resp := gw.Post(t, "/v1/responses", convertResponsesRequest, nil)
		body := gatewaytest.ReadBody(t, resp)

		if !strings.Contains(body, `"reasoning_tokens":310`) {
			t.Errorf("这一格恒写，丢了就是个确凿的假数 0:\n%s", body)
		}
	})
}

func ptr[T any](v T) *T { return &v }
