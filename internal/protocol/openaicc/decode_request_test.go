package openaicc_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

// 本文件测的是 CC 作**入口**的解码侧。输入一律是 testdata/golden/in-cc-* 六份
// opencode 1.18 真实发包，不手搭请求体——手搭的只会长成我以为的样子。

// inboundSamples 列在这里而不是扫目录：缺哪个 skip 哪个，而不是目录空着一路绿灯。
var inboundSamples = []string{
	"in-cc-text",
	"in-cc-tool-turn1",
	"in-cc-tool-turn2",
	"in-cc-parallel-turn1",
	"in-cc-parallel-turn2",
	"in-cc-consecutive-user",
}

func loadInbound(t *testing.T, name string) []byte {
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
		Verified bool `json:"verified"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	// verified 关卡：样本经脱敏改过字节，没人核过就不该被当成事实源。
	if !meta.Verified {
		t.Fatalf("%s 的 meta.json 仍是 verified:false", name)
	}
	body, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decodeInbound(t *testing.T, name string) *protocol.Request {
	t.Helper()
	req, err := openaicc.NewCodec().DecodeRequest(loadInbound(t, name), true)
	if err != nil {
		t.Fatalf("%s 解不动: %v", name, err)
	}
	return req
}

// DecodeRequest 是全函数（codec.go 的接口约束）：六份真实发包一份都不许解不动，
// 且 model / messages 这些必需品不许在解码里蒸发。
func TestDecodeRequestIsTotalOverRealSamples(t *testing.T) {
	for _, name := range inboundSamples {
		t.Run(name, func(t *testing.T) {
			req := decodeInbound(t, name)
			if req.Model == "" {
				t.Error("model 丢了")
			}
			if len(req.Messages) == 0 {
				t.Error("messages 丢了")
			}
			if req.MaxTokens != 32000 {
				t.Errorf("max_tokens = %d，样本里是 32000", req.MaxTokens)
			}
		})
	}
}

// system 消息不在 decode 侧上提：CC 允许 role=system 出现在中段，位置是信息。
// 上提是 Anthropic 出口的事（anthropic.splitSystem）。
func TestDecodeRequestKeepsSystemAsMessage(t *testing.T) {
	req := decodeInbound(t, "in-cc-text")
	if len(req.System) != 0 {
		t.Errorf("Request.System 应为空，CC 没有顶层 system 字段；实得 %d 块", len(req.System))
	}
	if req.Messages[0].Role != protocol.RoleSystem {
		t.Fatalf("首条消息角色 = %q，期望 system", req.Messages[0].Role)
	}
	if len(req.Messages[0].Content) != 1 || req.Messages[0].Content[0].Kind != protocol.BlockText {
		t.Fatalf("字符串 content 应退化为单个 text 块，实得 %+v", req.Messages[0].Content)
	}
}

// tool_calls 解成 tool_use 块，接在正文之后（canonical 是单一块序列）。
func TestDecodeRequestMapsToolCallsToBlocks(t *testing.T) {
	req := decodeInbound(t, "in-cc-tool-turn2")

	var assistant *protocol.Message
	for i := range req.Messages {
		if req.Messages[i].Role == protocol.RoleAssistant {
			assistant = &req.Messages[i]
		}
	}
	if assistant == nil {
		t.Fatal("没找到 assistant 消息")
	}
	if len(assistant.Content) != 1 {
		t.Fatalf("assistant 块数 = %d，样本里 content 为空串、只有一个调用", len(assistant.Content))
	}
	b := assistant.Content[0]
	if b.Kind != protocol.BlockToolUse || b.ToolCall == nil {
		t.Fatalf("块类型 = %q，期望 tool_use", b.Kind)
	}
	if b.ToolCall.ID != "call_goldenrec_read_01" {
		t.Errorf("ToolCall.ID = %q，与 tool 消息的 tool_call_id 对不上", b.ToolCall.ID)
	}
	if b.ToolCall.Name != "read" {
		t.Errorf("ToolCall.Name = %q", b.ToolCall.Name)
	}
	// arguments 按 CC 契约是 JSON 字符串，故 ArgsIsJSON 为 true。
	if !b.ToolCall.ArgsIsJSON {
		t.Error("ArgsIsJSON 应为 true：CC 的 arguments 按契约就是 JSON 字符串")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(b.ToolCall.Args), &args); err != nil {
		t.Fatalf("Args 不是 JSON: %v（%q）", err, b.ToolCall.Args)
	}
	if args["filePath"] != "/private/tmp/aig-ccsample/notes.md" {
		t.Errorf("入参丢了: %v", args)
	}
}

// role=tool 的独立消息解成 RoleTool + 一个 tool_result 块，tool_call_id 从消息级
// 落到块级——这是 CC 与 Anthropic 的形态分歧所在，归一到块上出口侧才好各自落位。
func TestDecodeRequestMapsToolMessagesToResultBlocks(t *testing.T) {
	req := decodeInbound(t, "in-cc-parallel-turn2")

	var results []*protocol.ToolResult
	for _, m := range req.Messages {
		if m.Role != protocol.RoleTool {
			continue
		}
		if len(m.Content) != 1 {
			t.Fatalf("一条 tool 消息应正好一个块，实得 %d", len(m.Content))
		}
		b := m.Content[0]
		if b.Kind != protocol.BlockToolResult || b.ToolResult == nil {
			t.Fatalf("块类型 = %q，期望 tool_result", b.Kind)
		}
		results = append(results, b.ToolResult)
	}
	// 并行两个调用 → 两条独立 tool 消息（Anthropic 那边是一条 user 消息两个块）。
	if len(results) != 2 {
		t.Fatalf("tool 消息数 = %d，样本里并行调用有两条", len(results))
	}
	if results[0].ToolCallID != "call_goldenrec_read_a" || results[1].ToolCallID != "call_goldenrec_read_b" {
		t.Errorf("tool_call_id 没对上: %q / %q", results[0].ToolCallID, results[1].ToolCallID)
	}
	if len(results[0].Content) != 1 || results[0].Content[0].Kind != protocol.BlockText {
		t.Errorf("结果正文应退化为单个 text 块，实得 %+v", results[0].Content)
	}
}

// 连着两条 user 不合并、不报错：CC 侧合法，合并是 Anthropic 出口的事。
func TestDecodeRequestKeepsConsecutiveUserMessages(t *testing.T) {
	req := decodeInbound(t, "in-cc-consecutive-user")
	var users int
	for _, m := range req.Messages {
		if m.Role == protocol.RoleUser {
			users++
		}
	}
	if users != 2 {
		t.Errorf("user 消息数 = %d，样本里是连着的两条", users)
	}
}

// stream_options 必须留在 Extras 里，且被读成 includeUsage——回程的 EncodeStream
// 靠它决定补不补 usage 帧，丢了就只能猜。
func TestDecodeRequestKeepsStreamOptions(t *testing.T) {
	req := decodeInbound(t, "in-cc-text")
	opts, ok := req.Extras["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options 没进 Extras: %+v", req.Extras)
	}
	if opts["include_usage"] != true {
		t.Errorf("include_usage 丢了: %v", opts)
	}
}

// 工具声明的两层嵌套（tools[].function.{name,description,parameters}）拍平到
// canonical，schema 按原始字节存。
func TestDecodeRequestFlattensToolDeclarations(t *testing.T) {
	req := decodeInbound(t, "in-cc-text")
	if len(req.Tools) == 0 {
		t.Fatal("tools 丢了")
	}
	tool := req.Tools[0]
	if tool.Kind != protocol.ToolFunction {
		t.Errorf("Kind = %q，期望 function", tool.Kind)
	}
	if tool.Name != "bash" {
		t.Errorf("Name = %q", tool.Name)
	}
	if tool.Description == "" {
		t.Error("description 丢了")
	}
	if len(tool.Schema) == 0 || !json.Valid(tool.Schema) {
		t.Errorf("parameters 没按原始字节存下来: %q", tool.Schema)
	}
	if req.ToolChoice.Mode != "auto" {
		t.Errorf("tool_choice = %+v，样本里是字符串 \"auto\"", req.ToolChoice)
	}
}

// tool_choice 的两种线上形态都要认，且认不得的取值当没说——塞个野值进 canonical
// 只会让出口侧原样发出一个上游不认的取值。
func TestDecodeRequestToolChoiceForms(t *testing.T) {
	cases := []struct {
		raw      string
		wantMode string
		wantName string
	}{
		{`"required"`, "required", ""},
		{`"none"`, "none", ""},
		{`{"type":"function","function":{"name":"read"}}`, "tool", "read"},
		{`"totally_new_mode"`, "", ""},
		{`{"type":"function"}`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			body := `{"model":"m","messages":[],"tool_choice":` + tc.raw + `}`
			req, err := openaicc.NewCodec().DecodeRequest([]byte(body), false)
			if err != nil {
				t.Fatalf("解不动: %v", err)
			}
			if req.ToolChoice.Mode != tc.wantMode || req.ToolChoice.Name != tc.wantName {
				t.Errorf("ToolChoice = %+v，期望 {%q %q}", req.ToolChoice, tc.wantMode, tc.wantName)
			}
		})
	}
}

// max_completion_tokens 是 max_tokens 的新名字，两者都要认。
func TestDecodeRequestAcceptsMaxCompletionTokens(t *testing.T) {
	body := `{"model":"m","messages":[],"max_completion_tokens":123}`
	req, err := openaicc.NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatalf("解不动: %v", err)
	}
	if req.MaxTokens != 123 {
		t.Errorf("MaxTokens = %d，期望 123", req.MaxTokens)
	}
}

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR42mP4z8AAAAMBAQD3A0FDAAAAAElFTkSuQmCC"

// content 的数组形态：image_url 的 https URL 落成 BlockImage，不进 Extras。
func TestDecodeRequestSurvivesMultimodalParts(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"看图"},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`
	req, err := openaicc.NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatalf("带图片的请求不该解不动: %v", err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("块数 = %d，期望 2", len(blocks))
	}
	if blocks[0].Kind != protocol.BlockText || blocks[0].Text != "看图" {
		t.Errorf("第一个块没解对: %+v", blocks[0])
	}
	if blocks[1].Kind != protocol.BlockImage {
		t.Errorf("image_url 应落成 BlockImage，实得 %q", blocks[1].Kind)
	}
	if blocks[1].Image == nil || blocks[1].Image.URL != "https://example.com/a.png" {
		t.Errorf("Image.URL 没解对: %+v", blocks[1].Image)
	}
}

