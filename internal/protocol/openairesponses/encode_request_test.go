package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

// 本文件测 Responses 的**编码**侧（#80，CC→R 与 A→R 的去程）。
//
// 后半段用真实入站样本走「入口 codec 解 → 本 codec 编」的半链，钉的是端到端形态；
// 前半段用手搭 canonical 逐项钉规则，两者互补：手搭的能造出样本里没有的组合
// （server 工具、孤儿结果、thinking 块），样本能证明我们没在纸上谈兵。

// encodeOut 编一次请求并解回 map，省得每个用例都写一遍。
func encodeOut(t *testing.T, req *protocol.Request, stream bool) (map[string]any, []string) {
	t.Helper()
	body, dropped, err := NewCodec().EncodeRequestReport(req, stream)
	if err != nil {
		t.Fatalf("EncodeRequestReport: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("编出来的不是 JSON: %v", err)
	}
	return out, dropped
}

func items(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	raw, ok := out["input"].([]any)
	if !ok {
		t.Fatalf("input 不是数组: %T", out["input"])
	}
	res := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("input 项不是对象: %T", it)
		}
		res = append(res, m)
	}
	return res
}

func hasDrop(dropped []string, want string) bool {
	for _, d := range dropped {
		if d == want {
			return true
		}
	}
	return false
}

func textBlock(s string) protocol.Block { return protocol.Block{Kind: protocol.BlockText, Text: s} }

// TestEncodeSystemBecomesDeveloperItem：系统内容走 input 里的 developer 消息项，
// **不写顶层 instructions**——五份真实 Responses 请求都没有那个字段。
func TestEncodeSystemBecomesDeveloperItem(t *testing.T) {
	req := &protocol.Request{
		Model:    "gpt-5.6",
		System:   []protocol.Block{textBlock("you are helpful")},
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{textBlock("hi")}}},
	}
	out, _ := encodeOut(t, req, false)

	if _, ok := out["instructions"]; ok {
		t.Error("发了 instructions：真实客户端不发它，严格中转会拒生字段")
	}
	got := items(t, out)
	if len(got) != 2 {
		t.Fatalf("input 项数 = %d, want 2: %v", len(got), got)
	}
	if got[0]["type"] != "message" || got[0]["role"] != "developer" {
		t.Errorf("首项 = %v, want developer 消息项", got[0])
	}
	parts := got[0]["content"].([]any)
	part := parts[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "you are helpful" {
		t.Errorf("developer 部件 = %v, want input_text", part)
	}
	if got[1]["role"] != "user" {
		t.Errorf("第二项 = %v, want user", got[1])
	}
}

// system 里的图必须发成 input_image，不能经 joinOutBlocks 静默蒸发。
func TestEncodeSystemImagesAreEmitted(t *testing.T) {
	out, dropped := encodeOut(t, &protocol.Request{
		Model: "m",
		System: []protocol.Block{
			textBlock("看图"),
			{Kind: protocol.BlockImage, Image: &protocol.Image{URL: "https://example.com/a.png"}},
		},
	}, false)
	if hasDrop(dropped, DropVendorContent) {
		t.Errorf("system 图不该 vendor_content: %v", dropped)
	}
	got := items(t, out)
	if len(got) != 1 || got[0]["role"] != "developer" {
		t.Fatalf("input = %v", got)
	}
	parts, _ := got[0]["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("developer content = %v", parts)
	}
	p1, _ := parts[1].(map[string]any)
	if p1["type"] != "input_image" || p1["image_url"] != "https://example.com/a.png" {
		t.Errorf("system 图没发出去: %v", p1)
	}
}

// TestEncodeMidConversationSystemStaysInPlace：中段的 system 消息原位编成 developer，
// 不上提。Responses 的 input 是有序列表，本来就装得下它站在哪。
func TestEncodeMidConversationSystemStaysInPlace(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleUser, Content: []protocol.Block{textBlock("a")}},
			{Role: protocol.RoleSystem, Content: []protocol.Block{textBlock("rule")}},
			{Role: protocol.RoleUser, Content: []protocol.Block{textBlock("b")}},
		},
	}
	got := items(t, mustOut(t, req))
	if len(got) != 3 || got[1]["role"] != "developer" {
		t.Fatalf("input = %v, want 第二项是 developer", got)
	}
}

func mustOut(t *testing.T, req *protocol.Request) map[string]any {
	t.Helper()
	out, _ := encodeOut(t, req, false)
	return out
}

