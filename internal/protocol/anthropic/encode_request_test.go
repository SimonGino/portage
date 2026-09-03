package anthropic

import (
	"encoding/json"
	"errors"
	"reflect"
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

// tool_choice 的两种会被拒的组合要处置（§5 坑清单，与 CC 出口同一条理由）。
// 处置分两档（口径层 v1.14 ⑧）：auto / none 落空省略但登记 tool_choice 档；
// 点名落空回 400——「指名没声明的工具就不发」原先是静默丢，改判 400。
func TestEncodeRequestToolChoice(t *testing.T) {
	tools := []protocol.Tool{{
		Kind: protocol.ToolFunction, Name: "wait", Schema: json.RawMessage(`{"type":"object"}`),
	}}
	cases := map[string]struct {
		choice  protocol.ToolChoice
		tools   []protocol.Tool
		want    any
		wantErr bool
	}{
		"auto":            {choice: protocol.ToolChoice{Mode: "auto"}, tools: tools, want: map[string]any{"type": "auto"}},
		"required 映成 any": {choice: protocol.ToolChoice{Mode: "required"}, tools: tools, want: map[string]any{"type": "any"}},
		"指名已声明的工具": {
			choice: protocol.ToolChoice{Mode: "tool", Name: "wait"}, tools: tools,
			want: map[string]any{"type": "tool", "name": "wait"},
		},
		"指名没声明的工具回 400":   {choice: protocol.ToolChoice{Mode: "tool", Name: "不存在"}, tools: tools, wantErr: true},
		"没有 tools 就不发但登记": {choice: protocol.ToolChoice{Mode: "auto"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			req := &protocol.Request{
				Model: "m", Tools: c.tools, ToolChoice: c.choice,
				Messages: []protocol.Message{userMsg("hi")},
			}
			if c.wantErr {
				_, _, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(req, false)
				if _, ok := errors.AsType[*protocol.RequestError](err); !ok {
					t.Fatalf("期望 *protocol.RequestError，得到 %v", err)
				}
				return
			}
			body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(req, false)
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}

			got := out["tool_choice"]
			if c.want == nil {
				if got != nil {
					t.Errorf("tool_choice = %v, 期望不发", got)
				}
				if !hasDrop(dropped, DropToolChoice) {
					t.Errorf("tool_choice 落空却没登记: %v", dropped)
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

// 转换失败口径（口径层 v1.14 ⑦，#96）：编码后 messages 一条不剩就回 400，错误是
// *protocol.RequestError（走 400 档，不落 500）。dropped 得跟错误一起回——上层要先
// 出丢弃日志再拒，让人看得出「为什么会空」。
func TestEncodeRequestRejectsEmptyMessages(t *testing.T) {
	_, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(&protocol.Request{
		Model: "m",
		Messages: []protocol.Message{{Role: protocol.RoleAssistant, Content: []protocol.Block{
			{Kind: protocol.BlockThinking, Extras: map[string]any{"encrypted_content": "gAAAAAB-opaque"}},
		}}},
	}, false)
	reqErr, ok := errors.AsType[*protocol.RequestError](err)
	if !ok {
		t.Fatalf("期望 *protocol.RequestError，得到 %v", err)
	}
	if reqErr.Param != protocol.ParamMessages || reqErr.Code != protocol.CodeEmptyArray {
		t.Errorf("code/param = %q/%q，期望 %q/%q", reqErr.Code, reqErr.Param, protocol.CodeEmptyArray, protocol.ParamMessages)
	}
	if !hasDrop(dropped, DropThinking) {
		t.Errorf("拒收时 dropped 也要一并回，好出日志: %v", dropped)
	}
}

// 口径层 v1.14 ⑧：required 落空、点名落空回 400，文案得说清楚是谁被丢了。
func TestEncodeRequestRejectsToolChoiceLeftEmpty(t *testing.T) {
	server := []protocol.Tool{{Kind: protocol.ToolServer, Name: "web_search"}}
	fn := []protocol.Tool{{Kind: protocol.ToolFunction, Name: "wait", Schema: json.RawMessage(`{"type":"object"}`)}}
	cases := map[string]struct {
		choice protocol.ToolChoice
		tools  []protocol.Tool
		wantIn []string // 错误文案里必须出现的字样
	}{
		"required 落空：只剩服务端工具":  {protocol.ToolChoice{Mode: "required"}, server, []string{"web_search"}},
		"required 落空：一个工具都没声明": {protocol.ToolChoice{Mode: "required"}, nil, []string{"没有声明"}},
		"点名的是服务端工具":            {protocol.ToolChoice{Mode: "tool", Name: "web_search"}, server, []string{"web_search", "服务端工具"}},
		"点名没声明的工具":             {protocol.ToolChoice{Mode: "tool", Name: "ghost"}, fn, []string{"ghost", "wait"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(&protocol.Request{
				Model: "m", Tools: c.tools, ToolChoice: c.choice,
				Messages: []protocol.Message{userMsg("hi")},
			}, false)
			reqErr, ok := errors.AsType[*protocol.RequestError](err)
			if !ok {
				t.Fatalf("期望 *protocol.RequestError，得到 %v", err)
			}
			if reqErr.Param != protocol.ParamToolChoice {
				t.Errorf("param = %q，期望 %q", reqErr.Param, protocol.ParamToolChoice)
			}
			for _, want := range c.wantIn {
				if !strings.Contains(reqErr.Message, want) {
					t.Errorf("文案没提 %q: %s", want, reqErr.Message)
				}
			}
		})
	}
}

// auto / none 落空不是错：tool_choice 省略、登记 tool_choice 档，名单记的是 mode。
func TestEncodeRequestRegistersToolChoiceLeftEmpty(t *testing.T) {
	for _, mode := range []string{"auto", "none"} {
		t.Run(mode, func(t *testing.T) {
			body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(&protocol.Request{
				Model: "m", ToolChoice: protocol.ToolChoice{Mode: mode},
				Tools:    []protocol.Tool{{Kind: protocol.ToolServer, Name: "web_search"}},
				Messages: []protocol.Message{userMsg("hi")},
			}, false)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "tool_choice") {
				t.Errorf("落空的 tool_choice 不该发: %s", body)
			}
			if got := dropped.Names(DropToolChoice); len(got) != 1 || got[0] != mode {
				t.Errorf("tool_choice 档名单 = %v，期望 [%s]", got, mode)
			}
		})
	}
}

// 口径层 v1.14 ⑨：工具类丢弃要带名字。丢三个服务端工具，三个名字都得在名单上，
// 档位本身不重复登记。
func TestEncodeRequestDropNamesEveryServerTool(t *testing.T) {
	_, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(&protocol.Request{
		Model: "m",
		Tools: []protocol.Tool{
			{Kind: protocol.ToolServer, Name: "web_search"},
			{Kind: protocol.ToolFunction, Name: "wait", Schema: json.RawMessage(`{"type":"object"}`)},
			{Kind: protocol.ToolServer, Name: "file_search"},
			{Kind: protocol.ToolServer, Name: "code_interpreter"},
		},
		Messages: []protocol.Message{userMsg("hi")},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := dropped.Names(DropServerTool)
	for _, want := range []string{"web_search", "file_search", "code_interpreter"} {
		found := false
		for _, n := range got {
			found = found || n == want
		}
		if !found {
			t.Errorf("server_tool 名单缺 %q: %v", want, got)
		}
	}
	n := 0
	for _, d := range dropped {
		if d.Kind == DropServerTool {
			n++
		}
	}
	if n != 1 {
		t.Errorf("server_tool 档登记了 %d 次，期望合并成 1 条: %v", n, dropped)
	}
}

func hasDrop(dropped protocol.Drops, want string) bool {
	for _, d := range dropped {
		if d.Kind == want {
			return true
		}
	}
	return false
}

// 无名服务端工具（Responses 的 web_search 只有 type，没有 name）在名单里退到 type，
// 与 CC 出口同规——两个出口的丢弃日志形态零差异。
func TestEncodeRequestDropNamesNamelessServerToolByType(t *testing.T) {
	_, dropped, err := NewCodec().EncodeRequestReport(&protocol.Request{
		Model: "m",
		Tools: []protocol.Tool{
			{Kind: protocol.ToolFunction, Name: "Read", Schema: json.RawMessage(`{"type":"object"}`)},
			{Kind: protocol.ToolServer, Extras: map[string]any{"type": "web_search", "external_web_access": true}},
		},
		Messages: []protocol.Message{userMsg("hi")},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := dropped.Names(DropServerTool)
	found := false
	for _, n := range got {
		found = found || n == "web_search"
	}
	if !found {
		t.Errorf("无名服务端工具应按 type 记进名单，得到 %v", got)
	}
}

// ——— 缺失结果补占位（与 CC 出口同规，占位文案逐字相同）———
//
// 不变量：assistant 发出的每个 tool_use，紧接着的 user 消息里必须有配对的
// tool_result，否则 Anthropic 400。客户端历史缺结果（取消的轮次、丢掉的 output）时
// 上游会拒、客户端又改不了自己的历史，只能由出口侧补一条明说结果缺失的占位。

// encodeReport 与 encodeReq 同源，另外交出丢弃清单（要看名单的用例用它）。
func encodeReport(t *testing.T, req *protocol.Request) (map[string]any, protocol.Drops) {
	t.Helper()
	body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(req, false)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("编出来的不是 JSON: %v\n%s", err, body)
	}
	return out, dropped
}

// msgShape 把编出来的消息压成 `role:块类型[标识]` 的序列，位置断言用。
func msgShape(t *testing.T, out map[string]any) []string {
	t.Helper()
	msgs, _ := out["messages"].([]any)
	shape := make([]string, 0, len(msgs))
	for _, raw := range msgs {
		m := raw.(map[string]any)
		var kinds []string
		for _, b := range m["content"].([]any) {
			block := b.(map[string]any)
			kind, _ := block["type"].(string)
			if id, ok := block["tool_use_id"].(string); ok {
				kind += "[" + id + "]"
			}
			if id, ok := block["id"].(string); ok {
				kind += "[" + id + "]"
			}
			kinds = append(kinds, kind)
		}
		shape = append(shape, m["role"].(string)+":"+strings.Join(kinds, ","))
	}
	return shape
}

// toolResultText 取某条消息里某个 tool_use_id 的结果正文。
func toolResultText(t *testing.T, out map[string]any, at int, id string) string {
	t.Helper()
	msgs := out["messages"].([]any)
	for _, b := range msgs[at].(map[string]any)["content"].([]any) {
		block := b.(map[string]any)
		if block["type"] == "tool_result" && block["tool_use_id"] == id {
			text, _ := block["content"].(string)
			return text
		}
	}
	t.Fatalf("第 %d 条消息里没有 %s 的结果: %v", at, id, msgs[at])
	return ""
}

// 有调用、没结果：占位 tool_result 合成进**紧随其后的那条 user 消息**，且排在该消息
// 其余块之前——Anthropic 要求 tool_result 站在 user 消息最前面，插在正文后面就白补了。
func TestEncodeRequestFillsMissingToolResult(t *testing.T) {
	out, dropped := encodeReport(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			userMsg("跑一下"),
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{
				Kind:     protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{ID: "call_1", Name: "exec", Args: "{}", ArgsIsJSON: true},
			}}},
			userMsg("算了"),
		},
	})
	want := []string{"user:text", "assistant:tool_use[call_1]", "user:tool_result[call_1],text"}
	if got := msgShape(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("消息序列 = %v，期望 %v", got, want)
	}
	if got := toolResultText(t, out, 2, "call_1"); got != protocol.MissingToolResultPlaceholder {
		t.Errorf("占位文案 = %q", got)
	}
	if got := dropped.Names(DropMissingResult); !reflect.DeepEqual(got, []string{"call_1"}) {
		t.Errorf("missing_result 名单 = %v，期望 [call_1]", got)
	}
}

// 并行轮里只缺一个：只补那一个，真回来的结果原样排在前面。
func TestEncodeRequestFillsOnlyTheMissingCallInParallelTurn(t *testing.T) {
	out, dropped := encodeReport(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{ID: "call_a", Name: "f", Args: "{}", ArgsIsJSON: true}},
				{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{ID: "call_b", Name: "g", Args: "{}", ArgsIsJSON: true}},
			}},
			{Role: protocol.RoleUser, Content: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
					ToolCallID: "call_a",
					Content:    []protocol.Block{{Kind: protocol.BlockText, Text: "A 的结果"}},
				}},
				{Kind: protocol.BlockText, Text: "接着说"},
			}},
		},
	})
	want := []string{
		"assistant:tool_use[call_a],tool_use[call_b]",
		"user:tool_result[call_a],tool_result[call_b],text",
	}
	if got := msgShape(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("消息序列 = %v，期望 %v——占位没排在正文之前，或者补多了", got, want)
	}
	if got := toolResultText(t, out, 1, "call_a"); got != "A 的结果" {
		t.Errorf("真结果被改写了: %q", got)
	}
	if got := dropped.Names(DropMissingResult); !reflect.DeepEqual(got, []string{"call_b"}) {
		t.Errorf("missing_result 名单 = %v，期望 [call_b]", got)
	}
}

