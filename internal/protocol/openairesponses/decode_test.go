package openairesponses

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// goldenDir 是全仓共用的转录库（见 internal/protocol/golden_test.go）。
const goldenDir = "../../../testdata/golden"

// inboundSamples 是 Codex CLI 0.144 的四份真实发包。列在这里而不是扫目录，是为了让
// 没采集的样本也看得见：缺哪个 skip 哪个，而不是目录空着一路绿灯。
var inboundSamples = []string{
	"in-responses-text",
	"in-responses-tool-turn1",
	"in-responses-tool-turn2",
	"in-responses-parallel-turn2",
}

func loadSample(t *testing.T, name string) []byte {
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

// DecodeRequest 是全函数（codec.go 的接口约束）：四份真实发包一份都不许解不动。
func TestDecodeRequestIsTotalOverRealSamples(t *testing.T) {
	for _, name := range inboundSamples {
		t.Run(name, func(t *testing.T) {
			req, err := NewCodec().DecodeRequest(loadSample(t, name), true)
			if err != nil {
				t.Fatalf("解不动: %v", err)
			}
			if req.Model == "" {
				t.Error("model 丢了")
			}
			if len(req.Messages) == 0 {
				t.Error("messages 空了——input 里的 message item 没落地")
			}
			// 四份样本的 input[0] 都是 additional_tools，工具必须被提升上来。
			if len(req.Tools) == 0 {
				t.Error("tools 空了——additional_tools 没提升到 Request.Tools")
			}
		})
	}
}

// additional_tools 里的 exec 是 custom 形态（入参是 JS 源码，不是 JSON），wait 与
// request_user_input 是 function。Kind 分错，编码侧就会把 JS 当 JSON 发出去。
func TestDecodeRequestClassifiesCustomAndFunctionTools(t *testing.T) {
	c := NewCodec()
	req, err := c.DecodeRequest(loadSample(t, "in-responses-tool-turn2"), true)
	if err != nil {
		t.Fatal(err)
	}

	kinds := map[string]protocol.ToolKind{}
	for _, tool := range req.Tools {
		kinds[tool.Name] = tool.Kind
	}
	want := map[string]protocol.ToolKind{
		"exec":               protocol.ToolCustom,
		"wait":               protocol.ToolFunction,
		"request_user_input": protocol.ToolFunction,
	}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("工具 %s 的 Kind = %q, 期望 %q", name, kinds[name], kind)
		}
	}

	// exec 的 lark 文法没有 canonical 对应物，进 Extras；它不能变成 Schema，
	// 否则 CC 出口会把一段文法当 JSON Schema 发出去。
	for _, tool := range req.Tools {
		if tool.Name != "exec" {
			continue
		}
		if _, ok := tool.Extras["format"]; !ok {
			t.Error("exec 的 format（lark 文法）没进 Extras")
		}
		if tool.Schema != nil {
			t.Errorf("exec 不该有 Schema, 却拿到 %s", tool.Schema)
		}
	}

	// 编码侧靠这份集合决定发 custom_tool_call 还是 function_call。
	if !c.customTools["exec"] || c.customTools["wait"] {
		t.Errorf("customTools = %v, 期望只有 exec", c.customTools)
	}
}

// turn2 的 input 是 developer×2 → user×2 → reasoning → custom_tool_call →
// custom_tool_call_output，跨了三次角色。连续同侧的 item 要并进同一条消息：
// 一个 tool_call 一条 assistant 消息会编出 assistant 连发，严格上游会拒。
func TestDecodeRequestGroupsConsecutiveItemsIntoMessages(t *testing.T) {
	req, err := NewCodec().DecodeRequest(loadSample(t, "in-responses-tool-turn2"), true)
	if err != nil {
		t.Fatal(err)
	}

	var roles []protocol.Role
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	// developer 归一为 system（protocol.Role 注释），user 直通，reasoning +
	// custom_tool_call 并成一条 assistant，工具结果挂 user。
	want := []protocol.Role{
		protocol.RoleSystem, protocol.RoleUser, protocol.RoleAssistant, protocol.RoleUser,
	}
	if len(roles) != len(want) {
		t.Fatalf("消息角色序列 = %v, 期望 %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("消息角色序列 = %v, 期望 %v", roles, want)
		}
	}

	assistant := req.Messages[2]
	if len(assistant.Content) != 2 {
		t.Fatalf("assistant 消息有 %d 块, 期望 2（thinking + tool_use）", len(assistant.Content))
	}
	if assistant.Content[0].Kind != protocol.BlockThinking {
		t.Errorf("第一块是 %q, 期望 thinking", assistant.Content[0].Kind)
	}
	if assistant.Content[1].Kind != protocol.BlockToolUse {
		t.Fatalf("第二块是 %q, 期望 tool_use", assistant.Content[1].Kind)
	}

	call := assistant.Content[1].ToolCall
	if call.Name != "exec" {
		t.Errorf("工具名 = %q", call.Name)
	}
	// ArgsIsJSON 必须是 false：exec 的 input 是 JavaScript 源码。标成 true，
	// CC 出口就不会给它合成包装对象，上游收到一段解不动的 arguments。
	if call.ArgsIsJSON {
		t.Error("exec 的 ArgsIsJSON = true，但它的入参是 JS 源码")
	}
	if call.Args == "" {
		t.Error("工具入参丢了")
	}

	// 结果靠 call_id 对回调用，不是靠 item id（`ctc_…`）。
	result := req.Messages[3].Content[0].ToolResult
	if result == nil || result.ToolCallID != call.ID {
		t.Errorf("tool_result 的 call_id 对不上调用: %+v vs %q", result, call.ID)
	}
	if len(result.Content) != 2 {
		t.Errorf("工具结果有 %d 块, 期望 2（执行摘要 + 程序输出）", len(result.Content))
	}
}

