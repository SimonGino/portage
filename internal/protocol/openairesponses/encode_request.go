package openairesponses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Responses 的**编码**侧：canonical → 出口请求体（#80，CC→R 与 A→R 的去程）。
//
// 与另外两个出口的形态差别，都源于 Responses 把消息、工具调用、工具结果**摊平成
// 一个有序 item 列表**（`input`），而不是分成 messages/tool_calls 两层：
//
//   - 系统提示不上提到顶层。五份真实 Responses 请求样本**都没有 instructions 字段**，
//     系统内容一律是 input 里的 `role:"developer"` 消息项。所以这里也不写
//     instructions——发一个上游没见客户端发过的顶层字段，严格中转会拒。
//   - assistant 的工具调用是**顶层 function_call 项**，不是挂在消息里的数组。
//   - 工具结果是顶层 function_call_output 项，output 是**纯字符串**（sub2api 两个
//     to_responses 转换器一致）。
//   - 工具声明是**扁平**的 `{"type":"function","name",…}`，没有 CC 那层 function 嵌套。
//
// 采样参数（temperature / top_p / stop）**一律不发**：sub2api 的两个 to_responses
// 转换器都有意省掉它们，理由是 gpt-5.x 推理模型收到就 400；`stop` 更是 Responses
// 请求里根本没有的参数（sub2api types.go 的 ResponsesRequest 无此字段）。不发不等于
// 没发生，登记进 dropped 由 relay 打警告。
//
// 本文件**不读** Codec 的 customTools / compaction 状态：那两份是入口侧
// DecodeRequest 填的、描述「本次客户端怎么声明工具」的知识，而走到这里的请求来自
// CC 或 Anthropic 客户端，它们没说过 Responses 的话。同理不合成 compaction item。

// DroppedOnEncode 列出 canonical → Responses 必然丢掉的东西，理由同另外两个 codec
// 的同名一节：codec 不持有 logger，谁丢的谁登记，由 relay 读这张表打日志。
const (
	DropMetadata      = "metadata"        // A 入口的 metadata.user_id 等
	DropCacheControl  = "cache_control"   // Anthropic 缓存断点，Responses 无对应概念
	DropThinking      = "thinking"        // thinking 块正文与 signature
	DropServerTool    = "server_tool"     // 入口协议声明的上游服务端工具
	DropVendorRequest = "vendor_request"  // 入口协议独有的顶层字段
	DropToolGrammar   = "tool_grammar"    // custom 工具的文法约束，跨不过去
	DropVendorContent = "vendor_content"  // Responses 认不得的内容块（多模态等）
	DropOrphanResult  = "orphan_result"   // 找不到对应工具调用的结果
	DropSampling      = "sampling_params" // temperature / top_p / stop，见文件头
	DropImageFileID   = "image_file_id"   // file_id 是上游作用域句柄，跨协议搬不走
	// DropThinkingParam 是**请求侧的思考参数**（口径层 v0.65 ⑤），与 DropThinking
	// （内容块）不是一档。effort 不在这一档——它现在原样直传成 reasoning.effort。
	DropThinkingParam = "thinking_param"
)

// EncodeRequest 把 canonical 编成 Responses 请求体。
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
		return nil, nil, fmt.Errorf("openairesponses: canonical 请求为空")
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
	out["input"] = encodeInput(req, drop)

	tools, declared := encodeOutTools(req.Tools, drop)
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if choice, ok := encodeOutToolChoice(req.ToolChoice, declared); ok {
		out["tool_choice"] = choice
	}

	if req.MaxTokens > 0 {
		// 只在客户端真的限了才发。Responses 的 max_output_tokens 可缺省，而
		// Anthropic 出口那种「零值补一个配置默认值」的做法在这里没有理由——那是
		// 因为 Anthropic 必填，这边不必填。
		out["max_output_tokens"] = req.MaxTokens
	}
	if stream {
		out["stream"] = true
	}
	// store:false 恒发：九份真实 Codex 请求都带它。不存留在上游侧是这个网关的默认
	// 立场，省略等于把留存与否交给上游的默认值。
	out["store"] = false

	if req.Temperature != nil || len(req.Stop) > 0 {
		drop(DropSampling)
	}

	// 思考档位原样直传（口径层 v0.65）：Responses 侧写 reasoning.effort。
	//
	// **只写 effort 这一个子键**，不顺手加 summary——「思考多少」与「展不展示」是正交
	// 两维（CLIProxyAPI 的 internal/thinking 也这么分），客户端只说了前者。代价记在
	// 口径层：CC→R / A→R 上上游可能因此不回摘要，那条路的推理仍然看不见。
	if req.Effort != "" {
		out["reasoning"] = map[string]any{"effort": req.Effort}
	}

	// 入口协议独有的顶层字段一律不带过去（Extras 永不外带，三个出口一致），
	// 且**按档分类**（思考参数单列一档，表在 protocol.IsThinkingParamKey）。
	if _, ok := req.Extras["metadata"]; ok {
		drop(DropMetadata)
	}
	for k := range req.Extras {
		if protocol.IsThinkingParamKey(k) {
			drop(DropThinkingParam)
		} else {
			drop(DropVendorRequest)
		}
	}

	body, err := marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return body, dropped, nil
}