// TestEncodeAssistantUsesOutputText：assistant 正文用 output_text，user 用 input_text。
func TestEncodeAssistantUsesOutputText(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleUser, Content: []protocol.Block{textBlock("q")}},
			{Role: protocol.RoleAssistant, Content: []protocol.Block{textBlock("a")}},
		},
	}
	got := items(t, mustOut(t, req))
	userPart := got[0]["content"].([]any)[0].(map[string]any)
	asstPart := got[1]["content"].([]any)[0].(map[string]any)
	if userPart["type"] != "input_text" {
		t.Errorf("user 部件 = %v, want input_text", userPart)
	}
	if asstPart["type"] != "output_text" {
		t.Errorf("assistant 部件 = %v, want output_text", asstPart)
	}
}

// TestEncodeToolCallAndOutputArePairedTopLevelItems：工具调用与结果都是**顶层项**，
// 靠 call_id 配对，且结果排在下一轮正文之前。
func TestEncodeToolCallAndOutputArePairedTopLevelItems(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleUser, Content: []protocol.Block{textBlock("q")}},
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				textBlock("calling"),
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{
					ID: "call_1", Name: "get_weather", Args: `{"city":"SH"}`, ArgsIsJSON: true,
				}},
			}},
			{Role: protocol.RoleUser, Content: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
					ToolCallID: "call_1", Content: []protocol.Block{textBlock("sunny")},
				}},
				textBlock("thanks"),
			}},
		},
	}
	got := items(t, mustOut(t, req))
	var types []string
	for _, it := range got {
		types = append(types, it["type"].(string))
	}
	want := []string{"message", "message", "function_call", "function_call_output", "message"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("项序 = %v, want %v", types, want)
	}
	call := got[2]
	if call["call_id"] != "call_1" || call["name"] != "get_weather" || call["arguments"] != `{"city":"SH"}` {
		t.Errorf("function_call = %v", call)
	}
	if _, nested := call["function"]; nested {
		t.Error("工具调用套了一层 function：那是 CC 的形状，Responses 是扁平的")
	}
	res := got[3]
	if res["call_id"] != "call_1" || res["output"] != "sunny" {
		t.Errorf("function_call_output = %v，output 应是纯字符串", res)
	}
}

// TestEncodeEmptyToolOutputGetsPlaceholder：空结果发 "(empty)"（照 sub2api，部分上游拒空串）。
func TestEncodeEmptyToolOutputGetsPlaceholder(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{ID: "c", Name: "n", Args: "{}", ArgsIsJSON: true}},
			}},
			{Role: protocol.RoleTool, Content: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{ToolCallID: "c"}},
			}},
		},
	}
	got := items(t, mustOut(t, req))
	if got[1]["output"] != "(empty)" {
		t.Errorf("空结果 output = %v, want \"(empty)\"", got[1]["output"])
	}
}

// TestEncodeOrphanToolResultDropped：引用不存在调用的结果丢掉并登记。
func TestEncodeOrphanToolResultDropped(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleTool, Content: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
					ToolCallID: "ghost", Content: []protocol.Block{textBlock("x")},
				}},
			}},
		},
	}
	out, dropped := encodeOut(t, req, false)
	if len(items(t, out)) != 0 {
		t.Errorf("孤儿结果没丢: %v", out["input"])
	}
	if !hasDrop(dropped, DropOrphanResult) {
		t.Errorf("dropped = %v, want 含 %s", dropped, DropOrphanResult)
	}
}

// TestEncodeCustomArgsAreWrapped：ArgsIsJSON 为假的入参包成 {"input":…}，
// 因为 Responses 的 function_call.arguments 按契约是 JSON 字符串。
func TestEncodeCustomArgsAreWrapped(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{
					ID: "c", Name: "exec", Args: "console.log(1)",
				}},
			}},
		},
	}
	got := items(t, mustOut(t, req))
	if got[0]["type"] != "function_call" {
		t.Fatalf("首项 = %v, want function_call（出口一律不合成 custom_tool_call）", got[0])
	}
	var wrapped map[string]string
	if json.Unmarshal([]byte(got[0]["arguments"].(string)), &wrapped) != nil ||
		wrapped[protocol.CustomToolArgsKey] != "console.log(1)" {
		t.Errorf("arguments = %v, want 包装过的", got[0]["arguments"])
	}
}

