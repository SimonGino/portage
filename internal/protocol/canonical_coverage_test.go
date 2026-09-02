package protocol_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 这个测试把「canonical 模型装得下所有入站样本」从一句自我感觉，变成一张逐项打勾
// 的清单。
//
// 做法：把 testdata/golden/in-*/request.json 里出现过的键路径全抽出来（数组下标归一
// 为 []），与下面 disposition 表比对。样本里冒出表上没有的路径 → 红；表上列了样本里
// 已经没有的路径 → 也红（防止表随样本演进烂掉）。
//
// 它不校验语义正确，只校验**没有字段被无声漏掉**。手搭清单必漏——上一轮就差点漏掉
// output_config。

// disposition 是一个键路径在 canonical 模型里的归宿。
type disposition string

const (
	// dField：有对应的 canonical 结构体字段，跨协议能转。
	dField disposition = "field"
	// dExtras：进 Extras，同协议原样取回，跨协议由 encode 侧按口径决定丢不丢。
	dExtras disposition = "extras"
	// dOpaque：整棵子树按原始字节保留（JSON Schema、lark 文法、工具入参），
	// 不解开也不下钻——键序与数值精度都可能影响上游行为。
	dOpaque disposition = "opaque"
	// dDropped：显式丢弃，且文档 §4 写明了丢弃的后果。
	dDropped disposition = "dropped"
)

// opaqueRoots 之下不再下钻，路径抽取到这里为止。
var opaqueRoots = []string{
	"tools[].input_schema",
	"input[].tools[].parameters",
	"input[].tools[].format",
	"messages[].content[].input",
	"tools[].function.parameters", // CC 的工具 JSON Schema，同 tools[].input_schema
	"tools[].parameters",          // Responses 顶层工具的 JSON Schema（ADE 实采）
	"tools[].tools[].parameters",  // Responses namespace 子工具的 JSON Schema
}

