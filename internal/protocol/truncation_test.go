package protocol_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// #106：上游流在工具参数中途干净 EOF（没有 finish_reason / message_delta），R 出口
// 不许发 response.completed，也不许把半截入参当成品放出。
//
// 两个上游各一条：CC 没有逐条终止符，工具 End 是解码侧在收尾时补的；Anthropic 的
// content_block_stop 没到，调用在编码侧收尾时才冲出。两条路进 R 编码器的形状不同，
// 都要钉。上游流是**构造样本**（手搭，形状照各包 decode 测试里的手抄转录），不是 golden。
func TestResponsesExitTruncatedMidToolArgs(t *testing.T) {
	const ccStream = `data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":""}}]}}]}

data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"rm -"}}]}}]}

`
	const anthropicStream = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"exec","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"rm -"}}

`
	cases := map[string]func() (<-chan protocol.Event, error){
		"CC 上游": func() (<-chan protocol.Event, error) {
			return openaicc.NewCodec().DecodeStream(strings.NewReader(ccStream))
		},
		"Anthropic 上游": func() (<-chan protocol.Event, error) {
			return anthropic.NewCodec().DecodeStream(strings.NewReader(anthropicStream))
		},
	}
	for name, decode := range cases {
		t.Run(name, func(t *testing.T) {
			ch, err := decode()
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := openairesponses.NewCodec().EncodeStream(&buf, ch); err != nil {
				t.Fatal(err)
			}
			var events []string
			var itemStatus any
			for _, line := range strings.Split(buf.String(), "\n") {
				if ev, ok := strings.CutPrefix(line, "event: "); ok {
					events = append(events, ev)
					continue
				}
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok || events[len(events)-1] != "response.output_item.done" {
					continue
				}
				var payload struct{ Item map[string]any }
				if err := json.Unmarshal([]byte(data), &payload); err != nil {
					t.Fatal(err)
				}
				itemStatus = payload.Item["status"]
			}
			if last := events[len(events)-1]; last != "response.incomplete" {
				t.Errorf("断流终帧 = %s，期望 response.incomplete: %v", last, events)
			}
			for _, ev := range events {
				if ev == "response.function_call_arguments.done" {
					t.Errorf("半截入参发了 arguments.done，Codex 会当成品执行: %v", events)
				}
			}
			if itemStatus != "incomplete" {
				t.Errorf("半截调用的 item status = %v，期望 incomplete", itemStatus)
			}
		})
	}
}

// responsesObservation 是一次「上游流 → R 出口」的观测：帧名序列、各工具 item 的终态（按放出
// 次序）、终帧的 incomplete_details.reason。
type responsesObservation struct {
	events   []string
	statuses []any
	reason   any
}

func encodeToResponses(t *testing.T, ch <-chan protocol.Event) responsesObservation {
	t.Helper()
	var buf bytes.Buffer
	if err := openairesponses.NewCodec().EncodeStream(&buf, ch); err != nil {
		t.Fatal(err)
	}
	var r responsesObservation
	for _, line := range strings.Split(buf.String(), "\n") {
		if ev, ok := strings.CutPrefix(line, "event: "); ok {
			r.events = append(r.events, ev)
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var payload struct {
			Item     map[string]any
			Response struct {
				IncompleteDetails map[string]any `json:"incomplete_details"`
			}
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatal(err)
		}
		switch r.events[len(r.events)-1] {
		case "response.output_item.done":
			if payload.Item["type"] == "function_call" {
				r.statuses = append(r.statuses, payload.Item["status"])
			}
		case "response.incomplete", "response.completed":
			r.reason = payload.Response.IncompleteDetails["reason"]
		}
	}
	return r
}

func (r responsesObservation) count(event string) int {
	n := 0
	for _, ev := range r.events {
		if ev == event {
			n++
		}
	}
	return n
}

// #127：工具调用已收尾（A 的 content_block_stop、CC 的收尾冲出），停因却是 length——
// End 不等于入参写完，R 出口不许把它当成品放出。构造样本，形状同上一条。
func TestResponsesExitLengthAfterToolEnd(t *testing.T) {
	anthropicStream := func(stop string) string {
		return `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"先看看"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_a","name":"read","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_b","name":"exec","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"rm -"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"` + stop + `"},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`
	}
	const ccStream = `data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}}]}

data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"exec","arguments":"{\"cmd\":\"rm -"}}]}}]}

data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}

data: [DONE]

