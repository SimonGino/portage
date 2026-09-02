package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// Responses namespace 摊平（口径层 v1.14 ①，#94）在两个出口的验收线：ADE 实采 55 个
// 工具（10 顶层 function + 8 个 namespace 里 45 个子项）经 R→CC 与 R→A 都要编出 55 个
// function 工具——摊平发生在解码侧，两个编码器拿到的是同一份名字，不加任何路径分支。
// 修前它们只编出 10 个，日志一句 dropped=[server_tool] 把 45 个吞了。
func TestNamespaceToolsSurviveConversion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(goldenDir, "in-responses-namespace-turn1", "request.json"))
	if err != nil {
		t.Skipf("样本尚未采集：in-responses-namespace-turn1（%v）", err)
	}
	req, err := openairesponses.NewCodec().DecodeRequest(raw, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("R→CC", func(t *testing.T) {
		body, dropped, err := openaicc.NewCodec().EncodeRequestReport(req, true)
		if err != nil {
			t.Fatal(err)
		}
		var sent struct {
			Tools []struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		if len(sent.Tools) != 55 {
			t.Fatalf("CC 出口编出 %d 个工具, 期望 55", len(sent.Tools))
		}
		var names []string
		for _, tool := range sent.Tools {
			if tool.Type != "function" {
				t.Errorf("工具 %q 的 type = %q", tool.Function.Name, tool.Type)
			}
			names = append(names, tool.Function.Name)
		}
		for _, want := range []string{"request_user_input", "multi_agent_v1__spawn_agent", "mcp__ade_asset_knowledge__readKnowledgeIndexFile"} {
			if !slices.Contains(names, want) {
				t.Errorf("CC 出口少了 %q", want)
			}
		}
		// 工具里唯一丢的是 web_search：那是服务端工具，与 namespace 无关，登记照旧
		// （名单里另有 ADE 请求级厂商字段的 vendor_request，不归本闸）。
		if !dropped.Has(openaicc.DropServerTool) {
			t.Errorf("dropped = %v, 期望含 %s", dropped, openaicc.DropServerTool)
		}
	})

	t.Run("R→A", func(t *testing.T) {
		body, dropped, err := anthropic.NewCodec().EncodeRequestReport(req, true)
		if err != nil {
			t.Fatal(err)
		}
		var sent struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		if len(sent.Tools) != 55 {
			t.Fatalf("Anthropic 出口编出 %d 个工具, 期望 55", len(sent.Tools))
		}
		var names []string
		for _, tool := range sent.Tools {
			names = append(names, tool.Name)
		}
		if !slices.Contains(names, "mcp__ade_asset_knowledge__readKnowledgeIndexFile") {
			t.Errorf("Anthropic 出口少了摊平名: %v", names)
		}
		if !dropped.Has(anthropic.DropServerTool) {
			t.Errorf("dropped = %v, 期望含 %s", dropped, anthropic.DropServerTool)
		}
	})
}
