package anthropic_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
)

// goldenDir 是全仓共用的转录库（见 testdata/golden/README.md）。
const goldenDir = "../../../testdata/golden"

// inboundMeta 只取本包用得上的字段。
//
// Verified 是**人工关卡**：入站样本经过脱敏，脱敏动作本身会改字节，没人核过就当
// 输入用，等于拿一份可能已经改坏的语料给实现判卷。M0 给上游样本设这道闸时理由
// 相同（golden_test.go 的 TestGoldenSamples）；portage-legacy#10 采集时标志已经写进
// meta.json，但当时还没有消费方，到这张票才真正接上（PO 裁定留到 portage-legacy#11，jinpenga）。
type inboundMeta struct {
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	Stream    bool   `json:"stream"`
	Endpoint  string `json:"endpoint"`
	Verified  bool   `json:"verified"`
}

// inboundAnthropicSamples 列在这里而不是扫目录：缺哪个就 skip 哪个子测试，而不是
// 目录空着一路绿灯（与 M0 的 m0Samples 同理由）。
var inboundAnthropicSamples = []string{
	"in-anthropic-text",
	"in-anthropic-tool-turn1",
	"in-anthropic-tool-turn2",
	"in-anthropic-parallel-turn1",
	"in-anthropic-parallel-turn2",
}

func loadInbound(t *testing.T, name string) ([]byte, inboundMeta) {
	t.Helper()
	dir := filepath.Join(goldenDir, name)

	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if os.IsNotExist(err) {
		t.Skipf("样本尚未采集：%s（用 cmd/goldenrec 的 inbound 模式录）", dir)
	}
	if err != nil {
		t.Fatal(err)
	}
	var meta inboundMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("meta.json 解析失败: %v", err)
	}
	if !meta.Verified {
		t.Fatalf("%s 的 meta.json 仍是 verified:false——脱敏与内容核对过一遍再置 true", name)
	}
	if meta.Direction != "inbound" {
		t.Fatalf("%s 的 direction = %q，这里只吃入站样本", name, meta.Direction)
	}
	body, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body, meta
}

// TestDecodeRequestIsTotalOverRealSamples 把「DecodeRequest 是全函数」钉成可执行的
// 断言：五份真实发包一份都不许解不动，且解出来的东西不能是空壳。
func TestDecodeRequestIsTotalOverRealSamples(t *testing.T) {
	codec := anthropic.NewCodec()
	for _, name := range inboundAnthropicSamples {
		t.Run(name, func(t *testing.T) {
			body, meta := loadInbound(t, name)

			req, err := codec.DecodeRequest(body, meta.Stream)
			if err != nil {
				t.Fatalf("真实入站样本解不动: %v", err)
			}
			if req.Model == "" {
				t.Error("Model 为空")
			}
			if req.MaxTokens == 0 {
				t.Error("MaxTokens 为零——Claude Code 每个请求都带它")
			}
			if len(req.System) == 0 {
				t.Error("System 为空")
			}
			if len(req.Messages) == 0 {
				t.Error("Messages 为空")
			}
			if len(req.Tools) == 0 {
				t.Error("Tools 为空")
			}
			if req.Stream != meta.Stream {
				t.Errorf("Stream = %v, 期望 %v", req.Stream, meta.Stream)
			}
			// metadata 必须留在 canonical 里：它对 Anthropic 上游有实义（判定是否
			// 官方 Claude Code），丢不丢是 encode 侧按出口协议决定的事。decode 侧
			// 丢了就再也捡不回来。
			if _, ok := req.Extras["metadata"]; !ok {
				t.Error("顶层 metadata 没进 Extras")
			}
		})
	}
}

// system 块上的 cache_control 断点是**位置敏感**的：脱敏口径专门保住了它，因为
// 断点位置本身就是被测行为。拼成一个字符串会把位置抹平。
func TestDecodeRequestKeepsSystemBlockBoundariesAndCacheControl(t *testing.T) {
	codec := anthropic.NewCodec()
	body, meta := loadInbound(t, "in-anthropic-text")

	req, err := codec.DecodeRequest(body, meta.Stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.System) < 2 {
		t.Fatalf("System 块数 = %d，实采是多块", len(req.System))
	}
	var withCache int
	for _, b := range req.System {
		if b.Kind != protocol.BlockText {
			t.Errorf("system 块 Kind = %q, 期望 text", b.Kind)
		}
		if b.Text == "" {
			t.Error("system 块正文为空")
		}
		if _, ok := b.Extras["cache_control"]; ok {
			withCache++
		}
	}
	if withCache == 0 {
		t.Error("没有一个 system 块带 cache_control——断点位置丢了")
	}
}