// data URI 拆成 MediaType + 裸 base64；载荷是 testdata/fixtures/tiny.png 的真字节。
func TestDecodeRequestImageDataURI(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + tinyPNG + `"}}]}]}`
	req, err := openaicc.NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatalf("解不动: %v", err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 1 {
		t.Fatalf("块数 = %d，期望 1", len(blocks))
	}
	if blocks[0].Kind != protocol.BlockImage || blocks[0].Image == nil {
		t.Fatalf("没解成 BlockImage: %+v", blocks[0])
	}
	if blocks[0].Image.MediaType != "image/png" {
		t.Errorf("MediaType = %q，期望 image/png", blocks[0].Image.MediaType)
	}
	if blocks[0].Image.Data != tinyPNG {
		t.Errorf("Data 对不上 tiny.png 的 base64")
	}
}

// input_audio 仍是未知块 + Extras，不落成图片。
func TestDecodeRequestKeepsUnknownAudioParts(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]}`
	req, err := openaicc.NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 1 || blocks[0].Kind != "input_audio" {
		t.Fatalf("input_audio 应原样留 Kind，实得 %+v", blocks)
	}
	if blocks[0].Image != nil {
		t.Error("input_audio 不该填 Image")
	}
	if blocks[0].Extras["input_audio"] == nil {
		t.Errorf("未知 part 的字段应进 Extras: %+v", blocks[0].Extras)
	}
}

func TestDecodeRequestSkipsEmptyDataURI(t *testing.T) {
	for _, uri := range []string{"data:image/png;base64,", "data:image/png;base64,   "} {
		body := `{"model":"m","messages":[{"role":"user","content":[` +
			`{"type":"text","text":"看图"},` +
			`{"type":"image_url","image_url":{"url":"` + uri + `"}}]}]}`
		req, err := openaicc.NewCodec().DecodeRequest([]byte(body), false)
		if err != nil {
			t.Fatalf("%q 解不动: %v", uri, err)
		}
		blocks := req.Messages[0].Content
		if len(blocks) != 1 || blocks[0].Kind != protocol.BlockText {
			t.Errorf("%q 空图应整块跳过，实得 %+v", uri, blocks)
		}
	}
}

// content 为 null（纯工具调用轮的常见形态）解成无块，不是一个空的 text 块——
// 空 text 块编到 Anthropic 会变成一条 content 为空数组的消息，那边直接拒。
func TestDecodeRequestTreatsNullContentAsNoBlocks(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"assistant","content":null,` +
		`"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`
	req, err := openaicc.NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatalf("解不动: %v", err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 1 || blocks[0].Kind != protocol.BlockToolUse {
		t.Fatalf("块序列 = %+v，期望只有一个 tool_use", blocks)
	}
}

