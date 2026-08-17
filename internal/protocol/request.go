package protocol

import "encoding/json"

// 本文件是请求侧的 canonical 模型（docs/MVP设计草案.md §4）。
//
// 定型依据是 testdata/golden/ 下 9 份**真实 harness 入站样本**（Claude Code 2.x
// 五份、Codex CLI 0.144 四份），不是凭协议文档拍的。样本逼出来的修正见文档 §4，
// 每条都标了是哪个样本证伪了原草案。
//
// 贯穿全模型的一条原则：**装得下 ≠ 转得过去**。canonical 层的职责是让 decode
// 成为全函数（任何合法入站字节都有地方放），跨协议丢什么由 encode 侧按口径决定。
// 「记为丢弃」和「无处存放」是两件事——前者是决策，后者是缺陷。

// Role 是消息角色。
//
// 含 RoleSystem 是被样本逼的：Anthropic 的 mid-conversation-system beta 会把一条
// role=system 的消息塞进 messages 数组中段（in-anthropic-tool-turn2 的序列是
// user → system → assistant → user）。原草案写的 "user/assistant/tool" 装不下它。
type Role string

// 没有 RoleDeveloper（PO 确认 jinpenga）：Responses 的 developer 角色在 decode 时**归一为 RoleSystem**，
// 原字符串不留。理由是这个映射不可逆也不需要可逆——同协议路径根本不进 codec，所以
// 没有任何一条链路会把 developer 原样转回去；而 R 出口方向收到的 RoleSystem 一律
// 按 Responses 惯例发 developer。这条不对称在 sub2api 两个方向上都能对上
// （chatcompletions_responses_bridge.go:518 收敛、anthropic_to_responses.go:133 展开）。
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool 只在 CC 侧出现（role=tool 的独立消息）；Anthropic / Responses 把
	// 工具结果放在 user 消息的块里。decode 侧不做归一，encode 侧各自落位。
	RoleTool Role = "tool"
)

// BlockKind 是内容块类型。
type BlockKind string

const (
	BlockText       BlockKind = "text"
	BlockThinking   BlockKind = "thinking"
	BlockToolUse    BlockKind = "tool_use"
	BlockToolResult BlockKind = "tool_result"
	// BlockImage 是跨协议图片块（#1）。载荷在 Image，三种来源各填一组字段。
	BlockImage BlockKind = "image"
)

// Block 是内容块。
//
// 三个协议的块形态差异很大，这里取「一个 struct 带判别字段」而非接口：codec 要
// 频繁按 Kind 分支，接口断言会让转换代码全是类型开关，收益为零。
type Block struct {
	Kind BlockKind

	// Text 承载 BlockText / BlockThinking 的正文。
	Text string

	// Image 仅 BlockImage。三种来源各填一组：MediaType+Data / URL / FileID。
	Image *Image

	// ToolCall 仅 BlockToolUse。
	ToolCall *ToolCall
	// ToolResult 仅 BlockToolResult。
	ToolResult *ToolResult

	// Extras 存本协议独有、跨协议无处安放的块级字段。已知住户：
	//   - cache_control：Anthropic 的缓存断点，五份样本的 system / text /
	//     tool_result 块上都有。它是**位置敏感**的——脱敏口径专门保住了它，
	//     因为断点位置本身就是被测行为。
	//   - signature：Anthropic thinking 块的签名。
	//   - encrypted_content / summary：Responses reasoning 的密文与摘要。
	Extras map[string]any
}

// Image 是一张图的载荷。三种来源互斥，各填一组字段；网关不代下载 URL。
//
// Data 是裸 base64，不含 data: 前缀。FileID 是上游作用域句柄，转换路径登记后丢弃。
type Image struct {
	MediaType string // mime，例如 "image/png"
	Data      string // 裸 base64，无 "data:" 前缀
	URL       string // https://... ；网关不代下载
	FileID    string // 上游作用域句柄；转换路径丢弃

	// Detail 是 CC / Responses 的图片精度提示（auto / low / high），空表示客户端
	// 没指定。Anthropic 没有对应的一格，那个出口登记 image_detail 后丢。
	//
	// 必须是一等字段而不是进 Extras：Extras 永不外带（三个出口只放行 cache_control
	// 与 metadata），放那里等于在 encode 处再死一次——CC↔Responses 这两个原生都支持
	// 它的协议之间照样过不去（口径层 v0.78 ①）。
	//
	// 取值原样转发、含 "auto"，不设白名单也不把 auto 解释成「等于没指定」而抹掉：
	// 同 v0.77 对 media_type 的裁定，网关不替上游校验，认不得让它自己 400。
	Detail string
}

