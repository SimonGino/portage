package anthropic_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/protocol/anthropic"
)

// flushCounter 是 EncodeStream 的下游：既收字节，也数 Flush。
//
// 数 Flush 不是形式主义——「流式输出逐字出现而非攒完一次性吐出」是 #11 的验收项，
// 而它在代码里的形态就是每帧一 flush。没有这个计数，攒着发的回归悄无声息。
type flushCounter struct {
	buf     strings.Builder
	flushes int
	// bytesAtFlush 记每次 flush 时已写出的字节数，用来断言 flush 不是最后一次性补的。
	bytesAtFlush []int
}

func (f *flushCounter) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *flushCounter) Flush() {
	f.flushes++
	f.bytesAtFlush = append(f.bytesAtFlush, f.buf.Len())
}

type frame struct {
	event string
	data  map[string]any
}

// parseFrames 把写出来的 SSE 拆成帧，顺带断言线格式本身：每帧必须有 event 行与
// data 行，data 必须是 JSON，且 data.type 与 event 名一致（实采转录如此）。
func parseFrames(t *testing.T, raw string) []frame {
	t.Helper()
	var out []frame
	for _, chunk := range strings.Split(strings.TrimRight(raw, "\n"), "\n\n") {
		if chunk == "" {
			continue
		}
		lines := strings.Split(chunk, "\n")
		if len(lines) != 2 {
			t.Fatalf("帧不是 event+data 两行: %q", chunk)
		}
		name, ok := strings.CutPrefix(lines[0], "event: ")
		if !ok {
			t.Fatalf("缺 event 行: %q", chunk)
		}
		payload, ok := strings.CutPrefix(lines[1], "data: ")
		if !ok {
			t.Fatalf("缺 data 行: %q", chunk)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			t.Fatalf("data 不是 JSON: %q", payload)
		}
		if data["type"] != name {
			t.Errorf("event 名 %q 与 data.type %v 不一致", name, data["type"])
		}
		out = append(out, frame{event: name, data: data})
	}
	return out
}

func encodeStream(t *testing.T, events []protocol.Event) ([]frame, *flushCounter) {
	t.Helper()
	ch := make(chan protocol.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)

	w := &flushCounter{}
	if err := anthropic.NewCodec().EncodeStream(w, ch); err != nil {
		t.Fatalf("EncodeStream 失败: %v", err)
	}
	return parseFrames(t, w.buf.String()), w
}

func eventNames(frames []frame) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		out = append(out, f.event)
	}
	return out
}

// 纯文本流的帧序列，对照 testdata/golden/raw/anthropic-text-turn1 的真实上游转录。
func TestEncodeStreamTextWireFormat(t *testing.T) {
	frames, w := encodeStream(t, []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "resp_1", Model: "gpt-5.6-luna"},
		{Type: protocol.EvTextDelta, Text: "你好"},
		{Type: protocol.EvTextDelta, Text: "，世界"},
		{Type: protocol.EvUsage, Usage: &protocol.Usage{InputTokens: 69, OutputTokens: 7}},
		{Type: protocol.EvDone, StopReason: "stop"},
	})

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	if got := eventNames(frames); !equal(got, want) {
		t.Fatalf("帧序列 = %v\n期望 = %v", got, want)
	}

	msg := frames[0].data["message"].(map[string]any)
	if msg["id"] != "resp_1" || msg["model"] != "gpt-5.6-luna" || msg["role"] != "assistant" {
		t.Errorf("message_start 的 message = %+v", msg)
	}
	if msg["stop_reason"] != nil {
		t.Errorf("message_start 的 stop_reason 应为 null，实际 %v", msg["stop_reason"])
	}

	block := frames[1].data["content_block"].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("首块 type = %v, 期望 text", block["type"])
	}
	if idx := frames[1].data["index"]; idx != float64(0) {
		t.Errorf("首块 index = %v, 期望 0", idx)
	}
	delta := frames[2].data["delta"].(map[string]any)
	if delta["type"] != "text_delta" || delta["text"] != "你好" {
		t.Errorf("正文增量 = %+v", delta)
	}

	tail := frames[len(frames)-2].data
	if got := tail["delta"].(map[string]any)["stop_reason"]; got != "end_turn" {
		t.Errorf("stop_reason = %v, 期望 end_turn", got)
	}
	usage := tail["usage"].(map[string]any)
	if usage["input_tokens"] != float64(69) || usage["output_tokens"] != float64(7) {
		t.Errorf("message_delta 的 usage = %+v", usage)
	}

	// 逐字出现：每帧一 flush，不是攒到最后补一次。
	if w.flushes != len(frames) {
		t.Errorf("flush %d 次，帧数 %d——攒着发就是「一次性吐出」", w.flushes, len(frames))
	}
	if len(w.bytesAtFlush) > 1 && w.bytesAtFlush[0] == w.bytesAtFlush[len(w.bytesAtFlush)-1] {
		t.Error("每次 flush 时字节数都一样，说明字节没有随帧写出")
	}
}