// TestEncodeToolsAreFlat：工具声明是扁平的，custom 工具补一份合成 schema。
func TestEncodeToolsAreFlat(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Tools: []protocol.Tool{
			{Kind: protocol.ToolFunction, Name: "f", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)},
			{Kind: protocol.ToolCustom, Name: "exec", Description: "js"},
			{Kind: protocol.ToolServer, Name: "web_search"},
		},
	}
	out, dropped := encodeOut(t, req, false)
	tools := out["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("工具数 = %d, want 2（server 工具丢掉）", len(tools))
	}
	fn := tools[0].(map[string]any)
	if fn["type"] != "function" || fn["name"] != "f" {
		t.Errorf("工具 = %v, want 扁平 function", fn)
	}
	if _, nested := fn["function"]; nested {
		t.Error("工具声明套了一层 function：那是 CC 的形状")
	}
	custom := tools[1].(map[string]any)
	schema, _ := json.Marshal(custom["parameters"])
	if !strings.Contains(string(schema), `"`+protocol.CustomToolArgsKey+`"`) {
		t.Errorf("custom 工具的 parameters = %s, want 合成的 CustomToolSchema", schema)
	}
	for _, want := range []string{DropServerTool, DropToolGrammar} {
		if !hasDrop(dropped, want) {
			t.Errorf("dropped = %v, want 含 %s", dropped, want)
		}
	}
}

// TestEncodeToolChoice：三种直传 + 指名形态 + 两种该省略的组合。
func TestEncodeToolChoice(t *testing.T) {
	tools := []protocol.Tool{{Kind: protocol.ToolFunction, Name: "f", Schema: json.RawMessage(`{}`)}}
	cases := []struct {
		name   string
		tools  []protocol.Tool
		choice protocol.ToolChoice
		want   any // nil 表示该省略
	}{
		{"auto", tools, protocol.ToolChoice{Mode: "auto"}, "auto"},
		{"none", tools, protocol.ToolChoice{Mode: "none"}, "none"},
		{"required", tools, protocol.ToolChoice{Mode: "required"}, "required"},
		{"named", tools, protocol.ToolChoice{Mode: "tool", Name: "f"},
			map[string]any{"type": "function", "name": "f"}},
		{"没有工具就不发", nil, protocol.ToolChoice{Mode: "auto"}, nil},
		{"指名一个没声明的工具就不发", tools, protocol.ToolChoice{Mode: "tool", Name: "ghost"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustOut(t, &protocol.Request{Model: "m", Tools: tc.tools, ToolChoice: tc.choice})
			got, present := out["tool_choice"]
			if tc.want == nil {
				if present {
					t.Errorf("tool_choice = %v, want 省略", got)
				}
				return
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tc.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("tool_choice = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestEncodeTopLevelKeys：顶层键的取舍。
func TestEncodeTopLevelKeys(t *testing.T) {
	temp := 0.7
	req := &protocol.Request{
		Model:       "gpt-5.6",
		MaxTokens:   512,
		Temperature: &temp,
		Stop:        []string{"END"},
		Messages:    []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{textBlock("hi")}}},
	}
	out, dropped := encodeOut(t, req, true)

	if out["model"] != "gpt-5.6" {
		t.Errorf("model = %v", out["model"])
	}
	if out["stream"] != true {
		t.Errorf("stream = %v, want true", out["stream"])
	}
	if out["store"] != false {
		t.Errorf("store = %v, want false（恒发，不把留存与否交给上游默认值）", out["store"])
	}
	if out["max_output_tokens"] != float64(512) {
		t.Errorf("max_output_tokens = %v", out["max_output_tokens"])
	}
	// 采样参数一律不发：gpt-5.x 推理模型收到就 400，Responses 也压根没有 stop 参数。
	for _, k := range []string{"temperature", "top_p", "stop", "max_tokens"} {
		if _, ok := out[k]; ok {
			t.Errorf("发了 %s：本不该带", k)
		}
	}
	if !hasDrop(dropped, DropSampling) {
		t.Errorf("dropped = %v, want 含 %s——不发不等于没发生", dropped, DropSampling)
	}
}

// TestEncodeMaxTokensOmittedWhenZero：没限就整键省略，不合成默认值。
func TestEncodeMaxTokensOmittedWhenZero(t *testing.T) {
	out := mustOut(t, &protocol.Request{Model: "m"})
	if _, ok := out["max_output_tokens"]; ok {
		t.Error("MaxTokens 为零仍发了 max_output_tokens")
	}
	if _, ok := out["stream"]; ok {
		t.Error("非流式仍发了 stream")
	}
}

// TestEncodeThinkingDroppedNotFabricated：thinking 块丢掉并登记，绝不合成 reasoning 项。
func TestEncodeThinkingDroppedNotFabricated(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{{Role: protocol.RoleAssistant, Content: []protocol.Block{
			{Kind: protocol.BlockThinking, Text: "let me think", Extras: map[string]any{"signature": "sig123"}},
			textBlock("answer"),
		}}},
	}
	body, dropped, err := NewCodec().EncodeRequestReport(req, false)
	if err != nil {
		t.Fatalf("EncodeRequestReport: %v", err)
	}
	for _, leak := range []string{"let me think", "sig123", "reasoning", "encrypted_content"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("请求体里出现了 %q：跨协议推理只能丢、不得伪造（口径层 v0.10）", leak)
		}
	}
	if !hasDrop(dropped, DropThinking) {
		t.Errorf("dropped = %v, want 含 %s", dropped, DropThinking)
	}
}

// TestEncodeVendorDropsRegistered：Extras 永不外带，认不得的块登记 vendor_content。
func TestEncodeVendorDropsRegistered(t *testing.T) {
	req := &protocol.Request{
		Model:  "m",
		Extras: map[string]any{"metadata": map[string]any{"user_id": "u"}, "thinking": map[string]any{}},
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{
			{Kind: "input_audio", Extras: map[string]any{"cache_control": map[string]any{}}},
		}}},
	}
	body, dropped, err := NewCodec().EncodeRequestReport(req, false)
	if err != nil {
		t.Fatalf("EncodeRequestReport: %v", err)
	}
	for _, leak := range []string{"metadata", "user_id", "thinking"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("Extras 外带了 %q", leak)
		}
	}
	for _, want := range []string{DropMetadata, DropVendorRequest, DropVendorContent, DropCacheControl} {
		if !hasDrop(dropped, want) {
			t.Errorf("dropped = %v, want 含 %s", dropped, want)
		}
	}
}

