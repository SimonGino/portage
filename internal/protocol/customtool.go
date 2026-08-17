package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 本文件是 custom 工具在「只认 JSON 入参」的协议里的等价表示。三件事必须逐字对称，
// 所以它们必须住在一起：
//
//  1. 声明侧：CustomToolSchema —— 告诉上游「这个工具收一个字符串参数，键叫 input」。
//  2. 出站包装：WrapCustomToolArgs —— 把自由文本入参包成 {"input":"<原文>"}。
//  3. 回程拆包：UnwrapCustomToolArgs —— 把上游回来的包装对象拆回自由文本。
//
// 为什么要有这一套：canonical 的 ToolCall.Args 不保证是 JSON。Codex code-mode 的
// exec 收的是 **JavaScript 源码**（§5 坑清单，in-responses-tool-turn2 实测），而
// Chat Completions 的 function.arguments 与 Anthropic 的 tool_use.input 都要求
// JSON。于是跨过去只能包一层，回来再拆掉。
//
// 为什么必须住在一个包里：三处规则一旦漂移，症状是**工具结果对不回去，而且不报错**
// ——上游照 A 规则回话，网关按 B 规则拆，客户端拿到一段自己不认得的东西。散在三个
// 协议包里各写一份，迟早漂。（此前确实是散的：包装在 openaicc、拆包在
// openairesponses，声明**根本没写**——见 CustomToolSchema 的注释。）
//
// 同一套判定见 sub2api 的 extractCustomToolCallInput。

// CustomToolArgsKey 是包装对象里承载自由文本的键。
//
// 取 input 而非自造一个不易撞名的键（如 __raw）：它同时要出现在**发给上游的工具
// 声明**里，模型看得见。一个眼熟的名字比一个像内部实现泄漏的名字更容易让模型照着填。
const CustomToolArgsKey = "input"

// CustomToolSchema 是 custom 工具转到只认 JSON Schema 的协议时的等价声明。
//
// 这一步此前是**缺的**，#25 才补上：Responses 的 custom 工具用 format（lark 文法）
// 描述入参，没有 parameters；转 CC 时 openaicc 原样照抄——抄了个空，于是上游收到一个
// 不带 parameters 的 function 声明。后果不是报错而是更糟的东西：**没有任何东西告诉
// 模型该回 {"input": …}**，模型自由发挥回一个 {"cmd": …}，回程拆包拆不动只好原样
// 给出去，Codex 拿到一段 JSON 当 JS 跑，语法错。发出去的声明和回来的拆包必须是
// 同一套约定，这个函数就是那份约定的声明侧。
//
// 文法约束（lark）本身带不过去——CC 与 Anthropic 都没有对应能力，它留在 Tool.Extras
// 里，由编码侧登记为丢弃。
func CustomToolSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"` + CustomToolArgsKey +
		`":{"type":"string"}},"required":["` + CustomToolArgsKey + `"]}`)
}

// WrapCustomToolArgs 把自由文本入参包成合法 JSON 对象。
//
// 空入参给 {}：目标协议不接受空串，而一个空对象在两边都是「没参数」的合法说法。
func WrapCustomToolArgs(args string) (string, error) {
	if args == "" {
		return "{}", nil
	}
	wrapped, err := json.Marshal(map[string]string{CustomToolArgsKey: args})
	if err != nil {
		return "", fmt.Errorf("包装非 JSON 工具入参: %w", err)
	}
	return string(wrapped), nil
}

// UnwrapCustomToolArgs 把包装对象拆回自由文本。
//
// 拆不动就原样返回，而不是报错：上游未必真按我们发过去的形状回话——第三方中转会
// 重写 arguments，模型也可能自作主张换个结构。原样给出去，客户端至少还有得看。
func UnwrapCustomToolArgs(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &obj) != nil {
		// 根本不是 JSON 对象：上游把自由文本原样回来了，正是我们想要的形态。
		return trimmed
	}
	raw, ok := obj[CustomToolArgsKey]
	if !ok {
		return trimmed
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		// 有 input 键但不是字符串，说明这是个**真的**带 input 参数的 JSON 工具，
		// 不是我们包出来的。别拆。
		return trimmed
	}
	return s
}