// 工具调用的块序列：正文块先关掉，工具块另起一个 index，参数走 input_json_delta。
//
// 并行调用在 Anthropic 这一侧**不能交错**——同一时刻只能有一个块开着。这条纪律
// 由本用例钉住：三个工具调用必须是三段互不嵌套的 start/delta*/stop。
func TestEncodeStreamSerializesParallelToolBlocks(t *testing.T) {
	frames, _ := encodeStream(t, []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "resp_1", Model: "m"},
		{Type: protocol.EvTextDelta, Text: "我来查一下"},
		{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_a", ToolName: "Read", ArgsIsJSON: true},
		{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{"file"`},
		{Type: protocol.EvToolArgsDelta, Index: 0, Text: `:"a.go"}`},
		{Type: protocol.EvToolCallEnd, Index: 0},
		{Type: protocol.EvToolCallStart, Index: 1, ToolID: "call_b", ToolName: "Grep", ArgsIsJSON: true},
		{Type: protocol.EvToolArgsDelta, Index: 1, Text: `{"pattern":"TODO"}`},
		{Type: protocol.EvToolCallEnd, Index: 1},
		{Type: protocol.EvDone, StopReason: "tool_calls"},
	})

	// 块 index 必须连号：正文 0、两个工具 1 和 2。客户端按 index 归位内容，
	// 跳号或重号都会让它把两个工具调用叠在一起。
	var open = -1
	var blocks []struct {
		index int
		kind  string
	}
	for _, f := range frames {
		switch f.event {
		case "content_block_start":
			if open >= 0 {
				t.Fatalf("index %v 还开着就又开了一个块", open)
			}
			open = int(f.data["index"].(float64))
			blocks = append(blocks, struct {
				index int
				kind  string
			}{open, f.data["content_block"].(map[string]any)["type"].(string)})
		case "content_block_delta":
			if int(f.data["index"].(float64)) != open {
				t.Fatalf("增量的 index %v 落在开着的块 %d 之外", f.data["index"], open)
			}
		case "content_block_stop":
			if int(f.data["index"].(float64)) != open {
				t.Fatalf("stop 的 index %v 与开着的块 %d 对不上", f.data["index"], open)
			}
			open = -1
		}
	}
	if open >= 0 {
		t.Fatal("有块开了没关")
	}

	if len(blocks) != 3 {
		t.Fatalf("编出 %d 个块，期望 3 个（正文 + 两个工具）", len(blocks))
	}
	for i, b := range blocks {
		if b.index != i {
			t.Errorf("第 %d 个块的 index = %d，块 index 必须连号", i, b.index)
		}
	}
	if blocks[0].kind != "text" || blocks[1].kind != "tool_use" || blocks[2].kind != "tool_use" {
		t.Errorf("块类型 = %+v", blocks)
	}

	// 工具块的 id/name 与参数分片
	var toolStart, argFrags = 0, []string{}
	for _, f := range frames {
		if f.event == "content_block_start" {
			if cb := f.data["content_block"].(map[string]any); cb["type"] == "tool_use" {
				toolStart++
				if cb["id"] == "" || cb["name"] == "" {
					t.Errorf("tool_use 块缺 id 或 name: %+v", cb)
				}
				if _, ok := cb["input"].(map[string]any); !ok {
					t.Errorf("tool_use 块的 input 应是空对象: %+v", cb["input"])
				}
			}
		}
		if f.event == "content_block_delta" {
			if d := f.data["delta"].(map[string]any); d["type"] == "input_json_delta" {
				argFrags = append(argFrags, d["partial_json"].(string))
			}
		}
	}
	if toolStart != 2 {
		t.Errorf("tool_use 块 %d 个，期望 2 个", toolStart)
	}
	// 分片原样转发：上游怎么切，客户端就怎么收。
	if got := strings.Join(argFrags[:2], ""); got != `{"file":"a.go"}` {
		t.Errorf("第一个工具的参数拼出来是 %q", got)
	}

	tail := frames[len(frames)-2].data["delta"].(map[string]any)
	if tail["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, 期望 tool_use（canonical 的 tool_calls 映过来）", tail["stop_reason"])
	}
}

// thinking 跨协议丢弃、不做伪映射（口径层 §2.6）：伪造的 thinking 块要连 signature
// 一起造，而那是上游侧凭据，造出来的块回带给上游会被拒。
func TestEncodeStreamDropsThinking(t *testing.T) {
	frames, _ := encodeStream(t, []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "r", Model: "m"},
		{Type: protocol.EvThinkingDelta, Text: "推理正文"},
		{Type: protocol.EvThinkingDelta, Text: "签名", Channel: protocol.ThinkingSignature},
		{Type: protocol.EvTextDelta, Text: "答案"},
		{Type: protocol.EvDone, StopReason: "stop"},
	})
	for _, f := range frames {
		raw, _ := json.Marshal(f.data)
		if strings.Contains(string(raw), "thinking") || strings.Contains(string(raw), "signature") {
			t.Errorf("thinking 漏进了下行流: %s", raw)
		}
	}
}

// stop_reason 映射表，以及**不允许空**这条硬约束：客户端按它决定要不要继续下一轮，
// 给个 null 会让 Claude Code 卡在半路。
func TestEncodeStopReasonIsNeverEmpty(t *testing.T) {
	for _, tc := range []struct{ canonical, want string }{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
		{"content_filter", "refusal"},
		{"", "end_turn"},      // 上游没说
		{"没见过的值", "end_turn"}, // 未知一律兜到 end_turn
	} {
		t.Run(tc.canonical, func(t *testing.T) {
			frames, _ := encodeStream(t, []protocol.Event{
				{Type: protocol.EvMessageStart, ID: "r", Model: "m"},
				{Type: protocol.EvTextDelta, Text: "hi"},
				{Type: protocol.EvDone, StopReason: tc.canonical},
			})
			got := frames[len(frames)-2].data["delta"].(map[string]any)["stop_reason"]
			if got != tc.want {
				t.Errorf("stop_reason = %v, 期望 %q", got, tc.want)
			}

			body, err := anthropic.NewCodec().EncodeFullBody([]protocol.Event{
				{Type: protocol.EvMessageStart, ID: "r", Model: "m"},
				{Type: protocol.EvTextDelta, Text: "hi"},
				{Type: protocol.EvDone, StopReason: tc.canonical},
			})
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if out["stop_reason"] != tc.want {
				t.Errorf("非流式 stop_reason = %v, 期望 %q", out["stop_reason"], tc.want)
			}
		})
	}
}

// 非流式聚合：正文拼成一个 text 块、工具调用的 input 必须是**对象**（客户端拿它
// 直接当参数用，字符串形态会让工具调用整个作废）。
func TestEncodeFullBodyAggregates(t *testing.T) {
	body, err := anthropic.NewCodec().EncodeFullBody([]protocol.Event{
		{Type: protocol.EvMessageStart, ID: "resp_1", Model: "gpt-5.6-luna"},
		{Type: protocol.EvTextDelta, Text: "我来"},
		{Type: protocol.EvTextDelta, Text: "查一下"},
		{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_a", ToolName: "Read", ArgsIsJSON: true},
		{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{"file"`},
		{Type: protocol.EvToolArgsDelta, Index: 0, Text: `:"a.go"}`},
		{Type: protocol.EvToolCallEnd, Index: 0},
		{Type: protocol.EvUsage, Usage: &protocol.Usage{InputTokens: 12, OutputTokens: 34, CacheReadTokens: 5}},
		{Type: protocol.EvDone, StopReason: "tool_calls"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var out struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			CacheReadTokens  int `json:"cache_read_input_tokens"`
			CacheWriteTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("编出来的不是合法 JSON: %v\n%s", err, body)
	}

	if out.Type != "message" || out.Role != "assistant" || out.ID != "resp_1" || out.Model != "gpt-5.6-luna" {
		t.Errorf("响应头部字段不对: %+v", out)
	}
	if len(out.Content) != 2 {
		t.Fatalf("content 有 %d 块，期望 2 块（正文 + 工具）", len(out.Content))
	}
	if out.Content[0].Type != "text" || out.Content[0].Text != "我来查一下" {
		t.Errorf("正文块 = %+v，多片增量应拼成一块", out.Content[0])
	}
	tool := out.Content[1]
	if tool.Type != "tool_use" || tool.ID != "call_a" || tool.Name != "Read" {
		t.Errorf("工具块 = %+v", tool)
	}
	if tool.Input["file"] != "a.go" {
		t.Errorf("tool_use.input 不是解开的对象: %+v", tool.Input)
	}
	if out.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q", out.StopReason)
	}
	if out.Usage.InputTokens != 12 || out.Usage.OutputTokens != 34 || out.Usage.CacheReadTokens != 5 {
		t.Errorf("usage = %+v", out.Usage)
	}
	// 缓存写入侧 CC 没有对应概念，恒零——但键必须在，Claude Code 缺键与 0 不当一回事。
	if !strings.Contains(string(body), "cache_creation_input_tokens") {
		t.Error("usage 缺 cache_creation_input_tokens 键")
	}
}

// 上游流中途出错：错误以 error 帧走在流里（首字节已出，改不了状态码），且不再补
// message_stop——补了客户端会以为响应正常收尾。
func TestEncodeStreamSurfacesMidStreamError(t *testing.T) {
	frames, _ := encodeStream(t, []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "r", Model: "m"},
		{Type: protocol.EvTextDelta, Text: "说到一半"},
		{Type: protocol.EvError, Message: "上游断了"},
	})

	last := frames[len(frames)-1]
	if last.event != "error" {
		t.Fatalf("最后一帧 = %q, 期望 error", last.event)
	}
	if got := last.data["error"].(map[string]any)["message"]; got != "上游断了" {
		t.Errorf("error.message = %v", got)
	}
	for _, f := range frames {
		if f.event == "message_stop" {
			t.Error("出错之后还发了 message_stop，客户端会当成正常收尾")
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
