package openaicc_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/protocol/anthropic"
	"github.com/SimonGino/ai-gateway/internal/protocol/openaicc"
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
// 链路真正要扛的是 Claude Code 实际发出来的形态（#11 的验收项之一）。
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
// A 入口走不到这条分支（Anthropic 的 input 恒是对象），但 #12 的 Codex custom 工具
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

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
