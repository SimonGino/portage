package openaicc_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

// loadUpstream 读一份**上游响应**转录（direction=upstream，M0 语料）。
func loadUpstream(t *testing.T, name string) ([]byte, protocol.Summary) {
	t.Helper()
	dir := filepath.Join(goldenDir, name)

	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if os.IsNotExist(err) {
		t.Skipf("样本尚未采集：%s", dir)
	}
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Expect   protocol.Summary `json:"expect"`
		Verified bool             `json:"verified"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.Verified {
		t.Fatalf("%s 的 meta.json 仍是 verified:false", name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "response.raw"))
	if err != nil {
		t.Fatal(err)
	}
	return raw, meta.Expect
}

func collect(t *testing.T, events <-chan protocol.Event) []protocol.Event {
	t.Helper()
	var out []protocol.Event
	for ev := range events {
		out = append(out, ev)
	}
	return out
}

func decodeStream(t *testing.T, raw []byte) []protocol.Event {
	t.Helper()
	// 按 64 字节切块喂：整块灌永远碰不到跨块的帧边界，而真实网络就是碎的。
	events, err := openaicc.NewCodec().DecodeStream(&choppyReader{data: raw, chunk: 64})
	if err != nil {
		t.Fatal(err)
	}
	return collect(t, events)
}

// choppyReader 每次只吐一小段，逼出跨块帧重组的路径。
type choppyReader struct {
	data  []byte
	chunk int
	pos   int
}

func (r *choppyReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := min(min(r.chunk, len(p)), len(r.data)-r.pos)
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

// 真实转录驱动：三个并行工具调用要分毫不差地重组回来，参数拼起来是合法 JSON。
func TestDecodeStreamRebuildsParallelToolCalls(t *testing.T) {
	raw, expect := loadUpstream(t, "cc-stream-parallel-tools")
	events := decodeStream(t, raw)

	if events[0].Type != protocol.EvMessageStart {
		t.Fatalf("第一个事件 = %v, 期望 EvMessageStart", events[0].Type)
	}
	if events[0].Model != expect.Model {
		t.Errorf("Model = %q, 期望 %q", events[0].Model, expect.Model)
	}

	calls := gatherToolCalls(t, events)
	if len(calls) != 3 {
		t.Fatalf("重组出 %d 个工具调用，实采是 3 个", len(calls))
	}
	seen := map[string]bool{}
	for _, call := range calls {
		if call.id == "" || call.name == "" {
			t.Errorf("工具调用缺 id 或 name: %+v", call)
		}
		if seen[call.id] {
			t.Errorf("同一个 id 出现两次: %q", call.id)
		}
		seen[call.id] = true
		var args map[string]any
		if err := json.Unmarshal([]byte(call.args), &args); err != nil {
			t.Errorf("参数拼不成 JSON: %q", call.args)
		}
		if args["city"] == "" || args["city"] == nil {
			t.Errorf("参数内容不对: %q", call.args)
		}
	}

	last := events[len(events)-1]
	if last.Type != protocol.EvDone {
		t.Fatalf("最后一个事件 = %v, 期望 EvDone", last.Type)
	}
	if last.StopReason != "tool_calls" {
		t.Errorf("StopReason = %q, 期望 tool_calls", last.StopReason)
	}
	// usage 出自流末那个 choices 为空的 chunk（要 stream_options.include_usage 才有）。
	if u := lastUsage(events); u == nil {
		t.Error("没解出 usage")
	} else if u.InputTokens != expect.InputTokens || u.OutputTokens != expect.OutputTokens {
		t.Errorf("usage = %+v, 期望 in=%d out=%d", u, expect.InputTokens, expect.OutputTokens)
	}
}

// **本票的主工作量**：并行调用下 CC 的参数分片按 index 交错到达。
//
// 实采的 cc-stream-parallel-tools 三个调用其实是顺序发的（0,0,1,1,2,2），钉不住
// 交错这条路径，所以这里手搭分片序列。手搭的是**测试输入**不是样本——golden 库里
// 只放真实字节，掺一份伪造的转录进去就是往事实里注水。
func TestDecodeStreamHandlesInterleavedToolArgFragments(t *testing.T) {
	frames := []string{
		`{"id":"chatcmpl-x","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"Read","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"Grep","arguments":""}}]}}]}`,
		// 从这里开始交错：a 的一片、b 的一片、a 的一片……
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"file"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"name":"","arguments":"{\"pat"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\":\"a.go\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"tern\":\"TODO\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	}
	var sb strings.Builder
	for _, f := range frames {
		sb.WriteString("data: " + f + "\n\n")
	}

	events := decodeStream(t, []byte(sb.String()))
	calls := gatherToolCalls(t, events)

	if len(calls) != 2 {
		t.Fatalf("重组出 %d 个工具调用，期望 2 个", len(calls))
	}
	want := []toolCall{
		{id: "call_a", name: "Read", args: `{"file":"a.go"}`},
		{id: "call_b", name: "Grep", args: `{"pattern":"TODO"}`},
	}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("第 %d 个调用 = %+v, 期望 %+v", i, calls[i], w)
		}
	}
}

// name 只在非空时覆盖：实采里后续分片带的是 "name":""，照抄会把工具名擦成空串，
// 而一个没有名字的工具调用在客户端那边等于凭空消失。
func TestDecodeStreamKeepsToolNameAgainstEmptyFragments(t *testing.T) {
	raw := "data: " + `{"id":"c","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"Read","arguments":""}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"

	calls := gatherToolCalls(t, decodeStream(t, []byte(raw)))
	if len(calls) != 1 || calls[0].name != "Read" {
		t.Errorf("工具名被空片擦掉了: %+v", calls)
	}
}

