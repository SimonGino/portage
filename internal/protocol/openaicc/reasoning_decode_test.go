package openaicc_test

import (
	"testing"
)

// 解码侧：completion_tokens_details.reasoning_tokens 要进 canonical Usage
// （口径层 v0.66，#97）。它是 OutputTokens 的**明细**，不从里面减掉。
func TestDecodeStreamCarriesReasoningTokens(t *testing.T) {
	const raw = `data: {"model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}

data: {"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":100,"total_tokens":110,"completion_tokens_details":{"reasoning_tokens":37}}}

data: [DONE]

`
	events := decodeStream(t, []byte(raw))
	u := lastUsage(events)
	if u == nil {
		t.Fatal("没解出 usage")
	}
	if u.ReasoningTokens != 37 {
		t.Errorf("ReasoningTokens = %d, 期望 37", u.ReasoningTokens)
	}
	if u.OutputTokens != 100 {
		t.Errorf("OutputTokens = %d, 期望 100（思考是它的明细，不从里面减）", u.OutputTokens)
	}
}