// TestEncodeNilRequest：nil 要报错而不是 panic。
func TestEncodeRequestInputImageParts(t *testing.T) {
	out, dropped := encodeOut(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{
			textBlock("看图"),
			{Kind: protocol.BlockImage, Image: &protocol.Image{MediaType: "image/png", Data: tinyPNG}},
		}}},
	}, false)
	if hasDrop(dropped, DropVendorContent) {
		t.Errorf("图片已转换，不该 vendor_content: %v", dropped)
	}
	got := items(t, out)
	if len(got) != 1 {
		t.Fatalf("input 项数 = %d", len(got))
	}
	parts, _ := got[0]["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("content 块数 = %d，期望 input_text + input_image", len(parts))
	}
	p0, _ := parts[0].(map[string]any)
	if p0["type"] != "input_text" || p0["text"] != "看图" {
		t.Errorf("文本 part = %v", p0)
	}
	p1, _ := parts[1].(map[string]any)
	if p1["type"] != "input_image" || p1["image_url"] != "data:image/png;base64,"+tinyPNG {
		t.Errorf("图片 part = %v", p1)
	}
}

// detail 编在 part **顶层**，与 image_url 平级（CC 那边在 image_url 对象里面）。
// 空 detail 不许凭空造出这一格：「没指定」与「指定了 auto」不是一回事。
func TestEncodeRequestInputImageDetail(t *testing.T) {
	imagePart := func(t *testing.T, img *protocol.Image) map[string]any {
		t.Helper()
		out, _ := encodeOut(t, &protocol.Request{
			Model: "m",
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{
				{Kind: protocol.BlockImage, Image: img},
			}}},
		}, false)
		parts, _ := items(t, out)[0]["content"].([]any)
		if len(parts) != 1 {
			t.Fatalf("content 块数 = %d，期望 1", len(parts))
		}
		p, _ := parts[0].(map[string]any)
		return p
	}

	t.Run("data 图带 detail", func(t *testing.T) {
		p := imagePart(t, &protocol.Image{MediaType: "image/png", Data: tinyPNG, Detail: "high"})
		if p["detail"] != "high" {
			t.Errorf("detail = %v，期望 high；且它必须在 part 顶层: %v", p["detail"], p)
		}
	})
	t.Run("url 图带 detail", func(t *testing.T) {
		p := imagePart(t, &protocol.Image{URL: "https://example.com/a.png", Detail: "low"})
		if p["detail"] != "low" {
			t.Errorf("detail = %v，期望 low", p["detail"])
		}
	})
	t.Run("auto 照发", func(t *testing.T) {
		p := imagePart(t, &protocol.Image{URL: "https://example.com/a.png", Detail: "auto"})
		if p["detail"] != "auto" {
			t.Errorf("auto 不是「等于没指定」，应原样发出，实得 %v", p["detail"])
		}
	})
	t.Run("空 detail 不发这一格", func(t *testing.T) {
		p := imagePart(t, &protocol.Image{URL: "https://example.com/a.png"})
		if _, ok := p["detail"]; ok {
			t.Errorf("客户端没指定就不该有 detail 键: %v", p)
		}
	})
}

