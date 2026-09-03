package openairesponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件测 Responses 的**解码**侧（portage-legacy#80）。
//
// 分工：golden 驱动的用例（TestGoldenDecode*）钉「真实上游字节解出来是什么」，
// 手搭 fixture 的用例钉真实样本覆盖不到的形态——function_call 流（九份转录里一份
// 都没有，线上 Codex 只用 custom 工具）、incomplete/failed 终帧、断流兜底，以及
// 整个非流式路径（§9.4 缺口②）。手搭的是**测试输入**不是样本，不进 golden 库。

// readGoldenResponse 读真实上游转录。样本没采就跳过，同 compaction_golden_test.go 的纪律。
func readGoldenResponse(t *testing.T, sample string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(compactionGoldenDir, sample, "response.raw"))
	if err != nil {
		t.Skipf("样本尚未采集：%s（见 testdata/golden/README.md）", sample)
	}
	return raw
}

// goldenExpect 读 meta.json 里独立核算过的 expect 块，用它当断言的事实源，
// 而不是把数字抄进测试——抄一遍就多一份会漂的副本。
type goldenExpect struct {
	Model            string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	StopReason       string
}

func readGoldenExpect(t *testing.T, sample string) goldenExpect {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(compactionGoldenDir, sample, "meta.json"))
	if err != nil {
		t.Skipf("样本尚未采集：%s", sample)
	}
	var meta struct {
		Expect goldenExpect `json:"expect"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		t.Fatalf("meta.json 解不动: %v", err)
	}
	return meta.Expect
}

func collect(t *testing.T, raw []byte) []protocol.Event {
	t.Helper()
	c := NewCodec()
	ch, err := c.DecodeStream(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	var events []protocol.Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func lastUsage(events []protocol.Event) *protocol.Usage {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == protocol.EvUsage {
			return events[i].Usage
		}
	}
	return nil
}

func typesOf(events []protocol.Event) []protocol.EventType {
	out := make([]protocol.EventType, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Type)
	}
	return out
}

// TestGoldenDecodeText：纯文本 happy-path 的整条事件序。
func TestGoldenDecodeText(t *testing.T) {
	const sample = "responses-stream-text"
	events := collect(t, readGoldenResponse(t, sample))
	want := readGoldenExpect(t, sample)

	if len(events) < 4 {
		t.Fatalf("事件太少: %v", typesOf(events))
	}
	if events[0].Type != protocol.EvMessageStart {
		t.Fatalf("首个事件不是 MessageStart: %v", typesOf(events))
	}
	if events[0].Model != want.Model {
		t.Errorf("Model = %q, want %q", events[0].Model, want.Model)
	}
	if !strings.HasPrefix(events[0].ID, "resp_") {
		t.Errorf("响应 id 没带过来: %q", events[0].ID)
	}

	// created 与 completed 之间只该有正文增量：content_part.* / output_text.done
	// 这些结构信号在 canonical 里没有对应事件，混进来就是凭空造内容。
	var text strings.Builder
	for _, ev := range events[1 : len(events)-2] {
		if ev.Type != protocol.EvTextDelta {
			t.Fatalf("正文段混进了 %v: %v", ev.Type, typesOf(events))
		}
		text.WriteString(ev.Text)
	}
	if text.String() != "pong" {
		t.Errorf("正文 = %q, want %q", text.String(), "pong")
	}

	u := lastUsage(events)
	if u == nil {
		t.Fatal("没放出 usage：call_logs 的 token 数会恒为零")
	}
	if u.InputTokens != want.InputTokens || u.OutputTokens != want.OutputTokens ||
		u.CacheReadTokens != want.CacheReadTokens || u.CacheWriteTokens != want.CacheWriteTokens {
		t.Errorf("usage = %+v, want in=%d out=%d cacheR=%d cacheW=%d", *u,
			want.InputTokens, want.OutputTokens, want.CacheReadTokens, want.CacheWriteTokens)
	}

	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "stop" || last.Truncated {
		t.Errorf("终事件 = %+v, want EvDone{stop, 未截断}", last)
	}
}

// TestGoldenDecodeToolTurn1：custom 工具那轮。三件事一起钉：
// reasoning 静默跳过、custom 入参整条包装成 JSON、停因判成 tool_calls。
func TestGoldenDecodeToolTurn1(t *testing.T) {
	const sample = "responses-stream-tool-turn1"
	events := collect(t, readGoldenResponse(t, sample))
	want := readGoldenExpect(t, sample)

	// 推理一个事件都不该产（口径层 v0.10：跨协议只能丢、不得伪造）。真实样本里
	// reasoning item 的 .added/.done 各带一串上千字符的密文，漏出去客户端会当正文渲染。
	for i, ev := range events {
		if ev.Type == protocol.EvThinkingDelta {
			t.Fatalf("事件 %d 是 ThinkingDelta：推理被伪造出来了", i)
		}
		if strings.Contains(ev.Text, "gAAAAAB") {
			t.Fatalf("事件 %d 的正文里混进了 encrypted_content 密文", i)
		}
	}

	var text strings.Builder
	var starts, ends, argsDeltas int
	var args strings.Builder
	var toolID, toolName string
	for _, ev := range events {
		switch ev.Type {
		case protocol.EvTextDelta:
			text.WriteString(ev.Text)
		case protocol.EvToolCallStart:
			starts++
			toolID, toolName = ev.ToolID, ev.ToolName
			if !ev.ArgsIsJSON {
				t.Error("ToolCallStart.ArgsIsJSON 为 false：响应侧不变式是入参一律 JSON")
			}
		case protocol.EvToolArgsDelta:
			argsDeltas++
			args.WriteString(ev.Text)
		case protocol.EvToolCallEnd:
			ends++
		}
	}

	if text.String() != "I’m reading only the first line of the specified file." {
		t.Errorf("正文 = %q", text.String())
	}
	if starts != 1 || ends != 1 {
		t.Errorf("工具调用 Start/End = %d/%d, want 1/1", starts, ends)
	}
	if toolName != "exec" || !strings.HasPrefix(toolID, "call_") {
		t.Errorf("工具标识 = %q/%q，ToolID 应取 call_id 而非 item id", toolName, toolID)
	}
	// 51 片 custom_tool_call_input.delta 只放出**一条** ArgsDelta：整条包装是
	// 有意为之（flushCustom），代价是丢掉分片节奏。
	if argsDeltas != 1 {
		t.Errorf("ArgsDelta 条数 = %d, want 1（custom 入参整条包装）", argsDeltas)
	}
	var wrapped map[string]string
	if err := json.Unmarshal([]byte(args.String()), &wrapped); err != nil {
		t.Fatalf("入参不是合法 JSON，CC/A 客户端解不动: %v", err)
	}
	if !strings.Contains(wrapped[protocol.CustomToolArgsKey], "tools.exec_command") {
		t.Errorf("包装后的入参丢了原文: %.80q", wrapped[protocol.CustomToolArgsKey])
	}

	u := lastUsage(events)
	if u == nil || u.ReasoningTokens != want.ReasoningTokens {
		t.Errorf("ReasoningTokens = %v, want %d（思考量只能靠 usage 带走）", u, want.ReasoningTokens)
	}
	if u != nil && u.InputTokens != want.InputTokens {
		t.Errorf("InputTokens = %d, want %d", u.InputTokens, want.InputTokens)
	}

	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "tool_calls" {
		t.Errorf("终事件 = %+v, want EvDone{tool_calls}——Responses 不发 finish_reason，"+
			"停因只能由 output 里有没有工具项判出来", last)
	}
}

// TestGoldenDecodeParallelTurn1：code-mode 并行那轮。线上仍只有一个 custom_tool_call
// （并行在入参那段 JS 里），所以形态与 tool-turn1 同构；这里钉的是 usage 与停因。
func TestGoldenDecodeParallelTurn1(t *testing.T) {
	const sample = "responses-stream-parallel-turn1"
	events := collect(t, readGoldenResponse(t, sample))
	want := readGoldenExpect(t, sample)

	u := lastUsage(events)
	if u == nil {
		t.Fatal("没放出 usage")
	}
	if u.InputTokens != want.InputTokens || u.OutputTokens != want.OutputTokens ||
		u.ReasoningTokens != want.ReasoningTokens {
		t.Errorf("usage = %+v, want in=%d out=%d reasoning=%d", *u,
			want.InputTokens, want.OutputTokens, want.ReasoningTokens)
	}
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "tool_calls" {
		t.Errorf("终事件 = %+v, want EvDone{tool_calls}", last)
	}
}

// TestGoldenDecodeToolTurn2：工具结果回带之后的那轮，output 里没有工具项，
// 停因必须是 stop——sawTool 的状态不该跨轮泄漏。
func TestGoldenDecodeToolTurn2(t *testing.T) {
	const sample = "responses-stream-tool-turn2"
	events := collect(t, readGoldenResponse(t, sample))

	for _, ev := range events {
		if ev.Type == protocol.EvToolCallStart {
			t.Fatal("这一轮不该有工具调用")
		}
	}
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "stop" || last.Truncated {
		t.Errorf("终事件 = %+v, want EvDone{stop, 未截断}", last)
	}
}

// TestGoldenDecodeParallelTurn2：并行样本的回带轮，性质同 tool-turn2——五份
// 入库转录至此每份都有解码用例吃过，§9.4 表里「五份真实上游转录」才算数字属实。
func TestGoldenDecodeParallelTurn2(t *testing.T) {
	const sample = "responses-stream-parallel-turn2"
	events := collect(t, readGoldenResponse(t, sample))
	want := readGoldenExpect(t, sample)

	for _, ev := range events {
		if ev.Type == protocol.EvToolCallStart {
			t.Fatal("这一轮不该有工具调用")
		}
	}
	if u := lastUsage(events); u == nil || u.InputTokens != want.InputTokens {
		t.Errorf("usage 没对上 expect：got %+v, want in=%d", u, want.InputTokens)
	}
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "stop" || last.Truncated {
		t.Errorf("终事件 = %+v, want EvDone{stop, 未截断}", last)
	}
}

// TestGoldenDecodeReasoningSummaryBecomesThinking：带 reasoning_summary_* 四种帧的那份
// 转录里，摘要正文**必须**变成 EvThinkingDelta（口径层 v0.62 推翻了原先 v0.10 的
// 「一律丢」），而 encrypted_content 一个字节都不许进事件流。
//
// 「恰好一次」是这条断言的重点：同一段摘要在上游流里出现三遍——summary_text.delta 的
// delta、summary_text.done 的 text、output_item.done 里那份 summary[]。三处都收就会把
// 摘要发三遍，到出口物化成三段重复的推理内容。
func TestGoldenDecodeReasoningSummaryBecomesThinking(t *testing.T) {
	const sample = "responses-stream-reasoning-turn1"
	raw := readGoldenResponse(t, sample)

	// 先确认这份样本里确实有摘要帧，否则这条断言是空跑。
	if !bytes.Contains(raw, []byte("response.reasoning_summary_text.delta")) {
		t.Skip("这份样本没有 reasoning_summary 帧，断言无的放矢")
	}
	var summary string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var f struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal([]byte(line[6:]), &f) == nil &&
			f.Type == "response.reasoning_summary_text.delta" && f.Delta != "" {
			summary = f.Delta
			break
		}
	}
	if summary == "" {
		t.Skip("样本里的摘要增量为空")
	}

	var thinking []protocol.Event
	for i, ev := range collect(t, raw) {
		if ev.Type == protocol.EvThinkingDelta {
			thinking = append(thinking, ev)
			continue
		}
		if strings.Contains(ev.Text, summary) {
			t.Fatalf("事件 %d 是 %v，正文里却混进了推理摘要: %.60q", i, ev.Type, ev.Text)
		}
	}

	if len(thinking) != 1 {
		t.Fatalf("摘要该恰好产 1 条 ThinkingDelta，实得 %d 条", len(thinking))
	}
	if thinking[0].Text != summary {
		t.Errorf("ThinkingDelta 正文 = %.60q，样本里的摘要增量 = %.60q", thinking[0].Text, summary)
	}
	if thinking[0].Channel != protocol.ThinkingSummary {
		t.Errorf("通道 = %q，摘要帧该走 ThinkingSummary", thinking[0].Channel)
	}

	// 密文封装（reasoning item 的 encrypted_content）不许出现在任何事件的正文里。
	for i, ev := range collect(t, raw) {
		if strings.Contains(ev.Text, "gAAAAAB") {
			t.Fatalf("事件 %d 的正文里混进了 encrypted_content 密文: %.60q", i, ev.Text)
		}
	}
}

// TestDecodeReasoningTextStreamIsBody：reasoning_text.delta（推理**正文**流）走
// ThinkingBody 通道，与摘要流分开。
//
// 手搭 fixture：九份真实转录里一次都没出现过这种帧（线上两条 R 上游只回摘要）。
func TestDecodeReasoningTextStreamIsBody(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`,
		`data: {"type":"response.reasoning_text.delta","output_index":0,"delta":"正文推理"}`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"摘要"}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5","status":"completed","output":[]}}`,
	)

	var got []protocol.Event
	for _, ev := range collect(t, raw) {
		if ev.Type == protocol.EvThinkingDelta {
			got = append(got, ev)
		}
	}
	if len(got) != 2 {
		t.Fatalf("该产 2 条 ThinkingDelta，实得 %d 条", len(got))
	}
	if got[0].Text != "正文推理" || got[0].Channel != protocol.ThinkingBody {
		t.Errorf("正文流 = %q/%q，期望 \"正文推理\"/ThinkingBody", got[0].Text, got[0].Channel)
	}
	if got[1].Text != "摘要" || got[1].Channel != protocol.ThinkingSummary {
		t.Errorf("摘要流 = %q/%q，期望 \"摘要\"/ThinkingSummary", got[1].Text, got[1].Channel)
	}
}

