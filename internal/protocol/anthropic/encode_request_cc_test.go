package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件补的是 Anthropic 出口在 **CC 入口**接上来之后才走得到的两条分支（portage-legacy#9）：
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

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR42mP4z8AAAAMBAQD3A0FDAAAAAElFTkSuQmCC"

// 认不得的内容块要**登记**再丢，不能静默蒸发。
//
// 图片已走 BlockImage 转换；这里用 input_audio 钉住 vendor_content 仍登记。
func TestEncodeRequestReportsUnknownContentBlocks(t *testing.T) {
	_, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model: "m", MaxTokens: 16,
			Messages: []protocol.Message{{
				Role: protocol.RoleUser,
				Content: []protocol.Block{
					{Kind: protocol.BlockText, Text: "听听这段"},
					{Kind: "input_audio", Extras: map[string]any{
						"input_audio": map[string]any{"data": "AAAA", "format": "wav"},
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

func TestEncodeRequestImageBlocks(t *testing.T) {
	png := tinyPNG
	cases := map[string]struct {
		img      *protocol.Image
		wantType string
		check    func(t *testing.T, src map[string]any, dropped []string, body string)
	}{
		"base64": {
			img:      &protocol.Image{MediaType: "image/png", Data: png},
			wantType: "base64",
			check: func(t *testing.T, src map[string]any, dropped []string, body string) {
				t.Helper()
				if src["media_type"] != "image/png" || src["data"] != png {
					t.Errorf("source = %v", src)
				}
				if hasDrop(dropped, DropVendorContent) {
					t.Errorf("图片不该登记 vendor_content: %v", dropped)
				}
			},
		},
		"url": {
			img:      &protocol.Image{URL: "https://example.com/a.png"},
			wantType: "url",
			check: func(t *testing.T, src map[string]any, dropped []string, _ string) {
				t.Helper()
				if src["url"] != "https://example.com/a.png" {
					t.Errorf("source = %v", src)
				}
			},
		},
		"空 MediaType 兜底 png": {
			img:      &protocol.Image{Data: png},
			wantType: "base64",
			check: func(t *testing.T, src map[string]any, _ []string, _ string) {
				t.Helper()
				if src["media_type"] != "image/png" {
					t.Errorf("空 media_type 应兜底 image/png，实得 %v", src["media_type"])
				}
			},
		},
		"svg 原样": {
			img:      &protocol.Image{MediaType: "image/svg+xml", Data: png},
			wantType: "base64",
			check: func(t *testing.T, src map[string]any, _ []string, _ string) {
				t.Helper()
				if src["media_type"] != "image/svg+xml" {
					t.Errorf("svg 应原样发出，实得 %v", src["media_type"])
				}
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
				&protocol.Request{
					Model: "m", MaxTokens: 16,
					Messages: []protocol.Message{{
						Role:    protocol.RoleUser,
						Content: []protocol.Block{{Kind: protocol.BlockImage, Image: c.img}},
					}},
				}, false)
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			src := imageSource(t, out)
			if src["type"] != c.wantType {
				t.Errorf("source.type = %v，期望 %s", src["type"], c.wantType)
			}
			c.check(t, src, dropped, string(body))
		})
	}
}

func TestEncodeRequestSkipsEmptyImageData(t *testing.T) {
	body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model: "m", MaxTokens: 16,
			Messages: []protocol.Message{{
				Role: protocol.RoleUser,
				Content: []protocol.Block{
					{Kind: protocol.BlockText, Text: "看图"},
					{Kind: protocol.BlockImage, Image: &protocol.Image{MediaType: "image/png", Data: "   "}},
				},
			}},
		}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"type":"image"`) {
		t.Errorf("空 base64 不该发出 image 块: %s", body)
	}
	if hasDrop(dropped, DropVendorContent) || hasDrop(dropped, DropImageFileID) {
		t.Errorf("空图不是丢弃项: %v", dropped)
	}
}

func TestEncodeRequestDropsImageFileID(t *testing.T) {
	body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
		&protocol.Request{
			Model: "m", MaxTokens: 16,
			Messages: []protocol.Message{{
				Role: protocol.RoleUser,
				Content: []protocol.Block{
					{Kind: protocol.BlockText, Text: "看图"},
					{Kind: protocol.BlockImage, Image: &protocol.Image{FileID: "file_xxx"}},
				},
			}},
		}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDrop(dropped, DropImageFileID) {
		t.Errorf("file_id 应登记 %s: %v", DropImageFileID, dropped)
	}
	if hasDrop(dropped, DropVendorContent) {
		t.Errorf("file_id 不该混进 vendor_content: %v", dropped)
	}
	if strings.Contains(string(body), "file_xxx") || strings.Contains(string(body), `"type":"image"`) {
		t.Errorf("file_id 图不该出现在请求体: %s", body)
	}
}

// Anthropic 没有 detail 这一格，丢了要说得出口，所以单开 image_detail 登记
// （口径层 v0.78 ③，理由同 v0.39 ③ 给 file_id 单开一档）。
//
// 反过来，file_id 那一格**不重复登记**：图整个都没发出去，再报一句「detail 也丢了」
// 是噪声，看日志的人会以为有两张图出了两种问题。
func TestEncodeRequestDropsImageDetail(t *testing.T) {
	cases := map[string]struct {
		img        *protocol.Image
		wantDetail bool
		wantFileID bool
	}{
		"base64 图带 detail": {
			img:        &protocol.Image{MediaType: "image/png", Data: tinyPNG, Detail: "high"},
			wantDetail: true,
		},
		"url 图带 detail": {
			img:        &protocol.Image{URL: "https://example.com/a.png", Detail: "auto"},
			wantDetail: true,
		},
		"没 detail 就不登记": {
			img: &protocol.Image{MediaType: "image/png", Data: tinyPNG},
		},
		"file_id 图只登记 file_id": {
			img:        &protocol.Image{FileID: "file_xxx", Detail: "high"},
			wantFileID: true,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
				&protocol.Request{
					Model: "m", MaxTokens: 16,
					Messages: []protocol.Message{{
						Role:    protocol.RoleUser,
						Content: []protocol.Block{{Kind: protocol.BlockImage, Image: c.img}},
					}},
				}, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := hasDrop(dropped, DropImageDetail); got != c.wantDetail {
				t.Errorf("登记 %s = %v，期望 %v：%v", DropImageDetail, got, c.wantDetail, dropped)
			}
			if got := hasDrop(dropped, DropImageFileID); got != c.wantFileID {
				t.Errorf("登记 %s = %v，期望 %v：%v", DropImageFileID, got, c.wantFileID, dropped)
			}
			if strings.Contains(string(body), "detail") {
				t.Errorf("Anthropic 请求体里不该有 detail: %s", body)
			}
		})
	}
}

func TestEncodeRequestKeepsToolResultImagesInPlace(t *testing.T) {
	out := encodeReq(t, &protocol.Request{
		Model: "m", MaxTokens: 16,
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
	})
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages 数 = %d，图应留在 tool_result 里，不抬成第三条", len(msgs))
	}
	last, _ := msgs[1].(map[string]any)
	blocks, _ := last["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("末条块数 = %d，期望一个 tool_result", len(blocks))
	}
	tr, _ := blocks[0].(map[string]any)
	inner, ok := tr["content"].([]any)
	if !ok {
		t.Fatalf("带图的 tool_result.content 应是数组，实得 %T %v", tr["content"], tr["content"])
	}
	if len(inner) != 2 {
		t.Fatalf("tool_result 块数 = %d，期望 text + image", len(inner))
	}
	if inner[0].(map[string]any)["type"] != "text" || inner[0].(map[string]any)["text"] != "结果文本" {
		t.Errorf("text 块不对: %v", inner[0])
	}
	img, _ := inner[1].(map[string]any)
	if img["type"] != "image" {
		t.Errorf("第二块应是 image: %v", img)
	}
	src, _ := img["source"].(map[string]any)
	if src["type"] != "base64" || src["data"] != tinyPNG {
		t.Errorf("image source 不对: %v", src)
	}
}

func imageSource(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	msgs, _ := out["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatal("没有 messages")
	}
	msg, _ := msgs[0].(map[string]any)
	blocks, _ := msg["content"].([]any)
	for _, raw := range blocks {
		b, _ := raw.(map[string]any)
		if b["type"] == "image" {
			src, _ := b["source"].(map[string]any)
			if src == nil {
				t.Fatal("image 块没有 source")
			}
			return src
		}
	}
	t.Fatalf("没有 image 块: %v", blocks)
	return nil
}

// 回带方向的 thinking 块：丢，但**要登记**（口径层 v0.62 ④）。
//
// 原先这里断言的是「不登记」，理由是这一格恒为空、每请求一条噪声。v0.62 之后不成立了：
// 出口现在会把推理合成给客户端看，客户端下一轮就带着文本原样发回来（还会补一个
// signature:""，Anthropic 见空签名直接 400）。丢的是一段客户端以为送到了的推理，
// 必须留痕。登记成 DropThinking 而不是 DropVendorContent——后者是「认不得的块」，
// 这里是「认得但过不去」，排障时是两件事。
func TestEncodeRequestReportsReplayedThinking(t *testing.T) {
	body, dropped, err := NewCodec(Options{DefaultMaxTokens: 8192}).EncodeRequestReport(
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
	if !hasDrop(dropped, DropThinking) {
		t.Errorf("回带的 thinking 被静默丢了: %v", dropped)
	}
	if hasDrop(dropped, DropVendorContent) {
		t.Errorf("thinking 不该登记成未知块: %v", dropped)
	}
	// 丢就是真丢：正文与 signature 都不许出现在出向字节里。
	for _, leaked := range []string{"让我想想", "thinking", "signature"} {
		if strings.Contains(string(body), leaked) {
			t.Errorf("回带的 %q 漏给了上游:\n%s", leaked, body)
		}
	}
}
