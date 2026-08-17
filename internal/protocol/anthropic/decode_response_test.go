package anthropic

import (
	"io"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件测 Anthropic 作**上游**的解码半边（#25，R→A 用）。
//
// 输入是手抄的 SSE，形状照 testdata/golden/raw/anthropic-* 五份真实转录——**不回放
// 那些文件**：`raw/` 在 .gitignore 里，CI 上根本不存在，回放式用例会集体 skip 成
// 一片假绿（#12 已经踩过这个）。所以真实字节的作用是定形状，定完把期望写死在这里。
//
// 手抄时刻意保留了两处真实特征：data 负载尾部的空格填充（上游的抗缓冲手段），以及
// 中间插进来的 ping 帧（五份转录每份都有）。这两样都是解码器必须无视的。

func collect(t *testing.T, raw string) []protocol.Event {
	t.Helper()
	ch, err := NewCodec().DecodeStream(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var events []protocol.Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func types(events []protocol.Event) []protocol.EventType {
	out := make([]protocol.EventType, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}

func assertTypes(t *testing.T, got []protocol.EventType, want ...protocol.EventType) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("事件序列 = %v, 期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("事件序列 = %v, 期望 %v", got, want)
		}
	}
}

// 照 anthropic-text-turn1 抄的：最短的一条完整流。
const respTextStream = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_011Cdn","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":2,"cache_creation_input_tokens":67805,"cache_read_input_tokens":0,"output_tokens":4}}    }

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}       }

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}   }

event: content_block_stop
data: {"type":"content_block_stop","index":0              }

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":2,"cache_creation_input_tokens":67805,"cache_read_input_tokens":0,"output_tokens":4}       }

event: message_stop
data: {"type":"message_stop"    }

`

func TestDecodeStreamText(t *testing.T) {
	events := collect(t, respTextStream)
	assertTypes(t, types(events),
		protocol.EvMessageStart, protocol.EvUsage,
		protocol.EvTextDelta,
		protocol.EvUsage, protocol.EvDone)

	if events[0].ID != "msg_011Cdn" || events[0].Model != "claude-sonnet-5" {
		t.Errorf("message_start 没取到 id/model: %+v", events[0])
	}
	if events[2].Text != "ok" {
		t.Errorf("正文 = %q", events[2].Text)
	}
	// 正文块的 content_block_start/stop 不该产生事件：块边界在 canonical 里是
	// 故意丢掉的（protocol/event.go）。上面的序列断言已经钉住了这条。

	last := events[len(events)-1]
	if last.StopReason != "stop" {
		t.Errorf("end_turn 应映成 canonical 的 stop, 得到 %q", last.StopReason)
	}
}

// usage 在一条流里出现两次，语义是**累计快照**而非两笔加数（protocol/event.go）。
// 消费方按后者覆盖前者处理，所以两帧都得原样放出来。
//
// 同时钉住归一：canonical 的 InputTokens 是**毛值**（含缓存两项），Anthropic 的
// 净值 input_tokens 在解码时就得把缓存加回去（protocol.Usage 的约定）。
func TestDecodeStreamEmitsUsageTwiceAsSnapshots(t *testing.T) {
	var usages []protocol.Usage
	for _, ev := range collect(t, respTextStream) {
		if ev.Type == protocol.EvUsage {
			usages = append(usages, *ev.Usage)
		}
	}
	if len(usages) != 2 {
		t.Fatalf("放出了 %d 次 usage, 期望 2（message_start + message_delta）", len(usages))
	}
	for i, u := range usages {
		if u.InputTokens != 2+67805 {
			t.Errorf("第 %d 次 usage 的 input_tokens = %d, 期望毛值 2+67805", i, u.InputTokens)
		}
		// 缓存两项本身照留：毛值是总量，明细还得能拆出来（计费/排障都要）。
		if u.CacheWriteTokens != 67805 {
			t.Errorf("第 %d 次 usage 的 cache_creation 丢了: %+v", i, u)
		}
	}
}

// 兼容上游的 message_delta 可能只带 output_tokens（不重复 input 与缓存两项）。
// 那一帧解出来 InputTokens 必然是 0——消费方靠「非零字段覆盖」不把 message_start
// 的毛值清掉，两件事必须一起成立才不会低估 total（#72）。
func TestDecodeStreamPartialUsageDeltaKeepsZeroInput(t *testing.T) {
	const stream = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_1","usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":1}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`
	var usages []protocol.Usage
	for _, ev := range collect(t, stream) {
		if ev.Type == protocol.EvUsage {
			usages = append(usages, *ev.Usage)
		}
	}
	if len(usages) != 2 {
		t.Fatalf("放出了 %d 次 usage, 期望 2", len(usages))
	}
	if usages[0].InputTokens != 100 {
		t.Errorf("message_start 的毛值 input = %d, 期望 10+90", usages[0].InputTokens)
	}
	if usages[1].InputTokens != 0 || usages[1].OutputTokens != 42 {
		t.Errorf("只带 output 的 message_delta 不该凭空造 input: %+v", usages[1])
	}

	var merged protocol.Usage
	for _, u := range usages {
		merged.MergeSnapshot(u)
	}
	if merged.InputTokens != 100 || merged.OutputTokens != 42 {
		t.Errorf("按非零字段合并后 = %+v, 期望 input 100 / output 42", merged)
	}
}