// TestDecodeFullBodyReasoningSummary：非流式的推理文本只能从 output 里的 reasoning
// 项的 summary[] 拿，多段逐段放出。
func TestDecodeFullBodyReasoningSummary(t *testing.T) {
	body := []byte(`{"id":"resp_1","model":"gpt-5","status":"completed","output":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAAABsecret","summary":[
			{"type":"summary_text","text":"**第一段**"},
			{"type":"summary_text","text":"**第二段**"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"答案"}]}]}`)

	events, err := (&Codec{}).DecodeFullBody(body)
	if err != nil {
		t.Fatalf("DecodeFullBody: %v", err)
	}

	var thinking []string
	for _, ev := range events {
		if ev.Type == protocol.EvThinkingDelta {
			if ev.Channel != protocol.ThinkingSummary {
				t.Errorf("通道 = %q，期望 ThinkingSummary", ev.Channel)
			}
			thinking = append(thinking, ev.Text)
		}
		if strings.Contains(ev.Text, "gAAAAAB") {
			t.Fatalf("密文漏进了事件正文: %.60q", ev.Text)
		}
	}
	if len(thinking) != 2 || thinking[0] != "**第一段**" || thinking[1] != "**第二段**" {
		t.Fatalf("推理段落 = %q，期望两段", thinking)
	}
}

