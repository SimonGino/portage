package anthropic

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件补的是 Anthropic 出口在 **CC 入口**接上来之后才走得到的两条分支（#9）：
// RoleTool 消息的落位，以及 temperature 的值域收窄。R→A 那条路径永远产生不了
// RoleTool（Responses 的工具结果在 user 消息的块里），所以它此前没有被覆盖。

// CC 的工具结果是「每个调用一条独立的 role=tool 消息」，Anthropic 要求同一轮的
// 结果全部挤进**同一条** user 消息。合并不是靠专门的逻辑，而是「非 assistant 一律
// 当 user」加上相邻同角色合并两条既有规则叠出来的——这个用例钉的就是这个叠加结果。
func TestEncodeRequestMergesConsecutiveToolMessages(t *testing.T) {
	toolResult := func(id, text string) protocol.Message {
		return protocol.Message{Role: protocol.RoleTool, Content: []protocol.Block{{
			Kind: protocol.BlockToolResult,
			ToolResult: &protocol.ToolResult{
				ToolCallID: id,
				Content:    []protocol.Block{{Kind: protocol.BlockText, Text: text}},
			},
		}}}
	}
	assistant := protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{
		{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{
			ID: "call_a", Name: "read", Args: `{"filePath":"notes.md"}`, ArgsIsJSON: true}},
		{Kind: protocol.BlockToolUse, ToolCall: &protocol.ToolCall{
			ID: "call_b", Name: "read", Args: `{"filePath":"cache.md"}`, ArgsIsJSON: true}},
	}}

	out := encodeReq(t, &protocol.Request{
		Model: "m", MaxTokens: 16,
		Messages: []protocol.Message{
			userMsg("同时读一下两个文件"),
			assistant,
			toolResult("call_a", "notes 的内容"),
			toolResult("call_b", "cache 的内容"),
		},
	})

	msgs, _ := out["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages 数 = %d，期望 user / assistant / user 三条——两条 tool 消息要并成一条", len(msgs))
	}
	last, _ := msgs[2].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("末条角色 = %v，Anthropic 的工具结果只能挂在 user 上", last["role"])
	}
	blocks, _ := last["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("末条块数 = %d，期望两个 tool_result 并在同一条消息里", len(blocks))
	}
	for i, want := range []string{"call_a", "call_b"} {
		b, _ := blocks[i].(map[string]any)
		if b["type"] != "tool_result" {
			t.Errorf("blocks[%d].type = %v", i, b["type"])
		}
		if b["tool_use_id"] != want {
			t.Errorf("blocks[%d].tool_use_id = %v，期望 %s——id 原样携带，不重编号", i, b["tool_use_id"], want)
		}
	}
}

// temperature：CC 与 Responses 收 0~2，Anthropic 超过 1 直接 400（展开层 §2）。
// 截断而不缩放——理由见 clampTemperature 的注释。
func TestEncodeRequestClampsTemperature(t *testing.T) {
	cases := map[string]struct{ in, want float64 }{
		"区间内原样":      {in: 0.7, want: 0.7},
		"上界原样":       {in: 1, want: 1},
		"OpenAI 的上界": {in: 2, want: 1},
		"超出一点也要收":    {in: 1.2, want: 1},
		"零原样":        {in: 0, want: 0},
		"负数抬到零":      {in: -0.5, want: 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := c.in
			out := encodeReq(t, &protocol.Request{
				Model: "m", MaxTokens: 16, Temperature: &in,
				Messages: []protocol.Message{userMsg("hi")},
			})
			got, ok := out["temperature"].(float64)
			if !ok {
				t.Fatalf("temperature 丢了: %v", out["temperature"])
			}
			if got != c.want {
				t.Errorf("temperature = %v，期望 %v", got, c.want)
			}
		})
	}
}

// 没给 temperature 就不发这个键：补一个默认值等于替客户端做了它没做的决定。
func TestEncodeRequestOmitsAbsentTemperature(t *testing.T) {
	out := encodeReq(t, &protocol.Request{
		Model: "m", MaxTokens: 16,
		Messages: []protocol.Message{userMsg("hi")},
	})
	if _, ok := out["temperature"]; ok {
		t.Errorf("没给就不该发 temperature，实得 %v", out["temperature"])
	}
}

// 认不得的内容块要**登记**再丢，不能静默蒸发。
//
// CC 的解码侧特意把未知 part 原样留住（免得一个带图的请求当场 400），代价是这些块
// 会一路走到这里。不登记的话客户端发了张图、上游收到一个被改成纯文本的请求，还照样
// 200 回来，日志里一个字都没有——比直接拒还糟。
func TestEncodeRequestReportsUnknownContentBlocks(t *testing.T) {
	_, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model: "m", MaxTokens: 16,
			Messages: []protocol.Message{{
				Role: protocol.RoleUser,
				Content: []protocol.Block{
					{Kind: protocol.BlockText, Text: "这张图里是什么"},
					{Kind: "image_url", Extras: map[string]any{
						"image_url": map[string]any{"url": "https://example.com/a.png"},
					}},
				},
			}},
		}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDrop(dropped, DropVendorContent) {
		t.Errorf("没登记未知内容块的丢弃: %v", dropped)
	}
}

// thinking 那一格反过来：它是口径定的必然丢弃，每次都丢，登记等于每请求一条噪声。
// 这条用例钉的是「两者不同」，免得日后有人顺手把 thinking 也塞进 default 分支。
func TestEncodeRequestDoesNotReportThinking(t *testing.T) {
	_, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model: "m", MaxTokens: 16,
			Messages: []protocol.Message{{
				Role: protocol.RoleAssistant,
				Content: []protocol.Block{
					{Kind: protocol.BlockThinking, Text: "让我想想"},
					{Kind: protocol.BlockText, Text: "答案是 42"},
				},
			}},
		}, false)
	if err != nil {
		t.Fatal(err)
	}
	if hasDrop(dropped, DropVendorContent) {
		t.Errorf("thinking 不该登记成未知块: %v", dropped)
	}
}
