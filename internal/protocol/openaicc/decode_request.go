package openaicc

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Chat Completions 的**入口**解码：入站请求字节 → canonical。
//
// 定型依据是 testdata/golden/in-cc-* 六份 opencode 1.18 真实发包，不是协议文档。
// 与 anthropic/decode.go 同一条纪律：DecodeRequest 必须是全函数，入口协议的任何
// 合法字节都要有地方放；跨协议丢什么是 encode 侧的决策，不是这里拒收的理由。
//
// CC 相对另外两个入口的形态差异集中在两处，都在下面各自的函数里写了：
//   - 工具结果是**每个调用一条独立的 role=tool 消息**（Anthropic 是全部挤进同一条
//     user 消息）。这里不做归一，原样解成 RoleTool 消息 + 一个 tool_result 块，
//     合并交给 Anthropic 出口的相邻同角色合并（anthropic/encode_request.go）。
//   - stream_options.include_usage 是 CC 独有的开关。它进 Extras，且**必须留住**：
//     回程的 EncodeStream 靠它决定补不补 usage 帧（见 codec.go 的 includeUsage）。

// topLevelKnown 是 canonical Request 有专属字段的顶层键。不在表里的一律进
// Request.Extras——stream_options / top_p / n / presence_penalty 这些都靠这条
// 通用规则接住，而不是逐个列举。
var topLevelKnown = map[string]bool{
	"model": true, "stream": true,
	"max_tokens": true, "max_completion_tokens": true,
	"messages": true, "tools": true, "tool_choice": true,
	"temperature": true, "stop": true,
}

func (c *Codec) DecodeRequest(body []byte, stream bool) (*protocol.Request, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("openaicc: 请求体不是 JSON 对象: %w", err)
	}

	req := &protocol.Request{Stream: stream}

	if err := unmarshalIf(root, "model", &req.Model); err != nil {
		return nil, err
	}
	// max_tokens 是被 max_completion_tokens 取代的老名字，两者都要认：opencode 发的
	// 是老名字（六份样本皆然），新版 SDK 发的是新名字。谁在就用谁，都在以新名字为准。
	if err := unmarshalIf(root, "max_tokens", &req.MaxTokens); err != nil {
		return nil, err
	}
	if err := unmarshalIf(root, "max_completion_tokens", &req.MaxTokens); err != nil {
		return nil, err
	}
	if err := unmarshalIf(root, "temperature", &req.Temperature); err != nil {
		return nil, err
	}
	if raw, ok := root["stop"]; ok {
		stop, err := decodeStop(raw)
		if err != nil {
			return nil, err
		}
		req.Stop = stop
	}

	// stream 以调用方传进来的为准（relay 已经从请求头部解过一次），理由同
	// anthropic/decode.go：请求体里的 stream 与它不一致时，那是 relay 的判断。

	if raw, ok := root["messages"]; ok {
		msgs, err := decodeMessages(raw)
		if err != nil {
			return nil, err
		}
		req.Messages = msgs
	}
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

	req.Extras = collectExtras(root, topLevelKnown)
	c.includeUsage = wantsUsage(req.Extras)
	return req, nil
}

// wantsUsage 从 Extras 里读 stream_options.include_usage。
//
// 不给就是不要：CC 的默认行为就是流末不发 usage chunk，客户端据此决定要不要等那一帧。
// 我们凭空补一帧会让严格按 OpenAI SDK 写的客户端多解一个它没预期的结构。
func wantsUsage(extras map[string]any) bool {
	opts, ok := extras["stream_options"].(map[string]any)
	if !ok {
		return false
	}
	on, _ := opts["include_usage"].(bool)
	return on
}

// decodeStop 解 stop：CC 允许字符串或字符串数组，canonical 只有数组。
func decodeStop(raw json.RawMessage) ([]string, error) {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if one == "" {
			return nil, nil
		}
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("openaicc: stop 既不是字符串也不是数组: %w", err)
	}
	return many, nil
}

