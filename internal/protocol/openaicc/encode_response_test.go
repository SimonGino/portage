package openaicc_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

// 本文件的断言对象是**线格式**，参照系是 testdata/golden/cc-stream-* 三份真实上游
// 转录：每帧只有 `data:` 一行，首帧是只带 role 的空 delta，finish_reason 单独一帧，
// usage 帧的 choices 是空数组，最后 `data: [DONE]`。

// ccChunk 是下行帧的解析形态。只列断言用得上的字段。
type ccChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int      `json:"index"`
		Delta        *ccDelta `json:"delta"`
		FinishReason *string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// ccDelta 是一帧里的增量。
type ccDelta struct {
	Role string `json:"role"`
	// Content / ReasoningContent 是正文与推理正文两格，客户端分开渲染（口径层 v0.62）。
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// delta / finish 把「取第 0 个 choice」这层样板收掉。usage 帧的 choices 是空数组，
// 两个方法都得对空数组安全。
func (c ccChunk) delta() *ccDelta {
	if len(c.Choices) == 0 {
		return nil
	}
	return c.Choices[0].Delta
}

func (c ccChunk) finish() *string {
	if len(c.Choices) == 0 {
		return nil
	}
	return c.Choices[0].FinishReason
}

// encodeStream 把事件序列走一遍编码，返回原始字节与解析好的帧。
//
// includeUsage 走的是**真实入口**：先 DecodeRequest 一个带不带 stream_options 的
// 请求体，再拿同一个 Codec 实例编码。手动置字段测不到那条状态传递。
func encodeStream(t *testing.T, requestBody string, events []protocol.Event) (string, []ccChunk) {
	t.Helper()
	codec := openaicc.NewCodec()
	if _, err := codec.DecodeRequest([]byte(requestBody), true); err != nil {
		t.Fatalf("请求解码失败: %v", err)
	}

	ch := make(chan protocol.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)

	var buf bytes.Buffer
	if err := codec.EncodeStream(&buf, ch); err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	raw := buf.String()

	var chunks []ccChunk
	for _, frame := range strings.Split(strings.TrimSpace(raw), "\n\n") {
		data, ok := strings.CutPrefix(frame, "data: ")
		if !ok {
			t.Fatalf("帧不是 `data: ` 开头（CC 的 SSE 没有 event: 行）:\n%q", frame)
		}
		if data == "[DONE]" {
			continue
		}
		var c ccChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("帧不是合法 JSON: %v\n%s", err, data)
		}
		chunks = append(chunks, c)
	}
	return raw, chunks
}

const streamingRequest = `{"model":"m","stream":true,"stream_options":{"include_usage":true},` +
	`"messages":[{"role":"user","content":"hi"}]}`

func textEvents() []protocol.Event {
	return []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "gpt-5.6-luna"},
		{Type: protocol.EvTextDelta, Text: "HTTP"},
		{Type: protocol.EvTextDelta, Text: " 长轮询"},
		{Type: protocol.EvUsage, Usage: &protocol.Usage{InputTokens: 4221, OutputTokens: 37, CacheReadTokens: 3840}},
		{Type: protocol.EvDone, StopReason: "stop"},
	}
}

