package openaicc

import (
	"testing"

	"github.com/SimonGino/ai-gateway/internal/protocol"
)

func feed(t *testing.T, tap protocol.Tap, raw string) protocol.Summary {
	t.Helper()
	for i := 0; i < len(raw); i++ {
		n, err := tap.Write([]byte(raw[i : i+1]))
		if n != 1 || err != nil {
			t.Fatalf("Tap.Write 必须恒定返回 (len(p), nil)，实得 (%d, %v)", n, err)
		}
	}
	return tap.Summary()
}

// 开了 stream_options.include_usage 的流：usage 只在最后一个 choices 为空的 chunk 里。
// finish_reason 则在它之前那个 chunk 上——两者不在同一帧，任何「只看最后一帧」的
// 实现都会丢一半。
const textStream = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1770000000,"model":"qwen3-max-2025-09-23","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1770000000,"model":"qwen3-max-2025-09-23","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1770000000,"model":"qwen3-max-2025-09-23","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1770000000,"model":"qwen3-max-2025-09-23","choices":[],"usage":{"prompt_tokens":31,"completion_tokens":12,"total_tokens":43,"prompt_tokens_details":{"cached_tokens":16}}}

data: [DONE]

`

// 并行 tool_calls：index 交错、参数跨 chunk。finish_reason 为 tool_calls。
const parallelToolStream = `data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"北京\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl-2","object":"chat.completion.chunk","model":"gpt-5","choices":[],"usage":{"prompt_tokens":120,"completion_tokens":48,"total_tokens":168}}

data: [DONE]

`

func TestTapStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want protocol.Summary
	}{
		{"纯文本", textStream, protocol.Summary{
			Model: "qwen3-max-2025-09-23", InputTokens: 31, OutputTokens: 12,
			CacheReadTokens: 16, StopReason: "stop"}},
		{"并行 tool_calls", parallelToolStream, protocol.Summary{
			Model: "gpt-5", InputTokens: 120, OutputTokens: 48, StopReason: "tool_calls"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := feed(t, NewTap(true), tc.raw); got != tc.want {
				t.Errorf("Summary = %+v\n期望 = %+v", got, tc.want)
			}
		})
	}
}

// 客户端没开 include_usage 时上游根本不发 usage，这是常态而非故障。
func TestTapStreamWithoutIncludeUsage(t *testing.T) {
	const raw = `data: {"id":"chatcmpl-3","model":"qwen3-max","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}

data: {"id":"chatcmpl-3","model":"qwen3-max","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}

data: [DONE]

`
	got := feed(t, NewTap(true), raw)
	want := protocol.Summary{Model: "qwen3-max", StopReason: "length"}
	if got != want {
		t.Errorf("Summary = %+v\n期望 = %+v（缺 usage 应为零值且不降级）", got, want)
	}
}

func TestTapNonStream(t *testing.T) {
	const body = `{"id":"chatcmpl-9","object":"chat.completion","created":1770000000,` +
		`"model":"qwen3-max-2025-09-23","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":31,"completion_tokens":12,"total_tokens":43,` +
		`"prompt_tokens_details":{"cached_tokens":16}}}`

	want := protocol.Summary{
		Model: "qwen3-max-2025-09-23", InputTokens: 31, OutputTokens: 12,
		CacheReadTokens: 16, StopReason: "stop"}
	if got := feed(t, NewTap(false), body); got != want {
		t.Errorf("Summary = %+v\n期望 = %+v", got, want)
	}
}

// [DONE] 不是 JSON，但它是流的正常收尾，不该被当成解析失败而降级。
func TestTapDoneSentinelIsNotADegradation(t *testing.T) {
	got := feed(t, NewTap(true), "data: [DONE]\n\n")
	if got.Degraded {
		t.Error("[DONE] 被当成了解析失败")
	}
	if (got != protocol.Summary{}) {
		t.Errorf("Summary 应为零值，实得 %+v", got)
	}
}
