package protocol

import (
	"strconv"
	"strings"
)

// 本文件是**转换失败**那一档 400 的文案与 code（口径层 v1.14 ⑦⑧）：跨协议编码之后
// 请求已经不成立——messages 空了、tool_choice 的硬要求落空了——由我们自己回 400，
// 不交上游裁。交上游的代价是归因反了：SGLang 那句 `Messages cannot be empty.` 原样
// 透出去，流水里渠道背「上游拒绝」，而它一个字节都没收到过错的东西。
//
// 三个出口共用一份构造器，是为了让 CC 出口与 Anthropic 出口对同一件事说同一句话；
// 判据（什么算落空）仍在各自的编码器里，这里只管把结论说出口。
//
// code 取 OpenAI 自家错误体里已有的词（RequestError.Code 的规矩）：`empty_array` 是它
// 对空数组参数的 code，`invalid_value` 是它对参数取值不合法的 code。Anthropic 入口回显
// 时这两位不落地（errorBody 只给 OpenAI 系放 code/param），只剩 message 一句。

const (
	// CodeEmptyArray 是 OpenAI 对空数组参数的 code。
	CodeEmptyArray = "empty_array"
	// CodeInvalidValue 是 OpenAI 对参数取值不合法的 code。
	CodeInvalidValue = "invalid_value"
	// ParamMessages / ParamToolChoice 是出问题的字段名，按入口协议的叫法：三协议里
	// tool_choice 同名；消息列表 Responses 叫 input，但落空的判定发生在出口编码之后，
	// 这一格报的是**出口侧**那个字段。
	ParamMessages   = "messages"
	ParamInput      = "input"
	ParamToolChoice = "tool_choice"
)

// EmptyMessagesRejection 是「转换后没有可发送的消息」那条 400。
//
// 文案里点明是**转换后**空：客户端自己发空 messages 与我们把它丢光，日志上要靠这句与
// 同一次请求的丢弃 Warn 对照才分得开（口径层 v1.14 ⑦），文案先把「哪一步空的」说清。
//
// param 是出口侧那个字段名：CC / Anthropic 出口传 ParamMessages，Responses 出口传
// ParamInput（口径层 v1.15，三个出口同一规则）。文案里的字段名跟着 param 走。
func EmptyMessagesRejection(param string) *RequestError {
	return &RequestError{
		Message: "转换后没有可发送的消息：" + param + " 里没有一条能转到渠道协议的消息" +
			"（空正文、只剩 thinking 块的消息都会被丢掉），请至少带一条有正文或工具调用的消息",
		Code:  CodeEmptyArray,
		Param: param,
	}
}

// ToolChoiceRejection 是「tool_choice 的硬要求落空」那条 400（口径层 v1.14 ⑧）。
//
// declared 是转换后真发得出去的工具名，droppedTools 是本次被丢的工具名（服务端工具
// 一类）。文案按落空的原因分三句，都点名：required 而无工具可发、点名的工具是被丢的、
// 点名的工具压根没声明——客户端照着改就能过。
func ToolChoiceRejection(choice ToolChoice, declared, droppedTools []string) *RequestError {
	var msg string
	switch choice.Mode {
	case "required":
		if len(droppedTools) > 0 {
			msg = "tool_choice 要求必须调用工具，但声明的工具跨协议转换后一个都不剩" +
				"（被丢的是服务端工具：" + strings.Join(droppedTools, ", ") + "）；" +
				"请至少声明一个 function 工具，或把 tool_choice 改成 auto"
		} else {
			msg = "tool_choice 要求必须调用工具，但请求没有声明任何工具；" +
				"请声明至少一个 function 工具，或去掉 tool_choice"
		}
	default:
		switch {
		case hasName(droppedTools, choice.Name):
			msg = "tool_choice 点名的工具 " + strconv.Quote(choice.Name) +
				" 是服务端工具，跨协议转换发不出去；请点名一个 function 工具，或把 tool_choice 改成 auto"
		case len(declared) == 0:
			msg = "tool_choice 点名了工具 " + strconv.Quote(choice.Name) +
				"，但请求没有声明任何可转换的工具；请先在 tools 里声明它"
		default:
			msg = "tool_choice 点名的工具 " + strconv.Quote(choice.Name) +
				" 不在 tools 里（本轮声明的工具：" + strings.Join(declared, ", ") + "）；请点名其中一个"
		}
	}
	return &RequestError{Message: msg, Code: CodeInvalidValue, Param: ParamToolChoice}
}