// 照 anthropic-tool-turn1 抄的：thinking 块（正文整段为空、只有 signature）+ 工具块。
const respToolStream = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_011C6v","usage":{"input_tokens":2,"cache_read_input_tokens":64171,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"","estimated_tokens":50}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EuwDCokBCBAYAipA"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01C6vh","name":"Read","input":{},"caller":{"type":"direct"}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"_path\": \"/tmp/a.txt\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":2,"output_tokens":159}}

event: message_stop
data: {"type":"message_stop"}

`

func TestDecodeStreamToolCall(t *testing.T) {
	events := collect(t, respToolStream)
	assertTypes(t, types(events),
		protocol.EvMessageStart, protocol.EvUsage,
		protocol.EvThinkingDelta, protocol.EvThinkingDelta,
		protocol.EvToolCallStart, protocol.EvToolArgsDelta, protocol.EvToolArgsDelta,
		protocol.EvToolCallEnd,
		protocol.EvUsage, protocol.EvDone)

	// thinking 与 signature 分走两条通道：后者不是给人看的正文，而是回带给**同一**
	// 上游的完整性凭据，跨协议必然作废（§5 坑清单）。混成一条，下游就会把一串
	// base64 当推理正文渲染出来。
	if events[2].Channel != protocol.ThinkingBody {
		t.Errorf("thinking_delta 的通道 = %q, 期望正文通道", events[2].Channel)
	}
	if events[3].Channel != protocol.ThinkingSignature {
		t.Errorf("signature_delta 的通道 = %q, 期望 signature", events[3].Channel)
	}
	if events[3].Text != "EuwDCokBCBAYAipA" {
		t.Errorf("signature 内容丢了: %q", events[3].Text)
	}

	start := events[4]
	if start.ToolID != "toolu_01C6vh" || start.ToolName != "Read" {
		t.Errorf("工具调用起始 = %+v", start)
	}
	// Anthropic 的 tool_use.input 恒是 JSON 对象。标成 false，Responses 编码侧就会
	// 把它当自由文本去拆包，拆出个 file_path 的值来。
	if !start.ArgsIsJSON {
		t.Error("ArgsIsJSON = false，但 Anthropic 的 input 恒是 JSON")
	}
	if start.Index != 1 {
		t.Errorf("工具块的 Index = %d, 期望 1（thinking 占了 0）", start.Index)
	}

	var args strings.Builder
	for _, ev := range events {
		if ev.Type == protocol.EvToolArgsDelta {
			args.WriteString(ev.Text)
		}
	}
	if args.String() != `{"file_path": "/tmp/a.txt"}` {
		t.Errorf("分片拼起来 = %q", args.String())
	}
	if events[len(events)-1].StopReason != "tool_calls" {
		t.Errorf("tool_use 应映成 canonical 的 tool_calls, 得到 %q", events[len(events)-1].StopReason)
	}
}

// 只有工具块要发 EvToolCallEnd。正文/thinking 的 content_block_stop 若也发，
// Responses 编码侧会拿一个不存在的调用去收口。
func TestDecodeStreamOnlyClosesToolBlocks(t *testing.T) {
	var ends int
	for _, ev := range collect(t, respToolStream) {
		if ev.Type == protocol.EvToolCallEnd {
			ends++
		}
	}
	if ends != 1 {
		t.Errorf("放出了 %d 个 EvToolCallEnd, 期望 1（三个 content_block_stop 里只有工具那个算）", ends)
	}
}

// 流断在 message_stop 之前：仍要给下游一个终点，否则编码侧一直等 EvDone，
// 客户端一直等 response.completed，两边一起挂着。
func TestDecodeStreamAlwaysTerminates(t *testing.T) {
	truncated := respTextStream[:strings.Index(respTextStream, "event: message_delta")]
	events := collect(t, truncated)
	if len(events) == 0 || events[len(events)-1].Type != protocol.EvDone {
		t.Fatalf("截断的流没有以 EvDone 收尾: %v", types(events))
	}
	last := events[len(events)-1]
	// stop_reason 一次都没到，兜底收尾把它填成了 stop——wire 上与正常收尾同形。
	// Truncated 是唯一分得开的那一位，压缩合成靠它判「这段摘要写完了没有」
	// （openairesponses 的 compactionNoItem）。
	if last.StopReason != "stop" || !last.Truncated {
		t.Errorf("兜底收尾没标成截断: %+v", last)
	}
}

// stop_reason 已经在 message_delta 里给过，流却断在 message_stop 之前——兜底收尾
// 要把它带上，别退回默认的 stop。
func TestDecodeStreamKeepsStopReasonOnTruncation(t *testing.T) {
	truncated := respToolStream[:strings.Index(respToolStream, "event: message_stop")]
	events := collect(t, truncated)
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "tool_calls" {
		t.Errorf("兜底收尾丢了 stop_reason: %+v", last)
	}
	// 上游把「为什么停」说清楚了，只是没走完收尾帧——不算截断。
	if last.Truncated {
		t.Error("上游已经声明过 stop_reason，不该标成截断")
	}
}

// error 帧：五份转录里一次都没出现（都是 200 正常流），照协议文档实现，用例手写。
// §9 缺口清单里记着这条。
func TestDecodeStreamSurfacesErrorFrame(t *testing.T) {
	events := collect(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m"}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`)
	var errored bool
	for _, ev := range events {
		if ev.Type == protocol.EvError {
			errored = true
			if ev.Message != "Overloaded" {
				t.Errorf("错误文案 = %q", ev.Message)
			}
		}
	}
	if !errored {
		t.Fatalf("error 帧没变成 EvError: %v", types(events))
	}
	// 错误之后不补 EvDone：客户端已经知道这轮废了，再给个正常收尾会让它以为还有救。
	for _, ev := range events {
		if ev.Type == protocol.EvDone {
			t.Error("error 之后还发了 EvDone")
		}
	}
}

