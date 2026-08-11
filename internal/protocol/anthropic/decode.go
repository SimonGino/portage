package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Anthropic Messages 的**解码**侧：入站请求字节 → canonical。
//
// 定型依据是 testdata/golden/in-anthropic-* 五份 Claude Code 2.x 真实发包，不是
// 协议文档。凡是样本里没出现过的形态，这里按「能装下就装、装不下也别报错」处理——
// DecodeRequest 必须是全函数（protocol/codec.go 的接口约束）：入口协议的任何合法
// 字节都要有地方放，跨协议丢什么是 encode 侧的决策，不是 decode 侧拒收的理由。

// topLevelKnown 是 canonical Request 有专属字段的顶层键。不在这张表里的顶层键
// 一律进 Request.Extras——metadata / thinking / context_management / output_config
// 都靠这条通用规则接住，而不是逐个列举：Claude Code 的 beta 头一变就会多出新字段，
// 白名单式解析会把它们静默吃掉。
var topLevelKnown = map[string]bool{
	"model": true, "stream": true, "max_tokens": true,
	"system": true, "messages": true, "tools": true, "tool_choice": true,
	"temperature": true, "stop_sequences": true,
}

func (c *Codec) DecodeRequest(body []byte, stream bool) (*protocol.Request, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("anthropic: 请求体不是 JSON 对象: %w", err)
	}

	req := &protocol.Request{Stream: stream}

	if err := unmarshalIf(root, "model", &req.Model); err != nil {
		return nil, err
	}
	if err := unmarshalIf(root, "max_tokens", &req.MaxTokens); err != nil {
		return nil, err
	}
	if err := unmarshalIf(root, "temperature", &req.Temperature); err != nil {
		return nil, err
	}
	if err := unmarshalIf(root, "stop_sequences", &req.Stop); err != nil {
		return nil, err
	}

	// stream 以调用方传进来的为准（relay 已经从请求头部解过一次），请求体里的
	// stream 只在两者不一致时才有意义，而那是 relay 的判断，不是这里的。

	if raw, ok := root["system"]; ok {
		blocks, err := decodeBlocks(raw)
		if err != nil {
			return nil, fmt.Errorf("anthropic: system: %w", err)
		}
		req.System = blocks
	}
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
	return req, nil
}

