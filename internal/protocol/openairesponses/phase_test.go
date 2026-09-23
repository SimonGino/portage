package openairesponses

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// #120（口径层 v1.27 ③）：message item 的 phase 在收口时推断。codex-rs 把缺失当
// FinalAnswer，开场白不标 commentary 就会被当成最终答案。

var (
	evStart   = protocol.Event{Type: protocol.EvMessageStart, ID: "x", Model: "m"}
	evText    = func(s string) protocol.Event { return protocol.Event{Type: protocol.EvTextDelta, Text: s} }
	evThink   = protocol.Event{Type: protocol.EvThinkingDelta, Text: "想", Channel: protocol.ThinkingBody}
	evToolEnd = []protocol.Event{
		{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_a", ToolName: "read"},
		{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{}`},
		{Type: protocol.EvToolCallEnd, Index: 0},
	}
	evDone = func(stop string, truncated bool) protocol.Event {
		return protocol.Event{Type: protocol.EvDone, StopReason: stop, Truncated: truncated}
	}
)

// messagePhases 收流里每个 message item 的 phase：added 时的与 done 时的分开收，
// 再核终帧 response.output 里的同一 item 是否一致。
func messagePhases(t *testing.T, frames []frame) []any {
	t.Helper()
	var done []any
	for _, f := range frames {
		item, _ := f.data["item"].(map[string]any)
		if item == nil || item["type"] != "message" {
			continue
		}
		switch f.event {
		case "response.output_item.added":
			if p, ok := item["phase"]; ok {
				t.Errorf("output_item.added 带了 phase=%v：此刻还推断不出来", p)
			}
		case "response.output_item.done":
			done = append(done, item["phase"])
		}
	}
	var final []any
	for _, it := range frames[len(frames)-1].data["response"].(map[string]any)["output"].([]any) {
		if m := it.(map[string]any); m["type"] == "message" {
			final = append(final, m["phase"])
		}
	}
	if !slices.Equal(done, final) {
		t.Errorf("终帧 output 的 phase %v 与 output_item.done 的 %v 不一致", final, done)
	}
	return done
}

func TestEncodeInfersMessagePhase(t *testing.T) {
	cases := []struct {
		name   string
		events []protocol.Event
		want   []any
	}{
		{"纯文本答案", []protocol.Event{evStart, evText("好了"), evDone("stop", false)}, []any{"final_answer"}},
		{"开场白 + 工具调用", append(append([]protocol.Event{evStart, evText("我先看看")}, evToolEnd...), evDone("tool_calls", false)), []any{"commentary"}},
		{"多条 message", append(append([]protocol.Event{evStart, evText("先想想"), evThink, evText("中间")}, evToolEnd...), evText("结论"), evDone("stop", false)), []any{"commentary", "commentary", "final_answer"}},
		{"截断", []protocol.Event{evStart, evText("半句"), evDone("length", false)}, []any{nil}},
		{"断流", []protocol.Event{evStart, evText("半句"), evDone("stop", true)}, []any{nil}},
		{"内容过滤", []protocol.Event{evStart, evText("半句"), evDone("content_filter", false)}, []any{nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := messagePhases(t, encodeStream(t, NewCodec(), tc.events...))
			if !slices.Equal(got, tc.want) {
				t.Errorf("phase = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// 非流式复用同一台状态机，phase 同源。
func TestEncodeFullBodyInfersMessagePhase(t *testing.T) {
	body, err := NewCodec().EncodeFullBody(append(append([]protocol.Event{evStart, evText("我先看看")}, evToolEnd...), evText("结论"), evDone("stop", false)))
	if err != nil {
		t.Fatal(err)
	}
	var full struct{ Output []map[string]any }
	if err := json.Unmarshal(body, &full); err != nil {
		t.Fatal(err)
	}
	var got []any
	for _, it := range full.Output {
		if it["type"] == "message" {
			got = append(got, it["phase"])
		}
	}
	if want := []any{"commentary", "final_answer"}; !slices.Equal(got, want) {
		t.Errorf("非流式 phase = %v，期望 %v", got, want)
	}
}
