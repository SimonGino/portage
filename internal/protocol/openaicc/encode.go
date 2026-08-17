package openaicc

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Chat Completions 的**编码**侧：canonical → 出口请求体。
//
// 贯穿全文件的前提是**严格中转**（§5 坑清单）：第三方 OpenAI 兼容上游远没有官方
// 宽容，content 给数组、tool_choice 引用未声明的工具、有 tool_choice 没有 tools，
// 都会被直接拒。规整放在这里做，不指望上游兜着。

// DroppedOnEncode 列出 canonical → CC 必然丢掉的东西，供上层打警告日志用。
//
// 做成导出的常量表而不是就地静默丢弃：口径层 §2.6 要求跨协议丢弃「+ 日志警告」，
// 而 codec 这一层刻意不持有 logger（它是纯函数，测试里不该冒日志）。谁丢的谁登记，
// 由 relay 在转换路径上读这张表打日志。
const (
	DropMetadata      = "metadata"       // A 入口的 metadata.user_id：上游据此判定是否官方 Claude Code
	DropCacheControl  = "cache_control"  // Anthropic 缓存断点，CC 协议无对应概念
	DropThinking      = "thinking"       // thinking 块正文与 signature
	DropServerTool    = "server_tool"    // 上游服务端工具声明（advisor_20260301 一类）
	DropVendorRequest = "vendor_request" // 其余入口协议独有的顶层字段
	DropToolGrammar   = "tool_grammar"   // custom 工具的文法约束（Responses format），CC 无对应能力
	DropVendorContent = "vendor_content" // CC 认不得的内容块（多模态等）
	DropImageFileID   = "image_file_id"  // file_id 是上游作用域句柄，跨协议搬不走
	// DropThinkingParam 是**请求侧的思考参数**（口径层 v0.65 ⑤），与 DropThinking
	// （内容块）不是一档：住户是思考开关本身（thinking.type / budget_tokens /
	// display）、reasoning.summary、各家的数值预算。effort 不在这一档——它现在原样
	// 直传成 reasoning_effort（protocol.Request.Effort）。
	DropThinkingParam = "thinking_param"
)

// EncodeRequest 把 canonical 编成 Chat Completions 请求体。
//
// 返回的 dropped 是本次转换实际丢掉的东西（DropXxx 常量），没丢就是空。调用方
// 拿它打日志——见 DroppedOnEncode 一节的理由。
func (c *Codec) EncodeRequest(req *protocol.Request, stream bool) ([]byte, error) {
	body, _, err := c.encodeRequest(req, stream)
	return body, err
}

// EncodeRequestReport 与 EncodeRequest 同源，另外交出丢弃清单。
//
// 不把清单塞进 Codec 接口：那会让六条转换路径的每个 encode 侧都背一个只有一半
// 实现用得上的返回值。走一个包级方法，relay 在转换分支上按需类型断言。
func (c *Codec) EncodeRequestReport(req *protocol.Request, stream bool) ([]byte, []string, error) {
	return c.encodeRequest(req, stream)
}