// mid-conversation-system beta 会把一条 role=system 的消息塞进 messages 中段，
// 且 content 是纯字符串。原草案的 role 集合与 content 形态都装不下它（§4.3）。
func TestDecodeRequestHandlesMidConversationSystem(t *testing.T) {
	codec := anthropic.NewCodec()
	body, meta := loadInbound(t, "in-anthropic-tool-turn2")

	req, err := codec.DecodeRequest(body, meta.Stream)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for i, m := range req.Messages {
		if m.Role != protocol.RoleSystem {
			continue
		}
		found = true
		if i == 0 || i == len(req.Messages)-1 {
			t.Errorf("role=system 出现在 messages[%d]，实采是中段", i)
		}
		if len(m.Content) != 1 || m.Content[0].Kind != protocol.BlockText || m.Content[0].Text == "" {
			t.Errorf("纯字符串 content 没退化成单个 text 块: %+v", m.Content)
		}
	}
	if !found {
		t.Fatal("没找到 role=system 的消息")
	}
}

// 工具轮的第二个请求才是 A→CC 真正要转的东西：assistant 的 tool_use 与 user 里的
// tool_result 必须成对解出来，id 原样携带。
func TestDecodeRequestPairsToolUseAndToolResult(t *testing.T) {
	codec := anthropic.NewCodec()
	body, meta := loadInbound(t, "in-anthropic-parallel-turn2")

	req, err := codec.DecodeRequest(body, meta.Stream)
	if err != nil {
		t.Fatal(err)
	}

	var callIDs, resultIDs []string
	for _, m := range req.Messages {
		for _, b := range m.Content {
			switch b.Kind {
			case protocol.BlockToolUse:
				if b.ToolCall == nil {
					t.Fatal("tool_use 块没有 ToolCall")
				}
				if b.ToolCall.Name == "" {
					t.Error("tool_use 缺 name")
				}
				if !b.ToolCall.ArgsIsJSON {
					t.Error("Anthropic 的 input 恒是 JSON 对象，ArgsIsJSON 应为 true")
				}
				var probe map[string]any
				if json.Unmarshal([]byte(b.ToolCall.Args), &probe) != nil {
					t.Errorf("Args 不是 JSON 对象: %q", b.ToolCall.Args)
				}
				callIDs = append(callIDs, b.ToolCall.ID)
			case protocol.BlockToolResult:
				if b.ToolResult == nil {
					t.Fatal("tool_result 块没有 ToolResult")
				}
				if len(b.ToolResult.Content) == 0 {
					t.Error("tool_result 内容为空")
				}
				resultIDs = append(resultIDs, b.ToolResult.ToolCallID)
			}
		}
	}

	if len(callIDs) < 2 {
		t.Fatalf("并行样本里只解出 %d 个 tool_use", len(callIDs))
	}
	if len(callIDs) != len(resultIDs) {
		t.Fatalf("tool_use %d 个、tool_result %d 个，对不上", len(callIDs), len(resultIDs))
	}
	for i, id := range callIDs {
		if id == "" {
			t.Error("tool_use 缺 id")
		}
		if resultIDs[i] != id {
			t.Errorf("第 %d 对 id 不一致: tool_use=%q tool_result=%q", i, id, resultIDs[i])
		}
	}
}

// 服务端工具（advisor_20260301 自带 type 与 model）要被认成 ToolServer，否则会被
// 当普通 function 编给 CC 上游，而那边既不认这个 type 也变不出这个能力。
func TestDecodeRequestClassifiesServerTool(t *testing.T) {
	codec := anthropic.NewCodec()
	body, meta := loadInbound(t, "in-anthropic-tool-turn1")

	req, err := codec.DecodeRequest(body, meta.Stream)
	if err != nil {
		t.Fatal(err)
	}
	var server, function int
	for _, tool := range req.Tools {
		switch tool.Kind {
		case protocol.ToolServer:
			server++
			if _, ok := tool.Extras["type"]; !ok {
				t.Error("服务端工具的 type 没进 Extras")
			}
		case protocol.ToolFunction:
			function++
			if len(tool.Schema) == 0 {
				t.Errorf("function 工具 %q 没有 schema", tool.Name)
			}
		}
	}
	if server != 1 {
		t.Errorf("认出 %d 个服务端工具，实采是 1 个（advisor）", server)
	}
	if function == 0 {
		t.Error("一个 function 工具都没认出来")
	}
}

