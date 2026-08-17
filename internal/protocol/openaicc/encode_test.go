package openaicc_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
)

const goldenDir = "../../../testdata/golden"

// ccRequest 是编码结果的解析形态。只列断言用得上的字段。
type ccRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"messages"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
	MaxTokens     int             `json:"max_tokens"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`

	// 入口协议独有的顶层字段一个都不该出现在这里。
	Metadata          json.RawMessage `json:"metadata"`
	Thinking          json.RawMessage `json:"thinking"`
	ContextManagement json.RawMessage `json:"context_management"`
	OutputConfig      json.RawMessage `json:"output_config"`
}

// decodeSample 把真实入站样本解成 canonical，作为编码侧用例的输入。
//
// 用真实样本而不是手搭 canonical：手搭的 Request 只会长成我以为的样子，而这条
// 链路真正要扛的是 Claude Code 实际发出来的形态（portage-legacy#11 的验收项之一）。
func decodeSample(t *testing.T, name string) *protocol.Request {
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
		Direction string `json:"direction"`
		Stream    bool   `json:"stream"`
		Verified  bool   `json:"verified"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	// 与 anthropic/decode_test.go 同一道人工关卡：没核过的样本不许当输入。
	if !meta.Verified {
		t.Fatalf("%s 的 meta.json 仍是 verified:false", name)
	}
	body, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	req, err := anthropic.NewCodec().DecodeRequest(body, meta.Stream)
	if err != nil {
		t.Fatalf("%s 解码失败: %v", name, err)
	}
	return req
}

func encode(t *testing.T, req *protocol.Request, stream bool) ([]byte, []string, ccRequest) {
	t.Helper()
	body, dropped, err := openaicc.NewCodec().EncodeRequestReport(req, stream)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var out ccRequest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("编出来的不是合法 JSON: %v\n%s", err, body)
	}
	return body, dropped, out
}

// 严格中转的第一条：content 必须是**字符串**。第三方 OpenAI 兼容上游对数组形态的
// content 直接拒，而 Anthropic 那边 content 恒是数组。
func TestEncodeRequestFlattensContentToPlainText(t *testing.T) {
	req := decodeSample(t, "in-anthropic-text")
	_, _, out := encode(t, req, true)

	if len(out.Messages) == 0 {
		t.Fatal("没编出任何消息")
	}
	for i, m := range out.Messages {
		if len(m.Content) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			t.Errorf("messages[%d].content 不是字符串: %s", i, m.Content)
		}
	}
	if out.Messages[0].Role != "system" {
		t.Errorf("第一条消息 role = %q, 期望 system（System 块序列前置成一条）", out.Messages[0].Role)
	}
}

// 流式必须强制注入 stream_options.include_usage：客户端发的是 Anthropic 请求，
// 不可能自己带上，不注入则流末没有 usage 帧，调用日志的 token 数恒为零。
func TestEncodeRequestForcesIncludeUsageOnStream(t *testing.T) {
	req := decodeSample(t, "in-anthropic-text")

	_, _, stream := encode(t, req, true)
	if !stream.Stream {
		t.Error("stream 没置上")
	}
	if stream.StreamOptions == nil || !stream.StreamOptions.IncludeUsage {
		t.Errorf("流式请求缺 stream_options.include_usage: %+v", stream.StreamOptions)
	}

	// 非流式不该带它：那是流式专用参数，带上会被严格上游当成非法字段。
	_, _, buffered := encode(t, req, false)
	if buffered.Stream {
		t.Error("非流式请求不该带 stream:true")
	}
	if buffered.StreamOptions != nil {
		t.Error("非流式请求不该带 stream_options")
	}
}

// tool_use → tool_calls、tool_result → 独立的 role=tool 消息，且 id 原样携带。
// id 一旦重编号，工具结果就对不回调用。
func TestEncodeRequestMapsToolRoundTrip(t *testing.T) {
	req := decodeSample(t, "in-anthropic-parallel-turn2")
	_, _, out := encode(t, req, true)

	var callIDs, resultIDs []string
	var assistantIdx = -1
	for i, m := range out.Messages {
		if len(m.ToolCalls) > 0 {
			if m.Role != "assistant" {
				t.Errorf("messages[%d] 带 tool_calls 但 role = %q", i, m.Role)
			}
			assistantIdx = i
			for _, call := range m.ToolCalls {
				if call.Type != "function" {
					t.Errorf("tool_call.type = %q, 期望 function", call.Type)
				}
				var probe map[string]any
				if json.Unmarshal([]byte(call.Function.Arguments), &probe) != nil {
					t.Errorf("function.arguments 不是 JSON 字符串: %q", call.Function.Arguments)
				}
				callIDs = append(callIDs, call.ID)
			}
		}
		if m.Role == "tool" {
			if assistantIdx < 0 || i < assistantIdx {
				t.Errorf("role=tool 出现在带 tool_calls 的 assistant 之前（messages[%d]）", i)
			}
			resultIDs = append(resultIDs, m.ToolCallID)
		}
	}

	if len(callIDs) < 2 {
		t.Fatalf("并行样本只编出 %d 个 tool_call", len(callIDs))
	}
	if len(callIDs) != len(resultIDs) {
		t.Fatalf("tool_call %d 个、role=tool 消息 %d 条，对不上", len(callIDs), len(resultIDs))
	}
	for i := range callIDs {
		if callIDs[i] != resultIDs[i] {
			t.Errorf("第 %d 对 id 不一致: %q vs %q", i, callIDs[i], resultIDs[i])
		}
	}
}

// 服务端工具丢掉、function 工具带上 schema。带过去只会被上游拒——它是上游侧能力，
// 不是一段可以转述的声明。
func TestEncodeRequestDropsServerTools(t *testing.T) {
	req := decodeSample(t, "in-anthropic-tool-turn1")
	_, dropped, out := encode(t, req, true)

	for _, tool := range out.Tools {
		if tool.Type != "function" {
			t.Errorf("tools 里出现非 function 形态: %q", tool.Type)
		}
		if tool.Function.Name == "advisor" {
			t.Error("服务端工具 advisor 被编给了 CC 上游")
		}
		if len(tool.Function.Parameters) == 0 {
			t.Errorf("工具 %q 没带 parameters", tool.Function.Name)
		}
	}
	if !contains(dropped, openaicc.DropServerTool) {
		t.Errorf("丢了服务端工具却没登记: %v", dropped)
	}
}

// 三样必然的丢弃都要登记在案，由 relay 打警告——口径层 §2.6 要的是「丢弃 + 日志
// 警告」，静默丢弃是明令禁止的那一种。
func TestEncodeRequestReportsMandatoryDrops(t *testing.T) {
	req := decodeSample(t, "in-anthropic-tool-turn2")
	body, dropped, out := encode(t, req, true)

	for _, want := range []string{
		openaicc.DropMetadata,     // 上游据此判定是否官方 Claude Code
		openaicc.DropCacheControl, // CC 没有缓存断点的概念
		openaicc.DropThinking,     // 跨协议丢弃，不做伪映射
	} {
		if !contains(dropped, want) {
			t.Errorf("丢弃清单缺 %q: %v", want, dropped)
		}
	}

	// 登记归登记，字段是真的不能出现在发给上游的请求里。
	for name, raw := range map[string]json.RawMessage{
		"metadata":           out.Metadata,
		"thinking":           out.Thinking,
		"context_management": out.ContextManagement,
		"output_config":      out.OutputConfig,
	} {
		if len(raw) > 0 {
			t.Errorf("入口协议独有字段 %s 漏到了 CC 请求里: %s", name, raw)
		}
	}
	if strings.Contains(string(body), "cache_control") {
		t.Error("cache_control 漏到了 CC 请求里")
	}
	if strings.Contains(string(body), "signature") {
		t.Error("thinking 的 signature 漏到了 CC 请求里")
	}
}

// tool_choice 的两种非法组合会被严格上游整体拒收，编码侧就得挡掉（§5 坑清单）。
func TestEncodeRequestSanitizesToolChoice(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	cases := []struct {
		name   string
		req    protocol.Request
		expect string // "" 表示不该出现 tool_choice
	}{
		{
			name: "有 tool_choice 没有 tools",
			req: protocol.Request{
				Model:      "m",
				ToolChoice: protocol.ToolChoice{Mode: "auto"},
			},
		},
		{
			name: "tool_choice 指名未声明的工具",
			req: protocol.Request{
				Model:      "m",
				Tools:      []protocol.Tool{{Kind: protocol.ToolFunction, Name: "Read", Schema: schema}},
				ToolChoice: protocol.ToolChoice{Mode: "tool", Name: "没声明过的"},
			},
		},
		{
			name: "服务端工具不算声明过",
			req: protocol.Request{
				Model:      "m",
				Tools:      []protocol.Tool{{Kind: protocol.ToolServer, Name: "advisor"}},
				ToolChoice: protocol.ToolChoice{Mode: "tool", Name: "advisor"},
			},
		},
		{
			name: "指名已声明的工具",
			req: protocol.Request{
				Model:      "m",
				Tools:      []protocol.Tool{{Kind: protocol.ToolFunction, Name: "Read", Schema: schema}},
				ToolChoice: protocol.ToolChoice{Mode: "tool", Name: "Read"},
			},
			expect: `{"type":"function","function":{"name":"Read"}}`,
		},
		{
			name: "required 透传",
			req: protocol.Request{
				Model:      "m",
				Tools:      []protocol.Tool{{Kind: protocol.ToolFunction, Name: "Read", Schema: schema}},
				ToolChoice: protocol.ToolChoice{Mode: "required"},
			},
			expect: `"required"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, out := encode(t, &tc.req, false)
			if tc.expect == "" {
				if len(out.ToolChoice) > 0 {
					t.Errorf("tool_choice 不该出现: %s", out.ToolChoice)
				}
				return
			}
			// 比结构不比字节：map 编出来的键序是排过的，拿字符串比等于顺带把
			// encoding/json 的键序实现当成契约。
			var got, want any
			if err := json.Unmarshal(out.ToolChoice, &got); err != nil {
				t.Fatalf("tool_choice 不是合法 JSON: %s", out.ToolChoice)
			}
			if err := json.Unmarshal([]byte(tc.expect), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("tool_choice = %s, 期望 %s", out.ToolChoice, tc.expect)
			}
		})
	}
}