// encodeInput 把 canonical 的 system + 消息序列摊成 Responses 的 input 项列表。
//
// 顺序规则（与 sub2api 的两个 to_responses 转换器一致）：
//   - req.System 的块先出，编成一条 developer 消息项；
//   - 消息按原序展开，中段的 RoleSystem 消息**原位**编成 developer 项（不上提——
//     Responses 的 input 是有序列表，本来就装得下它站在哪，没有 Anthropic 那种
//     「system 必须在顶层」的约束）；
//   - 一条消息里工具结果先出、正文后出；assistant 则正文先出、工具调用后出。
func encodeInput(req *protocol.Request, drop func(string)) []map[string]any {
	items := make([]map[string]any, 0, len(req.Messages)+1)

	if parts, ok := encodeOutUserParts(req.System, drop); ok {
		items = append(items, developerItem(parts))
	}

	// 先扫一遍真实出现过的工具调用 id：孤儿结果（引用一个本次请求里不存在的调用）
	// 会被上游拒，理由与 anthropic 出口那一处相同。
	seen := map[string]bool{}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Kind == protocol.BlockToolUse && b.ToolCall != nil {
				seen[b.ToolCall.ID] = true
			}
		}
	}

	for _, m := range req.Messages {
		if m.Role == protocol.RoleSystem {
			if parts, ok := encodeOutUserParts(m.Content, drop); ok {
				items = append(items, developerItem(parts))
			}
			continue
		}
		items = append(items, encodeOutMessage(m, seen, drop)...)
	}
	return items
}

// developerItem 编一条系统提示项。
//
// 角色用 developer 而不是 system：canonical 的 RoleSystem 在 Responses 侧的惯例写法
// 就是 developer（decode.go 的 normalizeRole 是这条映射的反向，两边对称）。
func developerItem(parts []map[string]any) map[string]any {
	return map[string]any{
		"type": "message", "role": "developer", "content": parts,
	}
}

// encodeOutMessage 编一条非 system 消息，可能摊成多个 item。
func encodeOutMessage(m protocol.Message, seen map[string]bool, drop func(string)) []map[string]any {
	var items []map[string]any

	// 工具结果先出：它回应的是上一轮的调用，站在本轮正文之前才符合时序。
	//
	// 抬出来的图**攒到所有结果项发完再发一条**，不是每个结果发一条：Anthropic 的并行
	// 工具轮把多个 tool_result 挤在同一条 user 消息里，逐个抬会把 user 项夹进两个
	// function_call_output 中间，调用与结果之间凭空多出一轮对话。
	var lifted []map[string]any
	for _, b := range m.Content {
		if b.Kind != protocol.BlockToolResult || b.ToolResult == nil {
			continue
		}
		if !seen[b.ToolResult.ToolCallID] {
			drop(DropOrphanResult)
			continue
		}
		items = append(items, encodeOutToolResult(b.ToolResult, drop))
		lifted = append(lifted, liftOutImageParts(b.ToolResult.Content, drop)...)
	}
	if len(lifted) > 0 {
		items = append(items, map[string]any{
			"type": "message", "role": "user", "content": lifted,
		})
	}

	if m.Role == protocol.RoleAssistant {
		if parts, ok := encodeOutAssistantParts(m.Content, drop); ok {
			// assistant 正文用 output_text——它描述的是**上游此前说过的话**，
			// 与 user 侧的 input_text 不是一个部件类型（decode.go 的 decodeContent
			// 两种都收，正是因为线上两种都真实出现）。图跟在同一条消息里，
			// 不经 joinOutBlocks，免得静默蒸发。
			items = append(items, map[string]any{
				"type": "message", "role": "assistant", "content": parts,
			})
		}
		for _, b := range m.Content {
			if b.Kind == protocol.BlockToolUse && b.ToolCall != nil {
				items = append(items, encodeOutToolCall(b.ToolCall))
			}
		}
		return items
	}

	if parts, ok := encodeOutUserParts(m.Content, drop); ok {
		// user 之外的角色（RoleTool、以及未知角色）一律当 user：Responses 的消息项
		// 只有 user/assistant/developer 三种落点，把一条内容整个丢掉比放错角色更糟。
		// RoleTool 的正文块正常情况下是空的（它的内容在 tool_result 块里，上面已经
		// 编成 function_call_output 了）。
		items = append(items, map[string]any{
			"type": "message", "role": "user", "content": parts,
		})
	}
	return items
}