func (c *Codec) encodeRequest(req *protocol.Request, stream bool) ([]byte, []string, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("openaicc: canonical 请求为空")
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

	msgs, err := encodeMessages(req, drop)
	if err != nil {
		return nil, nil, err
	}
	out["messages"] = msgs

	tools, declared := encodeTools(req.Tools, drop)
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if choice, ok := encodeToolChoice(req.ToolChoice, declared); ok {
		out["tool_choice"] = choice
	}

	if req.MaxTokens > 0 {
		out["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if len(req.Stop) > 0 {
		out["stop"] = req.Stop
	}
	if stream {
		out["stream"] = true
		// 强制注入（§5 坑清单）：不带它上游流末就没有 usage 帧，call_logs 的
		// token 数会恒为零。客户端本来发的是 Anthropic 请求，不可能自己带上。
		out["stream_options"] = map[string]any{"include_usage": true}
	}

	// 思考档位原样直传（口径层 v0.65）：CC 侧就是顶层 reasoning_effort 一个字符串。
	// 不校验域、域外值照发，客户端没发就不加（理由同 anthropic 出口那处注释）。
	if req.Effort != "" {
		out["reasoning_effort"] = req.Effort
	}

	// 入口协议独有的顶层字段一律不带过去。它们对 CC 上游没有意义，而严格中转会
	// 因为一个不认识的顶层键整体拒收——metadata / thinking / context_management /
	// output_config 都在此列。丢在明处：登记进 dropped，由 relay 打警告，且**按档
	// 分类**（思考参数单列一档，表在 protocol.IsThinkingParamKey）。
	if _, ok := req.Extras["metadata"]; ok {
		drop(DropMetadata)
	}
	for k := range req.Extras {
		switch {
		case k == "metadata":
			// 上面已单独登记成 DropMetadata。不跳过的话它会再落进 else 记一次
			// DropVendorRequest，日志就报了个客户端根本没发的幻影未知字段（#15）。
		case protocol.IsThinkingParamKey(k):
			drop(DropThinkingParam)
		default:
			drop(DropVendorRequest)
		}
	}

	body, err := marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return body, dropped, nil
}

// encodeMessages 把 canonical 消息序列铺成 CC 的 messages。
//
// 形态差异集中在三处：
//   - System 块序列 → 一条前置的 role=system 消息（拼纯文本）
//   - tool_result 块 → 独立的 role=tool 消息，且必须紧跟在带 tool_calls 的
//     assistant 之后。Anthropic 把它们放在 user 消息里，位置天然是对的，按序展开
//     即可，不需要重排
//   - thinking 块 → 丢弃。跨协议不做伪映射（口径层 §2.6）
func encodeMessages(req *protocol.Request, drop func(string)) ([]map[string]any, error) {
	msgs := make([]map[string]any, 0, len(req.Messages)+1)

	if content, ok := encodeMessageContent(req.System, drop); ok {
		msgs = append(msgs, map[string]any{"role": "system", "content": content})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case protocol.RoleAssistant:
			msg, err := encodeAssistant(m, drop)
			if err != nil {
				return nil, err
			}
			if msg != nil {
				msgs = append(msgs, msg)
			}
		default:
			msgs = append(msgs, encodeNonAssistant(m, drop)...)
		}
	}
	return msgs, nil
}

