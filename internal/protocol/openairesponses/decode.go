package openairesponses

import (
	"encoding/json"
	"fmt"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 OpenAI Responses 的**解码**侧：入站请求字节 → canonical。
//
// 定型依据是 testdata/golden/in-responses-* 四份 Codex CLI 0.144 真实发包，逐字段
// 的归宿表在 internal/protocol/canonical_coverage_test.go（那张表和这份实现必须
// 同时改，否则 coverage 测试当场红）。
//
// DecodeRequest 必须是全函数（protocol/codec.go 的接口约束）：合法的 Responses
// 字节不许解不动。样本没覆盖到的形态——顶层 tools、instructions、字符串形态的
// input、function_call 系列 item——照协议实现，不因为「没样本」就留个坑等运行时。

// topLevelKnown 是 canonical Request 有专属字段的顶层键。
//
// 不在这张表里的顶层键一律进 Request.Extras：reasoning / store / include /
// prompt_cache_key / client_metadata / text / parallel_tool_calls 都靠这条通用规则
// 接住，而不是逐个列举。Codex 每升一版就可能多发一个字段，白名单式解析会把它们
// 静默吃掉。
var topLevelKnown = map[string]bool{
	"model": true, "stream": true, "max_output_tokens": true,
	"instructions": true, "input": true, "tools": true, "tool_choice": true,
	"temperature": true,
	// previous_response_id 列在这里是为了**不让它进 Extras**，见下面的丢弃理由。
	"previous_response_id": true,
}

func (c *Codec) DecodeRequest(body []byte, stream bool) (*protocol.Request, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("openairesponses: 请求体不是 JSON 对象: %w", err)
	}

	req := &protocol.Request{Stream: stream}

	if err := unmarshalIf(root, "model", &req.Model); err != nil {
		return nil, err
	}
	if err := unmarshalIf(root, "max_output_tokens", &req.MaxTokens); err != nil {
		return nil, err
	}
	if err := unmarshalIf(root, "temperature", &req.Temperature); err != nil {
		return nil, err
	}

	// instructions 是 Responses 的系统提示位。Codex 不用它（系统提示走 developer
	// 消息，四份样本一致），但它是协议正字段，落到 System 上语义对得上。
	var instructions string
	if err := unmarshalIf(root, "instructions", &instructions); err != nil {
		return nil, err
	}
	if instructions != "" {
		req.System = []protocol.Block{{Kind: protocol.BlockText, Text: instructions}}
	}

	// previous_response_id 直接丢，不进 Extras（口径层 §5 待澄清 #2 已决：v1 只支持
	// 无状态用法，失配时删掉它改用完整 input 重建上下文，同 sub2api 的
	// RemovePreviousResponseIDFromBody）。进 Extras 反而危险——那等于把一个**上一个
	// 上游**才认得的句柄留在 canonical 里，任何编码侧一旦顺手带出去，上游要么报
	// 找不到、要么接到别人的会话上。丢在这里，丢得干净。

	if raw, ok := root["tools"]; ok {
		tools, err := decodeTools(raw)
		if err != nil {
			return nil, err
		}
		req.Tools = tools
	}
	if raw, ok := root["tool_choice"]; ok {
		choice, err := decodeToolChoice(raw)
		if err != nil {
			return nil, err
		}
		req.ToolChoice = choice
	}
	c.compaction, c.compactionDrops = false, nil
	if raw, ok := root["input"]; ok {
		if err := c.decodeInput(raw, req); err != nil {
			return nil, err
		}
	}

	req.Extras = collectExtras(root, topLevelKnown)

	if c.compaction {
		rewriteAsSummarizer(req)
	}

	// 记下这次请求里哪些工具是 custom 形态。编码响应时要靠它决定发
	// custom_tool_call 还是 function_call，并把 CC 侧合成的 JSON 包装对称拆回来
	// （见 encode.go）。这是 Decode 与 Encode 之间唯一的跨调用状态，也是 codec
	// 实例必须每请求一个的原因（internal/protocol/codecs/codecs.go）。
	c.customTools = nil
	for _, t := range req.Tools {
		if t.Kind == protocol.ToolCustom {
			if c.customTools == nil {
				c.customTools = map[string]bool{}
			}
			c.customTools[t.Name] = true
		}
	}
	return req, nil
}