// coverage 是键路径 → 归宿。改动这张表就得同步改 docs/MVP设计草案.md §4 的对照表。
var coverage = map[string]disposition{
	// ---- Anthropic Messages（Claude Code 2.x 实采）----
	"model":      dField, // Request.Model
	"stream":     dField, // Request.Stream
	"max_tokens": dField, // Request.MaxTokens

	"system":                      dField, // Request.System（块序列，非字符串）
	"system[]":                    dField,
	"system[].type":               dField,  // → Block.Kind
	"system[].text":               dField,  // → Block.Text
	"system[].cache_control":      dExtras, // → Block.Extras，断点位置是被测行为
	"system[].cache_control.type": dExtras,

	"messages":                                dField,
	"messages[]":                              dField,
	"messages[].role":                         dField, // → Message.Role（含 system，见 §4）
	"messages[].content":                      dField, // 字符串退化为单个 text 块
	"messages[].content[]":                    dField,
	"messages[].content[].type":               dField, // → Block.Kind
	"messages[].content[].text":               dField,
	"messages[].content[].thinking":           dField,  // BlockThinking.Text
	"messages[].content[].signature":          dExtras, // 回带同一上游用，跨协议丢
	"messages[].content[].id":                 dField,  // ToolCall.ID
	"messages[].content[].name":               dField,  // ToolCall.Name
	"messages[].content[].input":              dOpaque, // ToolCall.Args
	"messages[].content[].tool_use_id":        dField,  // ToolResult.ToolCallID
	"messages[].content[].content":            dField,  // ToolResult.Content（字符串退化）
	"messages[].content[].cache_control":      dExtras,
	"messages[].content[].cache_control.type": dExtras,
	// 图片（#1）。
	"messages[].content[].source":            dField, // → Image.{MediaType,Data,URL,FileID}
	"messages[].content[].source.type":       dField,
	"messages[].content[].source.media_type": dField,
	"messages[].content[].source.data":       dField,
	"messages[].content[].image_url":         dField, // CC image_url 对象
	"messages[].content[].image_url.url":     dField,
	// detail 在 CC 上住在 image_url 对象**里面**，Responses 上是 part 顶层的兄弟
	// （见下面 input[].content[].detail）——同一个语义、两种形状，别写反。
	"messages[].content[].image_url.detail": dField, // → Image.Detail（口径层 v0.78）

	// tool_result 里的图（#1）。容器形状三协议不一样，是本项目此前没记过的一条
	// 转换约束：Anthropic 的 tool_result.content 收 text+image 数组，CC 的
	// role=tool content 与 Responses 的 function_call_output.output 都只收字符串，
	// 于是那两个出口把图**抬成后续独立的 user 消息**。
	"messages[].content[].content[]":                   dField,
	"messages[].content[].content[].type":              dField,
	"messages[].content[].content[].text":              dField,
	"messages[].content[].content[].source":            dField,
	"messages[].content[].content[].source.type":       dField,
	"messages[].content[].content[].source.media_type": dField,
	"messages[].content[].content[].source.data":       dField,

	"tools":                dField,
	"tools[]":              dField,
	"tools[].name":         dField,
	"tools[].description":  dField,
	"tools[].input_schema": dOpaque, // Tool.Schema
	"tools[].type":         dExtras, // 服务端工具的 type（advisor_20260301）
	"tools[].model":        dExtras, // 服务端工具自带的模型名

	"metadata":         dExtras, // 上游据此判定是否官方 Claude Code，重序列化丢了会降级
	"metadata.user_id": dExtras,

	"thinking":                        dExtras,
	"thinking.type":                   dExtras,
	"thinking.display":                dExtras,
	"context_management":              dExtras,
	"context_management.edits":        dExtras,
	"context_management.edits[]":      dExtras,
	"context_management.edits[].type": dExtras,
	"context_management.edits[].keep": dExtras,
	"output_config":                   dExtras,
	"output_config.effort":            dExtras,

	// ---- OpenAI Responses（Codex CLI 0.144 实采）----
	"tool_choice":                dField, // Request.ToolChoice
	"parallel_tool_calls":        dExtras,
	"store":                      dExtras,
	"include":                    dExtras,
	"include[]":                  dExtras,
	"prompt_cache_key":           dExtras,
	"reasoning":                  dExtras,
	"reasoning.effort":           dExtras,
	"reasoning.context":          dExtras,
	"text":                       dExtras,
	"text.verbosity":             dExtras,
	"client_metadata":            dExtras,
	"client_metadata.session_id": dExtras,
	"client_metadata.thread_id":  dExtras,
	"client_metadata.turn_id":    dExtras,
	"client_metadata.x-codex-installation-id": dExtras,
	"client_metadata.x-codex-window-id":       dExtras,
	"client_metadata.x-codex-turn-metadata":   dExtras,

	"input":        dField, // 展平成 Request.Messages + Request.Tools
	"input[]":      dField,
	"input[].type": dField, // 判别式：message / custom_tool_call(_output) / reasoning / additional_tools
	"input[].role": dField, // developer 归一为 RoleSystem（见 Role 注释），user 直通

	"input[].content":             dField,
	"input[].content[]":           dField,
	"input[].content[].type":      dField, // input_text / input_image
	"input[].content[].text":      dField,
	"input[].content[].image_url": dField, // Responses 上是字符串，不是对象
	"input[].content[].detail":    dField, // → Image.Detail；顶层，不在 image_url 里

	"input[].call_id":       dField, // ToolCall.ID / ToolResult.ToolCallID
	"input[].name":          dField, // ToolCall.Name
	"input[].input":         dField, // ToolCall.Args（自由文本，不是 JSON）
	"input[].status":        dExtras,
	"input[].output":        dField, // ToolResult.Content
	"input[].output[]":      dField,
	"input[].output[].type": dField,
	"input[].output[].text": dField,

	"input[].encrypted_content": dExtras, // 上游侧密文，跨协议必然作废（§5 坑清单）
	"input[].summary":           dExtras,
	"input[].summary[]":         dExtras, // 实采里恒为空数组，但空数组也是结构

	// additional_tools 是个 input 项，decode 时提升到 Request.Tools。
	// 「它原本是条 developer 消息」这个位置信息随之丢失——回编 Responses 时按
	// 首项重建即可，语义等价；转 CC/Anthropic 时本就没有对应容器。
	"input[].tools":               dField,
	"input[].tools[]":             dField,
	"input[].tools[].type":        dField, // → Tool.Kind
	"input[].tools[].name":        dField,
	"input[].tools[].description": dField,
	"input[].tools[].parameters":  dOpaque, // Tool.Schema
	"input[].tools[].strict":      dExtras,
	"input[].tools[].format":      dOpaque, // lark 文法，无 schema 对应物

	// ---- OpenAI Responses（ADE 2026-09-02 实采，in-responses-namespace-turn1）----
	//
	// ADE 走的是**顶层 `tools`**，不是 Codex 的 additional_tools 项。tools[].name /
	// description / type 与上面 Anthropic 段同名，归宿不再重复列（type 在这一侧是
	// Tool.Kind 的判别式）。
	"instructions":                dField,  // → Request.System
	"tools[].parameters":          dOpaque, // Tool.Schema
	"tools[].strict":              dExtras,
	"tools[].external_web_access": dExtras, // web_search 的开关，随 ToolServer 进 Extras
	// namespace 子工具在解码侧摊平成一个个 Tool（口径层 v1.14 ①—④，#94）：名字拼成
	// `<ns>__<name>`（functions / 空 / 缺失免前缀），type 照旧是 Tool.Kind 的判别式，
	// 摊平名 → 来处记在 openairesponses.Codec.NamespaceTools()。外壳自己的 name /
	// description 只贡献前缀，不单独落地。
	"tools[].tools":               dField,
	"tools[].tools[]":             dField,
	"tools[].tools[].type":        dField,
	"tools[].tools[].name":        dField,  // 摊平进 Tool.Name
	"tools[].tools[].description": dField,  // Tool.Description
	"tools[].tools[].parameters":  dOpaque, // Tool.Schema
	"tools[].tools[].strict":      dExtras,

	// ---- OpenAI Chat Completions（opencode 1.18 实采，portage-legacy#27）----
	//
	// model / stream / max_tokens / messages / messages[].role / messages[].content
	// 与上面 Anthropic 段同名同归宿，不再重复列。这里只列 CC 独有的那些。

	// 工具调用：CC 把一轮的多个调用装在 assistant 消息的 tool_calls 数组里，
	// 结果则是**每个调用一条独立的 tool 消息**（实采 in-cc-parallel-turn2 两条）。
	// 这与 Anthropic 正相反——那边所有 tool_result 必须挤进同一条 user 消息，
	// 所以 CC→A 的编码侧要做合并，不是逐条平移。
	"messages[].tool_calls":                      dField, // → assistant 消息里的 tool_use 块序列
	"messages[].tool_calls[]":                    dField,
	"messages[].tool_calls[].id":                 dField, // → ToolCall.ID，与 tool_call_id 对上
	"messages[].tool_calls[].type":               dField, // 恒 "function"，没有第二种取值
	"messages[].tool_calls[].function":           dField,
	"messages[].tool_calls[].function.name":      dField, // → ToolCall.Name
	"messages[].tool_calls[].function.arguments": dField, // → ToolCall.Args（按契约是 JSON 字符串）
	"messages[].tool_call_id":                    dField, // → ToolResult.ToolCallID（消息级落到块级）

	// stream_options.include_usage 是 CC 独有的开关：不给就不发那个 usage chunk。
	// **不能丢**——入口半边的 EncodeStream 要靠它决定回程要不要补 usage 帧，
	// 丢了就只能猜，两个方向都会错一半。Anthropic / Responses 没有对应开关
	// （usage 恒发），所以它进 Extras 而不是 canonical 字段。
	"stream_options":               dExtras,
	"stream_options.include_usage": dExtras,

	"tools[].function":             dField,
	"tools[].function.name":        dField,  // → Tool.Name
	"tools[].function.description": dField,  // → Tool.Description
	"tools[].function.parameters":  dOpaque, // → Tool.Schema，整棵 JSON Schema 不下钻
}