// 纯 thinking 的 assistant 轮编出来会既没 content 也没 tool_calls——那样一条消息
// 严格上游会拒，整条略过才对。
func TestEncodeRequestSkipsEmptyAssistantMessage(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleUser, Content: []protocol.Block{{Kind: protocol.BlockText, Text: "hi"}}},
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{Kind: protocol.BlockThinking, Text: "想了想"}}},
			{Role: protocol.RoleUser, Content: []protocol.Block{{Kind: protocol.BlockText, Text: "还在吗"}}},
		},
	}
	_, dropped, out := encode(t, req, false)

	if len(out.Messages) != 2 {
		t.Fatalf("编出 %d 条消息，期望 2 条（空 assistant 被略过）", len(out.Messages))
	}
	for _, m := range out.Messages {
		if m.Role == "assistant" {
			t.Error("空的 assistant 消息没被略过")
		}
	}
	if !contains(dropped, openaicc.DropThinking) {
		t.Errorf("丢了 thinking 却没登记: %v", dropped)
	}
}

// 非 JSON 入参要包成 JSON 对象：CC 的 function.arguments 按契约必须是 JSON 字符串。
// A 入口走不到这条分支（Anthropic 的 input 恒是对象），但 portage-legacy#12 的 Codex custom 工具
// 会——这里先把不变量钉住。
func TestEncodeRequestWrapsNonJSONToolArgs(t *testing.T) {
	req := &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{{
			Role: protocol.RoleAssistant,
			Content: []protocol.Block{{
				Kind: protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{
					ID: "call_1", Name: "exec",
					Args: "await Promise.all([...])", ArgsIsJSON: false,
				},
			}},
		}},
	}
	_, _, out := encode(t, req, false)

	args := out.Messages[0].ToolCalls[0].Function.Arguments
	var probe map[string]string
	if err := json.Unmarshal([]byte(args), &probe); err != nil {
		t.Fatalf("非 JSON 入参没被包成 JSON 对象: %q", args)
	}
	if probe["input"] != "await Promise.all([...])" {
		t.Errorf("包装后取不回原文: %+v", probe)
	}
}