// thinking 块的正文进 Text、signature 进 Extras。两者去向不同：正文跨协议丢，
// signature 连在同协议下都只对**原上游**有效。
func TestDecodeRequestSplitsThinkingAndSignature(t *testing.T) {
	codec := anthropic.NewCodec()
	body, meta := loadInbound(t, "in-anthropic-tool-turn2")

	req, err := codec.DecodeRequest(body, meta.Stream)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Kind != protocol.BlockThinking {
				continue
			}
			found = true
			if _, ok := b.Extras["signature"]; !ok {
				t.Error("thinking 块的 signature 没进 Extras")
			}
			if _, ok := b.Extras["thinking"]; ok {
				t.Error("thinking 正文既进了 Text 又留在 Extras，会被编码侧重复消费")
			}
		}
	}
	if !found {
		t.Fatal("没找到 thinking 块")
	}
}

// 全函数不是「样本能解就行」：没见过的块类型、缺字段、纯字符串 content 都不许把
// 整条请求打死——真打死了，一个新 beta 上线当天全部请求就都回 400 了。
func TestDecodeRequestToleratesUnknownShapes(t *testing.T) {
	codec := anthropic.NewCodec()
	body := []byte(`{
		"model":"m","max_tokens":16,
		"system":"纯字符串 system",
		"messages":[
			{"role":"user","content":"字符串"},
			{"role":"assistant","content":[{"type":"未来的新块","payload":{"a":1}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"字符串结果"}]}
		],
		"未来的新顶层字段":{"x":1}
	}`)

	req, err := codec.DecodeRequest(body, false)
	if err != nil {
		t.Fatalf("合法但没见过的形态不该解不动: %v", err)
	}
	if len(req.System) != 1 || req.System[0].Text != "纯字符串 system" {
		t.Errorf("字符串 system 没退化成单块: %+v", req.System)
	}
	if got := req.Messages[1].Content[0].Kind; got != "未来的新块" {
		t.Errorf("未知块类型的 Kind = %q, 期望原样保留", got)
	}
	if _, ok := req.Messages[1].Content[0].Extras["payload"]; !ok {
		t.Error("未知块的字段没进 Extras——静默吃掉是最难查的那种丢失")
	}
	if _, ok := req.Extras["未来的新顶层字段"]; !ok {
		t.Error("未知顶层字段没进 Extras")
	}
	res := req.Messages[2].Content[0].ToolResult
	if res == nil || len(res.Content) != 1 || res.Content[0].Text != "字符串结果" {
		t.Errorf("字符串形态的 tool_result content 没退化成单块: %+v", res)
	}
}

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR42mP4z8AAAAMBAQD3A0FDAAAAAElFTkSuQmCC"

func TestDecodeRequestImageSource(t *testing.T) {
	codec := anthropic.NewCodec()
	body := []byte(`{
		"model":"m","max_tokens":16,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"看图"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNG + `"}},
			{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},
			{"type":"image","source":{"type":"file","file_id":"file_xxx"}}
		]}]
	}`)
	req, err := codec.DecodeRequest(body, false)
	if err != nil {
		t.Fatal(err)
	}
	blocks := req.Messages[0].Content
	if len(blocks) != 4 {
		t.Fatalf("块数 = %d，期望 4", len(blocks))
	}
	if blocks[1].Kind != protocol.BlockImage || blocks[1].Image == nil {
		t.Fatalf("base64 图没落成 BlockImage: %+v", blocks[1])
	}
	if blocks[1].Image.MediaType != "image/png" || blocks[1].Image.Data != tinyPNG {
		t.Errorf("base64 图字段不对: %+v", blocks[1].Image)
	}
	if blocks[1].Extras["source"] != nil {
		t.Errorf("source 不应再进 Extras: %+v", blocks[1].Extras)
	}
	if blocks[2].Image == nil || blocks[2].Image.URL != "https://example.com/a.png" {
		t.Errorf("url 图没解对: %+v", blocks[2].Image)
	}
	if blocks[3].Image == nil || blocks[3].Image.FileID != "file_xxx" {
		t.Errorf("file 图没解对: %+v", blocks[3].Image)
	}
}

// type 是判别式：type=file 带残留 data 仍只认 FileID，不能被 Carrier 的
// Data 优先吃成一张假 base64 图。
func TestDecodeRequestImageFileTypeWinsOverData(t *testing.T) {
	codec := anthropic.NewCodec()
	body := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[
		{"type":"image","source":{"type":"file","file_id":"file_xxx","data":"` + tinyPNG + `"}}
	]}]}`)
	req, err := codec.DecodeRequest(body, false)
	if err != nil {
		t.Fatal(err)
	}
	img := req.Messages[0].Content[0].Image
	if img == nil || img.FileID != "file_xxx" {
		t.Fatalf("应落成 FileID: %+v", img)
	}
	if img.Data != "" {
		t.Errorf("type=file 不该把残留 data 收成 Data: %+v", img)
	}
}