// messageKnown 是消息级有专属 canonical 归宿的键，其余进 Message.Extras。
var messageKnown = map[string]bool{
	"role": true, "content": true, "tool_calls": true, "tool_call_id": true,
}

// decodeMessages 解 messages 数组。
//
// role 不做校验：CC 侧 system / developer / user / assistant / tool 都合法，且
// role=system 允许出现在中段（encodeNonAssistant 原样保留它的位置）。把角色集合
// 钉死会当场拒收一个合法请求。**归一是另一回事**，见 normalizeRole。
func decodeMessages(raw json.RawMessage) ([]protocol.Message, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("openaicc: messages 不是数组: %w", err)
	}
	msgs := make([]protocol.Message, 0, len(items))
	for i, item := range items {
		msg, err := decodeMessage(item)
		if err != nil {
			return nil, fmt.Errorf("openaicc: messages[%d]: %w", i, err)
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// normalizeRole 把 CC 的 developer 折成 RoleSystem。
//
// canonical 没有 RoleDeveloper，这是已定口径（protocol/request.go 的 Role 注释，
// PO 确认）：R 入口早就这么归一，CC 入口没有理由不同。不归一的后果是实打实的——
// Anthropic 出口只把 RoleSystem 上提到顶层 system，其余非 assistant 一律当 user，
// 于是一条 developer 系统提示会降格成用户内容，还会跟紧随其后的 user 消息合并。
func normalizeRole(r protocol.Role) protocol.Role {
	if r == "developer" {
		return protocol.RoleSystem
	}
	return r
}

func decodeMessage(item map[string]json.RawMessage) (protocol.Message, error) {
	var msg protocol.Message
	if err := unmarshalIf(item, "role", &msg.Role); err != nil {
		return msg, err
	}
	msg.Role = normalizeRole(msg.Role)

	// role=tool：整条消息就是**一个**工具结果。tool_call_id 在消息级而不是块级，
	// 这正是 CC 与 Anthropic 的形态分歧所在——那边一条 user 消息里可以塞好几个
	// tool_result 块，各自带 tool_use_id。归一到块上，出口侧再按目标协议落位。
	if msg.Role == protocol.RoleTool {
		res := &protocol.ToolResult{}
		if err := unmarshalIf(item, "tool_call_id", &res.ToolCallID); err != nil {
			return msg, err
		}
		if raw, ok := item["content"]; ok {
			blocks, err := decodeBlocks(raw)
			if err != nil {
				return msg, err
			}
			res.Content = blocks
		}
		msg.Content = []protocol.Block{{Kind: protocol.BlockToolResult, ToolResult: res}}
		msg.Extras = collectExtras(item, messageKnown)
		return msg, nil
	}

	if raw, ok := item["content"]; ok {
		blocks, err := decodeBlocks(raw)
		if err != nil {
			return msg, err
		}
		msg.Content = blocks
	}
	// tool_calls 接在正文之后：CC 把「说了几句 + 调了几个工具」装成同一条 assistant
	// 消息的两个并列字段，canonical 是单一块序列，正文在前调用在后是唯一无损的铺法
	// （Anthropic 实采里也是这个次序）。
	if raw, ok := item["tool_calls"]; ok {
		calls, err := decodeToolCalls(raw)
		if err != nil {
			return msg, err
		}
		msg.Content = append(msg.Content, calls...)
	}
	msg.Extras = collectExtras(item, messageKnown)
	return msg, nil
}

// decodeBlocks 解一个 content 位置：可能是纯字符串，也可能是 part 数组。
//
// 纯字符串退化为单个 text 块（六份样本里 content 全是字符串）。null 解成无块——
// 纯工具调用轮的 assistant 消息带的就是 "content": ""（实采）或 null（SDK 常见）。
func decodeBlocks(raw json.RawMessage) ([]protocol.Block, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil, nil
		}
		return []protocol.Block{{Kind: protocol.BlockText, Text: s}}, nil
	}
	if string(raw) == "null" {
		return nil, nil
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("content 既不是字符串也不是数组: %w", err)
	}
	blocks := make([]protocol.Block, 0, len(items))
	for i, item := range items {
		b, err := decodePart(item)
		if err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
		}
		if omittedImage(b) {
			continue
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// decodePart 解一个 content part。
//
// text → BlockText；image_url → BlockImage（data URI 拆成 MediaType+Data，其余当
// URL）。空 data URI 返回一个没有 Image 的 BlockImage，由 decodeBlocks 丢掉。
// input_audio 等仍原样带着 type 进 Extras。
func decodePart(item map[string]json.RawMessage) (protocol.Block, error) {
	var kindStr string
	if err := unmarshalIf(item, "type", &kindStr); err != nil {
		return protocol.Block{}, err
	}
	if kindStr == "image_url" {
		img, omitted := parseCCImage(item)
		if omitted {
			return protocol.Block{Kind: protocol.BlockImage}, nil
		}
		if img != nil {
			return protocol.Block{
				Kind:   protocol.BlockImage,
				Image:  img,
				Extras: collectExtras(item, map[string]bool{"type": true, "image_url": true}),
			}, nil
		}
	}
	block := protocol.Block{Kind: protocol.BlockKind(kindStr)}
	known := map[string]bool{"type": true}
	if block.Kind == protocol.BlockText {
		if err := unmarshalIf(item, "text", &block.Text); err != nil {
			return block, err
		}
		known["text"] = true
	}
	block.Extras = collectExtras(item, known)
	return block, nil
}

// parseCCImage 解 image_url 对象。omitted=true 表示空载荷，整块跳过。
//
// detail 住在 image_url 对象**里面**——Responses 那边它是 image_url 的同级兄弟，在
// part 顶层，两边形状不对称，最容易写反的就是这一处。它必须在这里解出来：外层
// collectExtras 把整个 image_url 排除了，不解就连 Extras 都留不下（口径层 v0.78 ①）。
func parseCCImage(item map[string]json.RawMessage) (img *protocol.Image, omitted bool) {
	raw, ok := item["image_url"]
	if !ok {
		return nil, false
	}
	var obj struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	if mt, data, ok := protocol.ParseDataURI(obj.URL); ok {
		return &protocol.Image{MediaType: mt, Data: data, Detail: obj.Detail}, false
	}
	if strings.HasPrefix(obj.URL, "data:") {
		return nil, true
	}
	if strings.TrimSpace(obj.URL) == "" {
		return nil, true
	}
	return &protocol.Image{URL: obj.URL, Detail: obj.Detail}, false
}

func omittedImage(b protocol.Block) bool {
	return b.Kind == protocol.BlockImage && b.Image == nil
}

// decodeToolCalls 把 assistant 的 tool_calls 数组解成 tool_use 块序列。
func decodeToolCalls(raw json.RawMessage) ([]protocol.Block, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("tool_calls 不是数组: %w", err)
	}
	blocks := make([]protocol.Block, 0, len(items))
	for i, item := range items {
		call := &protocol.ToolCall{}
		if err := unmarshalIf(item, "id", &call.ID); err != nil {
			return nil, fmt.Errorf("tool_calls[%d]: %w", i, err)
		}
		if raw, ok := item["function"]; ok {
			var fn struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(raw, &fn); err != nil {
				return nil, fmt.Errorf("tool_calls[%d].function: %w", i, err)
			}
			call.Name = fn.Name
			// arguments 按 CC 契约就是 JSON 字符串，故 ArgsIsJSON 恒 true。它是
			// **字符串**不是对象，所以这里是解开一层引号，不是取原始字节——与
			// Anthropic 的 input（本身就是对象）走的路不同。
			call.Args, call.ArgsIsJSON = fn.Arguments, true
		}
		// type 恒 "function"，没有第二种取值，不进 Extras 也不丢信息。
		call.Extras = collectExtras(item, map[string]bool{
			"id": true, "type": true, "function": true, "index": true,
		})
		blocks = append(blocks, protocol.Block{Kind: protocol.BlockToolUse, ToolCall: call})
	}
	return blocks, nil
}

// decodeTools 解 tools 数组。
//
// CC 的工具声明只有 {"type":"function","function":{…}} 一种形态；type 是别的值
// （上游服务端工具，如某些中转自带的 web_search）按 ToolServer 收着，出口侧一律丢。
// CC 没有 custom 工具——那是 Responses 独有的。
func decodeTools(raw json.RawMessage) ([]protocol.Tool, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("openaicc: tools 不是数组: %w", err)
	}
	tools := make([]protocol.Tool, 0, len(items))
	for i, item := range items {
		var kind string
		if err := unmarshalIf(item, "type", &kind); err != nil {
			return nil, fmt.Errorf("openaicc: tools[%d]: %w", i, err)
		}
		tool := protocol.Tool{Kind: protocol.ToolFunction}
		if kind != "" && kind != "function" {
			tool.Kind = protocol.ToolServer
		}
		if raw, ok := item["function"]; ok {
			var fn map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fn); err != nil {
				return nil, fmt.Errorf("openaicc: tools[%d].function: %w", i, err)
			}
			if err := unmarshalIf(fn, "name", &tool.Name); err != nil {
				return nil, fmt.Errorf("openaicc: tools[%d]: %w", i, err)
			}
			if err := unmarshalIf(fn, "description", &tool.Description); err != nil {
				return nil, fmt.Errorf("openaicc: tools[%d]: %w", i, err)
			}
			// parameters 按原始字节存：键序与数值精度都可能影响上游行为，解开再
			// 合上是无谓的失真风险（同 anthropic 的 input_schema）。
			if schema, ok := fn["parameters"]; ok {
				tool.Schema = append(json.RawMessage(nil), schema...)
			}
			// function 下的其余键（strict 等）与工具级的其余键并到一处：Tool.Extras
			// 是平的，而这两层在 CC 之外的协议里本来就没有对应嵌套。
			tool.Extras = collectExtras(fn, map[string]bool{
				"name": true, "description": true, "parameters": true,
			})
		}
		for k, v := range collectExtras(item, map[string]bool{"type": true, "function": true}) {
			if tool.Extras == nil {
				tool.Extras = map[string]any{}
			}
			tool.Extras[k] = v
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// decodeToolChoice 解 tool_choice：CC 用字符串 "auto"/"none"/"required"，或
// {"type":"function","function":{"name":…}} 指名。
func decodeToolChoice(raw json.RawMessage) (protocol.ToolChoice, error) {
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		switch mode {
		case "auto", "none", "required":
			return protocol.ToolChoice{Mode: mode}, nil
		}
		// 认不得的字符串当没说：canonical 的 Mode 是白名单，塞个野值进去只会让
		// 出口侧原样发出一个上游不认的取值。
		return protocol.ToolChoice{}, nil
	}

	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return protocol.ToolChoice{}, fmt.Errorf("openaicc: tool_choice: %w", err)
	}
	if obj.Type == "function" && obj.Function.Name != "" {
		return protocol.ToolChoice{Mode: "tool", Name: obj.Function.Name}, nil
	}
	return protocol.ToolChoice{}, nil
}

// collectExtras 把 known 之外的键解成 any 存进 Extras。值走 any 而非
// json.RawMessage，理由同 anthropic/decode.go：消费方要把值重新塞进另一个 JSON。
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

// unmarshalIf 解 src[key] 到 dst，键不存在时什么都不做；存在但类型对不上仍然报错。
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