// Anthropic 入口的 image 块转到 CC 必须带上图，不能再登记 vendor_content。
func TestEncodeRequestReportsImageBlocks(t *testing.T) {
	req := decodeAnthropic(t, `{
		"model": "claude-sonnet-4", "max_tokens": 64,
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "这张图里是什么"},
			{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "`+tinyPNG+`"}}
		]}]
	}`)
	body, dropped, out := encode(t, req, false)

	if contains(dropped, openaicc.DropVendorContent) {
		t.Errorf("图片已转换，不该再登记 vendor_content: %v", dropped)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("消息数 = %d，期望 1", len(out.Messages))
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(out.Messages[0].Content, &parts); err != nil {
		t.Fatalf("带图时 content 必须是 part 数组: %s", out.Messages[0].Content)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "这张图里是什么" {
		t.Errorf("文本 part 不对: %+v", parts)
	}
	wantURI := "data:image/png;base64," + tinyPNG
	if len(parts) < 2 || parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != wantURI {
		t.Errorf("图片 part 不对: %+v", parts)
	}
	if !strings.Contains(string(body), tinyPNG) {
		t.Errorf("请求体里没有 tiny.png 的字节: %s", body)
	}
}

// 反过来钉住 default 分支的边界：tool_use 与 tool_result 不是在 joinBlocks 里丢的，
// 它们由调用方各自编成 tool_calls 与 role=tool 消息。把它们一并算进 vendor_content，
// 每个工具轮都会报一条「未知内容块」，这张表就再也没人看了。
func TestEncodeRequestDoesNotReportToolBlocksAsUnknown(t *testing.T) {
	req := decodeSample(t, "in-anthropic-tool-turn2")
	_, dropped, out := encode(t, req, false)

	if contains(dropped, openaicc.DropVendorContent) {
		t.Errorf("工具块被当成了未知内容块: %v", dropped)
	}
	var calls, results int
	for _, m := range out.Messages {
		calls += len(m.ToolCalls)
		if m.Role == "tool" {
			results++
		}
	}
	if calls == 0 || results == 0 {
		t.Fatalf("样本里没有工具轮，这条用例钉不住东西（tool_calls=%d role=tool=%d）", calls, results)
	}
}

// 纯文本消息的 content 仍是字符串；带图才发 part 数组。
func TestEncodeRequestTextOnlyStaysString(t *testing.T) {
	body, _, out := encode(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{{
			Role:    protocol.RoleUser,
			Content: []protocol.Block{{Kind: protocol.BlockText, Text: "你好"}},
		}},
	}, false)
	if len(out.Messages) != 1 {
		t.Fatalf("消息数 = %d", len(out.Messages))
	}
	var s string
	if err := json.Unmarshal(out.Messages[0].Content, &s); err != nil {
		t.Fatalf("纯文本 content 应是字符串: %s", out.Messages[0].Content)
	}
	if s != "你好" {
		t.Errorf("content = %q", s)
	}
	if strings.Contains(string(body), `"type":"text"`) {
		t.Errorf("纯文本不该发 part 数组: %s", body)
	}
}

func TestEncodeRequestImageURLPart(t *testing.T) {
	_, dropped, out := encode(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{{
			Role: protocol.RoleUser,
			Content: []protocol.Block{{
				Kind:  protocol.BlockImage,
				Image: &protocol.Image{URL: "https://example.com/a.png"},
			}},
		}},
	}, false)
	if contains(dropped, openaicc.DropVendorContent) {
		t.Errorf("URL 图不该丢: %v", dropped)
	}
	var parts []map[string]any
	if err := json.Unmarshal(out.Messages[0].Content, &parts); err != nil {
		t.Fatalf("带图 content 应是数组: %s", out.Messages[0].Content)
	}
	if len(parts) != 1 || parts[0]["type"] != "image_url" {
		t.Fatalf("parts = %v", parts)
	}
	obj, _ := parts[0]["image_url"].(map[string]any)
	if obj["url"] != "https://example.com/a.png" {
		t.Errorf("url = %v", obj["url"])
	}
}

