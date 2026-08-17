package openairesponses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件覆盖 Codex 压缩的全部三段，按下面的分节排：解码半边（portage-legacy#74 范围 1、2、4）
// 认出压缩 turn、改写成 summarizer、回带还原；合成半边（范围 3、5）产那一个 item
// 与静默期心跳；末尾是透传闸的判据。
//
// 手搭 fixture 而非 golden：真实压缩转录是 portage-legacy#73，仍未采（PO 2026-08-13 裁定先实现）。
// 这些用例钉的是我们自己的口径，钉不住「Codex 真的这么发包」——那笔账在 portage-legacy#73 不销。

func TestCompactionEnvelopeRoundTrip(t *testing.T) {
	const summary = "第一节：做了什么\n第二节：还差什么"
	env := encodeCompactionSummary(summary)
	if !strings.HasPrefix(env, envelopePrefix) {
		t.Fatalf("信封没带前缀: %q", env)
	}
	if strings.Contains(env, summary) {
		t.Error("摘要以明文躺在 encrypted_content 位上：那个位置客户端与日志都当密文待遇")
	}
	got, ok := decodeCompactionSummary(env)
	if !ok || got != summary {
		t.Fatalf("解不回来: %q, ok=%v", got, ok)
	}
}

func TestCompactionEnvelopeRejectsForeign(t *testing.T) {
	// 真 OpenAI 密文、别家网关的信封、被截断的 base64——三样都必须判成解不开，
	// 而不是解出一段乱码当摘要喂给上游。
	for _, in := range []string{
		"gAAAAABm7…真密文…",
		"ocx1:" + "5pGY6KaB",
		envelopePrefix + "!!!not-base64!!!",
		"",
	} {
		if got, ok := decodeCompactionSummary(in); ok {
			t.Errorf("%q 不该解得开，却得到 %q", in, got)
		}
	}
}

// 压缩 turn 要被认出来，并且改写成一次纯总结请求。
func TestDecodeCompactionTurnRewritesToSummarizer(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.6",
		"tool_choice": "auto",
		"text": {"verbosity": "high"},
		"reasoning": {"effort": "high"},
		"tools": [{"type": "custom", "name": "exec"}],
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "帮我改个 bug"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "改好了"}]},
			{"type": "compaction_trigger"}
		]
	}`)
	codec := NewCodec()
	req, err := codec.DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("解不动: %v", err)
	}
	if !codec.CompactionTurn() {
		t.Fatal("没认出这是压缩 turn")
	}
	if len(req.Tools) != 0 || req.ToolChoice.Mode != "" {
		t.Errorf("工具没剥干净: tools=%v choice=%+v", req.Tools, req.ToolChoice)
	}
	if _, ok := req.Extras["text"]; ok {
		t.Error("text（结构化输出/verbosity）没剥掉，摘要会被格式约束住")
	}
	// 推理不剥：压缩 turn 照样要推理，剥的只有工具与输出格式。档位从 v0.65 起提成
	// 一等字段 Request.Effort（reasoning 只剩 effort 时整个键被提空删掉），所以这里
	// 查的是那一格而不是 Extras。
	if req.Effort != "high" {
		t.Errorf("思考档位被误剥了: Effort=%q Extras[reasoning]=%v", req.Effort, req.Extras["reasoning"])
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != protocol.RoleUser || last.Content[0].Text != compactPrompt {
		t.Fatalf("末尾不是那条总结指令: %+v", last)
	}
	// trigger 本身不该在消息序列里留下任何痕迹。
	for _, m := range req.Messages[:len(req.Messages)-1] {
		for _, b := range m.Content {
			if strings.Contains(b.Text, ItemCompactionTrigger) {
				t.Error("compaction_trigger 漏进了消息内容")
			}
		}
	}
}

// 普通请求不该被误判成压缩 turn，也不该被剥工具。
func TestDecodeNonCompactionTurnUntouched(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.6",
		"tools": [{"type": "custom", "name": "exec"}],
		"input": [{"type": "message", "role": "user", "content": "你好"}]
	}`)
	codec := NewCodec()
	req, err := codec.DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("解不动: %v", err)
	}
	if codec.CompactionTurn() {
		t.Fatal("普通请求被判成了压缩 turn")
	}
	if len(req.Tools) != 1 {
		t.Errorf("工具被误剥: %v", req.Tools)
	}
	if len(req.Messages) != 1 {
		t.Errorf("凭空多出消息: %+v", req.Messages)
	}
}