// ToolCall 是一次工具调用的请求侧回带（assistant 历史里的 tool_use）。
type ToolCall struct {
	ID   string
	Name string

	// Args 是原样载荷，**不预设是 JSON**。
	//
	// Codex CLI 0.144 的 code-mode 只声明一个名为 exec 的 custom 工具，它的
	// input 是 JavaScript 源码（in-responses-tool-turn2 实测），不是 JSON 对象。
	// 原草案的 EvToolArgsDelta{JSONFragment} 与「parameters(JSON schema)」都建立
	// 在「参数必是 JSON」这个不成立的不变量上。这里存字符串，是不是 JSON 由
	// ArgsIsJSON 说了算。
	//
	// 编码到 CC 时的后果：function.arguments 按契约必须是 JSON 字符串，
	// ArgsIsJSON 为 false 时 encode 侧只能自行合成一个包装对象（见文档 §5 坑清单）。
	Args       string
	ArgsIsJSON bool

	// Extras：status（Responses custom_tool_call 带）等。
	Extras map[string]any
}

// ToolResult 是工具执行结果。
type ToolResult struct {
	// ToolCallID 对应 Anthropic tool_use_id / CC tool_call_id / Responses call_id。
	// 三协议互转时 id 原样携带，不重编号。
	ToolCallID string

	// Content 一律归一成块序列。Anthropic 样本里它是**纯字符串**，退化为单个
	// BlockText；Responses 样本里它是 []input_text（两段：执行摘要 + 程序输出），
	// 天然多块。用 []Block 是取两者的上界，字符串侧的信息不丢。
	Content []Block

	IsError bool
}

// ToolKind 区分工具的三种形态。
//
// 原草案的 Tool 只有 name/description/parameters(JSON schema)，被样本证伪了两次：
// Codex 的 exec 是 custom 工具，没有 schema，只有一份 lark 文法的 format；
// Claude Code 声明的 advisor_20260301 自带 type 与 model，是上游服务端工具。
type ToolKind string

const (
	// ToolFunction：name + JSON Schema 入参，三协议共通形态。
	ToolFunction ToolKind = "function"
	// ToolCustom：自由文本入参，可能带语法约束（Responses 的 custom 工具）。
	ToolCustom ToolKind = "custom"
	// ToolServer：上游自带的服务端工具，网关只负责原样带过去。
	ToolServer ToolKind = "server"
)

// Tool 是一条工具声明。
type Tool struct {
	Kind        ToolKind
	Name        string
	Description string

	// Schema 是 JSON Schema 原文，仅 ToolFunction 有。存 RawMessage 而非
	// map[string]any：schema 的键序与数值精度都可能影响上游行为，解开再合上
	// 是无谓的失真风险。
	Schema json.RawMessage

	// Extras：format（lark 文法）、strict、type、model 等。
	Extras map[string]any
}

// ToolChoice 是工具选择策略。
type ToolChoice struct {
	// Mode: "" | auto | none | required | tool
	Mode string
	// Name 仅 Mode == "tool" 时有意义。
	Name string
}

