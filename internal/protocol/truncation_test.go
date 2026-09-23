package protocol_test

import (
	"bytes"
	"encoding/json"
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
