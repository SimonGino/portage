package openaicc_test

import (
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件测 CC 侧推理内容的两个半边（口径层 v0.62）：
//   - 解码：上游的 reasoning_content → EvThinkingDelta（ThinkingBody 通道）
//   - 编码：EvThinkingDelta → delta.reasoning_content / message.reasoning_content
//
// 编码那半边的断言**流式与非流式共用一份**（assertCCThinking）：口径层 v0.62 ① 要的是
// 两条路共用同一台判定，跑两遍同一份断言才验得到，各写一套等于验了两件事。

// 解码：流式 delta.reasoning_content 与非流式 message.reasoning_content 同名同义，
// 都落 ThinkingBody 通道（它是推理正文，不是面向展示的摘要）。
func TestDecodeStreamReasoningContent(t *testing.T) {
	const raw = `data: {"model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"先想"},"finish_reason":null}]}

data: {"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"一下"},"finish_reason":null}]}

data: {"model":"m","choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":"stop"}]}

data: [DONE]

`
	var thinking, text strings.Builder
	for _, ev := range decodeStream(t, []byte(raw)) {
		switch ev.Type {
		case protocol.EvThinkingDelta:
			if ev.Channel != protocol.ThinkingBody {
				t.Errorf("通道 = %q，reasoning_content 是推理正文，该走 ThinkingBody", ev.Channel)
			}
			thinking.WriteString(ev.Text)
		case protocol.EvTextDelta:
			text.WriteString(ev.Text)
		}
	}
	if thinking.String() != "先想一下" {
		t.Errorf("推理正文 = %q，期望 \"先想一下\"", thinking.String())
	}
	if text.String() != "答案" {
		t.Errorf("正文 = %q，期望 \"答案\"", text.String())
	}
}

func TestDecodeFullBodyReasoningContent(t *testing.T) {
	const body = `{"id":"c1","model":"m","choices":[{"index":0,"message":
		{"role":"assistant","reasoning_content":"想过了","content":"答案"},"finish_reason":"stop"}]}`

	var thinking string
	for _, ev := range decodeFull(t, []byte(body)) {
		if ev.Type == protocol.EvThinkingDelta {
			thinking += ev.Text
		}
	}
	if thinking != "想过了" {
		t.Errorf("推理正文 = %q，期望 \"想过了\"（非流式与流式同一条路）", thinking)
	}
}

// ccThinkingEvents 是编码侧的公共输入：正文 + 摘要 + 签名 + 空串 + 一段回答。
//
// 正文与摘要都在里面：CC 出口对两条通道的落点相同（口径层 §2.6 矩阵），
// reasoning_content 一格装两者。
func ccThinkingEvents() []protocol.Event {
	return []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "c1", Model: "m"},
		{Type: protocol.EvThinkingDelta, Text: "推理", Channel: protocol.ThinkingBody},
		{Type: protocol.EvThinkingDelta, Text: "正文", Channel: protocol.ThinkingBody},
		{Type: protocol.EvThinkingDelta, Text: "摘要", Channel: protocol.ThinkingSummary},
		{Type: protocol.EvThinkingDelta, Text: "EroYBCkQIBxgCIkAcNVaZ7t+签名密文", Channel: protocol.ThinkingSignature},
		{Type: protocol.EvThinkingDelta, Text: "", Channel: protocol.ThinkingBody},
		{Type: protocol.EvTextDelta, Text: "答案"},
		{Type: protocol.EvDone, StopReason: "stop"},
	}
}

// assertCCThinking 是流式与非流式共用的那一份断言。
func assertCCThinking(t *testing.T, path, wire, thinking, text string) {
	t.Helper()
	if want := "推理正文摘要"; thinking != want {
		t.Errorf("%s: reasoning_content = %q，期望 %q（正文与摘要都进，空串不进）", path, thinking, want)
	}
	if text != "答案" {
		t.Errorf("%s: content = %q，推理不该混进正文", path, text)
	}
	if strings.Contains(wire, "签名密文") || strings.Contains(wire, "EroYBCkQIBxgCIkAcNVaZ7t+") {
		t.Errorf("%s: signature 漏进了下行字节:\n%s", path, wire)
	}
}