func TestEncodeRequestDropsImageFileID(t *testing.T) {
	out, dropped := encodeOut(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.Block{
			textBlock("看图"),
			{Kind: protocol.BlockImage, Image: &protocol.Image{FileID: "file-xxx"}},
		}}},
	}, false)
	if !hasDrop(dropped, DropImageFileID) {
		t.Errorf("file_id 应登记 %s: %v", DropImageFileID, dropped)
	}
	if hasDrop(dropped, DropVendorContent) {
		t.Errorf("file_id 不该混进 vendor_content: %v", dropped)
	}
	got := items(t, out)
	if len(got) != 1 {
		t.Fatalf("input 项数 = %d", len(got))
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "file-xxx") || strings.Contains(string(raw), "input_image") {
		t.Errorf("file_id 图不该出现: %s", raw)
	}
}

func TestEncodeRequestLiftsToolResultImages(t *testing.T) {
	out, dropped := encodeOut(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{
					ID: "call_1", Name: "f", Args: `{}`, ArgsIsJSON: true,
				}},
			}},
			{Role: protocol.RoleUser, Content: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
					ToolCallID: "call_1",
					Content: []protocol.Block{
						textBlock("结果文本"),
						{Kind: protocol.BlockImage, Image: &protocol.Image{MediaType: "image/png", Data: tinyPNG}},
					},
				}},
			}},
		},
	}, false)
	if hasDrop(dropped, DropVendorContent) {
		t.Errorf("抬图不该 vendor_content: %v", dropped)
	}
	got := items(t, out)
	// function_call + function_call_output + 抬出的 user
	if len(got) != 3 {
		t.Fatalf("input 项数 = %d，期望 3: %v", len(got), got)
	}
	if got[1]["type"] != "function_call_output" || got[1]["output"] != "结果文本" {
		t.Errorf("output 应是文本，实得 %v", got[1])
	}
	if got[2]["type"] != "message" || got[2]["role"] != "user" {
		t.Errorf("抬出的应是 user 消息: %v", got[2])
	}
	parts, _ := got[2]["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("抬出的 content = %v", parts)
	}
	p, _ := parts[0].(map[string]any)
	if p["type"] != "input_image" || p["image_url"] != "data:image/png;base64,"+tinyPNG {
		t.Errorf("抬出的图不对: %v", p)
	}
}

// 多张图抬进同一条 user，不按张拆消息。
func TestEncodeRequestLiftsToolResultImagesIntoOneUserMessage(t *testing.T) {
	out, _ := encodeOut(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{
					ID: "call_1", Name: "f", Args: `{}`, ArgsIsJSON: true,
				}},
			}},
			{Role: protocol.RoleUser, Content: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
					ToolCallID: "call_1",
					Content: []protocol.Block{
						{Kind: protocol.BlockImage, Image: &protocol.Image{URL: "https://a.example/1.png"}},
						{Kind: protocol.BlockImage, Image: &protocol.Image{URL: "https://a.example/2.png"}},
					},
				}},
			}},
		},
	}, false)
	got := items(t, out)
	if len(got) != 3 {
		t.Fatalf("input 项数 = %d，期望 function_call / output / 一条 user: %v", len(got), got)
	}
	parts, _ := got[2]["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("应是一条 user 里两张图，实得 %v", parts)
	}
}

func TestEncodeNilRequest(t *testing.T) {
	if _, err := NewCodec().EncodeRequest(nil, false); err == nil {
		t.Fatal("nil 请求没报错")
	}
}

// TestEncodeRequestReporterInterface：本 codec 得兑现 protocol.RequestEncodeReporter，
// 否则 relay 的类型断言会静默落空，跨协议丢弃永远打不出日志。
func TestEncodeRequestReporterInterface(t *testing.T) {
	var _ protocol.RequestEncodeReporter = NewCodec()
}

