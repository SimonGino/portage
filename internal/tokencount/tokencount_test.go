package tokencount

import (
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

func TestEstimateReturnsAtLeastOne(t *testing.T) {
	n, err := Estimate(&protocol.Request{})
	if err != nil {
		t.Fatalf("估算失败: %v", err)
	}
	if n < 1 {
		t.Errorf("Estimate = %d, 空请求也得 ≥ 1", n)
	}
}

func TestEstimateGrowsWithText(t *testing.T) {
	mk := func(text string) *protocol.Request {
		return &protocol.Request{Messages: []protocol.Message{{
			Role:    protocol.RoleUser,
			Content: []protocol.Block{{Kind: protocol.BlockText, Text: text}},
		}}}
	}
	small, err := Estimate(mk("hi"))
	if err != nil {
		t.Fatalf("估算失败: %v", err)
	}
	big, err := Estimate(mk(strings.Repeat("the quick brown fox jumps over the lazy dog ", 200)))
	if err != nil {
		t.Fatalf("估算失败: %v", err)
	}
	// 数量级判断而非精确值：估算器的承诺就到「随文本长度单调、同一个数量级」为止。
	// 上面那段 1800 词重复文本怎么也得上千 token，两位数的 hi 不该和它一个档。
	if small >= big {
		t.Errorf("小请求 %d ≥ 大请求 %d，token 数没跟着正文走", small, big)
	}
	if big < 1000 {
		t.Errorf("1800 词的请求估出 %d token，低得不像在数正文", big)
	}
}

func TestEstimateCountsToolsAndToolTurns(t *testing.T) {
	bare, err := Estimate(&protocol.Request{Messages: []protocol.Message{{
		Role:    protocol.RoleUser,
		Content: []protocol.Block{{Kind: protocol.BlockText, Text: "hi"}},
	}}})
	if err != nil {
		t.Fatalf("估算失败: %v", err)
	}
	desc := strings.Repeat("Reads a file from the local filesystem. ", 50)
	withTools, err := Estimate(&protocol.Request{
		Messages: []protocol.Message{
			{Role: protocol.RoleUser, Content: []protocol.Block{{Kind: protocol.BlockText, Text: "hi"}}},
			{Role: protocol.RoleAssistant, Content: []protocol.Block{{Kind: protocol.BlockToolUse,
				ToolCall: &protocol.ToolCall{ID: "toolu_1", Name: "read_file", Args: `{"path":"/etc/hosts"}`, ArgsIsJSON: true}}}},
			{Role: protocol.RoleUser, Content: []protocol.Block{{Kind: protocol.BlockToolResult,
				ToolResult: &protocol.ToolResult{ToolCallID: "toolu_1", Content: []protocol.Block{
					{Kind: protocol.BlockText, Text: strings.Repeat("127.0.0.1 localhost\n", 30)},
				}}}}},
		},
		Tools: []protocol.Tool{{Kind: protocol.ToolFunction, Name: "read_file", Description: desc,
			Schema: []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
	})
	if err != nil {
		t.Fatalf("估算失败: %v", err)
	}
	// 工具声明 + 一轮 tool_use/tool_result 加起来好几百 token，全被数进去的话
	// 不可能只比裸 hi 多出零头。漏掉任何一段（尤其 tool_result 的嵌套块）都过不去。
	if withTools < bare+300 {
		t.Errorf("带工具轮的请求 %d, 裸请求 %d：工具声明或工具轮没被数进去", withTools, bare)
	}
}

// 验收第三条：带图片的请求不会因为 base64 被当文本而爆量。
// 3MB 的 base64（约 2.25MB 解码）按文本数是几十万 token，按折算是 ~4400。
func TestEstimateBoundsBase64Attachments(t *testing.T) {
	data := strings.Repeat("iVBORw0KGgoAAAANSUhEUg", 131072) // ~2.9M chars 合法 base64 字符集
	n, err := Estimate(&protocol.Request{Messages: []protocol.Message{{
		Role: protocol.RoleUser,
		Content: []protocol.Block{
			{Kind: protocol.BlockText, Text: "看看这张图"},
			{Kind: protocol.BlockImage, Image: &protocol.Image{MediaType: "image/png", Data: data}},
		},
	}}})
	if err != nil {
		t.Fatalf("估算失败: %v", err)
	}
	// 折算值 = len*3/4/512 ≈ 4400。上界放到 3 倍，防的是「base64 被当正文数」
	// 那种量级错误（那会到几十万）；下界钉住「图片没有被整个吞掉」。
	if n > 15000 {
		t.Errorf("Estimate = %d：base64 疑似被当正文数了（折算应在 ~4400）", n)
	}
	if n < 256 {
		t.Errorf("Estimate = %d：图片折算下限 256 没生效", n)
	}
}
