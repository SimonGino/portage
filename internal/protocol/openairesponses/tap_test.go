package openairesponses

import (
	"fmt"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

func feed(t *testing.T, tap protocol.Tap, raw string) protocol.Summary {
	t.Helper()
	for i := range len(raw) {
		n, err := tap.Write([]byte(raw[i : i+1]))
		if n != 1 || err != nil {
			t.Fatalf("Tap.Write 必须恒定返回 (len(p), nil)，实得 (%d, %v)", n, err)
		}
	}
	return tap.Summary()
}

// Responses 的每个生命周期事件都裹一层完整 response 对象，中间态 status 是
// in_progress——它不能被当成终止原因记下来。
const functionCallStream = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","model":"gpt-5","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","name":"get_weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"{\"city\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_1","output_index":0,"delta":"\"北京\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"fc_1","type":"function_call","name":"get_weather","arguments":"{\"city\":\"北京\"}"}}

event: response.completed
data: {"type":"response.completed","sequence_number":5,"response":{"id":"resp_1","object":"response","model":"gpt-5-2025-08-07","status":"completed","output":[{"id":"fc_1","type":"function_call"}],"usage":{"input_tokens":210,"output_tokens":33,"total_tokens":243,"input_tokens_details":{"cached_tokens":128}}}}

`

func TestTapStreamFunctionCall(t *testing.T) {
	got := feed(t, NewTap(true), functionCallStream)
	// 模型名以最后一个事件为准：上游把 gpt-5 解析成了带日期的具体版本。
	want := protocol.Summary{
		Model: "gpt-5-2025-08-07", InputTokens: 210, OutputTokens: 33,
		CacheReadTokens: 128, StopReason: "completed"}
	if got != want {
		t.Errorf("Summary = %+v\n期望 = %+v", got, want)
	}
}

// 截断时 status 是 incomplete，真正有用的原因在 incomplete_details.reason 里。
func TestTapStreamIncompletePrefersTheSpecificReason(t *testing.T) {
	const raw = `event: response.incomplete
data: {"type":"response.incomplete","sequence_number":9,"response":{"id":"resp_2","model":"gpt-5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":4096}}}

`
	got := feed(t, NewTap(true), raw)
	want := protocol.Summary{
		Model: "gpt-5", InputTokens: 10, OutputTokens: 4096, StopReason: "max_output_tokens"}
	if got != want {
		t.Errorf("Summary = %+v\n期望 = %+v", got, want)
	}
}

// 只走到 in_progress 就断了：status 是中间态，不该被记成终止原因。
func TestTapStreamInProgressIsNotAStopReason(t *testing.T) {
	const raw = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_3","model":"gpt-5","status":"in_progress","output":[]}}

`
	got := feed(t, NewTap(true), raw)
	if got.StopReason != "" {
		t.Errorf("stop_reason = %q，in_progress 是中间态不该记", got.StopReason)
	}
	if got.Model != "gpt-5" {
		t.Errorf("model = %q", got.Model)
	}
}

func TestTapNonStream(t *testing.T) {
	// 非流式下 response 对象在顶层，没有外面那层 {"response":{…}} 包装。
	const body = `{"id":"resp_9","object":"response","created_at":1770000000,` +
		`"model":"gpt-5-2025-08-07","status":"completed",` +
		`"output":[{"id":"msg_1","type":"message","role":"assistant"}],` +
		`"usage":{"input_tokens":210,"output_tokens":33,"total_tokens":243,` +
		`"input_tokens_details":{"cached_tokens":128}}}`

	want := protocol.Summary{
		Model: "gpt-5-2025-08-07", InputTokens: 210, OutputTokens: 33,
		CacheReadTokens: 128, StopReason: "completed"}
	if got := feed(t, NewTap(false), body); got != want {
		t.Errorf("Summary = %+v\n期望 = %+v", got, want)
	}
}

// Anthropic 兼容端点会在 Responses 形状上多带 cache_creation_input_tokens；
// 官方 OpenAI 不发，缺失即零值。
func TestTapReadsCacheCreationWhenPresent(t *testing.T) {
	const body = `{"id":"resp_a","model":"claude-sonnet-4-5","status":"completed",` +
		`"usage":{"input_tokens":100,"output_tokens":20,` +
		`"input_tokens_details":{"cached_tokens":64},"cache_creation_input_tokens":36}}`

	got := feed(t, NewTap(false), body)
	if got.CacheWriteTokens != 36 || got.CacheReadTokens != 64 {
		t.Errorf("cache read/write = %d/%d, 期望 64/36", got.CacheReadTokens, got.CacheWriteTokens)
	}
}

// 思考 token（口径层 v0.66）。Responses 侧的键是
// output_tokens_details.reasoning_tokens——与 CC 的
// completion_tokens_details.reasoning_tokens 同语义、不同父键。
// 「报了 0」与「没报」要分得开，理由见 protocol.Summary.HasReasoningTokens。
func TestTapReasoningTokens(t *testing.T) {
	const frame = `data: {"type":"response.completed","response":{"model":"m",` +
		`"status":"completed","usage":{"input_tokens":10,"output_tokens":100%s}}}

`
	for _, c := range []struct {
		name    string
		details string
		want    protocol.Summary
	}{
		{"报了数", `,"output_tokens_details":{"reasoning_tokens":37}`,
			protocol.Summary{Model: "m", InputTokens: 10, OutputTokens: 100,
				ReasoningTokens: 37, HasReasoningTokens: true, StopReason: "completed"}},
		{"报了0", `,"output_tokens_details":{"reasoning_tokens":0}`,
			protocol.Summary{Model: "m", InputTokens: 10, OutputTokens: 100,
				HasReasoningTokens: true, StopReason: "completed"}},
		{"没报", ``,
			protocol.Summary{Model: "m", InputTokens: 10, OutputTokens: 100,
				StopReason: "completed"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := fmt.Sprintf(frame, c.details)
			if got := feed(t, NewTap(true), raw); got != c.want {
				t.Errorf("Summary = %+v\n期望 = %+v", got, c.want)
			}
		})
	}
}