// developer 归一为 system（protocol/request.go 的 Role 注释，PO 确认）。
//
// canonical 没有 RoleDeveloper，R 入口早就这么折。不折的后果在 CC→A 上是实打实的：
// Anthropic 出口只把 RoleSystem 上提到顶层 system，其余非 assistant 一律当 user，
// 一条 developer 系统提示会降格成用户内容、还会跟后面那条 user 合并。
func TestDecodeRequestNormalizesDeveloperRole(t *testing.T) {
	req, err := openaicc.NewCodec().DecodeRequest([]byte(`{"model":"m","messages":[
		{"role":"developer","content":"你是个严谨的助手"},
		{"role":"user","content":"你好"}]}`), false)
	if err != nil {
		t.Fatal(err)
	}

	if len(req.Messages) != 2 {
		t.Fatalf("消息数 = %d，归一只改角色不改条数", len(req.Messages))
	}
	if req.Messages[0].Role != protocol.RoleSystem {
		t.Errorf("developer 归一后 = %q，期望 %q", req.Messages[0].Role, protocol.RoleSystem)
	}
	if req.Messages[1].Role != protocol.RoleUser {
		t.Errorf("user 被动了: %q", req.Messages[1].Role)
	}
}

// system 与其余角色不受影响——归一只认 developer 一个。
func TestDecodeRequestLeavesOtherRolesAlone(t *testing.T) {
	req, err := openaicc.NewCodec().DecodeRequest([]byte(`{"model":"m","messages":[
		{"role":"system","content":"s"},
		{"role":"user","content":"u"},
		{"role":"assistant","content":"a"}]}`), false)
	if err != nil {
		t.Fatal(err)
	}

	want := []protocol.Role{protocol.RoleSystem, protocol.RoleUser, protocol.RoleAssistant}
	for i, w := range want {
		if req.Messages[i].Role != w {
			t.Errorf("messages[%d].Role = %q，期望 %q", i, req.Messages[i].Role, w)
		}
	}
}