// 纯文本流的帧序列，对照 cc-stream-text 的真实转录。
func TestEncodeStreamTextFrameSequence(t *testing.T) {
	raw, chunks := encodeStream(t, streamingRequest, textEvents())

	if !strings.HasSuffix(raw, "data: [DONE]\n\n") {
		t.Errorf("流末缺 [DONE]:\n%q", raw[max(0, len(raw)-120):])
	}
	if strings.Contains(raw, "event:") {
		t.Error("CC 的 SSE 不带 event: 行")
	}

	// 首帧：只带 role，没有正文。
	if len(chunks) < 1 || chunks[0].delta() == nil || chunks[0].delta().Role != "assistant" {
		t.Fatalf("首帧应是只带 role 的空 delta，实得 %+v", chunks[0])
	}
	if chunks[0].delta().Content != "" {
		t.Errorf("首帧不该带正文: %q", chunks[0].delta().Content)
	}
	if chunks[0].Object != "chat.completion.chunk" {
		t.Errorf("object = %q", chunks[0].Object)
	}

	// 正文逐字下发，**不合并**：合并等于把逐字输出重新变成一次性吐出。
	var text []string
	for _, c := range chunks {
		if c.delta() != nil && c.delta().Content != "" {
			text = append(text, c.delta().Content)
		}
	}
	if len(text) != 2 || strings.Join(text, "") != "HTTP 长轮询" {
		t.Errorf("正文帧 = %q，期望两帧原样转发", text)
	}

	// id / model / created 每帧一致，且 id 原样透传上游的。
	for i, c := range chunks {
		if c.ID != "chatcmpl-abc" || c.Model != "gpt-5.6-luna" {
			t.Errorf("chunks[%d] id/model = %q/%q，应逐帧一致且原样透传", i, c.ID, c.Model)
		}
		if c.Created == 0 {
			t.Errorf("chunks[%d] created 缺失", i)
		}
	}

	// finish_reason 单独占一帧，delta 是 {"content":""}。
	fin := chunks[len(chunks)-2]
	if fin.finish() == nil || *fin.finish() != "stop" {
		t.Fatalf("倒数第二帧应带 finish_reason=stop，实得 %+v", fin)
	}
	if fin.delta() == nil {
		t.Error("finish 帧的 delta 不该缺席（SDK 按「delta 一定在」解析）")
	}

	// usage 帧：choices 空数组，token 数与上游一致。
	last := chunks[len(chunks)-1]
	if last.Usage == nil {
		t.Fatal("要过 include_usage 就得有 usage 帧")
	}
	if len(last.Choices) != 0 {
		t.Errorf("usage 帧的 choices 应为空数组，实得 %d 项", len(last.Choices))
	}
	if last.Usage.PromptTokens != 4221 || last.Usage.CompletionTokens != 37 {
		t.Errorf("usage 数字没对上: %+v", last.Usage)
	}
	if last.Usage.TotalTokens != 4258 {
		t.Errorf("total_tokens = %d，期望 4221+37；CC 客户端普遍读这个字段", last.Usage.TotalTokens)
	}
	if last.Usage.PromptTokensDetails.CachedTokens != 3840 {
		t.Errorf("cached_tokens 丢了: %+v", last.Usage)
	}
}

// 后来的 usage 快照按**非零字段**覆盖（protocol/event.go 的 EvUsage 约定）：只报
// output_tokens 的末帧不许把 prompt_tokens 清零，否则 total 低估（portage-legacy#72）。
func TestEncodeStreamMergesPartialUsageSnapshots(t *testing.T) {
	_, chunks := encodeStream(t, streamingRequest, []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		{Type: protocol.EvUsage, Usage: &protocol.Usage{InputTokens: 4221, CacheReadTokens: 3840}},
		{Type: protocol.EvTextDelta, Text: "hi"},
		{Type: protocol.EvUsage, Usage: &protocol.Usage{OutputTokens: 37}},
		{Type: protocol.EvDone, StopReason: "stop"},
	})
	last := chunks[len(chunks)-1]
	if last.Usage == nil {
		t.Fatal("要过 include_usage 就得有 usage 帧")
	}
	if last.Usage.PromptTokens != 4221 || last.Usage.CompletionTokens != 37 {
		t.Errorf("usage = %+v, 期望 prompt 4221 / completion 37", last.Usage)
	}
	if last.Usage.TotalTokens != 4258 {
		t.Errorf("total_tokens = %d, 期望 4258", last.Usage.TotalTokens)
	}
	if last.Usage.PromptTokensDetails.CachedTokens != 3840 {
		t.Errorf("cached_tokens 被后一份快照清掉了: %+v", last.Usage)
	}
}

// 没要过 include_usage 就不发那一帧：CC 的默认行为就是不发，凭空补一帧会让严格按
// SDK 写的客户端多解一个它没预期的结构。
func TestEncodeStreamOmitsUsageFrameUnlessAsked(t *testing.T) {
	plain := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	_, chunks := encodeStream(t, plain, textEvents())
	for _, c := range chunks {
		if c.Usage != nil {
			t.Fatalf("没要 include_usage 却发了 usage 帧: %+v", c.Usage)
		}
	}
	// 该有的还得有：少发 usage 帧不等于把 finish_reason 也吞掉。
	fin := chunks[len(chunks)-1]
	if fin.finish() == nil || *fin.finish() != "stop" {
		t.Errorf("末帧应带 finish_reason，实得 %+v", fin)
	}
}

// 要过 include_usage 但上游一帧 usage 都没给：也不发。补零会让客户端把「上游没说」
// 记成「用了 0 个 token」。
func TestEncodeStreamDoesNotInventUsage(t *testing.T) {
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		{Type: protocol.EvTextDelta, Text: "hi"},
		{Type: protocol.EvDone, StopReason: "stop"},
	}
	_, chunks := encodeStream(t, streamingRequest, events)
	for _, c := range chunks {
		if c.Usage != nil {
			t.Fatalf("上游没给 usage 却编出来一帧: %+v", c.Usage)
		}
	}
}