`
	cases := map[string]struct {
		decode func() (<-chan protocol.Event, error)
		want   []any
	}{
		// A 逐块有终止符：只压最后一个已收尾的调用（PO 裁决），前一个照常 completed。
		"Anthropic max_tokens": {func() (<-chan protocol.Event, error) {
			return anthropic.NewCodec().DecodeStream(strings.NewReader(anthropicStream("max_tokens")))
		}, []any{"completed", "incomplete"}},
		"Anthropic pause_turn": {func() (<-chan protocol.Event, error) {
			return anthropic.NewCodec().DecodeStream(strings.NewReader(anthropicStream("pause_turn")))
		}, []any{"completed", "incomplete"}},
		"Anthropic model_context_window_exceeded": {func() (<-chan protocol.Event, error) {
			return anthropic.NewCodec().DecodeStream(strings.NewReader(anthropicStream("model_context_window_exceeded")))
		}, []any{"completed", "incomplete"}},
		// CC 没有逐条终止符，看不出截断落在哪一路：并行调用一律 incomplete。
		"CC length": {func() (<-chan protocol.Event, error) {
			return openaicc.NewCodec().DecodeStream(strings.NewReader(ccStream))
		}, []any{"incomplete", "incomplete"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ch, err := tc.decode()
			if err != nil {
				t.Fatal(err)
			}
			r := encodeToResponses(t, ch)
			if last := r.events[len(r.events)-1]; last != "response.incomplete" || r.reason != "max_output_tokens" {
				t.Errorf("终帧 = %s reason=%v，期望 response.incomplete / max_output_tokens", last, r.reason)
			}
			if fmt.Sprint(r.statuses) != fmt.Sprint(tc.want) {
				t.Errorf("工具 item status = %v，期望 %v", r.statuses, tc.want)
			}
			wantDone := 0
			for _, s := range tc.want {
				if s == "completed" {
					wantDone++
				}
			}
			if n := r.count("response.function_call_arguments.done"); n != wantDone {
				t.Errorf("arguments.done 发了 %d 次，期望 %d: %v", n, wantDone, r.events)
			}
		})
	}
}

// #127 PO 裁决（构造样本）：A 上游 content_block_stop 已到、message_delta 前断流——入参写完了，调用
// 维持 completed；终帧仍按 #106 发 response.incomplete。
func TestResponsesExitCutAfterToolBlockStop(t *testing.T) {
	const stream = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"read","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

`
	ch, err := anthropic.NewCodec().DecodeStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	r := encodeToResponses(t, ch)
	if fmt.Sprint(r.statuses) != "[completed]" || r.count("response.function_call_arguments.done") != 1 {
		t.Errorf("status = %v，arguments.done %d 次；期望 completed 且发 done", r.statuses, r.count("response.function_call_arguments.done"))
	}
	if last := r.events[len(r.events)-1]; last != "response.incomplete" {
		t.Errorf("终帧 = %s，期望 response.incomplete", last)
	}
}

// #98 PO 裁决：上游流干净 EOF 却没给终态（Truncated），A / CC 出口改发各自的流内 error 帧，
// 不再凭空补 message_stop / [DONE]；解码侧同时记一笔 StreamReadError，流水落 stream_aborted。
// 三个上游各一条构造样本（手搭，形状同上）。
func TestCleanEOFWithoutTerminalIsError(t *testing.T) {
	const ccStream = `data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"半截"}}]}

`
	const anthropicStream = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"半截"}}

`
	const responsesStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"m","status":"in_progress","output":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"半截"}

`
	type decoder interface {
		DecodeStream(r io.Reader) (<-chan protocol.Event, error)
		StreamReadError() error
	}
	upstreams := map[string]struct {
		codec  func() decoder
		stream string
	}{
		"CC 上游":        {func() decoder { return openaicc.NewCodec() }, ccStream},
		"Anthropic 上游": {func() decoder { return anthropic.NewCodec() }, anthropicStream},
		"Responses 上游": {func() decoder { return openairesponses.NewCodec() }, responsesStream},
	}
	exits := map[string]struct {
		encode    func(io.Writer, <-chan protocol.Event) error
		forbidden string
		last      string
	}{
		"A 出口":  {anthropic.NewCodec().EncodeStream, "message_stop", "event: error"},
		"CC 出口": {openaicc.NewCodec().EncodeStream, "[DONE]", `data: {"error"`},
	}
	for up, u := range upstreams {
		for ex, e := range exits {
			t.Run(up+"→"+ex, func(t *testing.T) {
				dec := u.codec()
				ch, err := dec.DecodeStream(strings.NewReader(u.stream))
				if err != nil {
					t.Fatal(err)
				}
				var buf bytes.Buffer
				if err := e.encode(&buf, ch); err != nil {
					t.Fatal(err)
				}
				out := buf.String()
				if strings.Contains(out, e.forbidden) || strings.Contains(out, "end_turn") {
					t.Errorf("没终态的流被补成了正常收尾:\n%s", out)
				}
				frames := strings.Split(strings.TrimSpace(out), "\n\n")
				if last := frames[len(frames)-1]; !strings.HasPrefix(last, e.last) {
					t.Errorf("终帧 = %q，期望 %s 开头", last, e.last)
				}
				if dec.StreamReadError() == nil {
					t.Error("解码侧没记 StreamReadError，流水会落 ok")
				}
			})
		}
	}
}

// 截断判据三家对称（#161 PO 裁决，#98 CC 那条的推广）：见过停因或协议终态帧（A message_stop /
// CC [DONE] / R 终态事件）任一即不算截断，照常收尾、流水落 ok。三条构造样本（手搭）。
// 反面「两者皆无的干净 EOF」由 TestCleanEOFWithoutTerminalIsError 钉。
func TestTerminalFrameWithoutStopReasonIsNotTruncated(t *testing.T) {
	type decoder interface {
		DecodeStream(r io.Reader) (<-chan protocol.Event, error)
		StreamReadError() error
	}
	cases := map[string]struct {
		codec  decoder
		stream string
	}{
		"CC [DONE] 无 finish_reason": {openaicc.NewCodec(), `data: {"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"好了"}}]}

data: [DONE]

`},
		"A message_stop 无 stop_reason": {anthropic.NewCodec(), `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"好了"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":null,"stop_sequence":null},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`},
		"R 终帧缺 response 对象": {openairesponses.NewCodec(), `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"m","status":"in_progress","output":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"好了"}

event: response.completed
data: {"type":"response.completed"}

`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ch, err := c.codec.DecodeStream(strings.NewReader(c.stream))
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := anthropic.NewCodec().EncodeStream(&buf, ch); err != nil {
				t.Fatal(err)
			}
			if out := buf.String(); !strings.Contains(out, `"stop_reason":"end_turn"`) || !strings.Contains(out, "event: message_stop") {
				t.Errorf("终态帧收尾应照常 end_turn + message_stop:\n%s", out)
			}
			if err := c.codec.StreamReadError(); err != nil {
				t.Errorf("StreamReadError = %v，见过终态帧不该记断流", err)
			}
		})
	}
}