// --- 以下用手搭 fixture，覆盖真实样本到不了的形态 ---

func sseFrames(frames ...string) []byte {
	var buf strings.Builder
	for _, f := range frames {
		buf.WriteString(f)
		buf.WriteString("\n\n")
	}
	return []byte(buf.String())
}

// TestDecodeFunctionCallStream：function 形态的工具流。
//
// 九份真实转录里**一份都没有**（线上 Codex 只声明 custom 工具），而 CC→R / A→R 两条
// 路径发出去的工具声明全是 function 形态，回来的就是这个形状。所以它只能手搭。
//
// 钉住两件事：Start 的 call_id 与 name 只能从 output_item.added 上取（增量帧只带
// item_id，实采可证），以及 function 入参**逐片透传**、不像 custom 那样攒起来。
func TestDecodeFunctionCallStream(t *testing.T) {
	raw := sseFrames(
		`event: response.created`+"\n"+
			`data: {"type":"response.created","response":{"id":"resp_x","model":"gpt-5.6"}}`,
		`event: response.output_item.added`+"\n"+
			`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"get_weather","arguments":""}}`,
		`event: response.function_call_arguments.delta`+"\n"+
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"city\":"}`,
		`event: response.function_call_arguments.delta`+"\n"+
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"\"SH\"}"}`,
		`event: response.output_item.done`+"\n"+
			`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"get_weather","arguments":"{\"city\":\"SH\"}"}}`,
		`event: response.completed`+"\n"+
			`data: {"type":"response.completed","response":{"id":"resp_x","model":"gpt-5.6","status":"completed","usage":{"input_tokens":10,"output_tokens":3}}}`,
	)
	events := collect(t, raw)

	var start *protocol.Event
	var frags []string
	var ends int
	for i := range events {
		switch events[i].Type {
		case protocol.EvToolCallStart:
			start = &events[i]
		case protocol.EvToolArgsDelta:
			frags = append(frags, events[i].Text)
		case protocol.EvToolCallEnd:
			ends++
		}
	}
	if start == nil {
		t.Fatal("没放出 ToolCallStart")
	}
	if start.ToolID != "call_abc" || start.ToolName != "get_weather" || !start.ArgsIsJSON {
		t.Errorf("Start = %+v, want call_abc/get_weather/JSON", *start)
	}
	if start.Index != 0 {
		t.Errorf("Index = %d, want 0（原样用 output_index）", start.Index)
	}
	if len(frags) != 2 {
		t.Errorf("ArgsDelta 条数 = %d, want 2（function 入参逐片透传，保住上游节奏）", len(frags))
	}
	if got := strings.Join(frags, ""); got != `{"city":"SH"}` {
		t.Errorf("入参拼起来 = %q", got)
	}
	if ends != 1 {
		t.Errorf("ToolCallEnd = %d, want 1", ends)
	}
	if last := events[len(events)-1]; last.StopReason != "tool_calls" {
		t.Errorf("停因 = %q, want tool_calls", last.StopReason)
	}
}

