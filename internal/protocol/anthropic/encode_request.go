package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Anthropic Messages 的**出口**编码：canonical → 上游请求体。
//
// 与 openaicc 那边的前提不同。CC 出口面对的是「严格中转」，规整是为了不被第三方
// 网关拒；这里面对的是 Anthropic 自己（或照它实现的中转），协议约束是**协议本身
// 写死的**，宽容度更低：
//
//   - max_tokens 必填，没有就是 400。
//   - messages 的 role 只收 user / assistant。canonical 里可能有 RoleSystem——
//     Responses 的 developer 消息就归一成了它（openairesponses/decode.go），必须
//     上提到顶层 system 字段，留在 messages 里会被拒。
//   - 相邻同角色消息要合并：Anthropic 要求 user/assistant 交替（§5 坑清单）。
//   - tool_result 必须待在 user 消息里，且引用的 tool_use_id 得真的出现过。
//   - 每个工具声明必须带 input_schema。
//
// Anthropic 协议没有 custom 工具形态。Responses 的 custom 工具（Codex 的 exec，
// 入参是 JS 源码）只能编成普通 function + 合成的 {"input": string} 声明，入参
// 对称包装——三件事在 protocol/customtool.go。

// DroppedOnEncode 列出 canonical → Anthropic 必然丢掉的东西，理由同 openaicc 的
// 同名一节：codec 不持有 logger，谁丢的谁登记，由 relay 读这张表打日志。
const (
	DropServerTool    = "server_tool"    // 入口协议声明的上游服务端工具（Responses 的 web_search 一类）
	DropToolGrammar   = "tool_grammar"   // custom 工具的文法约束（Responses format），Anthropic 无对应能力
	DropVendorRequest = "vendor_request" // 入口协议独有的顶层字段
	DropVendorContent = "vendor_content" // Anthropic 认不得的内容块（多模态等）
	DropOrphanResult  = "orphan_result"  // 找不到对应 tool_use 的 tool_result
)

// EncodeRequest 把 canonical 编成 Anthropic Messages 请求体。
func (c *Codec) EncodeRequest(req *protocol.Request, stream bool) ([]byte, error) {
	body, _, err := c.encodeRequest(req, stream)
	return body, err
}

// EncodeRequestReport 与 EncodeRequest 同源，另外交出丢弃清单（protocol.RequestEncodeReporter）。
func (c *Codec) EncodeRequestReport(req *protocol.Request, stream bool) ([]byte, []string, error) {
	return c.encodeRequest(req, stream)
}

func (c *Codec) encodeRequest(req *protocol.Request, stream bool) ([]byte, []string, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("anthropic: canonical 请求为空")
	}
	var dropped []string
	drop := func(what string) {
		for _, d := range dropped {
			if d == what {
				return
			}
		}
		dropped = append(dropped, what)
	}

	out := map[string]any{"model": req.Model}

	// max_tokens 必填。零值兜底用配置注入的默认值；连默认值都没配（Options 零值）
	// 时仍然要给一个正数，否则请求必被拒——宁可发一个保守的上限，也不要发一个
	// 注定 400 的请求。
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.opts.DefaultMaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = fallbackMaxTokens
	}
	out["max_tokens"] = maxTokens

	system, msgs := splitSystem(req)
	if len(system) > 0 {
		out["system"] = encodeBlocks(system)
	}
	out["messages"] = encodeMessages(msgs, drop)

	tools := encodeTools(req.Tools, drop)
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if choice, ok := encodeToolChoice(req.ToolChoice, tools); ok {
		out["tool_choice"] = choice
	}

	if req.Temperature != nil {
		out["temperature"] = clampTemperature(*req.Temperature)
	}
	if len(req.Stop) > 0 {
		out["stop_sequences"] = req.Stop
	}
	if stream {
		out["stream"] = true
	}

	// 入口协议独有的顶层字段不带过去：Responses 的 reasoning / store / include /
	// prompt_cache_key 之类，Anthropic 一个都不认。丢在明处。
	if len(req.Extras) > 0 {
		drop(DropVendorRequest)
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil, nil, fmt.Errorf("anthropic: 序列化请求体: %w", err)
	}
	return body, dropped, nil
}

// clampTemperature 把 OpenAI 侧的 0~2 收进 Anthropic 的 0~1（展开层 §2）。
//
// 两边这个字段同名不同域：CC 与 Responses 收 0~2，Anthropic 超过 1 直接 400。能走到
// 这里的只有 CC→A 与 R→A，入站值就是 OpenAI 域的，不 clamp 等于把一部分合法请求
// 变成必被上游拒的请求。
//
// 截断而不是线性缩放（不做 t/2）：缩放会**悄悄改掉**每一个请求的采样行为——客户端
// 发 0.7 期待的是 0.7，收到 0.35 的输出会更保守而它无从知晓。截断只动那些本来就
// 越界、否则会失败的值，动的范围最小。
func clampTemperature(t float64) float64 {
	switch {
	case t < 0:
		return 0
	case t > 1:
		return 1
	}
	return t
}