// G2：回带的压缩 item 还原成一条带引导语的 user 消息。
func TestDecodeCompactionItemRestored(t *testing.T) {
	env := encodeCompactionSummary("上一轮的摘要正文")
	body := []byte(`{
		"model": "gpt-5.6",
		"input": [
			{"type": "message", "role": "user", "content": "继续"},
			{"type": "compaction", "id": "cmp_1", "encrypted_content": "` + env + `"}
		]
	}`)
	codec := NewCodec()
	req, err := codec.DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("解不动: %v", err)
	}
	if codec.CompactionTurn() {
		t.Fatal("回带的压缩产物被误判成了新的压缩 turn——那会让每一轮都变成总结")
	}
	if len(codec.CompactionDrops()) != 0 {
		t.Errorf("解得开却记了丢弃: %v", codec.CompactionDrops())
	}
	text := lastText(t, req)
	if !strings.HasPrefix(text, summaryPrefix) {
		t.Errorf("引导语没套上，Codex 侧认不出这是摘要:\n%s", text)
	}
	if !strings.Contains(text, "上一轮的摘要正文") {
		t.Errorf("摘要正文没还原:\n%s", text)
	}
}

// 解不开的密文降级成占位，并登记一笔丢弃。
func TestDecodeCompactionItemOpaqueDegrades(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.6",
		"input": [
			{"type": "compaction_summary", "encrypted_content": "gAAAAAB_opaque_blob"}
		]
	}`)
	codec := NewCodec()
	req, err := codec.DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("解不动: %v", err)
	}
	if got := lastText(t, req); got != opaqueCompactionNote {
		t.Errorf("没降级成占位: %q", got)
	}
	if drops := codec.CompactionDrops(); len(drops) != 1 || drops[0] != itemCompactionSummary {
		t.Errorf("丢弃没登记: %v", drops)
	}
}

// context_compaction 不带密文时是 codex-rs 本地压缩的标记，没有摘要可还原，
// 不该凭空插一条占位消息。
func TestDecodeContextCompactionMarkerSkipped(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.6",
		"input": [
			{"type": "message", "role": "user", "content": "继续"},
			{"type": "context_compaction"}
		]
	}`)
	codec := NewCodec()
	req, err := codec.DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("解不动: %v", err)
	}
	if got := lastText(t, req); got != "继续" {
		t.Errorf("本地压缩标记被当成了内容: %q", got)
	}
	if len(codec.CompactionDrops()) != 0 {
		t.Errorf("不带密文的标记不该记丢弃: %v", codec.CompactionDrops())
	}
}

// ---- 合成半边（portage-legacy#74 范围 3、5）----

// compactionCodec 造一个已经处在合成模式的 codec，省去每个用例都先解一遍请求。
func compactionCodec() *Codec { return &Codec{compaction: true} }

func summaryEvents(text string, stop string) []protocol.Event {
	return []protocol.Event{
		{Type: protocol.EvMessageStart, ID: "msg_1", Model: "claude-sonnet-5"},
		{Type: protocol.EvTextDelta, Text: text},
		{Type: protocol.EvUsage, Usage: &protocol.Usage{InputTokens: 100, OutputTokens: 20}},
		{Type: protocol.EvDone, StopReason: stop},
	}
}