// Request 是入站请求归一后的形态。
type Request struct {
	Model string

	// System 是块序列而非字符串。原草案写的 System string 装不下实测样本：五份
	// Anthropic 样本的 system 都是数组，且块上带 cache_control 断点——字符串
	// 拼接会把断点位置抹平，而断点位置正是要被测的东西。
	System []Block

	Messages []Message
	Tools    []Tool

	ToolChoice ToolChoice

	// MaxTokens：Anthropic 必填，OpenAI 侧可缺省。编码到 Anthropic 出口时零值
	// 须由配置 default_max_tokens 补上。
	MaxTokens int

	Temperature *float64
	Stop        []string
	Stream      bool

	// Effort 是思考档位，三协议同域直传（口径层 v0.65）：Anthropic 的
	// output_config.effort、CC 的 reasoning_effort、Responses 的 reasoning.effort
	// 都落这一格。Anthropic 官方域为 low|medium|high|xhigh|max。
	//
	// 必须是一等字段而不是留在 Extras（与 Image.Detail 同理）：Extras 永不外带，
	// 留在那里等于每个出口都拿不到它，客户端点了 high 却被静默降级成默认档。
	//
	// **不做域校验、不钳值**：域外取值原样带给上游（口径层 v0.65 ⑤）——上游一个能看见
	// 的 400 好过网关静默把 max 改成 high。零值即「客户端没发」，出口一个字节都不加：
	// 网关不替客户端开思考（v0.65 ④/⑥）。
	//
	// 数值预算（Qwen 系的 thinking_budget）**不换算**进这里：参考实现之间的映射表互相
	// 矛盾（new-api high=4096 vs CLIProxyAPI high=24576），换算等于我们替客户端瞎猜。
	// 它走 DropThinkingParam 登记后丢。
	Effort string

	// Extras 存顶层的协议独有字段，同协议透传时原样取回。已知住户：
	//   Anthropic: metadata（上游据此判定是否官方 Claude Code，重序列化丢了会被
	//              降级成第三方 app，见 §5 坑清单）、thinking、context_management、
	//              output_config（effort 已提成一等字段 Effort，剩余键才留这儿）
	//   Responses: reasoning（同上）、store、include、prompt_cache_key、
	//              client_metadata、text、parallel_tool_calls
	Extras map[string]any
}

// LiftNestedEffort 把 extras[parent] 这个对象里的 effort 提出来，**并从 extras 里
// 删掉**（parent 被掏空后连它一起删），返回提到的档位；没有则返回空串。
//
// Anthropic 的 output_config.effort 与 Responses 的 reasoning.effort 共用这一处，
// 两个解码侧的删法必须一样。
//
// 「提完就删」是硬要求而不是清理癖：留着的话出口那边会因为「Extras 还有住户」登记一次
// DropVendorRequest——而这一格其实已经原样转发给上游了，账本就说了假话（口径层 v0.65 ⑦
// 要的正是账本可信）。
func LiftNestedEffort(extras map[string]any, parent string) string {
	obj, ok := extras[parent].(map[string]any)
	if !ok {
		return ""
	}
	effort, _ := obj["effort"].(string)
	if effort == "" {
		// 非字符串或空串：不认它，留在 Extras 里按思考参数登记后丢——总比把一个
		// 解不出来的值当档位发给上游好。
		return ""
	}
	delete(obj, "effort")
	if len(obj) == 0 {
		delete(extras, parent)
	}
	return effort
}

// thinkingParamKeys 是 Extras 里属于「思考参数」这一档（各出口的 DropThinkingParam）的顶层键。
//
// 单列一档而不是笼统归进 vendor_request：客户端点了思考却被丢掉，与它多发了一个我们不认识
// 的字段，排障时是两件完全不同的事（口径层 v0.65 ⑤）。
//
// 表住在 protocol 而不是各出口各存一份：三个出口的分类规则必须字字一样，而镜像三份的代价
// 已经付过一次——`output_config` 三份表一份都没写进去，于是解不出档位的 effort 在三个出口
// 全记成了 vendor_request，正好是这一档要防的那件事；补一次要改三处，漏一处就还是同一个洞。
// 收在这里之后新键不可能半落地。DropXxx 常量仍各包一份（那是名字，不是规则）。
var thinkingParamKeys = map[string]bool{
	"thinking":        true, // Anthropic 的思考开关本身：type / budget_tokens / display
	"thinking_budget": true, // Qwen 系数值预算：不换算成档位，理由见 Request.Effort
	"reasoning":       true, // Responses 的 reasoning 余下部分（summary 等），effort 已提走
	"output_config":   true, // Anthropic 的 effort 载体余下部分：LiftNestedEffort 解不出档位时整个留这儿
}

// IsThinkingParamKey 答「Extras 里这个顶层键该记 DropThinkingParam 还是 DropVendorRequest」。
func IsThinkingParamKey(key string) bool { return thinkingParamKeys[key] }

// Message 是一条消息。
type Message struct {
	Role Role

	// Content 一律块序列。纯字符串 content（Anthropic 的 system 角色消息、CC 的
	// 常规消息）退化为单个 BlockText。
	Content []Block

	Extras map[string]any
}
