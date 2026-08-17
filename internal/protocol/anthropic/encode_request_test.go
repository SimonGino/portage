package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件测 Anthropic 作**出口**的请求编码（portage-legacy#25，R→A 用）。
//
// 输入是 canonical，所以这里的用例形态大多来自 openairesponses.DecodeRequest 解出来
// 的样子——Responses 是目前唯一开通的 R→A 入口。真正的入站样本背书在那一侧
// （in-responses-*），这边钉的是 Anthropic 协议自己的硬约束：max_tokens 必填、
// messages 只收两个角色、相邻同角色要合并、input_schema 必填。

func encodeReq(t *testing.T, req *protocol.Request, opts ...Options) map[string]any {
	t.Helper()
	body, err := NewCodec(opts...).EncodeRequest(req, false)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("编出来的不是 JSON: %v\n%s", err, body)
	}
	return out
}

func userMsg(text string) protocol.Message {
	return protocol.Message{
		Role:    protocol.RoleUser,
		Content: []protocol.Block{{Kind: protocol.BlockText, Text: text}},
	}
}

// max_tokens 是 Anthropic 的必填字段，缺了就是 400。canonical 那边它可以是零值
// （Responses 的 max_output_tokens、CC 的 max_tokens 都允许缺省）。
func TestEncodeRequestAlwaysFillsMaxTokens(t *testing.T) {
	cases := map[string]struct {
		reqMax, optMax, want int
	}{
		"客户端给了就用客户端的": {reqMax: 1024, optMax: 8192, want: 1024},
		"客户端没给用配置默认":  {reqMax: 0, optMax: 8192, want: 8192},
		"配置也没给用兜底":    {reqMax: 0, optMax: 0, want: fallbackMaxTokens},
		"负数当没给":       {reqMax: -1, optMax: 8192, want: 8192},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out := encodeReq(t, &protocol.Request{
				Model: "m", MaxTokens: c.reqMax,
				Messages: []protocol.Message{userMsg("hi")},
			}, Options{DefaultMaxTokens: c.optMax})
			if got, ok := out["max_tokens"].(float64); !ok || int(got) != c.want {
				t.Errorf("max_tokens = %v, 期望 %d", out["max_tokens"], c.want)
			}
		})
	}
}

// RoleSystem 消息必须上提到顶层 system。Anthropic 的 messages 只收 user/assistant，
// 留在里面会被拒——而 canonical 里确实会有：Responses 的 developer 消息就归一成了它。
func TestEncodeRequestHoistsSystemMessages(t *testing.T) {
	out := encodeReq(t, &protocol.Request{
		Model:  "m",
		System: []protocol.Block{{Kind: protocol.BlockText, Text: "你是助手"}},
		Messages: []protocol.Message{
			{Role: protocol.RoleSystem, Content: []protocol.Block{
				{Kind: protocol.BlockText, Text: "be brief"}}},
			userMsg("读一下 a.txt"),
		},
	}, Options{DefaultMaxTokens: 8192})

	system, ok := out["system"].([]any)
	if !ok || len(system) != 2 {
		t.Fatalf("system = %v, 期望两块（Request.System + 上提的 developer 消息）", out["system"])
	}
	if system[1].(map[string]any)["text"] != "be brief" {
		t.Errorf("上提的顺序不对: %v", system)
	}

	msgs := out["messages"].([]any)
	for _, m := range msgs {
		if role := m.(map[string]any)["role"]; role != "user" && role != "assistant" {
			t.Errorf("messages 里出现了 %q，Anthropic 只收 user/assistant", role)
		}
	}
	if len(msgs) != 1 {
		t.Errorf("messages 有 %d 条, 期望 1（system 那条已上提）", len(msgs))
	}
}

// Anthropic 要求 user/assistant 交替（§5 坑清单）。canonical 允许连发同角色。
func TestEncodeRequestMergesAdjacentSameRoleMessages(t *testing.T) {
	out := encodeReq(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			userMsg("第一句"), userMsg("第二句"),
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				{Kind: protocol.BlockText, Text: "好"}}},
			userMsg("第三句"),
		},
	}, Options{DefaultMaxTokens: 8192})

	msgs := out["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages 有 %d 条, 期望 3（前两条 user 合并）", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if content := first["content"].([]any); len(content) != 2 {
		t.Errorf("合并后的第一条有 %d 块, 期望 2", len(content))
	}
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	for i := 1; i < len(roles); i++ {
		if roles[i] == roles[i-1] {
			t.Errorf("角色没交替: %v", roles)
		}
	}
}

// 丢掉 thinking 之后就空了的消息要整条剔除：Anthropic 不收 content 为空数组的消息。
func TestEncodeRequestDropsMessagesLeftEmpty(t *testing.T) {
	out := encodeReq(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			userMsg("问题"),
			// Responses 的 reasoning item 解出来就长这样：空 Text 的 thinking 块。
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				{Kind: protocol.BlockThinking, Extras: map[string]any{
					"encrypted_content": "gAAAAAB-opaque"}}}},
			userMsg("追问"),
		},
	}, Options{DefaultMaxTokens: 8192})

	msgs := out["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages 有 %d 条, 期望 1（空的 assistant 剔除后两条 user 合并）", len(msgs))
	}
	body, _ := json.Marshal(out)
	if string(body) == "" || strings.Contains(string(body), "encrypted_content") {
		t.Errorf("密文漏进了 Anthropic 请求: %s", body)
	}
}