// 正常收尾：恰好一个 compaction item，且摘要正文不以普通消息形态重复下发。
func TestEncodeCompactionSynthesizesExactlyOneItem(t *testing.T) {
	frames := encodeStream(t, compactionCodec(), summaryEvents("这是摘要", "stop")...)

	var items []map[string]any
	for _, f := range frames {
		if f.event == "response.output_item.done" {
			items = append(items, f.data["item"].(map[string]any))
		}
		if strings.HasPrefix(f.event, "response.output_text") || f.event == "response.content_part.added" {
			t.Errorf("摘要正文以普通消息形态漏了出去（%s）：Codex 会把它连同 item 里那份记两遍", f.event)
		}
	}
	if len(items) != 1 {
		t.Fatalf("要恰好一个 output item，得到 %d 个：0 个会让 Codex 直接 Fatal", len(items))
	}
	if items[0]["type"] != itemCompaction {
		t.Fatalf("item 类型不对: %v", items[0]["type"])
	}
	got, ok := decodeCompactionSummary(items[0]["encrypted_content"].(string))
	if !ok || got != "这是摘要" {
		t.Fatalf("信封里不是那段摘要: %q ok=%v", got, ok)
	}

	last := frames[len(frames)-1]
	if last.event != "response.completed" {
		t.Fatalf("终帧不是 completed: %s", last.event)
	}
	// 终帧的 output 也要带着那个 item：Codex 有从终帧收 output 的实现。
	out := last.data["response"].(map[string]any)["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("终帧 output 里要有那一个 item，得到 %d", len(out))
	}
}

// 截断 / 内容过滤 / 改调工具 / 空摘要都绝不产 item，而且都不许发成 completed——
// 「completed + 零 item」正是 portage-legacy#71 要杀的静默 Fatal 形态。
//
// 前几支合起来把 canonical 的非 stop 停因占全了：漏掉任何一个，都等于把「上游没写完」
// 当成「上游写完了」，一段半截摘要会被 Codex 当成替换历史装回去。
func TestEncodeCompactionRefusesPartialSummary(t *testing.T) {
	cases := []struct {
		name       string
		events     []protocol.Event
		wantFinal  string
		wantReason string
	}{
		{"截断", summaryEvents("写了一半", "length"), "response.incomplete", "max_output_tokens"},
		{"内容过滤", summaryEvents("写了一半", "content_filter"), "response.incomplete", "content_filter"},
		{"空摘要", summaryEvents("", "stop"), "response.failed", ""},
		// 上游写了个开头就改去调工具：工具事件被吞掉（见下一条用例），但停在那儿的
		// 半截正文同样不是一份摘要。rewriteAsSummarizer 剥了 tools，合规上游到不了
		// 这里；自带服务端工具的兼容网关到得了。
		{"改去调工具", []protocol.Event{
			{Type: protocol.EvMessageStart, ID: "msg_1"},
			{Type: protocol.EvTextDelta, Text: "我先查一下"},
			{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_1", ToolName: "web_search"},
			{Type: protocol.EvToolCallEnd, Index: 0},
			{Type: protocol.EvDone, StopReason: "tool_calls"},
		}, "response.failed", ""},
		// 断流是这批里唯一在 wire 上看不出破绽的一种：解码侧为了给下游一个合法取值，
		// 会把断流兜成 stop_reason=stop（anthropic 的 emitDone、openaicc 的 finish），
		// 只有 Truncated 位分得开。漏过去的话，半截摘要会带着 completed 装回历史。
		{"上游断流兜底收尾", []protocol.Event{
			{Type: protocol.EvMessageStart, ID: "msg_1"},
			{Type: protocol.EvTextDelta, Text: "写了一半"},
			{Type: protocol.EvDone, StopReason: "stop", Truncated: true},
		}, "response.failed", ""},
		// 连兜底收尾都没有（调用方直接喂事件、或将来某个解码器不兜）。
		{"压根没有收尾事件", []protocol.Event{
			{Type: protocol.EvMessageStart, ID: "msg_1"},
			{Type: protocol.EvTextDelta, Text: "写了一半"},
		}, "response.failed", ""},
		{"上游流内报错", []protocol.Event{
			{Type: protocol.EvMessageStart, ID: "msg_1"},
			{Type: protocol.EvTextDelta, Text: "写了一半"},
			{Type: protocol.EvError, Status: 500, Message: "上游炸了"},
		}, "response.failed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := encodeStream(t, compactionCodec(), tc.events...)
			for _, f := range frames {
				if f.event == "response.output_item.done" {
					t.Fatal("产出了 compaction item：残缺摘要会被 Codex 当成替换历史装回去，等于永久删掉前半程")
				}
				if f.event == "response.completed" {
					t.Fatal("发了 completed 却没有 item——这正是要杀的静默 Fatal 形态")
				}
			}
			last := frames[len(frames)-1]
			if last.event != tc.wantFinal {
				t.Fatalf("终帧要 %s，得到 %s", tc.wantFinal, last.event)
			}
			if tc.wantReason != "" {
				details := last.data["response"].(map[string]any)["incomplete_details"].(map[string]any)
				if details["reason"] != tc.wantReason {
					t.Errorf("incomplete_details.reason 要 %s，得到 %v", tc.wantReason, details["reason"])
				}
			}
		})
	}
}

