package anthropic

import (
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// feed 逐字节喂给 Tap。真实分块边界由 TCP 决定，逐字节是最恶劣的一种切法。
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

// 一次文本回复的完整流。usage 被刻意拆在两处：input/cache 只在 message_start 里
// 报一次，output_tokens 在 message_delta 里刷新——整体赋值的实现会把前者清掉。
const textStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"cache_creation_input_tokens":300,"cache_read_input_tokens":1024,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`

// 并行 tool_use：两个 content block 的 input_json_delta 交错。它们同样带 delta
// 字段但没有 stop_reason，不能把 message_delta 定下来的 tool_use 冲掉。
const parallelToolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_02","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"usage":{"input_tokens":88,"output_tokens":2}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"get_time","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"北京\"}"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":137}}

event: message_stop
data: {"type":"message_stop"}

`

func TestTapStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want protocol.Summary
	}{
		{"纯文本", textStream, protocol.Summary{
			Model: "claude-sonnet-4-5-20250929", InputTokens: 12, OutputTokens: 42,
			CacheReadTokens: 1024, CacheWriteTokens: 300, StopReason: "end_turn"}},
		{"并行 tool_use", parallelToolStream, protocol.Summary{
			Model: "claude-sonnet-4-5-20250929", InputTokens: 88, OutputTokens: 137,
			StopReason: "tool_use"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := feed(t, NewTap(true), tc.raw); got != tc.want {
				t.Errorf("Summary = %+v\n期望 = %+v", got, tc.want)
			}
		})
	}
}

func TestTapNonStream(t *testing.T) {
	const body = `{"id":"msg_01","type":"message","role":"assistant",` +
		`"model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"你好"}],` +
		`"stop_reason":"end_turn","stop_sequence":null,` +
		`"usage":{"input_tokens":12,"cache_creation_input_tokens":300,` +
		`"cache_read_input_tokens":1024,"output_tokens":42}}`

	want := protocol.Summary{
		Model: "claude-sonnet-4-5-20250929", InputTokens: 12, OutputTokens: 42,
		CacheReadTokens: 1024, CacheWriteTokens: 300, StopReason: "end_turn"}
	if got := feed(t, NewTap(false), body); got != want {
		t.Errorf("Summary = %+v\n期望 = %+v", got, want)
	}
}

// 上游不回 usage 是合法的（比如某些兼容端点），Tap 得给零值而不是把这次调用搞砸。
func TestTapWithoutUsageDegradesToZero(t *testing.T) {
	const raw = `event: message_start
data: {"type":"message_start","message":{"id":"msg_03","model":"claude-haiku-4-5","content":[]}}

event: message_stop
data: {"type":"message_stop"}

`
	got := feed(t, NewTap(true), raw)
	want := protocol.Summary{Model: "claude-haiku-4-5"}
	if got != want {
		t.Errorf("Summary = %+v\n期望 = %+v（缺 usage 应为零值）", got, want)
	}
}

// 流被从中间切断：解析到哪算哪，不能 panic、不能把已解出的字段吐掉。
func TestTapTruncatedStreamKeepsWhatItGot(t *testing.T) {
	cut := strings.Index(textStream, "event: content_block_stop")
	if cut < 0 {
		t.Fatal("样本被改动了，找不到截断点")
	}
	// 再往后切几个字节，让最后一帧本身也是残的。
	got := feed(t, NewTap(true), textStream[:cut+12])

	if got.Model != "claude-sonnet-4-5-20250929" || got.InputTokens != 12 {
		t.Errorf("截断前已解出的字段被丢了: %+v", got)
	}
	if got.StopReason != "" || got.OutputTokens != 1 {
		t.Errorf("截断后的字段不该凭空出现: %+v", got)
	}
	if got.Degraded {
		t.Error("截断是上游行为，不是 Tap 放弃解析，不该置 Degraded")
	}
}

// 畸形 JSON 混在合法帧之间：跳过它，别停下。
func TestTapSkipsMalformedFrames(t *testing.T) {
	raw := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"m\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"data: {这不是 JSON\n\n" +
		"data: [DONE]\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n"

	got := feed(t, NewTap(true), raw)
	want := protocol.Summary{Model: "m", InputTokens: 5, OutputTokens: 7, StopReason: "end_turn"}
	if got != want {
		t.Errorf("Summary = %+v\n期望 = %+v", got, want)
	}
}

// 超大单帧：放弃它并置 Degraded，但流里后面的 usage 照样要解出来——Anthropic 的
// output_tokens 就在最后一帧，一超限就整条罢工等于白记。
func TestTapOversizedFrameDegradesButKeepsParsing(t *testing.T) {
	huge := strings.Repeat("A", protocol.BufferLimit+1024)
	raw := `event: message_start
data: {"type":"message_start","message":{"model":"m","usage":{"input_tokens":5}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"` + huge + `"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":9}}

`
	tap := NewTap(true)
	// 这里按块喂（逐字节喂 4 MB 太慢），块大小取 8 KB 贴近真实读长度。
	for i := 0; i < len(raw); i += 8192 {
		end := min(i+8192, len(raw))
		if _, err := tap.Write([]byte(raw[i:end])); err != nil {
			t.Fatalf("Tap.Write 报错了: %v", err)
		}
	}
	got := tap.Summary()

	if !got.Degraded {
		t.Error("单帧超限却没有置 Degraded，日志会假装自己是准的")
	}
	want := protocol.Summary{Model: "m", InputTokens: 5, OutputTokens: 9, StopReason: "max_tokens", Degraded: true}
	if got != want {
		t.Errorf("Summary = %+v\n期望 = %+v（超限帧之后的帧仍须解析）", got, want)
	}
}