// 纯文本流：正文按到达顺序原样交出去，一片不少也不合并——合并会把「逐字出现」
// 的节奏改掉。
func TestDecodeStreamEmitsTextDeltasInOrder(t *testing.T) {
	raw, expect := loadUpstream(t, "cc-stream-text")
	events := decodeStream(t, raw)

	var text strings.Builder
	var deltas int
	for _, ev := range events {
		if ev.Type == protocol.EvTextDelta {
			deltas++
			text.WriteString(ev.Text)
		}
	}
	if deltas < 2 {
		t.Errorf("只解出 %d 条正文增量，实采是多片", deltas)
	}
	if text.Len() == 0 {
		t.Error("正文为空")
	}
	if got := doneReason(events); got != "stop" {
		t.Errorf("StopReason = %q, 期望 stop", got)
	}
	if u := lastUsage(events); u == nil || u.InputTokens != expect.InputTokens {
		t.Errorf("usage 与样本 expect 对不上: %+v", u)
	}
}

// 非流式响应走同一套状态机：choices[].message 与 choices[].delta 只有一处解析。
func TestDecodeFullBodyMatchesStreamSemantics(t *testing.T) {
	raw, expect := loadUpstream(t, "cc-tool")
	events, err := openaicc.NewCodec().DecodeFullBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("没解出任何事件")
	}
	if events[0].Type != protocol.EvMessageStart || events[0].Model != expect.Model {
		t.Errorf("首事件 = %+v, 期望 EvMessageStart model=%q", events[0], expect.Model)
	}
	calls := gatherToolCalls(t, events)
	if len(calls) == 0 {
		t.Fatal("非流式的 tool_calls 没解出来")
	}
	if got := doneReason(events); got != "tool_calls" {
		t.Errorf("StopReason = %q, 期望 tool_calls", got)
	}
	if u := lastUsage(events); u == nil || u.OutputTokens != expect.OutputTokens {
		t.Errorf("usage 与样本 expect 对不上: %+v", u)
	}
}

// finish_reason 查表，未知值一律 stop——宁可少说一句，也不把上游的新词直接捅给
// 客户端，它那边多半没有对应分支。
func TestDecodeStreamMapsStopReason(t *testing.T) {
	for _, tc := range []struct{ upstream, want string }{
		{"stop", "stop"},
		{"tool_calls", "tool_calls"},
		{"function_call", "tool_calls"},
		{"length", "length"},
		{"content_filter", "content_filter"},
		{"上游哪天新造的词", "stop"},
		{"", "stop"}, // 压根没给
	} {
		t.Run(tc.upstream, func(t *testing.T) {
			frame := `{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"hi"}`
			if tc.upstream != "" {
				frame += `,"finish_reason":"` + tc.upstream + `"`
			}
			frame += `}]}`
			if got := doneReason(decodeStream(t, []byte("data: "+frame+"\n\n"))); got != tc.want {
				t.Errorf("StopReason = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

// finish_reason 一次都没到时，兜底收尾会把 StopReason 填成 stop——wire 上与正常收尾
// 同形。Truncated 是唯一分得开的那一位，压缩合成靠它判「这段摘要写完了没有」
// （openairesponses 的 compactionNoItem）。
func TestDecodeStreamMarksTruncatedWhenUpstreamNeverFinished(t *testing.T) {
	withReason := `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}` + "\n\n"
	noReason := `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n"

	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"上游说了为什么停", withReason, false},
		{"上游没说就断了", noReason, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := decodeStream(t, []byte(tc.raw))
			last := events[len(events)-1]
			if last.Type != protocol.EvDone {
				t.Fatalf("末事件不是 EvDone: %v", last.Type)
			}
			if last.StopReason != "stop" {
				t.Errorf("StopReason 该兜成 stop（下游要一个合法取值），得到 %q", last.StopReason)
			}
			if last.Truncated != tc.want {
				t.Errorf("Truncated = %v，期望 %v", last.Truncated, tc.want)
			}
		})
	}
}

type toolCall struct{ id, name, args string }

// gatherToolCalls 把事件流里的工具调用还原成「一个调用一条记录」，顺带断言事件
// 序列本身合规：同一 index 的 Start/ArgsDelta*/End 必须连续且按序。
func gatherToolCalls(t *testing.T, events []protocol.Event) []toolCall {
	t.Helper()
	var out []toolCall
	open := -1
	var cur toolCall
	for _, ev := range events {
		switch ev.Type {
		case protocol.EvToolCallStart:
			if open >= 0 {
				t.Fatalf("index %d 还没 End，index %d 就 Start 了", open, ev.Index)
			}
			open = ev.Index
			cur = toolCall{id: ev.ToolID, name: ev.ToolName}
			if !ev.ArgsIsJSON {
				t.Errorf("CC 的 function.arguments 按契约是 JSON，ArgsIsJSON 应为 true")
			}
		case protocol.EvToolArgsDelta:
			if ev.Index != open {
				t.Fatalf("index %d 的参数片落在 index %d 的块里", ev.Index, open)
			}
			cur.args += ev.Text
		case protocol.EvToolCallEnd:
			if ev.Index != open {
				t.Fatalf("End 的 index = %d, 当前开着的是 %d", ev.Index, open)
			}
			out = append(out, cur)
			open = -1
		}
	}
	if open >= 0 {
		t.Fatalf("index %d 开了没关", open)
	}
	return out
}

func doneReason(events []protocol.Event) string {
	for _, ev := range events {
		if ev.Type == protocol.EvDone {
			return ev.StopReason
		}
	}
	return ""
}

func lastUsage(events []protocol.Event) *protocol.Usage {
	var out *protocol.Usage
	for _, ev := range events {
		if ev.Type == protocol.EvUsage && ev.Usage != nil {
			out = ev.Usage
		}
	}
	return out
}
