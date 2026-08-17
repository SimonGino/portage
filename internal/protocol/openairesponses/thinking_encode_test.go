package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件测 Responses 出口的推理合成（口径层 v0.62）：canonical 的推理内容 →
// reasoning item 一整套生命周期。帧序与字段照 responses-stream-reasoning-turn1 的真实
// 上游转录，以及 opencodex 的合成路径（Codex CLI 兼容首要参考）。
//
// 流式与非流式共用一份断言（assertRThinking）：EncodeFullBody 本来就复用同一台状态机
// （newStreamEncoder + io.Discard），跑两遍是为了把「共用」这件事钉在测里。

func rThinkingEvents() []protocol.Event {
	return []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "resp_1", Model: "m"},
		{Type: protocol.EvThinkingDelta, Text: "推理", Channel: protocol.ThinkingBody},
		{Type: protocol.EvThinkingDelta, Text: "正文", Channel: protocol.ThinkingBody},
		{Type: protocol.EvThinkingDelta, Text: "摘要", Channel: protocol.ThinkingSummary},
		{Type: protocol.EvThinkingDelta, Text: "EroYBCkQIBxgCIkAcNVaZ7t+签名密文", Channel: protocol.ThinkingSignature},
		{Type: protocol.EvThinkingDelta, Text: "", Channel: protocol.ThinkingBody},
		{Type: protocol.EvTextDelta, Text: "答案"},
		{Type: protocol.EvDone, StopReason: "stop"},
	}
}

// assertRThinking 是两条路共用的断言：output 列表里恰好一个 reasoning item，摘要正文
// 齐、签名与密文都不在字节里、item 上没有 encrypted_content 键。
func assertRThinking(t *testing.T, path, wire string, output []map[string]any) {
	t.Helper()
	if strings.Contains(wire, "签名密文") || strings.Contains(wire, "EroYBCkQIBxgCIkAcNVaZ7t+") {
		t.Errorf("%s: signature 漏进了下行字节:\n%s", path, wire)
	}
	if strings.Contains(wire, "encrypted_content") {
		t.Errorf("%s: 合成的 item 带了 encrypted_content（我们手里没有封装，空串也不许写）:\n%s", path, wire)
	}

	var reasoning []map[string]any
	for _, item := range output {
		if item["type"] == "reasoning" {
			reasoning = append(reasoning, item)
		}
	}
	if len(reasoning) != 1 {
		t.Fatalf("%s: reasoning item 数 = %d，期望 1（output=%v）", path, len(reasoning), output)
	}
	summary, _ := reasoning[0]["summary"].([]any)
	if len(summary) != 1 {
		t.Fatalf("%s: summary 段数 = %d，期望 1", path, len(summary))
	}
	part, _ := summary[0].(map[string]any)
	if part["type"] != "summary_text" {
		t.Errorf("%s: summary 段 type = %v，期望 summary_text", path, part["type"])
	}
	if want := "推理正文摘要"; part["text"] != want {
		t.Errorf("%s: summary 正文 = %v，期望 %q（正文与摘要都进，空串不进）", path, part["text"], want)
	}
	if _, ok := reasoning[0]["encrypted_content"]; ok {
		t.Errorf("%s: reasoning item 上有 encrypted_content 键", path)
	}
	// 次序：推理 item 排在正文 item 之前，两者都在 output 里。
	if len(output) != 2 || output[0]["type"] != "reasoning" || output[1]["type"] != "message" {
		t.Errorf("%s: output 该是 reasoning + message 两项，实得 %v", path, output)
	}
}

func TestEncodeStreamSynthesizesReasoningItem(t *testing.T) {
	frames := encodeStream(t, NewCodec(), rThinkingEvents()...)

	var wire strings.Builder
	var deltas strings.Builder
	var output []map[string]any
	for _, f := range frames {
		raw, _ := json.Marshal(f.data)
		wire.Write(raw)
		switch f.event {
		case "response.reasoning_summary_text.delta":
			deltas.WriteString(f.data["delta"].(string))
			if f.data["summary_index"].(float64) != 0 {
				t.Errorf("summary_index = %v，期望 0", f.data["summary_index"])
			}
			if f.data["item_id"] == "" {
				t.Error("推理增量帧没带 item_id，Codex 认不出它属于哪个 item")
			}
		case "response.completed":
			resp := f.data["response"].(map[string]any)
			for _, item := range resp["output"].([]any) {
				output = append(output, item.(map[string]any))
			}
		}
	}
	if deltas.String() != "推理正文摘要" {
		t.Errorf("摘要增量拼出来 = %q", deltas.String())
	}
	assertRThinking(t, "流式", wire.String(), output)

	// 帧序照实采：item 开出来先立 summary part 的槽位，收口时 text.done → part.done →
	// output_item.done。缺了 part.added，Codex 侧的 summary[0] 槽位立不起来。
	assertEventOrder(t, frames,
		"response.created", "response.in_progress",
		"response.output_item.added", "response.reasoning_summary_part.added",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done", "response.reasoning_summary_part.done",
		"response.output_item.done",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.output_item.done",
		"response.completed",
	)
}

func TestEncodeFullBodySynthesizesReasoningItem(t *testing.T) {
	body, err := NewCodec().EncodeFullBody(rThinkingEvents())
	if err != nil {
		t.Fatalf("EncodeFullBody: %v", err)
	}
	var out struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	assertRThinking(t, "非流式", string(body), out.Output)
}

// 压缩 turn 上一个推理帧都不许露头（#74 的「恰好一个 compaction item」不变式）：
// summarizer turn 有意留着上游的思考，那段推理只是让上游想清楚，不进 output。
func TestCompactionSwallowsThinking(t *testing.T) {
	c := NewCodec()
	body := []byte(`{"model":"gpt-5","input":[` +
		`{"type":"message","role":"user","content":"hi"},{"type":"compaction_trigger"}]}`)
	if _, err := c.DecodeRequest(body, true); err != nil {
		t.Fatalf("请求解码失败: %v", err)
	}
	if !c.CompactionTurn() {
		t.Fatal("没认出这是压缩 turn，这条断言会空跑")
	}
	frames := encodeStream(t, c, rThinkingEvents()...)
	for _, f := range frames {
		if strings.Contains(f.event, "reasoning") {
			t.Errorf("压缩 turn 里冒出了推理帧 %q，会破坏「恰好一个 compaction item」", f.event)
		}
		raw, _ := json.Marshal(f.data)
		if strings.Contains(string(raw), "推理") || strings.Contains(string(raw), "签名密文") {
			t.Errorf("压缩 turn 的下行字节里混进了推理:\n%s", raw)
		}
	}
}