// custom 工具：Anthropic 没有这个形态，只能编成普通工具 + 合成的 input_schema，
// 入参对称包装。不合成 schema，input_schema 就是空的——而它是必填。
func TestEncodeRequestSynthesizesSchemaForCustomTool(t *testing.T) {
	body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model: "m",
			Tools: []protocol.Tool{{
				Kind: protocol.ToolCustom, Name: "exec", Description: "跑一段 JS",
				Extras: map[string]any{"format": map[string]any{"syntax": "lark"}},
			}},
			Messages: []protocol.Message{userMsg("hi")},
		}, false)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("tools = %v", out.Tools)
	}
	if out.Tools[0].InputSchema.Type != "object" {
		t.Errorf("input_schema 是必填，却编成了 %+v", out.Tools[0].InputSchema)
	}
	if _, ok := out.Tools[0].InputSchema.Properties[protocol.CustomToolArgsKey]; !ok {
		t.Errorf("合成的 schema 没声明 %q: %s", protocol.CustomToolArgsKey, body)
	}
	// 文法约束带不过去（Anthropic 没有这个能力），要登记在案。
	if !hasDrop(dropped, DropToolGrammar) {
		t.Errorf("丢了文法约束却没登记: %v", dropped)
	}
	// format 本身不许漏进请求体：Anthropic 不认这个键。
	if strings.Contains(string(body), "lark") {
		t.Errorf("Responses 的 format 漏进了 Anthropic 请求: %s", body)
	}
}

// function 工具没带 schema 时也得给一个：input_schema 必填。
func TestEncodeRequestFillsMissingInputSchema(t *testing.T) {
	out := encodeReq(t, &protocol.Request{
		Model:    "m",
		Tools:    []protocol.Tool{{Kind: protocol.ToolFunction, Name: "wait"}},
		Messages: []protocol.Message{userMsg("hi")},
	}, Options{DefaultMaxTokens: 8192})
	tools := out["tools"].([]any)
	schema, ok := tools[0].(map[string]any)["input_schema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Errorf("没 schema 的工具编成了 %v", tools[0])
	}
}

// 非 JSON 入参（Codex 的 exec 收 JS 源码）必须包成 JSON 对象：Anthropic 的
// tool_use.input 只收对象。不包，请求直接被拒。
func TestEncodeRequestWrapsNonJSONToolArgs(t *testing.T) {
	js := `const r = await tools.exec_command({cmd:"cat a.txt"}); text(r.output)`
	out := encodeReq(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{
				Kind: protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{
					ID: "call_1", Name: "exec", Args: js, ArgsIsJSON: false,
				},
			}}},
		},
	}, Options{DefaultMaxTokens: 8192})

	block := out["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	input, ok := block["input"].(map[string]any)
	if !ok {
		t.Fatalf("tool_use.input 不是对象: %v", block["input"])
	}
	if input[protocol.CustomToolArgsKey] != js {
		t.Errorf("包装里的原文不对: %v", input)
	}
}

// 已经是 JSON 的入参原样塞进去，不再包一层——包了上游就看到一个套娃对象。
func TestEncodeRequestKeepsJSONToolArgsAsIs(t *testing.T) {
	out := encodeReq(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{
				Kind: protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{
					ID: "call_1", Name: "Read",
					Args: `{"file_path":"/tmp/a.txt"}`, ArgsIsJSON: true,
				},
			}}},
		},
	}, Options{DefaultMaxTokens: 8192})

	block := out["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	input := block["input"].(map[string]any)
	if input["file_path"] != "/tmp/a.txt" {
		t.Errorf("JSON 入参被动过: %v", input)
	}
	if _, wrapped := input[protocol.CustomToolArgsKey]; wrapped {
		t.Errorf("JSON 入参被多包了一层: %v", input)
	}
}

// 孤儿 tool_result（引用一个本次请求里不存在的 tool_use）会被 Anthropic 直接拒。
// 它确实会出现：入口侧历史被截断，或上一轮的调用没能编过来。
func TestEncodeRequestDropsOrphanToolResult(t *testing.T) {
	body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model: "m",
			Messages: []protocol.Message{
				{Role: protocol.RoleUser, Content: []protocol.Block{
					{Kind: protocol.BlockText, Text: "继续"},
					{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
						ToolCallID: "call_没见过",
						Content:    []protocol.Block{{Kind: protocol.BlockText, Text: "结果"}},
					}},
				}},
			},
		}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "call_没见过") {
		t.Errorf("孤儿 tool_result 被发出去了: %s", body)
	}
	if !hasDrop(dropped, DropOrphanResult) {
		t.Errorf("丢了孤儿结果却没登记: %v", dropped)
	}
	// 同一条消息里的正文不能跟着一起没了。
	if !strings.Contains(string(body), "继续") {
		t.Errorf("正文被误伤: %s", body)
	}
}