// inboundRoots 是入站样本的两个来源，各走各的闸。
//
// 两处都扫，是因为这张表要覆盖的是「canonical 装不装得下」，而**能证明形状的样本不
// 一定是转录**：图片那几格（#1）真实 harness 至今一份都没发过——实采样本的 content
// 全是字符串——但形状本身照官方文档是确定的。把构造样本挡在表外，等于让新加的图片
// 路径永远无样本可依，那几行会当场被判成「表随样本烂掉」的陈旧项。
//
// 反过来也不放松：构造样本**不进 golden/**（PO 2026-08-17 裁），它走 synthetic 那道
// 反向闸，钉死自己不是转录。缘由与升格规矩见 testdata/fixtures/README.md。
var inboundRoots = []struct {
	dir string
	// gate 读 meta.json，判这份样本够不够格当依据。
	gate func(meta struct {
		Verified  bool `json:"verified"`
		Synthetic bool `json:"synthetic"`
	}) string
}{
	{
		dir: "golden",
		gate: func(m struct {
			Verified  bool `json:"verified"`
			Synthetic bool `json:"synthetic"`
		}) string {
			// verified 关卡：入站样本经脱敏改过字节，没人核过就不该被当成事实源。
			// 这里跟 golden_test.go 同一道闸，理由见 cmd/goldenrec/main.go 注释。
			if !m.Verified {
				return "meta.json 仍是 verified:false——脱敏并核对后再置 true"
			}
			if m.Synthetic {
				return "meta.json 写着 synthetic:true——构造样本请放回 testdata/fixtures/，别冒充转录"
			}
			return ""
		},
	},
	{
		dir: "fixtures",
		gate: func(m struct {
			Verified  bool `json:"verified"`
			Synthetic bool `json:"synthetic"`
		}) string {
			// 反向闸：拦的是「构造样本被搬进 golden 冒充转录」。
			if !m.Synthetic {
				return "meta.json 没有 synthetic:true——真录到的样本请放进 testdata/golden/ 走 verified 那道闸"
			}
			return ""
		},
	},
}