// 「带 encrypted_content 的 input 不使转换报错」——§5 坑清单点名要的用例。
// 那串密文只有原上游解得开，跨协议必然作废；这里钉的是它**不炸**，且没有被
// 误当成明文推理正文塞进 Text。
func TestDecodeRequestKeepsEncryptedReasoningOutOfText(t *testing.T) {
	req, err := NewCodec().DecodeRequest(loadSample(t, "in-responses-tool-turn2"), true)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Kind != protocol.BlockThinking {
				continue
			}
			seen = true
			if b.Text != "" {
				t.Errorf("thinking 块的 Text = %q, 密文不该被当成正文", b.Text)
			}
			if _, ok := b.Extras["encrypted_content"]; !ok {
				t.Error("encrypted_content 没进 Extras")
			}
		}
	}
	if !seen {
		t.Error("样本里有 reasoning item，却没解出 thinking 块")
	}
}

// 顶层的 Responses 独有字段全进 Extras：白名单式解析会把 Codex 升级后新发的字段
// 静默吃掉，而 canonical_coverage_test 只查样本里出现过的。
func TestDecodeRequestParksResponsesOnlyFieldsInExtras(t *testing.T) {
	req, err := NewCodec().DecodeRequest(loadSample(t, "in-responses-text"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"reasoning", "store", "include", "prompt_cache_key",
		"client_metadata", "text", "parallel_tool_calls",
	} {
		if _, ok := req.Extras[key]; !ok {
			t.Errorf("顶层 %s 没进 Extras", key)
		}
	}
}

// previous_response_id 直接丢，连 Extras 都不进（口径层 §5 待澄清 #2：v1 只支持
// 无状态用法）。留着它等于把一个上一个上游才认得的句柄带在身上，被顺手发出去就是
// 「找不到」或者接错会话。
func TestDecodeRequestDropsPreviousResponseID(t *testing.T) {
	body := []byte(`{"model":"m","previous_response_id":"resp_abc","input":"hi"}`)
	req, err := NewCodec().DecodeRequest(body, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Extras["previous_response_id"]; ok {
		t.Error("previous_response_id 进了 Extras")
	}
	if len(req.Messages) != 1 || req.Messages[0].Content[0].Text != "hi" {
		t.Errorf("字符串形态的 input 没退化成一条 user 消息: %+v", req.Messages)
	}
}

// 样本没覆盖但协议允许的形态，一个都不许解不动——decode 是全函数。
// 跳过一个认不得的 item，不该把它前后两条同侧 item 劈成两条消息——严格的 CC
// 上游拒连发同 role。这条钉的是「跳过」而非「收口再跳过」。
func TestDecodeRequestSkipsUnknownItemWithoutSplittingMessages(t *testing.T) {
	body := `{"model":"m","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"前"}]},
		{"type":"web_search_call","id":"ws_1","status":"completed"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"后"}]}
	]}`
	req, err := NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 {
		var roles []protocol.Role
		for _, m := range req.Messages {
			roles = append(roles, m.Role)
		}
		t.Fatalf("解出 %d 条消息 %v, 期望 1 条（未知 item 不该断开攒消息）", len(req.Messages), roles)
	}
	if n := len(req.Messages[0].Content); n != 2 {
		t.Fatalf("消息里有 %d 块, 期望 2（前 + 后）", n)
	}
}

func TestDecodeRequestToleratesUnsampledShapes(t *testing.T) {
	cases := map[string]string{
		"顶层 tools":       `{"model":"m","tools":[{"type":"function","name":"f","parameters":{"type":"object"}}]}`,
		"instructions":   `{"model":"m","instructions":"be brief","input":[]}`,
		"function_call":  `{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{\"a\":1}"}]}`,
		"认不得的 item 类型":   `{"model":"m","input":[{"type":"web_search_call","id":"ws_1"}]}`,
		"tool_choice 对象": `{"model":"m","tool_choice":{"type":"function","name":"f"}}`,
		"空对象":            `{}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewCodec().DecodeRequest([]byte(body), false); err != nil {
				t.Fatalf("解不动: %v", err)
			}
		})
	}

	// function_call 与 custom_tool_call 的入参语义相反，别搞混。
	req, err := NewCodec().DecodeRequest(
		[]byte(`{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{\"a\":1}"}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	call := req.Messages[0].Content[0].ToolCall
	if !call.ArgsIsJSON || call.Args != `{"a":1}` {
		t.Errorf("function_call 入参 = %q (isJSON=%v), 期望 JSON 原文", call.Args, call.ArgsIsJSON)
	}
}

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR42mP4z8AAAAMBAQD3A0FDAAAAAElFTkSuQmCC"

func TestDecodeRequestInputImage(t *testing.T) {
	body := `{"model":"m","input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"看图"},
		{"type":"input_image","image_url":"data:image/png;base64,` + tinyPNG + `"},
		{"type":"input_image","image_url":"https://example.com/a.png"},
		{"type":"input_image","file_id":"file-xxx"}
	]}]}`
	req, err := NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 4 {
		t.Fatalf("块数 = %d，期望 4", len(blocks))
	}
	if blocks[0].Kind != protocol.BlockText || blocks[0].Text != "看图" {
		t.Errorf("文本块不对: %+v", blocks[0])
	}
	if blocks[1].Kind != protocol.BlockImage || blocks[1].Image == nil || blocks[1].Image.Data != tinyPNG {
		t.Errorf("data URI 图没解对: %+v", blocks[1])
	}
	if blocks[1].Image.MediaType != "image/png" {
		t.Errorf("MediaType = %q", blocks[1].Image.MediaType)
	}
	if blocks[2].Image == nil || blocks[2].Image.URL != "https://example.com/a.png" {
		t.Errorf("URL 图没解对: %+v", blocks[2].Image)
	}
	if blocks[3].Image == nil || blocks[3].Image.FileID != "file-xxx" {
		t.Errorf("file_id 图没解对: %+v", blocks[3].Image)
	}
}

// detail 在 Responses 上是 image_url / file_id 的**同级兄弟**，住在 part 顶层
// （CC 那边在 image_url 对象里面，形状不对称，别写反）。
//
// 顺带钉住「不双份存」：它进了 Image.Detail 就不能再留在 Extras 里——同一个值两处
// 表示，改一处漏一处。何况 Extras 永不外带，留在那儿也只是看着像留住了（v0.78 ①）。
func TestDecodeRequestInputImageDetail(t *testing.T) {
	body := `{"model":"m","input":[{"type":"message","role":"user","content":[
		{"type":"input_image","image_url":"data:image/png;base64,` + tinyPNG + `","detail":"high"},
		{"type":"input_image","image_url":"https://example.com/a.png","detail":"auto"},
		{"type":"input_image","file_id":"file-xxx","detail":"low"}
	]}]}`
	req, err := NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 3 {
		t.Fatalf("块数 = %d，期望 3", len(blocks))
	}
	want := []string{"high", "auto", "low"}
	for i, w := range want {
		if blocks[i].Image == nil {
			t.Fatalf("blocks[%d] 没解成图: %+v", i, blocks[i])
		}
		if blocks[i].Image.Detail != w {
			t.Errorf("blocks[%d].Image.Detail = %q，期望 %q", i, blocks[i].Image.Detail, w)
		}
		if _, ok := blocks[i].Extras["detail"]; ok {
			t.Errorf("blocks[%d] 的 detail 同时留在 Extras 里，是双重表示: %+v", i, blocks[i].Extras)
		}
	}
}

func TestDecodeRequestUnknownContentStaysUnknown(t *testing.T) {
	body := `{"model":"m","input":[{"type":"message","role":"user","content":[
		{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}
	]}]}`
	req, err := NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 1 || blocks[0].Kind != "input_audio" {
		t.Fatalf("input_audio 不应被折成 BlockText: %+v", blocks)
	}
	if blocks[0].Text != "" {
		t.Errorf("未知 part 不该填 Text: %q", blocks[0].Text)
	}
	if blocks[0].Extras["input_audio"] == nil {
		t.Errorf("未知 part 的字段应进 Extras: %+v", blocks[0].Extras)
	}
}

func TestDecodeRequestSkipsEmptyInputImage(t *testing.T) {
	body := `{"model":"m","input":[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"看图"},
		{"type":"input_image","image_url":"data:image/png;base64,"},
		{"type":"input_image","image_url":"data:image/png;base64,   "}
	]}]}`
	req, err := NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 1 || blocks[0].Kind != protocol.BlockText {
		t.Errorf("空 data URI 应跳过，实得 %+v", blocks)
	}
}