// decodeMessages 解 messages 数组。
//
// role 不做校验：mid-conversation-system beta 会在 messages 中段塞一条 role=system
// 的消息（in-anthropic-tool-turn2 实测），把角色集合钉死在 user/assistant 上的解析
// 会当场拒收一个合法请求。
func decodeMessages(raw json.RawMessage) ([]protocol.Message, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("anthropic: messages 不是数组: %w", err)
	}
	msgs := make([]protocol.Message, 0, len(items))
	for i, item := range items {
		var msg protocol.Message
		if err := unmarshalIf(item, "role", &msg.Role); err != nil {
			return nil, fmt.Errorf("anthropic: messages[%d]: %w", i, err)
		}
		if content, ok := item["content"]; ok {
			blocks, err := decodeBlocks(content)
			if err != nil {
				return nil, fmt.Errorf("anthropic: messages[%d]: %w", i, err)
			}
			msg.Content = blocks
		}
		msg.Extras = collectExtras(item, map[string]bool{"role": true, "content": true})
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// decodeBlocks 解一个 content 位置：可能是纯字符串，也可能是块数组。
//
// 纯字符串退化为单个 text 块（canonical 只有块序列一种形态）。这个退化是无损的：
// 编码侧遇到「单个 text 块」时可以还原成字符串，也可以照发数组，两者语义相同。
func decodeBlocks(raw json.RawMessage) ([]protocol.Block, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []protocol.Block{{Kind: protocol.BlockText, Text: s}}, nil
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("content 既不是字符串也不是数组: %w", err)
	}
	blocks := make([]protocol.Block, 0, len(items))
	for i, item := range items {
		b, err := decodeBlock(item)
		if err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// blockKnown 按块类型列出「有专属 canonical 字段」的键，其余进 Block.Extras。
var blockKnown = map[protocol.BlockKind]map[string]bool{
	protocol.BlockText:       {"type": true, "text": true},
	protocol.BlockThinking:   {"type": true, "thinking": true},
	protocol.BlockToolUse:    {"type": true, "id": true, "name": true, "input": true},
	protocol.BlockToolResult: {"type": true, "tool_use_id": true, "content": true, "is_error": true},
}

func decodeBlock(item map[string]json.RawMessage) (protocol.Block, error) {
	var kindStr string
	if err := unmarshalIf(item, "type", &kindStr); err != nil {
		return protocol.Block{}, err
	}
	// 未知块类型原样带着 type 走：Kind 是字符串类型，装得下没见过的形态。
	// 报错等于让一个新 beta 块类型把整条请求打死，而它多半只是我们不认识。
	kind := protocol.BlockKind(kindStr)
	block := protocol.Block{Kind: kind}

	switch kind {
	case protocol.BlockText:
		if err := unmarshalIf(item, "text", &block.Text); err != nil {
			return block, err
		}
	case protocol.BlockThinking:
		// signature 不进 Text，进 Extras：它不是给人看的正文，而是回带给**同一**
		// 上游的完整性凭据，跨协议必然作废（§5 坑清单）。
		if err := unmarshalIf(item, "thinking", &block.Text); err != nil {
			return block, err
		}
	case protocol.BlockToolUse:
		call := &protocol.ToolCall{}
		if err := unmarshalIf(item, "id", &call.ID); err != nil {
			return block, err
		}
		if err := unmarshalIf(item, "name", &call.Name); err != nil {
			return block, err
		}
		// input 按原始字节存：键序与数值精度都可能影响上游行为，解开再合上是无谓
		// 的失真风险（canonical 模型 Tool.Schema 同理）。Anthropic 的 input 恒为
		// JSON 对象，故 ArgsIsJSON 为 true——Codex custom 工具那种自由文本入参是
		// Responses 侧的事。
		if raw, ok := item["input"]; ok {
			call.Args, call.ArgsIsJSON = string(raw), true
		}
		block.ToolCall = call
	case protocol.BlockToolResult:
		res := &protocol.ToolResult{}
		if err := unmarshalIf(item, "tool_use_id", &res.ToolCallID); err != nil {
			return block, err
		}
		if err := unmarshalIf(item, "is_error", &res.IsError); err != nil {
			return block, err
		}
		if raw, ok := item["content"]; ok {
			inner, err := decodeBlocks(raw)
			if err != nil {
				return block, fmt.Errorf("tool_result: %w", err)
			}
			res.Content = inner
		}
		block.ToolResult = res
	}

	known := blockKnown[kind]
	if known == nil {
		known = map[string]bool{"type": true}
	}
	block.Extras = collectExtras(item, known)
	return block, nil
}

// decodeTools 解 tools 数组。
//
// 三态判别：带 type 的是上游服务端工具（实采里是 advisor_20260301，自带 model），
// 其余按 function。Anthropic 协议没有 custom 工具形态——那是 Responses 独有的。
func decodeTools(raw json.RawMessage) ([]protocol.Tool, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("anthropic: tools 不是数组: %w", err)
	}
	tools := make([]protocol.Tool, 0, len(items))
	for i, item := range items {
		tool := protocol.Tool{Kind: protocol.ToolFunction}
		if _, ok := item["type"]; ok {
			tool.Kind = protocol.ToolServer
		}
		if err := unmarshalIf(item, "name", &tool.Name); err != nil {
			return nil, fmt.Errorf("anthropic: tools[%d]: %w", i, err)
		}
		if err := unmarshalIf(item, "description", &tool.Description); err != nil {
			return nil, fmt.Errorf("anthropic: tools[%d]: %w", i, err)
		}
		if schema, ok := item["input_schema"]; ok {
			tool.Schema = append(json.RawMessage(nil), schema...)
		}
		tool.Extras = collectExtras(item, map[string]bool{
			"name": true, "description": true, "input_schema": true,
		})
		tools = append(tools, tool)
	}
	return tools, nil
}

// decodeToolChoice 解 tool_choice：Anthropic 用 {"type":"auto|any|tool|none","name":…}。
//
// any → required 是 canonical 的归一取值（口径见 protocol.ToolChoice.Mode 注释）。
func decodeToolChoice(raw json.RawMessage) (protocol.ToolChoice, error) {
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return protocol.ToolChoice{}, fmt.Errorf("anthropic: tool_choice: %w", err)
	}
	mode := obj.Type
	if mode == "any" {
		mode = "required"
	}
	return protocol.ToolChoice{Mode: mode, Name: obj.Name}, nil
}

// collectExtras 把 known 之外的键解成 any 存进 Extras。
//
// 值走 any 而非 json.RawMessage：Extras 的消费方是 encode 侧，它要把值重新塞回
// 另一个 JSON 里，any 直接 Marshal 就行；RawMessage 虽然更保真，但两边都得记得用
// json.RawMessage 包一层，漏一次就会编出被转义的字符串。
//
// 顶层 metadata 就是靠这条路留在 canonical 里的——它对 Anthropic 上游有实义
// （判定是否官方 Claude Code），编到 CC 出口时才丢，且丢在明处（见 encode 侧）。
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