// 读流本身失败（连接断在半路）也要变成流内错误，而不是静默截断。
func TestDecodeStreamReportsReadFailure(t *testing.T) {
	events := collect(t, "")
	_ = events
	ch, err := NewCodec().DecodeStream(io.MultiReader(
		strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\"}}\n\n"),
		&failingReader{},
	))
	if err != nil {
		t.Fatal(err)
	}
	var sawErr bool
	for ev := range ch {
		if ev.Type == protocol.EvError {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("读流失败没变成 EvError")
	}
}

type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// 认不得的帧、解不动的负载都跳过，不打死整条流——ping 已经在上面的用例里覆盖了，
// 这里补更脏的形态。
func TestDecodeStreamToleratesJunkFrames(t *testing.T) {
	events := collect(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m"}}

: 这是一条注释心跳

event: some_new_beta_event
data: {"type":"some_new_beta_event","whatever":1}

event: content_block_delta
data: not json at all

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"活着"}}

event: message_stop
data: {"type":"message_stop"}

`)
	assertTypes(t, types(events),
		protocol.EvMessageStart, protocol.EvTextDelta, protocol.EvDone)
	if events[1].Text != "活着" {
		t.Errorf("正文 = %q", events[1].Text)
	}
}

// 非流式：五份转录全是流式，所以这条路没有真实样本背书（§9 缺口）。形状照协议
// 文档 + 与流式那半边的对称性写，用例钉的是「两条路径解出同一串事件」。
func TestDecodeFullBodyMirrorsStream(t *testing.T) {
	events, err := NewCodec().DecodeFullBody([]byte(`{
		"id":"msg_1","model":"claude-sonnet-5","type":"message","role":"assistant",
		"content":[
			{"type":"thinking","thinking":"想想","signature":"sig1"},
			{"type":"text","text":"我看看"},
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/tmp/a.txt"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":5}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	assertTypes(t, types(events),
		protocol.EvMessageStart, protocol.EvUsage,
		protocol.EvThinkingDelta, protocol.EvThinkingDelta,
		protocol.EvTextDelta,
		protocol.EvToolCallStart, protocol.EvToolArgsDelta, protocol.EvToolCallEnd,
		protocol.EvDone)

	if events[5].ToolName != "Read" || !events[5].ArgsIsJSON {
		t.Errorf("工具调用起始 = %+v", events[5])
	}
	if !strings.Contains(events[6].Text, "file_path") {
		t.Errorf("工具入参 = %q", events[6].Text)
	}
	if events[len(events)-1].StopReason != "tool_calls" {
		t.Errorf("stop = %q", events[len(events)-1].StopReason)
	}
	// 非流式同样走毛值归一：10 + 缓存读 5。
	if u := events[1].Usage; u == nil || u.InputTokens != 15 || u.CacheReadTokens != 5 {
		t.Errorf("usage = %+v, 期望毛值 input 15 / cache_read 5", u)
	}
}