// 紧随其后不是 user：单插一条只含占位结果的 user 消息。这会打断相邻 assistant 的
// 合并，但那正是不变量要的——合并会让两轮调用挤在一起、中间一个结果都没有。
func TestEncodeRequestInsertsUserMessageForMissingToolResult(t *testing.T) {
	out, _ := encodeReport(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{
				Kind:     protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{ID: "call_1", Name: "exec", Args: "{}", ArgsIsJSON: true},
			}}},
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{Kind: protocol.BlockText, Text: "那我直说"}}},
		},
	})
	want := []string{"assistant:tool_use[call_1]", "user:tool_result[call_1]", "assistant:text"}
	if got := msgShape(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("消息序列 = %v，期望 %v", got, want)
	}
}

// 带调用的 assistant 就是最后一条：补一条 user 消息收尾。
func TestEncodeRequestFillsMissingToolResultAtTail(t *testing.T) {
	out, _ := encodeReport(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			userMsg("跑"),
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{
				Kind:     protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{ID: "call_1", Name: "exec", Args: "{}", ArgsIsJSON: true},
			}}},
		},
	})
	want := []string{"user:text", "assistant:tool_use[call_1]", "user:tool_result[call_1]"}
	if got := msgShape(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("消息序列 = %v，期望 %v", got, want)
	}
}