// 上游在 summarizer turn 里照样调工具时，那次调用不许占用 output。
func TestEncodeCompactionSuppressesToolItems(t *testing.T) {
	frames := encodeStream(t, compactionCodec(),
		protocol.Event{Type: protocol.EvMessageStart, ID: "msg_1"},
		protocol.Event{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_1", ToolName: "exec"},
		protocol.Event{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{"cmd":"ls"}`},
		protocol.Event{Type: protocol.EvToolCallEnd, Index: 0},
		protocol.Event{Type: protocol.EvTextDelta, Text: "摘要"},
		protocol.Event{Type: protocol.EvDone, StopReason: "stop"},
	)
	var items int
	for _, f := range frames {
		if f.event == "response.output_item.done" {
			items++
			if f.data["item"].(map[string]any)["type"] != itemCompaction {
				t.Error("工具 item 混进了 output，「恰好一个」当场不成立")
			}
		}
	}
	if items != 1 {
		t.Fatalf("要恰好一个 item，得到 %d", items)
	}
}

// 非流式压缩 turn：产不出 item 时明着报错，不回一份空 output 的 completed。
func TestEncodeFullBodyCompaction(t *testing.T) {
	body, err := compactionCodec().EncodeFullBody(summaryEvents("摘要正文", "stop"))
	if err != nil {
		t.Fatalf("EncodeFullBody: %v", err)
	}
	var resp struct {
		Status string `json:"status"`
		Output []struct {
			Type      string `json:"type"`
			Encrypted string `json:"encrypted_content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "completed" || len(resp.Output) != 1 || resp.Output[0].Type != itemCompaction {
		t.Fatalf("非流式没合成出那一个 item: %s", body)
	}
	if got, ok := decodeCompactionSummary(resp.Output[0].Encrypted); !ok || got != "摘要正文" {
		t.Fatalf("信封不对: %q", got)
	}

	if _, err := compactionCodec().EncodeFullBody(summaryEvents("半截", "length")); err == nil {
		t.Fatal("截断的非流式压缩 turn 该报错，不该回一份空 output")
	}
	truncated := summaryEvents("半截", "stop")
	truncated[len(truncated)-1].Truncated = true
	if _, err := compactionCodec().EncodeFullBody(truncated); err == nil {
		t.Fatal("断流兜底收尾的非流式压缩 turn 该报错：wire 上它与正常收尾同形")
	}
}

// 静默期心跳：被吞掉的那些增量期间要有字节下行，且必须是 SSE 注释行
// （合规解析器忽略它，不会被当成事件）。
//
// 思考增量与正文增量同等对待：rewriteAsSummarizer 有意留着 reasoning，开思考的上游
// 会先想上几十秒才写第一个摘要 token——那段静默通常是整轮里最长的一截。
func TestEncodeCompactionHeartbeat(t *testing.T) {
	var buf bytes.Buffer
	clock := time.Unix(0, 0)
	enc := compactionCodec().newStreamEncoder(&buf)
	enc.now = func() time.Time { return clock }

	feed := func(ev protocol.Event, advance time.Duration) {
		clock = clock.Add(advance)
		if err := enc.event(ev); err != nil {
			t.Fatal(err)
		}
	}
	think := func(text string, advance time.Duration) {
		feed(protocol.Event{Type: protocol.EvThinkingDelta, Text: text, Channel: protocol.ThinkingBody}, advance)
	}
	text := func(s string, advance time.Duration) {
		feed(protocol.Event{Type: protocol.EvTextDelta, Text: s}, advance)
	}
	if err := enc.event(protocol.Event{Type: protocol.EvMessageStart, ID: "msg_1"}); err != nil {
		t.Fatal(err)
	}
	think("嗯", time.Second)         // 刚开流不久，不该发
	think("想", heartbeatInterval)   // 够一个间隔，发一次
	think("完", heartbeatInterval/2) // 不够，不发
	text("a", heartbeatInterval)    // 思考转正文，又够一个间隔，再发一次
	text("b", heartbeatInterval/2)  // 不够，不发
	text("c", heartbeatInterval)    // 第三次
	if err := enc.event(protocol.Event{Type: protocol.EvDone, StopReason: "stop"}); err != nil {
		t.Fatal(err)
	}
	if err := enc.finish(); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if n := strings.Count(out, "\n\n: portage"); n != 3 {
		t.Errorf("心跳没独立成帧（要顶在帧首、自成一块）: %q", out)
	}
	if n := strings.Count(out, ": portage compaction in progress\n\n"); n != 3 {
		t.Errorf("心跳发了 %d 次，期望 3 次", n)
	}
	if strings.Contains(out, "abc") {
		t.Error("摘要正文漏进了下行字节")
	}
	if strings.Contains(out, "嗯想完") {
		t.Error("思考正文漏进了下行字节")
	}
}

// ---- 透传闸判据 ----
//
// HasCompactionTrigger 只服务透传那半边（server 的 rejectCompaction）：转换路径靠
// DecodeRequest 认 trigger，不扫字节。判据本身记在展开层 §7.6。
func TestHasCompactionTrigger(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "尾部带 trigger（Codex 压缩 turn 的实际形态）",
			body: `{"model":"gpt-5","input":[{"type":"message","role":"user","content":"hi"},{"type":"compaction_trigger"}]}`,
			want: true,
		},
		{
			// 位置不当判据：opencodex 见到的是尾项，但没有哪条协议承诺它只能在尾部。
			name: "trigger 不在尾部照样算",
			body: `{"input":[{"type":"compaction_trigger"},{"type":"message","role":"user","content":"hi"}]}`,
			want: true,
		},
		{
			name: "普通 turn",
			body: `{"model":"gpt-5","input":[{"type":"message","role":"user","content":"hi"}]}`,
			want: false,
		},
		{
			name: "input 是字符串",
			body: `{"input":"hello"}`,
			want: false,
		},
		{
			name: "没有 input",
			body: `{"model":"gpt-5"}`,
			want: false,
		},
		{
			// 解不动只让那一项落空：混进来的字符串元素不该把后面的 trigger 遮掉。
			name: "数组里混了解不动的元素",
			body: `{"input":["x",{"type":"compaction_trigger"}]}`,
			want: true,
		},
		{
			name: "整体不是 JSON",
			body: `not json`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasCompactionTrigger([]byte(tc.body)); got != tc.want {
				t.Fatalf("HasCompactionTrigger = %v, 期望 %v", got, tc.want)
			}
		})
	}
}

func lastText(t *testing.T, req *protocol.Request) string {
	t.Helper()
	if len(req.Messages) == 0 {
		t.Fatal("消息序列是空的")
	}
	last := req.Messages[len(req.Messages)-1]
	if len(last.Content) == 0 {
		t.Fatal("末条消息没有内容块")
	}
	return last.Content[len(last.Content)-1].Text
}