// 并行工具调用：上游 index 重编成 CC 的 0..n-1。
//
// Anthropic 上游给的 Index 是**内容块**下标（正文占 0，工具从 1 起），而 CC 客户端
// 拿 index 当 tool_calls 数组下标用，直接透传会在数组里留一个空洞。
func TestEncodeStreamRenumbersToolCallIndexes(t *testing.T) {
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		{Type: protocol.EvTextDelta, Text: "这就读"},
		{Type: protocol.EvToolCallStart, Index: 1, ToolID: "call_a", ToolName: "read", ArgsIsJSON: true},
		{Type: protocol.EvToolArgsDelta, Index: 1, Text: `{"filePath":`},
		{Type: protocol.EvToolArgsDelta, Index: 1, Text: `"notes.md"}`},
		{Type: protocol.EvToolCallEnd, Index: 1},
		{Type: protocol.EvToolCallStart, Index: 2, ToolID: "call_b", ToolName: "read", ArgsIsJSON: true},
		{Type: protocol.EvToolArgsDelta, Index: 2, Text: `{"filePath":"cache.md"}`},
		{Type: protocol.EvToolCallEnd, Index: 2},
		{Type: protocol.EvDone, StopReason: "tool_calls"},
	}
	_, chunks := encodeStream(t, streamingRequest, events)

	args := map[int]string{}
	starts := map[int]string{}
	for _, c := range chunks {
		if c.delta() == nil {
			continue
		}
		for _, tc := range c.delta().ToolCalls {
			if tc.ID != "" {
				starts[tc.Index] = tc.ID
				if tc.Type != "function" {
					t.Errorf("首帧 type = %q，期望 function", tc.Type)
				}
				if tc.Function.Name != "read" {
					t.Errorf("首帧 name = %q", tc.Function.Name)
				}
			}
			args[tc.Index] += tc.Function.Arguments
		}
	}
	if starts[0] != "call_a" || starts[1] != "call_b" {
		t.Fatalf("index 没重编成 0/1: %+v", starts)
	}
	if args[0] != `{"filePath":"notes.md"}` {
		t.Errorf("第一个调用的入参拼错了: %q", args[0])
	}
	if args[1] != `{"filePath":"cache.md"}` {
		t.Errorf("第二个调用的入参拼错了: %q", args[1])
	}

	// 这一串没有 EvUsage，所以末帧就是 finish 帧。
	fin := chunks[len(chunks)-1]
	if fin.finish() == nil || *fin.finish() != "tool_calls" {
		t.Errorf("finish_reason = %v，期望 tool_calls——客户端按它决定要不要跑工具", fin.finish())
	}
}

// 流内错误：发 error 帧，且**不补 [DONE]**——那是「正常收完」的记号。
func TestEncodeStreamErrorSkipsDone(t *testing.T) {
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		{Type: protocol.EvTextDelta, Text: "半句"},
		{Type: protocol.EvError, Message: "上游断了"},
	}
	raw, chunks := encodeStream(t, streamingRequest, events)
	if strings.Contains(raw, "[DONE]") {
		t.Error("发过 error 之后不该补 [DONE]，客户端会把这一轮当成功的")
	}
	last := chunks[len(chunks)-1]
	if last.Error == nil || last.Error.Message != "上游断了" {
		t.Fatalf("末帧应是 error 帧，实得 %+v", last)
	}
	for _, c := range chunks {
		if c.finish() != nil {
			t.Error("错误流不该带 finish_reason")
		}
	}
}

// 上游一个 id 都没给：补一个 chatcmpl- 前缀的。前缀本身是排障线索——
// 有上游 id 时一律原样透传，所以 chatcmpl- 出现即代表「网关补的」。
func TestEncodeStreamFillsMissingID(t *testing.T) {
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, Model: "m"},
		{Type: protocol.EvDone, StopReason: "stop"},
	}
	_, chunks := encodeStream(t, streamingRequest, events)
	if !strings.HasPrefix(chunks[0].ID, "chatcmpl-") || len(chunks[0].ID) <= len("chatcmpl-") {
		t.Errorf("id = %q，期望补一个 chatcmpl- 前缀的", chunks[0].ID)
	}
}

// ---- 非流式 ----