// 孤儿与缺失同时出现：先丢孤儿、再补缺失。两者互不制造对方要处理的情况——孤儿引用的
// 调用不存在，进不了「有没有结果」的分母；补出来的占位挂的是真实存在的 tool_use，
// 绝不会变成下一轮的孤儿。
func TestEncodeRequestDropsOrphanThenFillsMissing(t *testing.T) {
	out, dropped := encodeReport(t, &protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{
				Kind:     protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{ID: "call_1", Name: "exec", Args: "{}", ArgsIsJSON: true},
			}}},
			{Role: protocol.RoleUser, Content: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolResult: &protocol.ToolResult{
					ToolCallID: "call_没见过",
					Content:    []protocol.Block{{Kind: protocol.BlockText, Text: "孤儿"}},
				}},
				{Kind: protocol.BlockText, Text: "继续"},
			}},
		},
	})
	want := []string{"assistant:tool_use[call_1]", "user:tool_result[call_1],text"}
	if got := msgShape(t, out); !reflect.DeepEqual(got, want) {
		t.Errorf("消息序列 = %v，期望 %v", got, want)
	}
	if !hasDrop(dropped, DropOrphanResult) {
		t.Errorf("孤儿没登记: %v", dropped)
	}
	if got := dropped.Names(DropMissingResult); !reflect.DeepEqual(got, []string{"call_1"}) {
		t.Errorf("missing_result 名单 = %v，期望 [call_1]", got)
	}
}