// TestDecodeIncompleteIsLength：截断终帧映成 length。
func TestDecodeIncompleteIsLength(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"half"}`,
		`data: {"type":"response.incomplete","response":{"id":"r","model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":7,"output_tokens":2}}}`,
	)
	events := collect(t, raw)
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "length" {
		t.Errorf("终事件 = %+v, want EvDone{length}", last)
	}
	if last.Truncated {
		t.Error("Truncated 为真：incomplete 是上游明说的收尾，不是断流")
	}
	if u := lastUsage(events); u == nil || u.InputTokens != 7 {
		t.Error("incomplete 帧上的 usage 没收：它与 completed 一样是终帧")
	}
}

// TestDecodeIncompleteContentFilterMapsAcross：content_filter 是 incomplete_details
// 里另一个有 canonical 对应取值的理由，不能与上游自造词一起兜成 stop。
func TestDecodeIncompleteContentFilterMapsAcross(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.incomplete","response":{"id":"r","model":"m","status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`,
	)
	events := collect(t, raw)
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "content_filter" {
		t.Errorf("终事件 = %+v, want EvDone{content_filter}", last)
	}
}

// TestDecodeFailedBecomesError：response.failed 转 EvError，且不带上任何连接信息。
func TestDecodeFailedBecomesError(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"server_error","message":"upstream exploded"}}}`,
	)
	events := collect(t, raw)
	last := events[len(events)-1]
	if last.Type != protocol.EvError || last.Message != "upstream exploded" {
		t.Fatalf("终事件 = %+v, want EvError{upstream exploded}", last)
	}
	for _, ev := range events {
		if ev.Type == protocol.EvDone {
			t.Error("出错之后还放了 EvDone：下游会以为这是正常收尾")
		}
	}
}

// TestDecodeBareErrorFrame：裸 error 帧（不裹 response 对象）也认。
func TestDecodeBareErrorFrame(t *testing.T) {
	raw := sseFrames(
		`event: error` + "\n" +
			`data: {"type":"error","error":{"code":"rate_limit","message":"slow down"}}`,
	)
	events := collect(t, raw)
	if len(events) != 1 || events[0].Type != protocol.EvError || events[0].Message != "slow down" {
		t.Fatalf("events = %+v, want 单个 EvError{slow down}", events)
	}
}

// errReader 在吐完 body 之后报一个非 EOF 错，模拟上游传输中断。
type errReader struct {
	body []byte
	done bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.body), nil
	}
	return 0, errors.New("connection reset by peer")
}

// TestDecodeStreamReadErrorIsFlagged：传输断了要既放 EvError 又置 StreamReadFlag。
//
// 两件事缺一不可：只放 EvError 的话，收场时与「上游回了个错误对象」混成一样，
// 客户端断线会被记成干净的 ok（protocol.StreamReadReporter 存在的理由）。
func TestDecodeStreamReadErrorIsFlagged(t *testing.T) {
	c := NewCodec()
	var reporter protocol.StreamReadReporter = c
	ch, err := c.DecodeStream(&errReader{body: []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"m\"}}\n\n")})
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	var events []protocol.Event
	for ev := range ch {
		events = append(events, ev)
	}
	last := events[len(events)-1]
	if last.Type != protocol.EvError {
		t.Fatalf("终事件 = %+v, want EvError", last)
	}
	if reporter.StreamReadError() == nil {
		t.Error("StreamReadError 为 nil：断流会被记成干净收尾")
	}
}

// TestDecodeTruncatedStreamFallsBack：流断在终帧之前，兜一个 EvDone{stop} 并标 Truncated。
func TestDecodeTruncatedStreamFallsBack(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"half"}`,
	)
	events := collect(t, raw)
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "stop" || !last.Truncated {
		t.Errorf("终事件 = %+v, want EvDone{stop, Truncated}", last)
	}
}