// 配得上的 tool_result 要留下，且挂在 user 侧（canonical 沿用 Anthropic 的摆法）。
func TestEncodeRequestKeepsMatchedToolResult(t *testing.T) {
	out := encodeReq(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{
				Kind:     protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{ID: "call_1", Name: "exec", Args: "{}", ArgsIsJSON: true},
			}}},
			{Role: protocol.RoleUser, Content: []protocol.Block{{
				Kind: protocol.BlockToolResult,
				ToolResult: &protocol.ToolResult{
					ToolCallID: "call_1",
					Content: []protocol.Block{
						{Kind: protocol.BlockText, Text: "Script completed"},
						{Kind: protocol.BlockText, Text: "alpha-one"},
					},
				},
			}}},
		},
	}, Options{DefaultMaxTokens: 8192})

	msgs := out["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Errorf("tool_result 挂在了 %q 上", last["role"])
	}
	block := last["content"].([]any)[0].(map[string]any)
	if block["type"] != "tool_result" || block["tool_use_id"] != "call_1" {
		t.Errorf("tool_result 编错了: %v", block)
	}
	// 多段结果拼成一段纯文本，中间用换行分隔——不加分隔会把两段粘成一个词。
	if block["content"] != "Script completed\nalpha-one" {
		t.Errorf("结果内容 = %q", block["content"])
	}
}

// 服务端工具（上游侧能力）直接丢并登记：目标上游既不认这个 type 也变不出这个能力。
func TestEncodeRequestDropsServerTools(t *testing.T) {
	body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model: "m",
			Tools: []protocol.Tool{
				{Kind: protocol.ToolServer, Name: "web_search"},
				{Kind: protocol.ToolFunction, Name: "wait", Schema: json.RawMessage(`{"type":"object"}`)},
			},
			Messages: []protocol.Message{userMsg("hi")},
		}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "web_search") {
		t.Errorf("服务端工具被发出去了: %s", body)
	}
	if !hasDrop(dropped, DropServerTool) {
		t.Errorf("没登记服务端工具的丢弃: %v", dropped)
	}
}

// 入口协议独有的顶层字段一个都不许带过去，且要登记。
func TestEncodeRequestKeepsVendorExtrasOut(t *testing.T) {
	body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model:    "m",
			Messages: []protocol.Message{userMsg("hi")},
			Extras: map[string]any{
				"reasoning":        map[string]any{"effort": "high"},
				"store":            false,
				"prompt_cache_key": "cache-key-1",
			},
		}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"reasoning", "store", "prompt_cache_key"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("Responses 侧字段 %q 漏进了 Anthropic 请求: %s", forbidden, body)
		}
	}
	if !hasDrop(dropped, DropVendorRequest) {
		t.Errorf("没登记顶层字段的丢弃: %v", dropped)
	}
}

// tool_choice 的两种会被拒的组合要挡掉（§5 坑清单，与 CC 出口同一条理由）。
func TestEncodeRequestToolChoice(t *testing.T) {
	tools := []protocol.Tool{{
		Kind: protocol.ToolFunction, Name: "wait", Schema: json.RawMessage(`{"type":"object"}`),
	}}
	cases := map[string]struct {
		choice protocol.ToolChoice
		tools  []protocol.Tool
		want   any
	}{
		"auto":            {protocol.ToolChoice{Mode: "auto"}, tools, map[string]any{"type": "auto"}},
		"required 映成 any": {protocol.ToolChoice{Mode: "required"}, tools, map[string]any{"type": "any"}},
		"指名已声明的工具": {
			protocol.ToolChoice{Mode: "tool", Name: "wait"}, tools,
			map[string]any{"type": "tool", "name": "wait"},
		},
		"指名没声明的工具就不发":  {protocol.ToolChoice{Mode: "tool", Name: "不存在"}, tools, nil},
		"没有 tools 就不发": {protocol.ToolChoice{Mode: "auto"}, nil, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out := encodeReq(t, &protocol.Request{
				Model: "m", Tools: c.tools, ToolChoice: c.choice,
				Messages: []protocol.Message{userMsg("hi")},
			}, Options{DefaultMaxTokens: 8192})

			got := out["tool_choice"]
			if c.want == nil {
				if got != nil {
					t.Errorf("tool_choice = %v, 期望不发", got)
				}
				return
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(c.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("tool_choice = %s, 期望 %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestEncodeRequestStreamFlag(t *testing.T) {
	body, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequest(
		&protocol.Request{Model: "m", Messages: []protocol.Message{userMsg("hi")}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"stream":true`) {
		t.Errorf("流式标记没带上: %s", body)
	}
}

func TestEncodeRequestRejectsNil(t *testing.T) {
	if _, err := NewCodec().EncodeRequest(nil, false); err == nil {
		t.Error("nil 请求应当报错")
	}
}

func hasDrop(dropped []string, want string) bool {
	for _, d := range dropped {
		if d == want {
			return true
		}
	}
	return false
}