// fallbackMaxTokens 是连配置都没给时的最后兜底。
//
// 取 4096 而非某个很大的数：这个分支只在配置被显式设成 0 或 codec 被绕过 codecs.New
// 直接构造时才走到，属于「不该发生但发生了」。发一个保守值，宁可截断也不要让请求
// 直接 400——截断了客户端还能看见半截回答，400 什么都没有。
const fallbackMaxTokens = 4096

// splitSystem 把 RoleSystem 消息从消息序列里摘出来，并到 System 块序列后面。
//
// Anthropic 的 messages 只收 user / assistant。canonical 里之所以会有 RoleSystem
// 消息，是因为 Responses 的 developer 消息归一成了它（openairesponses 的
// normalizeRole）；Anthropic 入口自己解出来的 system 走的是 Request.System 字段。
//
// 按原序上提，不管它原本站在消息序列第几位。中段的 system 消息（Anthropic 的
// mid-conversation-system beta 会发，见 decodeMessages 的注释）上提之后就丢了
// 「它插在哪」这个信息——实采里 Responses 侧的 developer 消息全在最前，没有中段
// 用例，所以先按简单规则来，真遇到再说，不为一个没见过的形态提前设计。
func splitSystem(req *protocol.Request) ([]protocol.Block, []protocol.Message) {
	system := append([]protocol.Block(nil), req.System...)
	msgs := make([]protocol.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == protocol.RoleSystem {
			system = append(system, m.Content...)
			continue
		}
		msgs = append(msgs, m)
	}
	return system, msgs
}

// encodeMessages 铺 messages，顺带做两件 Anthropic 特有的规整。
//
// 一是**相邻同角色合并**：canonical 允许连发同角色（CC 那边合法，in-cc-consecutive-user
// 实采就是连着两条 user），Anthropic 要求交替。
//
// 二是 tool_result 的归属。CC 入口解出来的是**每个结果一条 RoleTool 消息**
// （openaicc/decode_request.go 不做归一），而 Anthropic 要求同一轮的所有 tool_result
// 挤进同一条 user 消息里。这里不需要为它写专门的合并逻辑——RoleTool 落在下面
// 「非 assistant 一律当 user」那条上，紧跟着就被相邻同角色合并并成一条。两条并行
// 调用的结果（in-cc-parallel-turn2）因此自动合成一条 user 消息的两个块。
//
// Responses 入口解出来的则本就是 Anthropic 摆法（结果在 user 消息的块里），按序编
// 出去即可。两条入口路径殊途同归。
//
// 空内容的消息剔除：纯 thinking 的 assistant 消息在丢掉 thinking 之后就空了，而
// Anthropic 不收 content 为空数组的消息。
func encodeMessages(msgs []protocol.Message, drop func(string)) []map[string]any {
	// 先扫一遍收集真实出现过的 tool_use id：孤儿 tool_result（引用一个本次请求里
	// 不存在的调用）会被 Anthropic 直接拒，而它确实会出现——入口侧历史被截断、
	// 或者上一轮的调用没能编过来。
	seen := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Kind == protocol.BlockToolUse && b.ToolCall != nil {
				seen[b.ToolCall.ID] = true
			}
		}
	}

	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		role := string(m.Role)
		if role != string(protocol.RoleAssistant) {
			// user 之外的角色（含未知角色）一律当 user：Anthropic 只有两个角色位，
			// 而把一条内容整个丢掉比把它放错角色更糟。
			role = string(protocol.RoleUser)
		}
		blocks := encodeBlocksFiltered(m.Content, seen, drop)
		if len(blocks) == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1]["role"] == role {
			prev := out[n-1]["content"].([]map[string]any)
			out[n-1]["content"] = append(prev, blocks...)
			continue
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	return out
}

// encodeBlocks 编一组块，不做过滤（system 位用）。
func encodeBlocks(blocks []protocol.Block) []map[string]any {
	return encodeBlocksFiltered(blocks, nil, func(string) {})
}