// TestDecodeCustomToolTruncatedStillFlushes：custom 入参攒到一半流断了，攒着的照样放出去。
func TestDecodeCustomToolTruncatedStillFlushes(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_z","name":"exec","input":""}}`,
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","output_index":0,"delta":"console.log(1)"}`,
	)
	events := collect(t, raw)
	var args string
	var ends int
	for _, ev := range events {
		switch ev.Type {
		case protocol.EvToolArgsDelta:
			args = ev.Text
		case protocol.EvToolCallEnd:
			ends++
		}
	}
	if ends != 1 {
		t.Errorf("ToolCallEnd = %d, want 1（半个调用也比凭空消失强）", ends)
	}
	var wrapped map[string]string
	if json.Unmarshal([]byte(args), &wrapped) != nil || wrapped[protocol.CustomToolArgsKey] != "console.log(1)" {
		t.Errorf("兜底放出的入参 = %q，want 包装过的 console.log(1)", args)
	}
}

// TestDecodeInterleavedCustomToolCalls：两路 custom_tool_call 同时开着、分片交错到达。
//
// 单槽 pending 时这条必挂：后一个 output_item.added 把前一个连同它攒了一半的分片
// 一起覆盖，前者的 flushCustom 因 index 对不上直接返回，客户端拿到的是一个
// Start+End、零入参的调用——而且不报错。canonical 契约（protocol/event.go）明写
// 并行调用的分片按 index 交错到达，形态参考 CLIProxyAPI 的同款用例（两 item 同开）。
func TestDecodeInterleavedCustomToolCalls(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_a","name":"exec","input":""}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"ctc_2","type":"custom_tool_call","call_id":"call_b","name":"apply","input":""}}`,
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","output_index":0,"delta":"aa"}`,
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_2","output_index":1,"delta":"bb"}`,
		`data: {"type":"response.custom_tool_call_input.done","item_id":"ctc_1","output_index":0}`,
		`data: {"type":"response.custom_tool_call_input.done","item_id":"ctc_2","output_index":1}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed"}}`,
	)
	events := collect(t, raw)

	args := map[int]string{}
	starts := map[int]string{}
	var order []int
	for _, ev := range events {
		switch ev.Type {
		case protocol.EvToolCallStart:
			starts[ev.Index] = ev.ToolID
			order = append(order, ev.Index)
		case protocol.EvToolArgsDelta:
			args[ev.Index] += ev.Text
		}
	}
	if starts[0] != "call_a" || starts[1] != "call_b" {
		t.Fatalf("两路 Start 没都放出来: %+v", starts)
	}
	if len(order) != 2 || order[0] != 0 || order[1] != 1 {
		t.Errorf("Start 次序 = %v，期望按首次出现 0→1", order)
	}
	for index, want := range map[int]string{0: "aa", 1: "bb"} {
		var wrapped map[string]string
		if json.Unmarshal([]byte(args[index]), &wrapped) != nil {
			t.Fatalf("index %d 的入参不是 JSON: %q", index, args[index])
		}
		if got := wrapped[protocol.CustomToolArgsKey]; got != want {
			t.Errorf("index %d 的入参 = %q，want %q（交错的分片各归各路）", index, got, want)
		}
	}
}

