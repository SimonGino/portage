package openaicc_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

// TestDecodeFollowsFirstChoiceOnly（#133）：上游回了多个 choice 时，转换路径只跟
// **第一个出现的 index**，其余 index 的 delta / message / finish_reason 一律不进
// canonical 流。缺 index 按 0；字符串 index 能解析就认，解析不了按缺失。
// 构造样本，见 testdata/fixtures/README.md。
func TestDecodeFollowsFirstChoiceOnly(t *testing.T) {
	for _, tc := range []struct {
		fixture  string
		stream   bool
		text     string
		thinking string
		tools    []toolCall
		stop     string
		input    int
	}{
		{
			fixture:  "cc-stream-multi-choice",
			stream:   true,
			text:     "甲",
			thinking: "先想",
			tools:    []toolCall{{id: "call_keep", name: "get_weather", args: `{"city":"北京"}`}},
			stop:     "tool_calls",
			input:    120,
		},
		{
			fixture: "cc-stream-multi-choice-first-index1",
			stream:  true,
			text:    "Hello",
			stop:    "stop",
			input:   30,
		},
		{
			fixture: "cc-stream-choice-index-string",
			stream:  true,
			text:    "Hello, world",
			stop:    "stop",
			input:   30,
		},
		{
			fixture: "cc-multi-choice",
			tools:   []toolCall{{id: "call_keep", name: "get_time", args: "{}"}},
			stop:    "tool_calls",
			input:   30,
		},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			raw := loadFixture(t, tc.fixture, "response.raw")
			var events []protocol.Event
			if tc.stream {
				events = decodeStream(t, raw)
			} else {
				events = decodeFull(t, raw)
			}

			var text, thinking strings.Builder
			for _, ev := range events {
				switch ev.Type {
				case protocol.EvTextDelta:
					text.WriteString(ev.Text)
				case protocol.EvThinkingDelta:
					thinking.WriteString(ev.Text)
				}
			}
			if text.String() != tc.text {
				t.Errorf("正文 = %q，期望 %q", text.String(), tc.text)
			}
			if thinking.String() != tc.thinking {
				t.Errorf("推理 = %q，期望 %q", thinking.String(), tc.thinking)
			}
			if got := gatherToolCalls(t, events); !slices.Equal(got, tc.tools) {
				t.Errorf("工具调用 = %+v，期望 %+v", got, tc.tools)
			}
			if got := doneReason(events); got != tc.stop {
				t.Errorf("StopReason = %q，期望 %q", got, tc.stop)
			}
			// 只带 usage、choices 为空的收尾帧照旧处理。
			if u := lastUsage(events); u == nil || u.InputTokens != tc.input {
				t.Errorf("usage = %+v，期望 InputTokens=%d", u, tc.input)
			}
		})
	}
}

// TestTapToleratesStringChoiceIndex（#133）：透传路径的 Tap 不看 choice index，字符串
// index 不能让整帧解析失败、把 usage 嗅丢。
func TestTapToleratesStringChoiceIndex(t *testing.T) {
	tap := openaicc.NewTap(true)
	raw := loadFixture(t, "cc-stream-choice-index-string", "response.raw")
	if _, err := tap.Write(raw); err != nil {
		t.Fatal(err)
	}
	if sum := tap.Summary(); sum.InputTokens != 30 || sum.OutputTokens != 6 {
		t.Errorf("Tap usage = %d/%d，期望 30/6", sum.InputTokens, sum.OutputTokens)
	}
}