// encodeOutToolCall 编一次工具调用。
//
// 一律编成 function_call，**不合成 custom_tool_call**：custom 形态是客户端在
// Responses 请求里声明出来的，而这条路上的客户端说的是 CC 或 Anthropic，它们没有
// custom 工具这个概念。凭空发一个 custom_tool_call 会与我们发出去的 function 工具
// 声明自相矛盾。
//
// arguments 按契约是 JSON 字符串；canonical 的 Args 不保证是，走对称包装
// （protocol/customtool.go）。
func encodeOutToolCall(call *protocol.ToolCall) map[string]any {
	args := call.Args
	switch {
	case args == "":
		args = "{}"
	case !call.ArgsIsJSON:
		args = wrapCustomArgs(call.Args)
	}
	return map[string]any{
		// call_id 而不是 item id：工具结果靠它对回调用，重编号就对不上了
		// （§5 坑清单，三协议一致）。canonical 里根本没存 item id。
		"type": "function_call", "call_id": call.ID,
		"name": call.Name, "arguments": args,
	}
}

// encodeOutToolResult 编一条工具结果。
//
// output 是**纯字符串**（sub2api 两个 to_responses 转换器一致），多块用换行拼。
// 空结果发 "(empty)" 而不是空串，同样照 sub2api：部分上游拒收空 output。
func encodeOutToolResult(res *protocol.ToolResult, drop func(string)) map[string]any {
	var parts []string
	for _, b := range res.Content {
		if _, ok := b.Extras["cache_control"]; ok {
			drop(DropCacheControl)
		}
		if b.Kind == protocol.BlockText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	output := strings.Join(parts, "\n")
	if output == "" {
		output = "(empty)"
	}
	return map[string]any{
		"type": "function_call_output", "call_id": res.ToolCallID, "output": output,
	}
}

// encodeOutTools 编工具声明，返回声明出去的工具名集合供 tool_choice 校验用。
//
// Responses 的工具是扁平形态：`{"type":"function","name":…,"parameters":…}`，没有
// CC 那层 function 嵌套（九份真实请求样本一致）。
func encodeOutTools(tools []protocol.Tool, drop func(string)) ([]map[string]any, map[string]bool) {
	out := make([]map[string]any, 0, len(tools))
	declared := map[string]bool{}
	for _, t := range tools {
		if t.Kind == protocol.ToolServer {
			// 服务端工具是**上游侧**能力，目标上游既不认这个 type 也变不出这个能力。
			drop(DropServerTool)
			continue
		}
		fn := map[string]any{"type": "function", "name": t.Name}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		switch {
		case len(t.Schema) > 0:
			fn["parameters"] = json.RawMessage(t.Schema)
		case t.Kind == protocol.ToolCustom:
			// canonical 里出现 custom 工具，只可能是 R 入口解出来的；但那条路
			// （R→R）根本不进 codec，所以这一支实际到不了。留着是为了让「没有
			// Schema 的工具也发得出去」这件事有定义——合成一份 {"input": string}
			// 声明（protocol.CustomToolSchema），而不是发一个没有 parameters 的
			// 工具让模型自由发挥。文法约束跨不过去，登记。
			fn["parameters"] = protocol.CustomToolSchema()
			drop(DropToolGrammar)
		}
		out = append(out, fn)
		declared[t.Name] = true
	}
	return out, declared
}

// encodeOutToolChoice 把 canonical 的选择策略编成 Responses 形态，并挡掉两种会被
// 上游拒的组合：没有 tools 却带 tool_choice、tool_choice 指名一个没声明的工具。
func encodeOutToolChoice(choice protocol.ToolChoice, declared map[string]bool) (any, bool) {
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
		// 指名形态是扁平的 {"type":"function","name":X}，与工具声明同构，
		// 没有 CC 那层 function 嵌套（sub2api anthropic_to_responses.go）。
		return map[string]any{"type": "function", "name": choice.Name}, true
	}
	return nil, false
}

// encodeOutUserParts 编 user / developer 消息的 content 数组（正文部件是 input_text）。
func encodeOutUserParts(blocks []protocol.Block, drop func(string)) ([]map[string]any, bool) {
	return encodeOutParts(blocks, "input_text", drop)
}