// TestDecodeIncompleteFlushesPendingCustomArgs：response.incomplete 把流打断时，攒着
// 的 custom 分片照样以 EvToolArgsDelta + EvToolCallEnd 放出去。
//
// 打断意味着上游不会再补 custom_tool_call_input.done 与 output_item.done，而终帧一旦
// 发过 EvDone，finish 里那条「半个调用也比凭空消失强」的兜底就被 doneSent 挡死了。
func TestDecodeIncompleteFlushesPendingCustomArgs(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_a","name":"exec","input":""}}`,
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","output_index":0,"delta":"console.log(1"}`,
		`data: {"type":"response.incomplete","response":{"id":"r","model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
	)
	events := collect(t, raw)

	var args string
	var ends int
	for _, ev := range events {
		switch ev.Type {
		case protocol.EvToolArgsDelta:
			args = ev.Text
		case protocol.EvToolCallEnd:
			ends++
		}
	}
	if ends != 1 {
		t.Errorf("ToolCallEnd = %d, want 1（打断也要把这一路收口）", ends)
	}
	var wrapped map[string]string
	if json.Unmarshal([]byte(args), &wrapped) != nil || wrapped[protocol.CustomToolArgsKey] != "console.log(1" {
		t.Errorf("放出的入参 = %q，want 包装过的半截分片", args)
	}
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "length" {
		t.Errorf("终事件 = %+v, want EvDone{length}——冲缓冲不许动 incomplete 的停因映射", last)
	}
	// 次序：入参与收口都排在 EvDone 之前，客户端才拼得完整。
	for i, ev := range events {
		if ev.Type == protocol.EvDone && i != len(events)-1 {
			t.Errorf("EvDone 不在末位（下标 %d/%d）", i, len(events)-1)
		}
	}
}

// TestDecodeFullBodySynthetic：非流式路径。
//
// **没有真实样本**：九份 Responses 转录全是 stream:true，golden 终帧里那个 output
// 数组是中转侧的降级重建（tool-turn1 里 custom_tool_call 被重建成没有入参的
// function_call），不能当线格真值。缺口记在展开层 §9.4②。
func TestDecodeFullBodySynthetic(t *testing.T) {
	body := []byte(`{
	  "id":"resp_full","model":"gpt-5.6","status":"completed",
	  "output":[
	    {"type":"reasoning","id":"rs_1","encrypted_content":"gAAAAABsecret","summary":[]},
	    {"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
	    {"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"SH\"}"},
	    {"type":"custom_tool_call","call_id":"call_2","name":"exec","input":"console.log(1)"}
	  ],
	  "usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40,"cache_write_tokens":5},
	           "output_tokens":20,"output_tokens_details":{"reasoning_tokens":8}}
	}`)
	c := NewCodec()
	events, err := c.DecodeFullBody(body)
	if err != nil {
		t.Fatalf("DecodeFullBody: %v", err)
	}

	if events[0].Type != protocol.EvMessageStart || events[0].ID != "resp_full" {
		t.Errorf("首事件 = %+v", events[0])
	}
	for i, ev := range events {
		if ev.Type == protocol.EvThinkingDelta || strings.Contains(ev.Text, "gAAAAAB") {
			t.Fatalf("事件 %d 泄漏了 reasoning: %+v", i, ev)
		}
	}

	var starts int
	var argsByTool = map[string]string{}
	var curTool string
	var text string
	for _, ev := range events {
		switch ev.Type {
		case protocol.EvTextDelta:
			text += ev.Text
		case protocol.EvToolCallStart:
			starts++
			curTool = ev.ToolName
			if !ev.ArgsIsJSON {
				t.Errorf("%s 的 ArgsIsJSON 为 false", ev.ToolName)
			}
		case protocol.EvToolArgsDelta:
			argsByTool[curTool] += ev.Text
		}
	}
	if text != "done" {
		t.Errorf("正文 = %q", text)
	}
	if starts != 2 {
		t.Errorf("工具调用数 = %d, want 2", starts)
	}
	if argsByTool["get_weather"] != `{"city":"SH"}` {
		t.Errorf("function 入参 = %q（原样带，不重新序列化）", argsByTool["get_weather"])
	}
	var wrapped map[string]string
	if json.Unmarshal([]byte(argsByTool["exec"]), &wrapped) != nil ||
		wrapped[protocol.CustomToolArgsKey] != "console.log(1)" {
		t.Errorf("custom 入参 = %q，want 包装过的", argsByTool["exec"])
	}

	u := lastUsage(events)
	if u == nil || u.InputTokens != 100 || u.CacheReadTokens != 40 ||
		u.CacheWriteTokens != 5 || u.ReasoningTokens != 8 {
		t.Errorf("usage = %+v", u)
	}
	last := events[len(events)-1]
	if last.Type != protocol.EvDone || last.StopReason != "tool_calls" || last.Truncated {
		t.Errorf("终事件 = %+v, want EvDone{tool_calls, 未截断}", last)
	}
}

// TestDecodeFullBodyIncomplete：非流式的截断与流式判一样的停因。
func TestDecodeFullBodyIncomplete(t *testing.T) {
	body := []byte(`{"id":"r","model":"m","status":"incomplete",
	  "incomplete_details":{"reason":"max_output_tokens"},
	  "output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"half"}]}]}`)
	events, err := NewCodec().DecodeFullBody(body)
	if err != nil {
		t.Fatalf("DecodeFullBody: %v", err)
	}
	last := events[len(events)-1]
	if last.StopReason != "length" || last.Truncated {
		t.Errorf("终事件 = %+v, want EvDone{length, 未截断}", last)
	}
}

// TestDecodeFullBodyNotJSON：解不动要报错，不能悄悄回一个空事件序列。
func TestDecodeFullBodyNotJSON(t *testing.T) {
	if _, err := NewCodec().DecodeFullBody([]byte("<html>502</html>")); err == nil {
		t.Fatal("非 JSON 响应体没报错")
	}
}

var _ io.Reader = (*errReader)(nil)

// collectWith 同 collect，但由调用方持有 codec——响应侧的丢弃登记挂在 codec 实例上
// （ResponseDrops），collect 那个版本把实例扔了就读不到。
func collectWith(t *testing.T, c *Codec, raw []byte) []protocol.Event {
	t.Helper()
	ch, err := c.DecodeStream(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	var events []protocol.Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

// TestDecodeStreamRegistersUnknownItemsAndEvents：上游自带服务端工具时要留痕。
//
// 认不得的 output item（web_search_call 这批）与不在 known 表里的事件名此前一律跳过
// 不留痕——上游搜了一圈、钱花了，我们的日志一个字都没有。只登记不合成
// server_tool_use、不计费（PO 2026-09-02）。
//
// 一并钉住两条去重：item 的 added 与 done 只记一次，同一个事件名来两帧也只记一次。
func TestDecodeStreamRegistersUnknownItemsAndEvents(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"resp_ws","model":"gpt-5.6"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ws_abc","type":"web_search_call","status":"in_progress"}}`,
		`data: {"type":"response.web_search_call.searching","item_id":"ws_abc","output_index":0}`,
		`data: {"type":"response.web_search_call.completed","item_id":"ws_abc","output_index":0}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ws_abc","type":"web_search_call","status":"completed"}}`,
		`data: {"type":"response.gizmo.progress","output_index":0}`,
		`data: {"type":"response.gizmo.progress","output_index":0}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant"}}`,
		`data: {"type":"response.output_text.delta","output_index":1,"delta":"查到了"}`,
		`data: {"type":"response.output_text.done","output_index":1,"text":"查到了"}`,
		`data: {"type":"response.completed","response":{"id":"resp_ws","model":"gpt-5.6","status":"completed","usage":{"input_tokens":9,"output_tokens":2}}}`,
	)
	c := NewCodec()
	events := collectWith(t, c, raw)

	drops := c.ResponseDrops()
	want := []string{"web_search_call(ws_abc)", "event:response.gizmo.progress"}
	if len(drops) != len(want) {
		t.Fatalf("ResponseDrops = %v, want %v（added/done 与重复事件名各只记一次）", drops, want)
	}
	for i, w := range want {
		if drops[i] != w {
			t.Errorf("ResponseDrops[%d] = %q, want %q", i, drops[i], w)
		}
	}
	// 搜索结果原文不进日志：登记里只该有类型与 id。
	for _, d := range drops {
		if strings.Contains(d, "查到了") || strings.Contains(d, "in_progress") {
			t.Errorf("登记里带上了 item 内容: %q", d)
		}
	}

	// 正文事件一个不少：登记是旁路，不许改动事件流。
	var text string
	for _, ev := range events {
		if ev.Type == protocol.EvTextDelta {
			text += ev.Text
		}
	}
	if text != "查到了" {
		t.Errorf("正文 = %q, want 查到了——登记不该动正文", text)
	}
	if last := events[len(events)-1]; last.Type != protocol.EvDone || last.StopReason != "stop" {
		t.Errorf("终事件 = %+v，want EvDone{stop}——服务端工具不算 tool_calls 收尾", last)
	}
}

// TestDecodeStreamKnownStructuralEventsStaySilent：明知故跳的结构信号不许刷屏。
//
// knownRespEvents 那张表存在的理由就是这个：in_progress / content_part.* /
// *.done 每条流都来一堆，逐个登记会把「上游自带搜索」这条真信号淹掉。
func TestDecodeStreamKnownStructuralEventsStaySilent(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.in_progress","response":{"id":"r"}}`,
		`data: {"type":"response.queued","response":{"id":"r"}}`,
		`data: {"type":"response.content_part.added","output_index":0}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"hi"}`,
		`data: {"type":"response.output_text.done","output_index":0,"text":"hi"}`,
		`data: {"type":"response.content_part.done","output_index":0}`,
		`data: {"type":"response.reasoning_summary_part.added","output_index":0}`,
		`data: {"type":"response.reasoning_summary_part.done","output_index":0}`,
		`data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{}"}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed"}}`,
	)
	c := NewCodec()
	collectWith(t, c, raw)
	if drops := c.ResponseDrops(); len(drops) != 0 {
		t.Errorf("ResponseDrops = %v, want 空——结构信号是明知故跳，不该登记", drops)
	}
}

// TestDecodeStreamUnknownItemWithoutIDFallsBackToIndex：上游没给 item id 时用
// output_index 兜底，不能记成一个光秃秃的类型名。
func TestDecodeStreamUnknownItemWithoutIDFallsBackToIndex(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.output_item.added","output_index":3,"item":{"type":"mcp_call","name":"search"}}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed"}}`,
	)
	c := NewCodec()
	collectWith(t, c, raw)
	if got := c.ResponseDrops(); len(got) != 1 || got[0] != "mcp_call(#3)" {
		t.Errorf("ResponseDrops = %v, want [mcp_call(#3)]", got)
	}
}

// TestDecodeStreamResetsResponseDropsPerRequest：每请求状态要真的归零。
func TestDecodeStreamResetsResponseDrops(t *testing.T) {
	first := sseFrames(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"ws_1","type":"web_search_call"}}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed"}}`,
	)
	c := NewCodec()
	collectWith(t, c, first)
	if len(c.ResponseDrops()) != 1 {
		t.Fatalf("第一轮 ResponseDrops = %v, want 一条", c.ResponseDrops())
	}
	clean := sseFrames(
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"hi"}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed"}}`,
	)
	collectWith(t, c, clean)
	if drops := c.ResponseDrops(); len(drops) != 0 {
		t.Errorf("第二轮 ResponseDrops = %v, want 空——上一轮的登记带进来了", drops)
	}
}

