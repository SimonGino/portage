package openairesponses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件的断言对象是**线格式**，参照系是 testdata/golden/raw/resp-* 三份真实上游
// SSE 转录。这里手写期望而不是回放转录文件，是因为 raw/ 在 .gitignore 里——CI 上
// 那些文件根本不存在，回放式用例会集体 skip 成一片假绿。转录的作用是定形状，
// 定完写死在这里。

type frame struct {
	event string
	data  map[string]any
}

// parseFrames 把 SSE 字节拆成帧，顺带校验每帧都是 `event:` + `data:` 两行。
func parseFrames(t *testing.T, raw string) []frame {
	t.Helper()
	var frames []frame
	for _, blk := range strings.Split(strings.TrimRight(raw, "\n"), "\n\n") {
		if blk == "" {
			continue
		}
		lines := strings.SplitN(blk, "\n", 2)
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("帧不是 event/data 两行:\n%s", blk)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &data); err != nil {
			t.Fatalf("data 不是 JSON: %v\n%s", err, blk)
		}
		frames = append(frames, frame{event: strings.TrimPrefix(lines[0], "event: "), data: data})
	}
	return frames
}

func encodeStream(t *testing.T, c *Codec, events ...protocol.Event) []frame {
	t.Helper()
	ch := make(chan protocol.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	var buf bytes.Buffer
	if err := c.EncodeStream(&buf, ch); err != nil {
		t.Fatalf("EncodeStream: %v", err)
	}
	return parseFrames(t, buf.String())
}

func eventNames(frames []frame) []string {
	names := make([]string, len(frames))
	for i, f := range frames {
		names[i] = f.event
	}
	return names
}

func assertEventOrder(t *testing.T, frames []frame, want ...string) {
	t.Helper()
	got := eventNames(frames)
	if len(got) != len(want) {
		t.Fatalf("帧序 = %v\n期望 = %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("帧序 = %v\n期望 = %v", got, want)
		}
	}
	// data.type 必须与 event 行一致：实采转录里两者恒等，客户端两边都可能读。
	for _, f := range frames {
		if f.data["type"] != f.event {
			t.Errorf("event 行是 %q 而 data.type 是 %v", f.event, f.data["type"])
		}
	}
}

// 正文帧序照 resp-text 转录：created → in_progress → output_item.added →
// content_part.added → output_text.delta* → output_text.done → content_part.done
// → output_item.done → completed。正文比工具多一层 content_part，别少写。
func TestEncodeStreamTextWireFormat(t *testing.T) {
	frames := encodeStream(t, NewCodec(),
		protocol.Event{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "gpt-5.6-luna"},
		protocol.Event{Type: protocol.EvTextDelta, Text: "po"},
		protocol.Event{Type: protocol.EvTextDelta, Text: "ng"},
		protocol.Event{Type: protocol.EvUsage, Usage: &protocol.Usage{InputTokens: 14444, OutputTokens: 5, CacheReadTokens: 3840}},
		protocol.Event{Type: protocol.EvDone, StopReason: "stop"},
	)

	assertEventOrder(t, frames,
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.output_item.done", "response.completed",
	)

	// 逐字下发：两条 delta 各自成帧，不许在编码侧攒成一条。
	if frames[4].data["delta"] != "po" || frames[5].data["delta"] != "ng" {
		t.Errorf("正文分片被改写了: %v / %v", frames[4].data["delta"], frames[5].data["delta"])
	}
	if frames[6].data["text"] != "pong" {
		t.Errorf("output_text.done 的 text = %v, 期望拼全", frames[6].data["text"])
	}

	// sequence_number 全流连号，从 0 起（实采 102 帧无一例外）。客户端拿它判丢帧。
	for i, f := range frames {
		if got, ok := f.data["sequence_number"].(float64); !ok || int(got) != i {
			t.Fatalf("第 %d 帧的 sequence_number = %v", i, f.data["sequence_number"])
		}
	}

	// usage 只在终帧。
	final := frames[len(frames)-1].data["response"].(map[string]any)
	if final["status"] != "completed" {
		t.Errorf("终态 = %v", final["status"])
	}
	usage := final["usage"].(map[string]any)
	if usage["input_tokens"] != float64(14444) || usage["output_tokens"] != float64(5) {
		t.Errorf("usage 对不上: %v", usage)
	}
	// canonical 的 input 是毛值，Responses 的 input_tokens 也是毛值，直映；
	// total = 毛值 + 输出，Codex 拿它判压缩触发点。
	if usage["total_tokens"] != float64(14449) {
		t.Errorf("total_tokens = %v, 期望 14444+5", usage["total_tokens"])
	}
	details := usage["input_tokens_details"].(map[string]any)
	if details["cached_tokens"] != float64(3840) {
		t.Errorf("cached_tokens = %v, 期望 3840", details["cached_tokens"])
	}
	if len(final["output"].([]any)) != 1 {
		t.Errorf("终帧的 output 没列出正文 item: %v", final["output"])
	}
}

// 后来的 usage 快照按**非零字段**覆盖（protocol/event.go 的 EvUsage 约定）：某些
// 兼容上游末帧只报 `{"output_tokens":N}`，整结构体覆盖会把 input 清零，total 随之
// 低估，Codex 的压缩触发点被推后、先撞上游 400（#72）。
func TestEncodeStreamMergesPartialUsageSnapshots(t *testing.T) {
	frames := encodeStream(t, NewCodec(),
		protocol.Event{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		protocol.Event{Type: protocol.EvUsage, Usage: &protocol.Usage{InputTokens: 14444, CacheReadTokens: 3840}},
		protocol.Event{Type: protocol.EvTextDelta, Text: "pong"},
		protocol.Event{Type: protocol.EvUsage, Usage: &protocol.Usage{OutputTokens: 5}},
		protocol.Event{Type: protocol.EvDone, StopReason: "stop"},
	)
	final := frames[len(frames)-1].data["response"].(map[string]any)
	usage := final["usage"].(map[string]any)
	if usage["input_tokens"] != float64(14444) || usage["output_tokens"] != float64(5) {
		t.Errorf("usage = %v, 期望 input 14444 / output 5", usage)
	}
	if usage["total_tokens"] != float64(14449) {
		t.Errorf("total_tokens = %v, 期望 14449", usage["total_tokens"])
	}
	details := usage["input_tokens_details"].(map[string]any)
	if details["cached_tokens"] != float64(3840) {
		t.Errorf("cached_tokens 被后一份快照清掉了: %v", details)
	}
}

// 流以 response.completed 收尾，**不发 `data: [DONE]`**。那是 Chat Completions 的
// 收尾方式；三份 Responses 转录的最后一个字节都是 completed 帧。多发一行，严格
// 客户端会当成解不动的帧。
func TestEncodeStreamHasNoDoneSentinel(t *testing.T) {
	ch := make(chan protocol.Event, 1)
	ch <- protocol.Event{Type: protocol.EvTextDelta, Text: "hi"}
	close(ch)
	var buf bytes.Buffer
	if err := NewCodec().EncodeStream(&buf, ch); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "[DONE]") {
		t.Error("流里出现了 [DONE]")
	}
	if !strings.HasSuffix(buf.String(), "\n\n") {
		t.Error("流没有以空行收尾")
	}
}

// 声明成 custom 的工具，响应要还原成 custom_tool_call + 自由文本入参：
// CC 出口把 JS 源码包成了 `{"input":"…"}`，这里必须对称拆回来，否则 Codex 的
// exec 收到一段包着 JS 的 JSON，直接语法错。
func TestEncodeStreamRestoresCustomToolCall(t *testing.T) {
	c := NewCodec()
	if _, err := c.DecodeRequest(loadSample(t, "in-responses-tool-turn2"), true); err != nil {
		t.Fatal(err)
	}

	const js = `const r = await tools.exec_command({cmd:"ls"}); text(r.output)`
	wrapped, err := json.Marshal(map[string]string{"input": js})
	if err != nil {
		t.Fatal(err)
	}

	frames := encodeStream(t, c,
		protocol.Event{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		protocol.Event{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_x", ToolName: "exec"},
		protocol.Event{Type: protocol.EvToolArgsDelta, Index: 0, Text: string(wrapped)},
		protocol.Event{Type: protocol.EvToolCallEnd, Index: 0},
		protocol.Event{Type: protocol.EvDone, StopReason: "tool_calls"},
	)

	// 工具 item 没有 content_part 那一层（实采如此）。
	assertEventOrder(t, frames,
		"response.created", "response.in_progress",
		"response.output_item.added",
		"response.custom_tool_call_input.delta",
		"response.custom_tool_call_input.done",
		"response.output_item.done",
		"response.completed",
	)

	added := frames[2].data["item"].(map[string]any)
	if added["type"] != "custom_tool_call" {
		t.Errorf("item.type = %v, 期望 custom_tool_call", added["type"])
	}
	if added["call_id"] != "call_x" {
		t.Errorf("call_id = %v, 工具结果要靠它对回调用", added["call_id"])
	}
	if !strings.HasPrefix(added["id"].(string), "ctc_") {
		t.Errorf("item id = %v, 期望 ctc_ 前缀", added["id"])
	}

	// 核心断言：包装被拆掉了，客户端拿到的是原样 JS。
	if frames[3].data["delta"] != js {
		t.Errorf("delta = %q\n期望原样 JS = %q", frames[3].data["delta"], js)
	}
	if frames[4].data["input"] != js {
		t.Errorf("input.done = %q, 期望 %q", frames[4].data["input"], js)
	}
	if done := frames[5].data["item"].(map[string]any); done["input"] != js || done["status"] != "completed" {
		t.Errorf("output_item.done 的 item 不对: %v", done)
	}
}

// 同一份事件流，只因为请求里没把 exec 声明成 custom，就得走 function_call +
// JSON 入参那条路。这条用例钉的是「响应形态取决于请求声明」这个 seam 真的通着——
// 编码侧读不到 customTools 的话，两个子测试会编出一模一样的东西。
func TestEncodeStreamItemTypeFollowsRequestDeclaration(t *testing.T) {
	args := `{"input":"raw text"}`
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_x", ToolName: "exec"},
		{Type: protocol.EvToolArgsDelta, Index: 0, Text: args},
		{Type: protocol.EvToolCallEnd, Index: 0},
		{Type: protocol.EvDone, StopReason: "tool_calls"},
	}

	t.Run("声明成 function 就不拆包", func(t *testing.T) {
		c := NewCodec()
		if _, err := c.DecodeRequest([]byte(
			`{"model":"m","tools":[{"type":"function","name":"exec","parameters":{}}]}`), true); err != nil {
			t.Fatal(err)
		}
		frames := encodeStream(t, c, events...)
		assertEventOrder(t, frames,
			"response.created", "response.in_progress",
			"response.output_item.added",
			"response.function_call_arguments.delta",
			"response.function_call_arguments.done",
			"response.output_item.done",
			"response.completed",
		)
		if item := frames[2].data["item"].(map[string]any); item["type"] != "function_call" {
			t.Errorf("item.type = %v, 期望 function_call", item["type"])
		}
		// 真的只收一个 input 字符串参数的 JSON 工具：包装不该被误拆。
		if frames[3].data["delta"] != args {
			t.Errorf("delta = %q, JSON 工具的 arguments 被误拆了", frames[3].data["delta"])
		}
	})

	t.Run("声明成 custom 就拆包", func(t *testing.T) {
		c := NewCodec()
		if _, err := c.DecodeRequest([]byte(
			`{"model":"m","tools":[{"type":"custom","name":"exec"}]}`), true); err != nil {
			t.Fatal(err)
		}
		frames := encodeStream(t, c, events...)
		if frames[3].event != "response.custom_tool_call_input.delta" {
			t.Fatalf("帧序 = %v", eventNames(frames))
		}
		if frames[3].data["delta"] != "raw text" {
			t.Errorf("delta = %q, 期望拆出 raw text", frames[3].data["delta"])
		}
	})
}

// 上游没按我们发过去的形状回话（第三方中转重写 arguments、模型自作主张换结构）时，
// 拆不动就原样给出去，不报错也不吞掉——客户端至少还有得看。
func TestEncodeStreamPassesUnwrappableArgsThrough(t *testing.T) {
	cases := map[string]string{
		"根本不是 JSON":   `just some text`,
		"没有 input 键":  `{"cmd":"ls"}`,
		"input 不是字符串": `{"input":{"cmd":"ls"}}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			c := NewCodec()
			if _, err := c.DecodeRequest([]byte(`{"model":"m","tools":[{"type":"custom","name":"exec"}]}`), true); err != nil {
				t.Fatal(err)
			}
			frames := encodeStream(t, c,
				protocol.Event{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_x", ToolName: "exec"},
				protocol.Event{Type: protocol.EvToolArgsDelta, Index: 0, Text: args},
				protocol.Event{Type: protocol.EvToolCallEnd, Index: 0},
			)
			if frames[3].data["delta"] != args {
				t.Errorf("delta = %q, 期望原样 %q", frames[3].data["delta"], args)
			}
		})
	}
}

// 正文后面跟工具调用：正文 item 必须先 done，再开工具 item，output_index 依次 +1。
// 一个 item 没收口就开下一个，客户端按 output_index 组装出来的就是错位的。
func TestEncodeStreamClosesTextBeforeToolItem(t *testing.T) {
	c := NewCodec()
	if _, err := c.DecodeRequest([]byte(`{"model":"m","tools":[{"type":"custom","name":"exec"}]}`), true); err != nil {
		t.Fatal(err)
	}
	frames := encodeStream(t, c,
		protocol.Event{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		protocol.Event{Type: protocol.EvTextDelta, Text: "正在查"},
		protocol.Event{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_x", ToolName: "exec"},
		protocol.Event{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{"input":"ls"}`},
		protocol.Event{Type: protocol.EvToolCallEnd, Index: 0},
		protocol.Event{Type: protocol.EvDone, StopReason: "tool_calls"},
	)

	assertEventOrder(t, frames,
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.output_item.added",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done",
		"response.output_item.done",
		"response.completed",
	)

	if frames[7].data["output_index"] != float64(0) {
		t.Errorf("正文 item 的 output_index = %v, 期望 0", frames[7].data["output_index"])
	}
	if frames[8].data["output_index"] != float64(1) {
		t.Errorf("工具 item 的 output_index = %v, 期望 1", frames[8].data["output_index"])
	}
	if got := len(frames[12].data["response"].(map[string]any)["output"].([]any)); got != 2 {
		t.Errorf("终帧的 output 有 %d 项, 期望 2", got)
	}
}

// 上游 id 原样透传（口径层 v0.31），一个都没给时才补 `resp_`——前缀本身就是
// 「这个 id 是网关补的」的信号，排障时一眼能分。
func TestEncodeCarriesUpstreamResponseID(t *testing.T) {
	frames := encodeStream(t, NewCodec(),
		protocol.Event{Type: protocol.EvMessageStart, ID: "chatcmpl-Bx7q2Kd", Model: "m"},
		protocol.Event{Type: protocol.EvTextDelta, Text: "hi"},
	)
	if got := frames[0].data["response"].(map[string]any)["id"]; got != "chatcmpl-Bx7q2Kd" {
		t.Errorf("response.id = %v, 上游 id 该原样透传", got)
	}

	body, err := NewCodec().EncodeFullBody([]protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-Bx7q2Kd", Model: "m"},
		{Type: protocol.EvTextDelta, Text: "hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	if err := json.Unmarshal(body, &full); err != nil {
		t.Fatal(err)
	}
	if full["id"] != "chatcmpl-Bx7q2Kd" {
		t.Errorf("非流式 id = %v", full["id"])
	}
}

func TestEncodeFillsMissingResponseID(t *testing.T) {
	frames := encodeStream(t, NewCodec(), protocol.Event{Type: protocol.EvTextDelta, Text: "hi"})
	id, _ := frames[0].data["response"].(map[string]any)["id"].(string)
	if !strings.HasPrefix(id, "resp_") || len(id) <= len("resp_") {
		t.Errorf("补出来的 id = %q, 期望 resp_ 前缀且非空", id)
	}
}

// 非流式与流式共用一台状态机，聚合结果必须对得上——两套逻辑各写一遍必然漂移，
// 而非流式的样本远比流式少，漂了不容易发现。
func TestEncodeFullBodyMatchesStreamAggregate(t *testing.T) {
	c := NewCodec()
	if _, err := c.DecodeRequest([]byte(`{"model":"m","tools":[{"type":"custom","name":"exec"}]}`), true); err != nil {
		t.Fatal(err)
	}
	events := []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "gpt-5.6-luna"},
		{Type: protocol.EvTextDelta, Text: "查一下"},
		{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_x", ToolName: "exec"},
		{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{"input":"ls"}`},
		{Type: protocol.EvToolCallEnd, Index: 0},
		{Type: protocol.EvUsage, Usage: &protocol.Usage{InputTokens: 10, OutputTokens: 3}},
		{Type: protocol.EvDone, StopReason: "tool_calls"},
	}

	body, err := c.EncodeFullBody(events)
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	if err := json.Unmarshal(body, &full); err != nil {
		t.Fatal(err)
	}
	if full["object"] != "response" || full["status"] != "completed" {
		t.Errorf("非流式响体的骨架不对: object=%v status=%v", full["object"], full["status"])
	}
	output := full["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output 有 %d 项, 期望 2（正文 + 工具）", len(output))
	}
	msg := output[0].(map[string]any)
	if msg["type"] != "message" || msg["content"].([]any)[0].(map[string]any)["text"] != "查一下" {
		t.Errorf("正文 item 不对: %v", msg)
	}
	call := output[1].(map[string]any)
	if call["type"] != "custom_tool_call" || call["input"] != "ls" {
		t.Errorf("工具 item 不对（包装没拆？）: %v", call)
	}
}

// 被截断的回答有独立终态：混在 completed 里发，客户端会以为模型说完了。
func TestEncodeMarksTruncationIncomplete(t *testing.T) {
	frames := encodeStream(t, NewCodec(),
		protocol.Event{Type: protocol.EvMessageStart, ID: "x", Model: "m"},
		protocol.Event{Type: protocol.EvTextDelta, Text: "半句"},
		protocol.Event{Type: protocol.EvDone, StopReason: "length"},
	)
	last := frames[len(frames)-1]
	if last.event != "response.incomplete" {
		t.Fatalf("终帧是 %q, 期望 response.incomplete", last.event)
	}
	resp := last.data["response"].(map[string]any)
	if resp["status"] != "incomplete" {
		t.Errorf("status = %v", resp["status"])
	}
	if d, ok := resp["incomplete_details"].(map[string]any); !ok || d["reason"] != "max_output_tokens" {
		t.Errorf("incomplete_details = %v", resp["incomplete_details"])
	}
}

// 首字节之后才发现的上游错误只能走流内表达（此时状态码已经承诺出去了）。
// 发完 failed 就收手：后面再补一个 completed 会让客户端以为响应正常收尾。
func TestEncodeStreamSurfacesMidStreamError(t *testing.T) {
	frames := encodeStream(t, NewCodec(),
		protocol.Event{Type: protocol.EvMessageStart, ID: "x", Model: "m"},
		protocol.Event{Type: protocol.EvTextDelta, Text: "半句"},
		protocol.Event{Type: protocol.EvError, Status: 500, Message: "上游炸了"},
	)
	last := frames[len(frames)-1]
	if last.event != "response.failed" {
		t.Fatalf("帧序 = %v", eventNames(frames))
	}
	resp := last.data["response"].(map[string]any)
	if resp["status"] != "failed" {
		t.Errorf("status = %v", resp["status"])
	}
	if e, ok := resp["error"].(map[string]any); !ok || e["message"] != "上游炸了" {
		t.Errorf("error = %v", resp["error"])
	}
	for _, f := range frames {
		if f.event == "response.completed" {
			t.Error("发过 failed 之后还发了 completed")
		}
	}
}

// 上游流断在半截（没等到 EvToolCallEnd）：攒着的调用照样放出去。半个工具调用比
// 凭空消失强——客户端至少看得见调用意图。
func TestEncodeStreamFlushesTruncatedToolCall(t *testing.T) {
	c := NewCodec()
	if _, err := c.DecodeRequest([]byte(`{"model":"m","tools":[{"type":"custom","name":"exec"}]}`), true); err != nil {
		t.Fatal(err)
	}
	frames := encodeStream(t, c,
		protocol.Event{Type: protocol.EvMessageStart, ID: "x", Model: "m"},
		protocol.Event{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_x", ToolName: "exec"},
		protocol.Event{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{"input":"ls`},
	)
	var sawItem bool
	for _, f := range frames {
		if f.event == "response.output_item.done" {
			sawItem = true
		}
	}
	if !sawItem {
		t.Fatalf("半截的工具调用被吞了: %v", eventNames(frames))
	}
	if frames[len(frames)-1].event != "response.completed" {
		t.Errorf("流没有收尾: %v", eventNames(frames))
	}
}