// 非流式的 stop_reason 缺失同样要置 Truncated：包解得开，所以它不是「流断了」，
// 而是「上游没声明这轮是怎么收的」——对压缩合成是同一个失格理由，一份没声明收尾的
// 响应不该被当成完整摘要装回 Codex 的历史（openairesponses 的 compactionNoItem）。
//
// 这条盯的是两条路径的对称：流式那半边由 emitDone 置位，非流式的 EvDone 是手搓的。
func TestDecodeFullBodyMarksMissingStopReasonTruncated(t *testing.T) {
	head := `{"id":"msg_1","model":"claude-sonnet-5","type":"message","role":"assistant",` +
		`"content":[{"type":"text","text":"写了一半"}]`
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"上游说了为什么停", head + `,"stop_reason":"end_turn"}`, false},
		{"stop_reason 缺失", head + `}`, true},
		{"stop_reason 是 null", head + `,"stop_reason":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, err := NewCodec().DecodeFullBody([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.Type != protocol.EvDone {
				t.Fatalf("末事件不是 EvDone: %v", last.Type)
			}
			if last.StopReason != "stop" {
				t.Errorf("StopReason 该兜成 stop（下游要一个合法取值），得到 %q", last.StopReason)
			}
			if last.Truncated != tc.want {
				t.Errorf("Truncated = %v, 期望 %v", last.Truncated, tc.want)
			}
		})
	}
}

func TestDecodeFullBodySurfacesErrorPayload(t *testing.T) {
	events, err := NewCodec().DecodeFullBody(
		[]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != protocol.EvError || events[0].Message != "Overloaded" {
		t.Errorf("错误响应解成了 %+v", events)
	}
}

func TestDecodeFullBodyRejectsNonJSON(t *testing.T) {
	if _, err := NewCodec().DecodeFullBody([]byte(`<html>502</html>`)); err == nil {
		t.Error("非 JSON 响应体应当报错")
	}
}

func TestCanonicalStopReasonMapping(t *testing.T) {
	cases := map[string]string{
		"end_turn":      "stop",
		"tool_use":      "tool_calls",
		"max_tokens":    "length",
		"refusal":       "content_filter",
		"stop_sequence": "stop",
		"":              "stop",
		"某个新词":          "stop", // 不把上游的新词直接捅给客户端
	}
	for in, want := range cases {
		if got := canonicalStopReason(in); got != want {
			t.Errorf("canonicalStopReason(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 与编码侧那条映射必须互逆，否则 A→A（虽然走透传）与 A→X→A 的语义会对不上。
func TestStopReasonMappingsAreInverse(t *testing.T) {
	for _, canonical := range []string{"stop", "tool_calls", "length", "content_filter"} {
		if got := canonicalStopReason(mapStopReason(canonical)); got != canonical {
			t.Errorf("%q 映到 Anthropic 再映回来变成了 %q", canonical, got)
		}
	}
}