// detail 编回 image_url 对象**里面**（口径层 v0.78）。空 detail 不许凭空造出这一格：
// 「客户端没指定」与「客户端指定了 auto」在 CC 那边不是一回事。
func TestEncodeRequestImageDetail(t *testing.T) {
	imagePart := func(t *testing.T, img *protocol.Image) map[string]any {
		t.Helper()
		_, _, out := encode(t, &protocol.Request{
			Model: "m",
			Messages: []protocol.Message{{
				Role:    protocol.RoleUser,
				Content: []protocol.Block{{Kind: protocol.BlockImage, Image: img}},
			}},
		}, false)
		var parts []map[string]any
		if err := json.Unmarshal(out.Messages[0].Content, &parts); err != nil {
			t.Fatalf("带图 content 应是数组: %s", out.Messages[0].Content)
		}
		if len(parts) != 1 {
			t.Fatalf("parts = %v", parts)
		}
		obj, _ := parts[0]["image_url"].(map[string]any)
		return obj
	}

	t.Run("data 图带 detail", func(t *testing.T) {
		obj := imagePart(t, &protocol.Image{MediaType: "image/png", Data: tinyPNG, Detail: "high"})
		if obj["detail"] != "high" {
			t.Errorf("image_url.detail = %v，期望 high", obj["detail"])
		}
	})
	t.Run("url 图带 detail", func(t *testing.T) {
		obj := imagePart(t, &protocol.Image{URL: "https://example.com/a.png", Detail: "low"})
		if obj["detail"] != "low" {
			t.Errorf("image_url.detail = %v，期望 low", obj["detail"])
		}
	})
	t.Run("auto 照发", func(t *testing.T) {
		obj := imagePart(t, &protocol.Image{URL: "https://example.com/a.png", Detail: "auto"})
		if obj["detail"] != "auto" {
			t.Errorf("auto 不是「等于没指定」，应原样发出，实得 %v", obj["detail"])
		}
	})
	t.Run("空 detail 不发这一格", func(t *testing.T) {
		obj := imagePart(t, &protocol.Image{URL: "https://example.com/a.png"})
		if _, ok := obj["detail"]; ok {
			t.Errorf("客户端没指定就不该有 detail 键: %v", obj)
		}
	})
}