func TestCanonicalModelCoversInboundSamples(t *testing.T) {
	var dirs []string
	gates := map[string]func(struct {
		Verified  bool `json:"verified"`
		Synthetic bool `json:"synthetic"`
	}) string{}
	for _, root := range inboundRoots {
		found, err := filepath.Glob(filepath.Join("..", "..", "testdata", root.dir, "in-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(found) == 0 {
			t.Fatalf("testdata/%s 下没找到任何 in-* 入站样本；这个测试对着空集合永远绿，等于没有", root.dir)
		}
		for _, d := range found {
			if name := filepath.Base(d); gates[name] != nil {
				t.Fatalf("样本名 %s 在 golden/ 与 fixtures/ 下各有一份——同名两档说不清哪份是依据", name)
			}
			dirs = append(dirs, d)
			gates[filepath.Base(d)] = root.gate
		}
	}

	seen := map[string][]string{} // path → 出现在哪些样本
	for _, dir := range dirs {
		name := filepath.Base(dir)
		metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var meta struct {
			Verified  bool `json:"verified"`
			Synthetic bool `json:"synthetic"`
		}
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			t.Fatalf("%s meta.json: %v", name, err)
		}
		if reason := gates[name](meta); reason != "" {
			t.Fatalf("%s 的 %s", name, reason)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "request.json"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, p := range keyPaths(v, "") {
			if !contains(seen[p], name) {
				seen[p] = append(seen[p], name)
			}
		}
	}

	var missing, stale []string
	for p := range seen {
		if _, ok := coverage[p]; !ok {
			missing = append(missing, fmt.Sprintf("%s（出现在 %s）", p, strings.Join(seen[p], ", ")))
		}
	}
	for p := range coverage {
		if _, ok := seen[p]; !ok {
			stale = append(stale, p)
		}
	}
	slices.Sort(missing)
	slices.Sort(stale)

	if len(missing) > 0 {
		t.Errorf("样本里有 canonical 模型没交代的字段，共 %d 条。\n"+
			"每一条都得决定归宿（field / extras / opaque / dropped），同步写进 "+
			"docs/MVP设计草案.md §4 再补到 coverage 表：\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("coverage 表上有样本里已经不存在的路径，共 %d 条——表跟着样本烂掉了，"+
			"删掉或补样本：\n  %s", len(stale), strings.Join(stale, "\n  "))
	}
}

// keyPaths 抽出所有键路径，数组下标归一为 []，opaqueRoots 之下不再下钻。
func keyPaths(v any, prefix string) []string {
	if prefix != "" {
		for _, root := range opaqueRoots {
			if prefix == root {
				return nil
			}
		}
	}
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out = append(out, p)
			out = append(out, keyPaths(child, p)...)
		}
	case []any:
		p := prefix + "[]"
		out = append(out, p)
		for _, child := range t {
			out = append(out, keyPaths(child, p)...)
		}
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