// --- 半链：真实入站样本 → 入口 codec 解 → 本 codec 编 ---

// TestEncodeFromGoldenCCRequest：in-cc-tool-turn2 的真实入站请求走 CC 解 → R 编。
// 这份样本齐全（system + 10 个工具 + assistant 的 tool_calls + role=tool 结果）。
func TestEncodeFromGoldenCCRequest(t *testing.T) {
	body := readGoldenRequest(t, "in-cc-tool-turn2")
	req, err := openaicc.NewCodec().DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("CC 入口解不动: %v", err)
	}
	out, _ := encodeOut(t, req, true)

	if _, ok := out["instructions"]; ok {
		t.Error("发了 instructions")
	}
	if out["store"] != false {
		t.Error("没发 store:false")
	}
	if _, ok := out["temperature"]; ok {
		t.Error("发了 temperature")
	}
	got := items(t, out)
	if len(got) == 0 {
		t.Fatal("input 为空")
	}
	if got[0]["role"] != "developer" {
		t.Errorf("首项 = %v, want developer（CC 的 system 消息）", got[0])
	}
	if len(req.Tools) > 0 {
		tools, ok := out["tools"].([]any)
		if !ok || len(tools) == 0 {
			t.Fatal("工具声明没编出来")
		}
		fn := tools[0].(map[string]any)
		if fn["type"] != "function" || fn["name"] == nil {
			t.Errorf("工具 = %v, want 扁平 function", fn)
		}
	}
}

// TestEncodeFromGoldenAnthropicRequest：in-anthropic-tool-turn2 走 A 解 → R 编。
// 这份样本带工具调用与结果，正好钉住 call_id 在整条链上原样携带。
func TestEncodeFromGoldenAnthropicRequest(t *testing.T) {
	body := readGoldenRequest(t, "in-anthropic-tool-turn2")
	req, err := anthropic.NewCodec().DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("Anthropic 入口解不动: %v", err)
	}
	out, _ := encodeOut(t, req, true)
	got := items(t, out)

	var calls, results int
	callIDs := map[string]bool{}
	for _, it := range got {
		switch it["type"] {
		case "function_call":
			calls++
			callIDs[it["call_id"].(string)] = true
		case "function_call_output":
			results++
			if !callIDs[it["call_id"].(string)] {
				t.Errorf("结果 %v 的 call_id 对不上任何调用", it["call_id"])
			}
		}
	}
	if calls == 0 || results == 0 {
		t.Fatalf("样本里的工具调用/结果没编出来: calls=%d results=%d", calls, results)
	}
	if got[0]["role"] != "developer" {
		t.Errorf("首项 = %v, want developer（Anthropic 的 system 数组）", got[0])
	}
}

// 同 openaicc 那条：一条 user 消息挂多个 tool_result，头一个带图，抬出来的 user 项
// 必须落在**所有**工具结果项之后，不许夹在两个 function_call_output 中间。
func TestEncodeRequestLiftsImagesAfterAllToolOutputs(t *testing.T) {
	out, _ := encodeOut(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{
					ID: "call_1", Name: "f", Args: `{}`, ArgsIsJSON: true,
				}},
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{
					ID: "call_2", Name: "g", Args: `{}`, ArgsIsJSON: true,
				}},
			}},
			{Role: protocol.RoleUser, Content: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
					ToolCallID: "call_1",
					Content: []protocol.Block{
						{Kind: protocol.BlockImage, Image: &protocol.Image{
							URL: "https://a.example/1.png", Detail: "high",
						}},
					},
				}},
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
					ToolCallID: "call_2",
					Content:    []protocol.Block{{Kind: protocol.BlockText, Text: "第二个结果"}},
				}},
			}},
		},
	}, false)

	var types []string
	for _, it := range items(t, out) {
		typ, _ := it["type"].(string)
		types = append(types, typ)
	}
	want := []string{"function_call", "function_call", "function_call_output", "function_call_output", "message"}
	if len(types) != len(want) {
		t.Fatalf("项类型序列 = %v，期望 %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("项类型序列 = %v，期望 %v——抬出来的图夹进了两个工具结果中间", types, want)
		}
	}

	// 抬图与普通图走同一个 part 编码器，detail 本该顺带就过去了——但「本该」不是断言。
	parts, _ := items(t, out)[4]["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("抬出来的 parts = %v", parts)
	}
	p, _ := parts[0].(map[string]any)
	if p["detail"] != "high" {
		t.Errorf("抬出来的图丢了 detail: %v", p)
	}
}
