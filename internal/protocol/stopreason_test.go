package protocol_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// #116：Anthropic 的 pause_turn / model_context_window_exceeded 是「上游没写完」，
// 整链（Anthropic 上游解码 → 两个 OpenAI 出口编码）都不许报成正常收尾。
//
// 放在 protocol_test 是因为要钉的是跨包的一条链：停因映射在 anthropic 解码侧，
// 后果落在出口编码侧（压缩 turn 的 compactionNoItem、普通 turn 的终态）。
//
// 上游流是构造样本（形状照 anthropic 包 decode_response_test 的手抄转录），不是 golden：
// 这两个停因手上没有真实转录。

func anthropicStream(stopReason string) string {
	return `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"写了一半的摘要"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"` + stopReason + `","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`
}

const compactionTurn = `{"model":"m","input":[
	{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
	{"type":"compaction_trigger"}
]}`

func TestAnthropicUnfinishedStopReasonsAreNotCompleted(t *testing.T) {
	for _, stop := range []string{"pause_turn", "model_context_window_exceeded"} {
		t.Run(stop+"/压缩turn", func(t *testing.T) {
			rc := openairesponses.NewCodec()
			if _, err := rc.DecodeRequest([]byte(compactionTurn), true); err != nil {
				t.Fatal(err)
			}
			if !rc.CompactionTurn() {
				t.Fatal("没认出压缩 turn")
			}
			out := encodeR(t, rc, stop)
			if strings.Contains(out, "response.output_item.done") {
				t.Error("半截摘要被合成成 compaction item，会被 Codex 当替换历史装回去")
			}
			if strings.Contains(out, "event: response.completed") || !strings.Contains(out, "event: response.incomplete") {
				t.Errorf("终帧要 response.incomplete:\n%s", out)
			}
		})
		t.Run(stop+"/普通turn", func(t *testing.T) {
			out := encodeR(t, openairesponses.NewCodec(), stop)
			if !strings.Contains(out, "event: response.incomplete") || !strings.Contains(out, `"incomplete_details":{"reason":"max_output_tokens"}`) {
				t.Errorf("R 出口要报 incomplete / max_output_tokens:\n%s", out)
			}
			ch, err := anthropic.NewCodec().DecodeStream(strings.NewReader(anthropicStream(stop)))
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := openaicc.NewCodec().EncodeStream(&buf, ch); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), `"finish_reason":"length"`) {
				t.Errorf("CC 出口 finish_reason 要 length:\n%s", buf.String())
			}
		})
	}
}

func encodeR(t *testing.T, rc *openairesponses.Codec, stop string) string {
	t.Helper()
	ch, err := anthropic.NewCodec().DecodeStream(strings.NewReader(anthropicStream(stop)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := rc.EncodeStream(&buf, ch); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