// decodeInput 解 input 位，把 Responses 的扁平 item 序列还原成 canonical 的
// 消息序列。
//
// input 可以是纯字符串（协议允许 `"input": "hello"`），退化为一条 user 消息。
func (c *Codec) decodeInput(raw json.RawMessage, req *protocol.Request) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		req.Messages = append(req.Messages, protocol.Message{
			Role:    protocol.RoleUser,
			Content: []protocol.Block{{Kind: protocol.BlockText, Text: s}},
		})
		return nil
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("openairesponses: input 既不是字符串也不是数组: %w", err)
	}

	// Responses 把 assistant 一轮里的推理、工具调用摊成同级的兄弟 item，而
	// canonical（与 Anthropic、CC）都是「一条消息带若干块」。所以这里把**连续的**
	// 同侧 item 攒成一条消息，遇到换边才收口。
	//
	// 不按 item 一对一造消息，是因为下游 openaicc.EncodeRequest 认的是
	// 「assistant 消息里带 tool_use 块」；一个 tool_call 一条 assistant 消息会编出
	// 一串各带一个 tool_calls 的消息，严格上游会因为 assistant 连发而拒。
	var pending []protocol.Block
	var pendingRole protocol.Role
	flush := func() {
		if len(pending) == 0 {
			return
		}
		req.Messages = append(req.Messages, protocol.Message{Role: pendingRole, Content: pending})
		pending, pendingRole = nil, ""
	}
	add := func(role protocol.Role, blocks ...protocol.Block) {
		if pendingRole != role {
			flush()
			pendingRole = role
		}
		pending = append(pending, blocks...)
	}

	for i, item := range items {
		var kind string
		if err := unmarshalIf(item, "type", &kind); err != nil {
			return fmt.Errorf("openairesponses: input[%d]: %w", i, err)
		}
		switch kind {
		case "additional_tools":
			// additional_tools 原本是条 developer 消息，工具声明藏在它的 tools 里。
			// 提升到 Request.Tools 之后「它站在消息序列第几位」这个信息就丢了——
			// 无所谓：回编 Responses 时按首项重建语义等价，转 CC/Anthropic 时那边
			// 本来就没有对应容器。
			//
			// 不 flush：这个 item 不带内容，收口只会把它前后两条同侧 item 劈成两条
			// 消息，正是下面攒消息要避免的那件事。
			if raw, ok := item["tools"]; ok {
				tools, err := decodeTools(raw)
				if err != nil {
					return fmt.Errorf("openairesponses: input[%d]: %w", i, err)
				}
				req.Tools = append(req.Tools, tools...)
			}

		case "", "message":
			var role protocol.Role
			if err := unmarshalIf(item, "role", &role); err != nil {
				return fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			blocks, err := decodeContent(item["content"])
			if err != nil {
				return fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			add(normalizeRole(role), blocks...)

		case "reasoning":
			// 推理 item 没有明文正文可留：summary 实采恒为空数组，encrypted_content
			// 是**上游侧密文**，只有原上游解得开（§5 坑清单）。所以这里造一个空文本的
			// thinking 块，两样都进 Extras——不是为了转出去（跨协议必然作废），是为了
			// 「带 encrypted_content 的 input 不使转换报错」这条用例有东西可钉。
			add(protocol.RoleAssistant, protocol.Block{
				Kind:   protocol.BlockThinking,
				Extras: collectExtras(item, map[string]bool{"type": true, "id": true}),
			})

		case "custom_tool_call", "function_call":
			call, err := decodeToolCall(item, kind)
			if err != nil {
				return fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			add(protocol.RoleAssistant, protocol.Block{Kind: protocol.BlockToolUse, ToolCall: call})

		case "custom_tool_call_output", "function_call_output":
			result, err := decodeToolResult(item)
			if err != nil {
				return fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			// 工具结果挂 user：Responses 自己不给它角色，而 canonical 沿用 Anthropic
			// 的摆法（结果在 user 消息的块里）。CC 出口会再把它拆成 role=tool 消息。
			add(protocol.RoleUser, protocol.Block{Kind: protocol.BlockToolResult, ToolResult: result})

		case ItemCompactionTrigger:
			// 压缩 turn 的触发器。只置标志，item 本身不进消息序列——它在协议上不是内容，
			// 是一句「这一轮请你总结」。改写成 summarizer 的动作在 DecodeRequest 收尾时
			// 统一做（那时 tools 才收齐，additional_tools 也提升完了）。
			c.compaction = true

		case itemCompaction, itemCompactionSummary, itemContextCompaction:
			// G2 回带还原（#74）。Codex 把上一轮压缩产出的 item 原样放回 input 尾部，
			// 而上游（Anthropic / CC）没有任何位置装得下它——静默跳过的后果是整段历史
			// 凭空消失，表现成模型忽然失忆。
			var encrypted string
			if err := unmarshalIf(item, "encrypted_content", &encrypted); err != nil {
				return fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			if kind == itemContextCompaction && encrypted == "" {
				// codex-rs **本地**压缩留下的标记，可以不带密文。那种形态里没有摘要
				// 可还原，占位也没有意义（历史该有的内容就在同一份 input 的前面）。
				continue
			}
			text, restored := compactionItemText(encrypted)
			if !restored {
				// 登记而不是静默降级：解不开只有两种来路——先经透传渠道压缩成功后混路
				// 到转换渠道，或这段历史是别的网关压的。两种都值得在流水/日志里留一行，
				// 否则「模型好像忘了前半段」这类反馈永远查不到根。
				c.compactionDrops = append(c.compactionDrops, kind)
			}
			// 挂 user：摘要在 Codex 的原生流程里就是一条 user 消息（summaryPrefix 是它的
			// 引导语）。
			//
			// 不需要像 opencodex 那样在这里清悬挂的 pendingReasoning：那边 reasoning item
			// 要攒着等后面的工具调用来配对，压缩边界会让它错挂；这边 reasoning 在自己那一
			// 支就地变成块了，没有跨 item 的待配对状态可挂错。
			add(protocol.RoleUser, protocol.Block{Kind: protocol.BlockText, Text: text})

		default:
			// 认不得的 item 类型：跳过，不报错（decode 必须是全函数）。
			//
			// 不给它找个 Extras 角落存着，是因为存了也没用——同协议路径根本不进
			// codec（透传保真优先），所以一个未知 item 在这里的唯一去向就是被跨协议
			// 编码侧丢掉，存在哪儿都一样丢。留个假容器只会让人以为它还在。
			//
			// 也不 flush：跳过的东西不该在消息序列上留下疤。收口会把它前后两条同侧
			// item 劈成两条同 role 消息，而严格的 CC 上游正是拒这个。
			//
			// compaction_trigger 曾经落在这一支，静默跳过的后果不是「丢个字段」而是长
			// 会话砖死（见 compaction.go）；它和压缩产物 item 现在各有自己的分支。这条
			// 注释是留给下一个来读这一支的人的：跳过的代价要按 item 逐个想，不是所有
			// 未知 item 都只值一行日志。
		}
	}
	flush()
	return nil
}

// rewriteAsSummarizer 把一个压缩 turn 改写成纯总结请求（#74 范围 2）。
//
// 剥掉的三样东西各有各的必须：
//
//   - tools / tool_choice：留着上游多半会去调工具，而这一轮的产物必须是一段文本摘要。
//     一个工具调用回来，合成侧攒到的正文就是空的。
//   - text（Responses 的输出格式位，结构化输出与 verbosity 都在里面）：它会把回答按
//     schema 约束住，而我们要的是自由文本。
//
// 图片不用剥：canonical 目前没有承载图片数据的字段（protocol.BlockImage 是占位，#33），
// 到不了这里。
//
// 摘要指令追加成**最后一条 user 消息**，与 sub2api 的 Grok 支同一个摆法——上游看到的
// 就是「一段历史 + 一句请你总结」，不需要它认得任何压缩概念。
func rewriteAsSummarizer(req *protocol.Request) {
	req.Tools = nil
	req.ToolChoice = protocol.ToolChoice{}
	delete(req.Extras, "text")
	req.Messages = append(req.Messages, protocol.Message{
		Role:    protocol.RoleUser,
		Content: []protocol.Block{{Kind: protocol.BlockText, Text: compactPrompt}},
	})
}

// CompactionTurn 报告 DecodeRequest 刚解的那个请求是不是 Codex 压缩 turn。
//
// 编码侧靠它切到合成模式（encode.go），server 侧靠它决定要不要开静默期心跳与打日志。
// 与 customTools 同一个理由挂在实例上：这是解码侧才知道、编码侧必须知道的事，而
// Codec 接口的 EncodeStream 只看得见事件流。
func (c *Codec) CompactionTurn() bool { return c.compaction }

// CompactionDrops 列出解码时没能还原的回带压缩 item 的 type，供调用方打丢弃日志
// （口径层 §2.6：跨协议丢弃要有日志警告，不静默）。codec 是纯函数、不持有 logger。
func (c *Codec) CompactionDrops() []string { return c.compactionDrops }

// normalizeRole 把 Responses 的角色归一到 canonical。
//
// developer → RoleSystem 是不可逆且不需要可逆的（见 protocol.Role 注释）：R 入口
// 转出去时目标协议没有 developer，R 出口方向收到 RoleSystem 一律发 developer。
func normalizeRole(r protocol.Role) protocol.Role {
	if r == "developer" {
		return protocol.RoleSystem
	}
	if r == "" {
		return protocol.RoleUser
	}
	return r
}

// decodeContent 解 content / output 位的块数组。
//
// input_text / output_text / text 三个 type 都落成 BlockText：它们的区别是「谁写的」，
// 而那个信息已经在消息的 role 上了，块级再留一份没有消费方。
func decodeContent(raw json.RawMessage) ([]protocol.Block, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []protocol.Block{{Kind: protocol.BlockText, Text: s}}, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("content 既不是字符串也不是数组: %w", err)
	}
	blocks := make([]protocol.Block, 0, len(parts))
	for _, p := range parts {
		var text string
		if err := unmarshalIf(p, "text", &text); err != nil {
			return nil, err
		}
		blocks = append(blocks, protocol.Block{
			Kind:   protocol.BlockText,
			Text:   text,
			Extras: collectExtras(p, map[string]bool{"type": true, "text": true}),
		})
	}
	return blocks, nil
}

// decodeToolCall 解 custom_tool_call / function_call 两种调用 item。
//
// 差别只在入参：custom 的 input 是自由文本（Codex 的 exec 收 JavaScript 源码），
// function 的 arguments 是 JSON 字符串。ArgsIsJSON 记的就是这个区别，编码侧靠它
// 决定要不要合成包装对象（§5 坑清单）。
func decodeToolCall(item map[string]json.RawMessage, kind string) (*protocol.ToolCall, error) {
	call := &protocol.ToolCall{}
	if err := unmarshalIf(item, "call_id", &call.ID); err != nil {
		return nil, err
	}
	if err := unmarshalIf(item, "name", &call.Name); err != nil {
		return nil, err
	}
	argsKey := "input"
	if kind == "function_call" {
		argsKey, call.ArgsIsJSON = "arguments", true
	}
	if err := unmarshalIf(item, argsKey, &call.Args); err != nil {
		return nil, err
	}
	// id（`ctc_…` / `fc_…`）与 call_id 不是一回事：前者是 item 标识，后者才是工具
	// 结果回指的那个。只有 call_id 进 canonical，item id 随 status 一起进 Extras。
	call.Extras = collectExtras(item, map[string]bool{
		"type": true, "call_id": true, "name": true, argsKey: true,
	})
	return call, nil
}

func decodeToolResult(item map[string]json.RawMessage) (*protocol.ToolResult, error) {
	result := &protocol.ToolResult{}
	if err := unmarshalIf(item, "call_id", &result.ToolCallID); err != nil {
		return nil, err
	}
	blocks, err := decodeContent(item["output"])
	if err != nil {
		return nil, err
	}
	result.Content = blocks
	return result, nil
}

// decodeTools 解工具声明数组。
func decodeTools(raw json.RawMessage) ([]protocol.Tool, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("openairesponses: tools 不是数组: %w", err)
	}
	tools := make([]protocol.Tool, 0, len(items))
	for i, item := range items {
		var tool protocol.Tool
		var kind string
		if err := unmarshalIf(item, "type", &kind); err != nil {
			return nil, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
		}
		if err := unmarshalIf(item, "name", &tool.Name); err != nil {
			return nil, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
		}
		if err := unmarshalIf(item, "description", &tool.Description); err != nil {
			return nil, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
		}
		if raw, ok := item["parameters"]; ok {
			// 原样存字节：JSON Schema 的键序与数值精度都可能影响上游行为，解开再
			// 合上是白担的失真风险。
			tool.Schema = append(json.RawMessage(nil), raw...)
		}
		switch kind {
		case "custom":
			// custom 工具没有 schema，只有一份 lark 文法的 format，它进 Extras
			// （canonical 没有对应物，转 CC 时丢）。
			tool.Kind = protocol.ToolCustom
		case "", "function":
			tool.Kind = protocol.ToolFunction
		default:
			// web_search / file_search 等上游自带能力：网关变不出这个能力，
			// 只能原样带给认得它的上游，转 CC 时由编码侧丢弃。
			tool.Kind = protocol.ToolServer
		}
		tool.Extras = collectExtras(item, map[string]bool{
			"name": true, "description": true, "parameters": true,
		})
		tools = append(tools, tool)
	}
	return tools, nil
}

// decodeToolChoice 解 tool_choice：可能是字符串，也可能是 {type,name} 对象。
func decodeToolChoice(raw json.RawMessage) (protocol.ToolChoice, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return protocol.ToolChoice{Mode: s}, nil
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return protocol.ToolChoice{}, fmt.Errorf("openairesponses: tool_choice 解不动: %w", err)
	}
	// {"type":"function","name":"x"} / {"type":"custom","name":"x"} 都是「点名这个
	// 工具」，canonical 只记 Mode=tool + 名字，type 本身由 Tool.Kind 说了算。
	if obj.Name != "" {
		return protocol.ToolChoice{Mode: "tool", Name: obj.Name}, nil
	}
	return protocol.ToolChoice{Mode: obj.Type}, nil
}

// collectExtras / unmarshalIf 与 anthropic 包里的同名函数逐字相同。
//
// 现在是第二份。留着不抽公共包，是因为抽它要动 anthropic 那条**已经在跑**的
// A→CC 路径，而本次改动的主题是 R→CC；等 openaicc.DecodeRequest 落地要出第三份
// 时再提到 internal/protocol/jsonx，那时收益才盖得过改动面。

// collectExtras 把 known 之外的键解成 any 存进 Extras。
//
// 值走 any 而非 json.RawMessage：Extras 的消费方是 encode 侧，它要把值重新塞回
// 另一个 JSON 里，any 直接 Marshal 就行。
func collectExtras(src map[string]json.RawMessage, known map[string]bool) map[string]any {
	var extras map[string]any
	for k, raw := range src {
		if known[k] {
			continue
		}
		var v any
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		if extras == nil {
			extras = map[string]any{}
		}
		extras[k] = v
	}
	return extras
}

// unmarshalIf 解 src[key] 到 dst，键不存在时什么都不做。
//
// 键存在但类型对不上仍然报错：那是真的解不动，不是「没给」。
func unmarshalIf(src map[string]json.RawMessage, key string, dst any) error {
	raw, ok := src[key]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("字段 %s: %w", key, err)
	}
	return nil
}