// encodeAssistant 编一条 assistant 消息：正文拼纯文本，tool_use 变 tool_calls。
//
// content 为空而 tool_calls 非空是合法形态（纯工具调用轮），此时 content 整键
// 省略——部分严格上游对 content:null 与空字符串的接受度不一，省略是三家都认的。
func encodeAssistant(m protocol.Message, drop func(string)) (map[string]any, error) {
	msg := map[string]any{"role": "assistant"}
	var calls []map[string]any

	for _, b := range m.Content {
		if b.Kind == protocol.BlockToolUse && b.ToolCall != nil {
			call, err := encodeToolCall(b.ToolCall)
			if err != nil {
				return nil, err
			}
			calls = append(calls, call)
		}
	}
	if content, ok := encodeMessageContent(m.Content, drop); ok {
		msg["content"] = content
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	if _, ok := msg["content"]; !ok && len(calls) == 0 {
		// 整条消息只剩被丢掉的块（例如纯 thinking 的 assistant 轮）。发一条既没
		// content 也没 tool_calls 的 assistant 消息，严格上游会拒；整条略过。
		return nil, nil
	}
	return msg, nil
}

// encodeNonAssistant 编 user / system / tool 角色的消息。
//
// 一条 canonical 消息可能展开成多条 CC 消息：Anthropic 把若干 tool_result 与正文
// 塞在同一条 user 消息里，CC 那边每个工具结果都得是独立的 role=tool 消息。
func encodeNonAssistant(m protocol.Message, drop func(string)) []map[string]any {
	var out []map[string]any
	// 抬出来的图**攒到所有 tool 消息发完再发**，不是每个结果发一条。Anthropic 的并行
	// 工具轮把多个 tool_result 挤在同一条 user 消息里（in-anthropic-parallel-* 实采
	// 可证），逐个抬会把 user 夹进两条 tool 中间——而 CC 那边 role=tool 必须紧跟着带
	// tool_calls 的 assistant，中间插一条 user 就是一个必被上游拒的请求。
	var lifted []map[string]any
	for _, b := range m.Content {
		if b.Kind != protocol.BlockToolResult || b.ToolResult == nil {
			continue
		}
		if _, ok := b.Extras["cache_control"]; ok {
			drop(DropCacheControl)
		}
		// tool_call_id 原样携带（§5 坑清单）：Anthropic 的 tool_use_id 与 CC 的
		// tool_call_id 是同一个标识，重编号会让结果对不回调用。
		out = append(out, map[string]any{
			"role":         "tool",
			"tool_call_id": b.ToolResult.ToolCallID,
			"content":      joinBlocks(b.ToolResult.Content, drop),
		})
		lifted = append(lifted, liftImageParts(b.ToolResult.Content, drop)...)
	}
	if len(lifted) > 0 {
		out = append(out, map[string]any{"role": "user", "content": lifted})
	}

	role := string(m.Role)
	switch m.Role {
	case protocol.RoleSystem:
		// mid-conversation-system：CC 允许 role=system 出现在消息中段，位置原样保留。
		role = "system"
	case protocol.RoleTool:
		// canonical 里的 RoleTool 只会来自 CC 入口，A 入口到不了这里；真到了就说明
		// 上游侧已经是 CC 形态，原样发。
		role = "tool"
	default:
		role = "user"
	}
	if content, ok := encodeMessageContent(m.Content, drop); ok {
		out = append(out, map[string]any{"role": role, "content": content})
	}
	return out
}

func encodeToolCall(call *protocol.ToolCall) (map[string]any, error) {
	// CC 契约要求 arguments 是 JSON 字符串，而 canonical 的 Args 不保证是——包装与
	// 拆包、以及告诉模型该回什么形状的那份声明，三者住在 protocol/customtool.go。
	args := call.Args
	switch {
	case args == "":
		args = "{}"
	case !call.ArgsIsJSON:
		wrapped, err := protocol.WrapCustomToolArgs(call.Args)
		if err != nil {
			return nil, fmt.Errorf("openaicc: %w", err)
		}
		args = wrapped
	}
	return map[string]any{
		"id":   call.ID,
		"type": "function",
		"function": map[string]any{
			"name":      call.Name,
			"arguments": args,
		},
	}, nil
}

// encodeTools 编工具声明，返回声明出去的工具名集合供 tool_choice 校验用。
//
// 服务端工具（Anthropic 的 advisor_20260301 一类）直接丢：它是**上游侧**能力，
// 第三方 CC 上游既不认这个 type 也变不出这个能力，带过去只会被拒。
func encodeTools(tools []protocol.Tool, drop func(string)) ([]map[string]any, map[string]bool) {
	out := make([]map[string]any, 0, len(tools))
	declared := map[string]bool{}
	for _, t := range tools {
		if t.Kind == protocol.ToolServer {
			drop(DropServerTool)
			continue
		}
		fn := map[string]any{"name": t.Name}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		switch {
		case len(t.Schema) > 0:
			fn["parameters"] = json.RawMessage(t.Schema)
		case t.Kind == protocol.ToolCustom:
			// custom 工具没有 parameters（Responses 用 format 里的 lark 文法描述
			// 入参），照抄就是抄了个空。必须替它合成一份声明，否则**没有任何东西
			// 告诉模型该回 {"input": …}**，回程拆包就拆了个寂寞。文法约束本身带不
			// 过去，由 joinTools 之外的 drop 登记（CC 没有对应能力）。
			fn["parameters"] = protocol.CustomToolSchema()
			drop(DropToolGrammar)
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
		declared[t.Name] = true
	}
	return out, declared
}

// encodeToolChoice 把 canonical 的选择策略编成 CC 形态，并挡掉两种会被上游拒的组合
// （§5 坑清单）：没有 tools 却带 tool_choice、tool_choice 指名一个没声明的工具。
func encodeToolChoice(choice protocol.ToolChoice, declared map[string]bool) (any, bool) {
	if choice.Mode == "" || len(declared) == 0 {
		return nil, false
	}
	switch choice.Mode {
	case "auto", "none", "required":
		return choice.Mode, true
	case "tool":
		if !declared[choice.Name] {
			return nil, false
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": choice.Name},
		}, true
	}
	return nil, false
}

// encodeMessageContent 编一条消息的 content。
//
// 带可转发的图时发 part 数组；纯文本仍发字符串——第三方 CC 上游拒收纯文本的数组形态。
func encodeMessageContent(blocks []protocol.Block, drop func(string)) (any, bool) {
	if !protocol.HasConvertibleImage(blocks) {
		text := joinBlocks(blocks, drop)
		if text == "" {
			return nil, false
		}
		return text, true
	}
	var parts []map[string]any
	for _, b := range blocks {
		if _, ok := b.Extras["cache_control"]; ok {
			drop(DropCacheControl)
		}
		switch b.Kind {
		case protocol.BlockText:
			if b.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": b.Text})
			}
		case protocol.BlockThinking:
			drop(DropThinking)
		case protocol.BlockToolUse, protocol.BlockToolResult:
			// 由调用方另发。
		case protocol.BlockImage:
			if part, ok := encodeImagePart(b.Image, drop); ok {
				parts = append(parts, part)
			}
		default:
			drop(DropVendorContent)
		}
	}
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

func encodeImagePart(img *protocol.Image, drop func(string)) (map[string]any, bool) {
	var url string
	switch img.Carrier() {
	case protocol.CarrierData:
		url = protocol.FormatDataURI(img.MediaType, img.Data)
	case protocol.CarrierURL:
		url = img.URL
	case protocol.CarrierFile:
		drop(DropImageFileID)
		return nil, false
	default:
		return nil, false
	}
	obj := map[string]any{"url": url}
	// detail 在 CC 上归 image_url 对象**里面**（Responses 那边在 part 顶层）。原样
	// 转发、含 "auto"，不设白名单也不把 auto 当「等于没指定」抹掉——口径层 v0.78 ②。
	// 空值不发这一格：「没指定」与「指定了 auto」在上游那边不是一回事。
	if img.Detail != "" {
		obj["detail"] = img.Detail
	}
	return map[string]any{
		"type":      "image_url",
		"image_url": obj,
	}, true
}

// liftImageParts 挑出 tool_result 里的图，编成 part 数组交给调用方。
//
// CC 的 role=tool content 只能是字符串，图放不进去，只能抬到一条 user 消息里；但
// 那条消息由调用方在**所有** tool 消息发完之后统一发一条，所以这里只出 part，不出
// 消息——理由见 encodeNonAssistant 里那段注释。
func liftImageParts(blocks []protocol.Block, drop func(string)) []map[string]any {
	var parts []map[string]any
	for _, b := range blocks {
		if b.Kind != protocol.BlockImage {
			continue
		}
		part, ok := encodeImagePart(b.Image, drop)
		if !ok {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

// joinBlocks 把块序列拼成一段纯文本。
//
// 严格中转要的就是纯文本（§5 坑清单：content 为数组会被第三方上游拒）。多块之间
// 用换行连接——原本它们在 Anthropic 侧是并列的独立块，不加分隔会把两段正文粘成
// 一个词。
//
// 顺带登记丢弃：thinking 块整块丢（含 signature），cache_control 断点丢，其余认不得
// 的块类型走 default 登记 vendor_content。三者都是「装得下但转不过去」，登记在案由
// relay 打警告。
func joinBlocks(blocks []protocol.Block, drop func(string)) string {
	var parts []string
	for _, b := range blocks {
		if _, ok := b.Extras["cache_control"]; ok {
			drop(DropCacheControl)
		}
		switch b.Kind {
		case protocol.BlockText:
			// 空串不算丢内容：那是「这块没话说」，不是「这块被我扔了」。
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case protocol.BlockThinking:
			drop(DropThinking)
		case protocol.BlockToolUse, protocol.BlockToolResult:
			// 这两种块不在这里落地，也不是在这里丢的：tool_use 由调用方编成
			// assistant 的 tool_calls，tool_result 由调用方编成独立的 role=tool
			// 消息。拼纯文本时跳过它们是分工，不是丢弃，所以不登记。

		case protocol.BlockImage:
			if b.Image.Carrier() == protocol.CarrierFile {
				drop(DropImageFileID)
			}
			// 图由 encodeMessageContent / liftImages 另发；这里只拼正文。

		default:
			// 认不得的块类型：跳过，**并且登记**。canonical 的 BlockKind 是字符串，
			// 装得下没见过的形态（input_audio 就是这么留住的），但 CC 的 content
			// 这里已经被压成纯文本，这些块无处安放。
			drop(DropVendorContent)
		}
	}
	return strings.Join(parts, "\n")
}

// marshal 关掉 HTML 转义。
//
// encoding/json 默认把 < > & 编成 < 一类，工具描述与系统提示里这些字符很常见，
// 转义后虽然语义不变，但样本比对与人工排障时满屏 < 没法看。
func marshal(v any) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("openaicc: 编码请求体: %w", err)
	}
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}