// encodeBlocksFiltered 编一组块。seen 非 nil 时校验 tool_result 的归属。
func encodeBlocksFiltered(blocks []protocol.Block, seen map[string]bool, drop func(string)) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		switch b.Kind {
		case protocol.BlockText:
			if b.Text == "" {
				continue
			}
			out = append(out, map[string]any{"type": "text", "text": b.Text})

		case protocol.BlockThinking:
			// 跨协议丢弃（口径层 §2.6，§5 坑清单）。这里**不登记 drop**：thinking
			// 在 A→A 方向不经过 codec，能走到这儿的只有 R→A，而 Responses 侧的
			// reasoning 块本来就只有一段谁也解不开的密文（encrypted_content），
			// canonical 里的 Text 恒为空。丢一个空块不值得报警。
			continue

		case protocol.BlockToolUse:
			if b.ToolCall == nil {
				continue
			}
			out = append(out, encodeToolUse(b.ToolCall))

		case protocol.BlockToolResult:
			if b.ToolResult == nil {
				continue
			}
			if seen != nil && !seen[b.ToolResult.ToolCallID] {
				drop(DropOrphanResult)
				continue
			}
			out = append(out, encodeToolResult(b.ToolResult))

		default:
			// 认不得的块类型：跳过，**并且登记**。canonical 的 BlockKind 是字符串，
			// 装得下没见过的形态（CC 的 image_url / input_audio 就是这么留住的），
			// 但 Anthropic 只认它自己那四种，原样发过去会被拒。
			//
			// 不登记就是静默改写语义：客户端发了张图，上游收到的是一个被改成纯文本
			// 的请求，还照样 200 回来。thinking 那一格可以不登记（它是**口径**定的
			// 必然丢弃，每次都丢，报了等于每请求一条噪声）；这一格不同，它是「我不
			// 认识这个东西」，恰恰是需要看见的那种。
			drop(DropVendorContent)
			continue
		}
	}
	return out
}

// encodeToolUse 编一次工具调用。
//
// input 必须是 JSON 对象。canonical 的 Args 不保证是——Responses 的 custom 工具收
// 自由文本，于是走对称包装（protocol/customtool.go）。ArgsIsJSON 为 true 时原样
// 塞进去：那是上游给的原始字节，解开再合上是无谓的失真风险。
func encodeToolUse(call *protocol.ToolCall) map[string]any {
	var input any = map[string]any{}
	switch {
	case call.Args == "":
	case call.ArgsIsJSON:
		input = json.RawMessage(call.Args)
	default:
		wrapped, err := protocol.WrapCustomToolArgs(call.Args)
		if err != nil {
			// WrapCustomToolArgs 只在 json.Marshal 一个 map[string]string 时才会
			// 出错，实际上不会发生；真发生了就发空对象，别把整条请求打死。
			break
		}
		input = json.RawMessage(wrapped)
	}
	return map[string]any{
		"type":  "tool_use",
		"id":    call.ID,
		"name":  call.Name,
		"input": input,
	}
}

// encodeToolResult 编一条工具结果。
//
// content 拼成纯文本而不是块数组：canonical 的结果块序列来自 Responses 的 output
// （若干段 input_text），Anthropic 收字符串也收数组，字符串更省事且无歧义。
func encodeToolResult(res *protocol.ToolResult) map[string]any {
	var parts []string
	for _, b := range res.Content {
		if b.Kind == protocol.BlockText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	out := map[string]any{
		"type":        "tool_result",
		"tool_use_id": res.ToolCallID,
		"content":     strings.Join(parts, "\n"),
	}
	if res.IsError {
		out["is_error"] = true
	}
	return out
}

// encodeTools 编工具声明。
//
// input_schema 是**必填**，所以三种来源都得给出点什么：有 Schema 的原样带；custom
// 工具合成 {"input": string}（见 protocol.CustomToolSchema，这也是让模型知道该回
// 什么形状的唯一途径）；两者都没有的给一个空对象 schema。
//
// 服务端工具直接丢：它是**上游侧**能力，目标上游既不认这个 type 也变不出这个能力。
func encodeTools(tools []protocol.Tool, drop func(string)) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if t.Kind == protocol.ToolServer {
			drop(DropServerTool)
			continue
		}
		tool := map[string]any{"name": t.Name}
		if t.Description != "" {
			tool["description"] = t.Description
		}
		switch {
		case len(t.Schema) > 0:
			tool["input_schema"] = json.RawMessage(t.Schema)
		case t.Kind == protocol.ToolCustom:
			tool["input_schema"] = protocol.CustomToolSchema()
			drop(DropToolGrammar)
		default:
			tool["input_schema"] = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, tool)
	}
	return out
}

// encodeToolChoice 把 canonical 的选择策略编成 Anthropic 形态。
//
// required → any 是 decodeToolChoice 那条归一的反向。没有 tools 就不发
// tool_choice，指名一个没声明的工具也不发：两者都会被拒（§5 坑清单，与 CC 出口
// 同一条理由）。
func encodeToolChoice(choice protocol.ToolChoice, tools []map[string]any) (any, bool) {
	if choice.Mode == "" || len(tools) == 0 {
		return nil, false
	}
	switch choice.Mode {
	case "auto", "none":
		return map[string]any{"type": choice.Mode}, true
	case "required":
		return map[string]any{"type": "any"}, true
	case "tool":
		for _, t := range tools {
			if t["name"] == choice.Name {
				return map[string]any{"type": "tool", "name": choice.Name}, true
			}
		}
	}
	return nil, false
}