func TestEncodeStreamSynthesizesReasoningContent(t *testing.T) {
	raw, chunks := encodeStream(t, `{"model":"m","stream":true}`, ccThinkingEvents())

	var thinking, text strings.Builder
	for _, c := range chunks {
		d := c.delta()
		if d == nil {
			continue
		}
		if d.ReasoningContent != "" {
			thinking.WriteString(d.ReasoningContent)
			// 推理帧不许同时带正文：客户端把两格分开渲染，混在一帧里语义就含混了。
			if d.Content != "" {
				t.Errorf("同一帧里既有 reasoning_content 又有 content: %+v", d)
			}
		}
		text.WriteString(d.Content)
	}
	assertCCThinking(t, "流式", raw, thinking.String(), text.String())
}

func TestEncodeFullBodySynthesizesReasoningContent(t *testing.T) {
	raw, body := encodeFullRaw(t, ccThinkingEvents())
	msg := body.Choices[0].Message
	text := ""
	if msg.Content != nil {
		text = *msg.Content
	}
	assertCCThinking(t, "非流式", raw, msg.ReasoningContent, text)
}

// 没有推理时 reasoning_content 整键省略（不是空串）：它不是官方字段，凭空写一个空串
// 等于告诉客户端「这轮想过、想了个空」。
func TestEncodeFullBodyOmitsEmptyReasoningContent(t *testing.T) {
	raw, _ := encodeFullRaw(t, []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "c1", Model: "m"},
		{Type: protocol.EvThinkingDelta, Text: "只有签名", Channel: protocol.ThinkingSignature},
		{Type: protocol.EvTextDelta, Text: "答案"},
		{Type: protocol.EvDone, StopReason: "stop"},
	})
	if strings.Contains(raw, "reasoning_content") {
		t.Errorf("没有推理正文却写了 reasoning_content:\n%s", raw)
	}
}

// 解码：`reasoning` 是 reasoning_content 的别名（vLLM 较新版本的 CC 端点发这个），
// 两条路都要认，落的通道与断句都与事实标准那个键一致。
func TestDecodeStreamReasoningAlias(t *testing.T) {
	const raw = `data: {"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"model":"m","choices":[{"index":0,"delta":{"reasoning":" The user"},"finish_reason":null}]}

data: {"model":"m","choices":[{"index":0,"delta":{"reasoning":" wants"},"finish_reason":null}]}

data: {"model":"m","choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":"stop"}]}

data: [DONE]

`
	var thinking, text strings.Builder
	for _, ev := range decodeStream(t, []byte(raw)) {
		switch ev.Type {
		case protocol.EvThinkingDelta:
			if ev.Channel != protocol.ThinkingBody {
				t.Errorf("通道 = %q，reasoning 是推理正文，该走 ThinkingBody", ev.Channel)
			}
			thinking.WriteString(ev.Text)
		case protocol.EvTextDelta:
			text.WriteString(ev.Text)
		}
	}
	if thinking.String() != " The user wants" {
		t.Errorf("推理正文 = %q，期望 \" The user wants\"", thinking.String())
	}
	if text.String() != "答案" {
		t.Errorf("正文 = %q，期望 \"答案\"", text.String())
	}
}

func TestDecodeFullBodyReasoningAlias(t *testing.T) {
	const body = `{"id":"c1","model":"m","choices":[{"index":0,"message":
		{"role":"assistant","reasoning":"想过了","content":"答案"},"finish_reason":"stop"}]}`

	var thinking string
	for _, ev := range decodeFull(t, []byte(body)) {
		if ev.Type == protocol.EvThinkingDelta {
			thinking += ev.Text
		}
	}
	if thinking != "想过了" {
		t.Errorf("推理正文 = %q，期望 \"想过了\"（非流式 message.reasoning 同样是别名）", thinking)
	}
}

// 两键同时有值时只放一份：reasoning_content 优先，reasoning 当没看见。同一帧放两份
// 会让出口那边开两次思考块。
func TestDecodeReasoningAliasPrefersStandardKey(t *testing.T) {
	const raw = `data: {"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"标准","reasoning":"别名"},"finish_reason":"stop"}]}

data: [DONE]

`
	var got []string
	for _, ev := range decodeStream(t, []byte(raw)) {
		if ev.Type == protocol.EvThinkingDelta {
			got = append(got, ev.Text)
		}
	}
	if len(got) != 1 || got[0] != "标准" {
		t.Errorf("推理正文事件 = %v，期望恰好一条 [\"标准\"]", got)
	}
}