// TestDecodeFullBodyRegistersUnknownItems：非流式同款登记。
func TestDecodeFullBodyRegistersUnknownItems(t *testing.T) {
	body := []byte(`{"id":"resp_full","model":"m","status":"completed","output":[
	  {"type":"web_search_call","id":"ws_abc","status":"completed","action":{"type":"search","query":"portage 网关"}},
	  {"type":"image_generation_call","id":"ig_1"},
	  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"查到了"}]}
	]}`)
	c := NewCodec()
	events, err := c.DecodeFullBody(body)
	if err != nil {
		t.Fatalf("DecodeFullBody: %v", err)
	}
	want := []string{"web_search_call(ws_abc)", "image_generation_call(ig_1)"}
	got := c.ResponseDrops()
	if len(got) != len(want) {
		t.Fatalf("ResponseDrops = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ResponseDrops[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, d := range got {
		if strings.Contains(d, "portage 网关") {
			t.Errorf("搜索词进了登记: %q", d)
		}
	}
	var text string
	for _, ev := range events {
		if ev.Type == protocol.EvTextDelta {
			text += ev.Text
		}
	}
	if text != "查到了" {
		t.Errorf("正文 = %q，want 查到了", text)
	}
}

// TestDecodeStreamRefusalBecomesText：模型拒答走正文放出去。
//
// 此前 response.refusal.delta 整个跳过，客户端看到的是一个空回复——它不知道模型
// 拒答了，只当网关坏了。只认 .delta 不认 .done（done 带全文，收两遍就发两遍）。
func TestDecodeStreamRefusalBecomesText(t *testing.T) {
	raw := sseFrames(
		`data: {"type":"response.created","response":{"id":"r","model":"m"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant"}}`,
		`data: {"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"抱歉，"}`,
		`data: {"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"我不能帮这个忙。"}`,
		`data: {"type":"response.refusal.done","item_id":"msg_1","output_index":0,"content_index":0,"refusal":"抱歉，我不能帮这个忙。"}`,
		`data: {"type":"response.completed","response":{"id":"r","model":"m","status":"completed"}}`,
	)
	c := NewCodec()
	events := collectWith(t, c, raw)

	var text string
	var deltas int
	for _, ev := range events {
		if ev.Type == protocol.EvTextDelta {
			text += ev.Text
			deltas++
		}
	}
	if text != "抱歉，我不能帮这个忙。" {
		t.Errorf("拒答正文 = %q", text)
	}
	if deltas != 2 {
		t.Errorf("TextDelta 条数 = %d, want 2——.done 带的是全文，收了就发两遍", deltas)
	}
	if drops := c.ResponseDrops(); len(drops) != 0 {
		t.Errorf("ResponseDrops = %v, want 空——拒答是发出去了的，不是丢弃", drops)
	}
}

// TestDecodeFullBodyRefusalPartBecomesText：非流式的 refusal 部件同样走正文。
//
// 形状是 {"type":"refusal","refusal":"…"}，正文在 refusal 键上不在 text 上
// （openai-python 2.24.0 / litellm types 一致）。
func TestDecodeFullBodyRefusalPartBecomesText(t *testing.T) {
	body := []byte(`{"id":"r","model":"m","status":"completed","output":[
	  {"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"抱歉，我不能帮这个忙。"}]}
	]}`)
	c := NewCodec()
	events, err := c.DecodeFullBody(body)
	if err != nil {
		t.Fatalf("DecodeFullBody: %v", err)
	}
	var text string
	for _, ev := range events {
		if ev.Type == protocol.EvTextDelta {
			text += ev.Text
		}
	}
	if text != "抱歉，我不能帮这个忙。" {
		t.Errorf("拒答正文 = %q", text)
	}
	if drops := c.ResponseDrops(); len(drops) != 0 {
		t.Errorf("ResponseDrops = %v, want 空", drops)
	}
}