// ccBody 是非流式响应的解析形态。
type ccBody struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string           `json:"role"`
			Content   *string          `json:"content"`
			ToolCalls []map[string]any `json:"tool_calls"`
			// ReasoningContent 是推理正文那一格（口径层 v0.62），有数才写。
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func encodeFull(t *testing.T, events []protocol.Event) ccBody {
	t.Helper()
	_, body := encodeFullRaw(t, events)
	return body
}

// encodeFullRaw 与 encodeFull 同源，另外交出原始字节——「某个字段一个字节都不许出现」
// 这类断言只能对着字节查，解析后的结构看不见「键省没省」。
func encodeFullRaw(t *testing.T, events []protocol.Event) (string, ccBody) {
	t.Helper()
	raw, err := openaicc.NewCodec().EncodeFullBody(events)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var body ccBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("编出来的不是合法 JSON: %v\n%s", err, raw)
	}
	return string(raw), body
}

// 非流式纯文本，对照 cc-text 的真实响应。
func TestEncodeFullBodyText(t *testing.T) {
	body := encodeFull(t, textEvents())
	if body.Object != "chat.completion" {
		t.Errorf("object = %q", body.Object)
	}
	if len(body.Choices) != 1 {
		t.Fatalf("choices 数 = %d", len(body.Choices))
	}
	ch := body.Choices[0]
	if ch.Message.Role != "assistant" {
		t.Errorf("role = %q", ch.Message.Role)
	}
	if ch.Message.Content == nil || *ch.Message.Content != "HTTP 长轮询" {
		t.Errorf("正文没拼对: %v", ch.Message.Content)
	}
	if ch.FinishReason != "stop" {
		t.Errorf("finish_reason = %q", ch.FinishReason)
	}
	// 非流式恒带 usage，与 include_usage 无关——那个开关只管流式的额外帧。
	if body.Usage.PromptTokens != 4221 || body.Usage.TotalTokens != 4258 {
		t.Errorf("usage 没对上: %+v", body.Usage)
	}
}

// 纯工具调用轮：content 是空串而不是缺键（SDK 读它时两者不是一回事），
// tool_calls 的 arguments 是能解开的 JSON 字符串。
func TestEncodeFullBodyToolCalls(t *testing.T) {
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		{Type: protocol.EvToolCallStart, Index: 1, ToolID: "call_a", ToolName: "get_weather", ArgsIsJSON: true},
		{Type: protocol.EvToolArgsDelta, Index: 1, Text: `{"city":`},
		{Type: protocol.EvToolArgsDelta, Index: 1, Text: `"北京"}`},
		{Type: protocol.EvToolCallEnd, Index: 1},
		{Type: protocol.EvDone, StopReason: "tool_calls"},
	}
	body := encodeFull(t, events)
	ch := body.Choices[0]
	if ch.Message.Content == nil || *ch.Message.Content != "" {
		t.Errorf("纯工具调用轮的 content 应是空串而不是缺键，实得 %v", ch.Message.Content)
	}
	if len(ch.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls 数 = %d", len(ch.Message.ToolCalls))
	}
	call := ch.Message.ToolCalls[0]
	if call["id"] != "call_a" || call["type"] != "function" {
		t.Errorf("调用没编对: %+v", call)
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("name = %v", fn["name"])
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
		t.Fatalf("arguments 不是 JSON 字符串: %v", err)
	}
	if args["city"] != "北京" {
		t.Errorf("入参没拼对: %v", args)
	}
	if ch.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q", ch.FinishReason)
	}
}

// 上游一片入参都没发：补 "{}"，否则客户端解一个空串会直接抛。
func TestEncodeFullBodyFillsEmptyToolArgs(t *testing.T) {
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "x", Model: "m"},
		{Type: protocol.EvToolCallStart, Index: 0, ToolID: "c", ToolName: "f"},
		{Type: protocol.EvToolCallEnd, Index: 0},
		{Type: protocol.EvDone, StopReason: "tool_calls"},
	}
	body := encodeFull(t, events)
	fn := body.Choices[0].Message.ToolCalls[0]["function"].(map[string]any)
	if fn["arguments"] != "{}" {
		t.Errorf("arguments = %q，期望 {}", fn["arguments"])
	}
}

// 事件流里带 error：整个响应体编不出来，由上层回错误。半截 JSON 递给客户端更糟。
func TestEncodeFullBodyRefusesErrorEvent(t *testing.T) {
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "x", Model: "m"},
		{Type: protocol.EvError, Message: "上游 500"},
	}
	if _, err := openaicc.NewCodec().EncodeFullBody(events); err == nil {
		t.Fatal("带 error 事件时应报错")
	}
}