// encodeOutAssistantParts 编 assistant 消息的 content 数组（正文部件是 output_text）。
//
// 正文部件类型是这两个半边**唯一**的差别，所以走同一个 encodeOutParts：output_text
// 描述的是上游此前说过的话，input_text 是客户端说的话，Responses 把这个区别编在部件
// 类型上而不是角色上（decode.go 的 decodeContent 两种都收，正因为线上两种都真实出现）。
func encodeOutAssistantParts(blocks []protocol.Block, drop func(string)) ([]map[string]any, bool) {
	return encodeOutParts(blocks, "output_text", drop)
}

// encodeOutParts 编一条消息的 content 数组，textType 决定正文部件叫什么。
//
// 没有图时仍走 joinOutBlocks 拼成**一个**正文部件：多块之间的分隔与丢弃登记都在那里，
// 图片这一路不该顺手改掉无图消息的形状。
func encodeOutParts(blocks []protocol.Block, textType string, drop func(string)) ([]map[string]any, bool) {
	if !protocol.HasConvertibleImage(blocks) {
		text := joinOutBlocks(blocks, drop)
		if text == "" {
			return nil, false
		}
		return []map[string]any{{"type": textType, "text": text}}, true
	}
	var parts []map[string]any
	for _, b := range blocks {
		if _, ok := b.Extras["cache_control"]; ok {
			drop(DropCacheControl)
		}
		switch b.Kind {
		case protocol.BlockText:
			if b.Text != "" {
				parts = append(parts, map[string]any{"type": textType, "text": b.Text})
			}
		case protocol.BlockThinking:
			drop(DropThinking)
		case protocol.BlockToolUse, protocol.BlockToolResult:
			// 由调用方另编成顶层 item。
		case protocol.BlockImage:
			if part, ok := encodeOutImage(b.Image, drop); ok {
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

func encodeOutImage(img *protocol.Image, drop func(string)) (map[string]any, bool) {
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
	part := map[string]any{"type": "input_image", "image_url": url}
	// detail 在 Responses 上与 image_url **平级**，在 part 顶层（CC 那边在 image_url
	// 对象里面）。原样转发、含 "auto"，不设白名单也不把 auto 当「等于没指定」抹掉
	// ——口径层 v0.78 ②。空值不发这一格：「没指定」与「指定了 auto」不是一回事。
	if img.Detail != "" {
		part["detail"] = img.Detail
	}
	return part, true
}

// liftOutImageParts 挑出 tool_result 里的图，编成 part 数组交给调用方。
//
// function_call_output.output 只收字符串，图放不进去，只能抬到一条 user 项里；但那
// 条项由调用方在**所有**结果项发完之后统一发一条，所以这里只出 part，不出项——理由
// 见 encodeOutMessage 里那段注释。
func liftOutImageParts(blocks []protocol.Block, drop func(string)) []map[string]any {
	var parts []map[string]any
	for _, b := range blocks {
		if b.Kind != protocol.BlockImage {
			continue
		}
		part, ok := encodeOutImage(b.Image, drop)
		if !ok {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

// joinOutBlocks 把块序列拼成一段纯文本，顺带登记丢弃。
//
// 拼纯文本而不是保留多个 content part：canonical 的块序列来自另外两个协议，那边的
// 多块是并列正文，Responses 一个 input_text 部件装得下，拆成多个部件只是徒增形态
// 差异。多块之间用换行连接，避免把两段正文粘成一个词。
func joinOutBlocks(blocks []protocol.Block, drop func(string)) string {
	var parts []string
	for _, b := range blocks {
		if _, ok := b.Extras["cache_control"]; ok {
			drop(DropCacheControl)
		}
		switch b.Kind {
		case protocol.BlockText:
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case protocol.BlockThinking:
			// 回带方向的思考一律丢 + 登记（口径层 v0.62 ③）。三个出口在这一格
			// 现在一模一样——anthropic 那处此前不登记的理由（R→A 来的 reasoning
			// 块正文恒空、丢个空块不值得报警）在出口开始合成之后就不成立了，已随本批
			// 补齐，所以这里不再有「与那边不同」这回事。
			//
			// 丢的是**内容**：Anthropic 解码侧产的是有正文的 thinking 块，客户端下一轮
			// 会带着它回来。不改成合成 reasoning 项——那需要 encrypted_content，是上游
			// 侧密文，我们既造不出也不该复用（出向合成那一侧同样不写它）。
			drop(DropThinking)
		case protocol.BlockToolUse, protocol.BlockToolResult:
			// 这两种块由调用方编成独立的 item，跳过是分工不是丢弃，不登记。

		case protocol.BlockImage:
			if b.Image.Carrier() == protocol.CarrierFile {
				drop(DropImageFileID)
			}
			// 图由 encodeOutUserParts / liftOutImages 另发。

		default:
			// 认不得的块类型：跳过并登记。不登记就是静默改写语义。
			drop(DropVendorContent)
		}
	}
	return strings.Join(parts, "\n")
}