func TestEncodeRequestLiftsToolResultImages(t *testing.T) {
	_, dropped, out := encode(t, &protocol.Request{
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
						{Kind: protocol.BlockText, Text: "结果文本"},
						{Kind: protocol.BlockImage, Image: &protocol.Image{MediaType: "image/png", Data: tinyPNG}},
					},
				}},
			}},
		},
	}, false)
	if contains(dropped, openaicc.DropVendorContent) {
		t.Errorf("tool_result 里的图应抬走，不该 vendor_content: %v", dropped)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("消息数 = %d，期望 assistant / tool / user，实得 %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[1].Role != "tool" {
		t.Errorf("第二条 role = %q，期望 tool", out.Messages[1].Role)
	}
	var toolText string
	if err := json.Unmarshal(out.Messages[1].Content, &toolText); err != nil || toolText != "结果文本" {
		t.Errorf("role=tool content 应是文本字符串，实得 %s", out.Messages[1].Content)
	}
	if out.Messages[2].Role != "user" {
		t.Errorf("抬出的图应跟一条 user，实得 %q", out.Messages[2].Role)
	}
	var parts []map[string]any
	if err := json.Unmarshal(out.Messages[2].Content, &parts); err != nil {
		t.Fatalf("抬出的 user content 应是数组: %s", out.Messages[2].Content)
	}
	if len(parts) != 1 || parts[0]["type"] != "image_url" {
		t.Fatalf("抬出的 part = %v", parts)
	}
	obj, _ := parts[0]["image_url"].(map[string]any)
	if obj["url"] != "data:image/png;base64,"+tinyPNG {
		t.Errorf("抬出的 data URI 不对: %v", obj["url"])
	}
}

