package protocol

import (
	"bytes"
	"encoding/json"
)

// DecodeToolArgs 把一处**声称是 JSON** 的工具入参原始字节解成 canonical 的 Args
// 字符串：CC 的 `tool_calls[].function.arguments` 与 Responses 的
// `function_call.arguments` 共用这一条读法。
//
// 契约形态是 JSON **字符串**，解开一层引号就是入参本身（截断的半截也照样解得开，
// 后面的救治闸再收拾）。但回带历史里它可以是任何东西，其中**对象/数组形态**
// （`"arguments":{"city":"北京"}`，new-api 这类中转真会这么发）必须无损收下：往
// string 解必然失败，失败后当残缺入参救治的话，一条入参整个蒸发，而它本身就是
// 合法 JSON，原始字节直接就是 canonical 要的那个串。
//
// null 与缺失一样当没有：`null` 是合法 JSON，原样带走会让出口发出一个
// `"arguments":null`，比空串更糟。返回空串，交给调用方的救治闸归成 `{}`。
func DecodeToolArgs(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		// 错误可以忽略：raw 是一次成功的 json.Unmarshal 交出来的，引号开头就意味着
		// 这一层字符串本身合法（半截 arguments 只是**内容**不合法，见救治闸）。
		_ = json.Unmarshal(trimmed, &s)
		return s
	}
	// 对象 / 数组 / 数字 / 布尔：它来自一次成功的 json.Unmarshal，本身就是合法 JSON。
	return string(raw)
}