func TestEncodeRequestLiftsToolResultImagesIntoOneUserMessage(t *testing.T) {
	_, _, out := encode(t, &protocol.Request{
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
	if len(out.Messages) != 3 {
		t.Fatalf("消息数 = %d，期望 assistant / tool / 一条 user，实得 %+v", len(out.Messages), out.Messages)
	}
	var parts []map[string]any
	if err := json.Unmarshal(out.Messages[2].Content, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("应是一条 user 里两张图，实得 %v", parts)
	}
}

func TestEncodeRequestEmptyMediaTypeDefaultsPNG(t *testing.T) {
	_, _, out := encode(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{{
			Role: protocol.RoleUser,
			Content: []protocol.Block{{
				Kind:  protocol.BlockImage,
				Image: &protocol.Image{Data: tinyPNG},
			}},
		}},
	}, false)
	var parts []map[string]any
	if err := json.Unmarshal(out.Messages[0].Content, &parts); err != nil {
		t.Fatal(err)
	}
	obj, _ := parts[0]["image_url"].(map[string]any)
	url, _ := obj["url"].(string)
	if url != "data:image/png;base64,"+tinyPNG {
		t.Errorf("空 media_type 应兜底 image/png，实得 %q", url)
	}
}

// decodeAnthropic 把一段手写的 Anthropic 请求体解成 canonical。
//
// 与 decodeSample 分工：真实样本里没有图片轮（现有 harness 转录都是纯文本与工具），
// 而这条链路的入口形态必须是真的 Anthropic JSON——手搭 canonical 的话，image 块长
// 成什么样就成了我说了算，decode 侧哪天改了 Kind 的取值这里也照样绿。
func decodeAnthropic(t *testing.T, body string) *protocol.Request {
	t.Helper()
	req, err := anthropic.NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return req
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// 一条 user 消息里挂着**多个** tool_result（Anthropic 的并行工具轮就是这个形状，
// in-anthropic-parallel-* 实采样本可证），其中头一个带图。
//
// 抬出来的 user 消息必须落在**所有** tool 消息之后。夹在两条 tool 中间是硬错：CC 那边
// role=tool 必须紧跟着带 tool_calls 的 assistant，中间插一条 user 会被上游拒。
func TestEncodeRequestLiftsImagesAfterAllToolMessages(t *testing.T) {
	_, _, out := encode(t, &protocol.Request{
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

	var roles []string
	for _, m := range out.Messages {
		roles = append(roles, m.Role)
	}
	want := []string{"assistant", "tool", "tool", "user"}
	if len(roles) != len(want) {
		t.Fatalf("角色序列 = %v，期望 %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("角色序列 = %v，期望 %v——抬出来的图夹进了两条 tool 中间", roles, want)
		}
	}

	// 抬图与普通图走同一个 part 编码器，detail 本该顺带就过去了——但「本该」不是断言。
	var parts []map[string]any
	if err := json.Unmarshal(out.Messages[3].Content, &parts); err != nil {
		t.Fatalf("抬出来的 user content 应是数组: %s", out.Messages[3].Content)
	}
	if len(parts) != 1 {
		t.Fatalf("抬出来的 parts = %v", parts)
	}
	obj, _ := parts[0]["image_url"].(map[string]any)
	if obj["detail"] != "high" {
		t.Errorf("抬出来的图丢了 detail: %v", obj)
	}
}
