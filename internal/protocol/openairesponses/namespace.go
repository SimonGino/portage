package openairesponses

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件是 Responses `type=namespace` 声明在解码侧的**摊平**（口径层 v1.14 ①—④，#94）。
//
// namespace 不是工具种类，是声明的分组外壳：子项该是 function / custom 还是什么，进了
// 外壳还是什么，外壳只贡献名字。此前 decodeTools 把整个外壳当成 ToolServer，子项落在
// Extras["tools"] 里没人读，两个出口整体丢弃——ADE 55 个工具丢 45 个，模型无工具可调、
// 日志只有一句 dropped=[server_tool]。
//
// 摊平规则（照 codex-rs `is_default_namespace()`）：`functions`、空、缺失是**默认命名
// 空间**，子项对外用裸名——Codex 主路径的三份样本字节不变；其余命名空间的子项对外用
// `<命名空间名>__<子工具名>`。命名空间名自身可含 `__`（ADE 实测 `mcp__ade_asset_knowledge`），
// 所以回程还原**只能查每请求映射表**（Codec.namespaceTools），任何按分隔符拆串都会拆错。
//
// 两道就地 400（口径层 ③④）都是 *protocol.RequestError，形态同 stateful.go 的
// previous_response_id 闸，是「decode 必须是全函数」的第二处例外：
//   - 摊平后一名两源（`functions` 子项撞顶层、跨命名空间摊平后撞、同名 namespace）——
//     回程只能靠覆盖顺序猜，自动改名又是发明规范没有的名字，Codex 认不出。
//   - 摊平名不满足 `^[a-zA-Z0-9_-]{1,64}$`（CC 与 Anthropic 两家共同上限）——这是我们
//     自己拼出来的名字，拼出必被拒的名字再让上游报错，归因是反的。不截断（会制造新撞名）
//     不转义。
//
// 两处都**只管命名空间相关的名字**：顶层同名工具不查重（今天 CC 编码侧也不查，不在本闸
// 内），顶层工具与默认命名空间子项的名字格式交上游裁（那是内容格式、上游的能力面）。

// NamespaceTool 是一个摊平名的来处：命名空间名 + 裸子工具名。
//
// 只记非默认命名空间的子项：默认命名空间与顶层工具对外就是裸名，回程照旧不带
// namespace 字段，表里没有它们的位置。
type NamespaceTool struct {
	Namespace string
	Name      string
}

// CodeInvalidValue 是两道闸回给客户端的 code。挑 OpenAI 自家校验入参用的那个词
// （tools[i].name 不合规时官方回的就是 invalid_value + param 指到那一格），客户端至少
// 认得它是「请求里某个值不对」而不是「服务坏了」。
const CodeInvalidValue = "invalid_value"

// defaultNamespace 是 Codex 把顶层工具收进去的那个壳的名字；它与空 / 缺失同义。
const defaultNamespace = "functions"

func isDefaultNamespace(ns string) bool { return ns == "" || ns == defaultNamespace }

// flatToolName 拼摊平名。只有这一处拼，也**只许**这一处拼——反向（拆）不存在。
func flatToolName(ns, name string) string { return ns + "__" + name }

// toolNamePattern 是 CC 与 Anthropic 两家共同的工具名上限（口径层 v1.14 ④）。
var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

const toolNameMaxLen = 64

// toolOrigin 记一条 Tool 是从请求体哪里来的，撞名时点名两个来源用。
type toolOrigin struct {
	// path 是它在请求体里的 JSON 路径，如 tools[7].tools[2] 或 input[0].tools[1]。
	path string
	// inNamespace 为真表示它是某个 namespace 声明的子项（含默认命名空间）。
	inNamespace bool
	// namespace 是非默认命名空间名；顶层工具与默认命名空间子项为空。
	namespace string
	// bare 是裸名（子项自己的 name）。
	bare string
}

func (o toolOrigin) String() string {
	switch {
	case o.namespace != "":
		return fmt.Sprintf("%s（命名空间 %s 的子工具 %s）", o.path, o.namespace, o.bare)
	case o.inNamespace:
		return fmt.Sprintf("%s（默认命名空间的子工具 %s）", o.path, o.bare)
	default:
		return fmt.Sprintf("%s（顶层工具 %s）", o.path, o.bare)
	}
}

// toolDecoder 攒一次请求里所有容器（顶层 tools 与各个 additional_tools 项）解出的工具，
// origins 与 tools 同下标。攒在一处而不是各容器各解各的，是因为撞名要跨容器查：Codex 的
// `functions` 子项与 ADE 的顶层工具本就住在不同容器里。
type toolDecoder struct {
	tools   []protocol.Tool
	origins []toolOrigin
}

// decode 解一个工具声明数组，path 是它在请求体里的 JSON 路径（"tools" / "input[3].tools"）。
//
// namespace 项在这里展开：读 item["tools"] 把子项逐个解成 Tool，非默认命名空间的加前缀。
// 子项种类不变（口径层 ②）——custom 子项与顶层 custom 完全同规，走同一个 decodeTool。
// 子项里再出现 namespace（规范说不嵌套）不特判：decodeTool 会把它当认不得的种类归成
// ToolServer，由出口按服务端工具丢弃并登记，decode 仍是全函数。
func (d *toolDecoder) decode(raw json.RawMessage, path string) error {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("openairesponses: %s 不是数组: %w", path, err)
	}
	for i, item := range items {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		var kind string
		if err := unmarshalIf(item, "type", &kind); err != nil {
			return fmt.Errorf("openairesponses: %s: %w", itemPath, err)
		}
		if kind != "namespace" {
			tool, err := decodeTool(item, kind)
			if err != nil {
				return fmt.Errorf("openairesponses: %s: %w", itemPath, err)
			}
			d.add(tool, toolOrigin{path: itemPath, bare: tool.Name})
			continue
		}

		var ns string
		if err := unmarshalIf(item, "name", &ns); err != nil {
			return fmt.Errorf("openairesponses: %s: %w", itemPath, err)
		}
		children, ok := item["tools"]
		if !ok {
			// 空壳：没有子项就什么都不贡献。外壳自己的 description 之类随之丢——
			// 它描述的是分组，canonical 没有分组这个概念。
			continue
		}
		var subs []map[string]json.RawMessage
		if err := json.Unmarshal(children, &subs); err != nil {
			return fmt.Errorf("openairesponses: %s.tools 不是数组: %w", itemPath, err)
		}
		for j, sub := range subs {
			subPath := fmt.Sprintf("%s.tools[%d]", itemPath, j)
			var subKind string
			if err := unmarshalIf(sub, "type", &subKind); err != nil {
				return fmt.Errorf("openairesponses: %s: %w", subPath, err)
			}
			tool, err := decodeTool(sub, subKind)
			if err != nil {
				return fmt.Errorf("openairesponses: %s: %w", subPath, err)
			}
			origin := toolOrigin{path: subPath, inNamespace: true, bare: tool.Name}
			if !isDefaultNamespace(ns) {
				origin.namespace = ns
				tool.Name = flatToolName(ns, tool.Name)
			}
			d.add(tool, origin)
		}
	}
	return nil
}

func (d *toolDecoder) add(tool protocol.Tool, origin toolOrigin) {
	d.tools = append(d.tools, tool)
	d.origins = append(d.origins, origin)
}

// table 过两道闸并给出映射表（摊平名 → 来处）。出错时返回的一律是 *protocol.RequestError。
//
// 名字校验只对**我们拼出来的**名字（非默认命名空间的子项）做；撞名只在至少一方来自
// namespace 声明时才拒——两个顶层同名工具不在本闸内（见文件头）。
func (d *toolDecoder) table() (map[string]NamespaceTool, error) {
	first := map[string]int{} // 摊平名 → 首见下标
	var table map[string]NamespaceTool
	for i, t := range d.tools {
		o := d.origins[i]
		if o.namespace != "" && !toolNamePattern.MatchString(t.Name) {
			return nil, invalidFlatName(t.Name, o)
		}
		if j, dup := first[t.Name]; dup {
			if o.inNamespace || d.origins[j].inNamespace {
				return nil, flatNameCollision(t.Name, d.origins[j], o)
			}
			continue
		}
		first[t.Name] = i
		if o.namespace != "" {
			if table == nil {
				table = map[string]NamespaceTool{}
			}
			table[t.Name] = NamespaceTool{Namespace: o.namespace, Name: o.bare}
		}
	}
	return table, nil
}

// flatNameCollision 造「一名两源」那条 400，两个来源都点名——客户端要改的是其中一个，
// 只报一个它就得自己猜另一个在哪。param 指后来的那个：先到的那个通常是它原本就有的。
func flatNameCollision(name string, a, b toolOrigin) *protocol.RequestError {
	return &protocol.RequestError{
		Message: fmt.Sprintf("工具名 %q 摊平后有两个来源：%s 与 %s。转换出去的协议分不开同名工具，"+
			"回程也无从还原：请改掉其中一个的名字（或它的命名空间名）后重发。", name, a, b),
		Code:  CodeInvalidValue,
		Param: b.path + ".name",
	}
}

// invalidFlatName 造「摊平名不合规」那条 400。分开说超长与含非法字符：两者要改的东西
// 不一样，一句「不满足正则」让人对着 48 个字符的名字数半天。
func invalidFlatName(name string, o toolOrigin) *protocol.RequestError {
	reason := "含字母、数字、下划线、连字符之外的字符"
	if len(name) > toolNameMaxLen {
		reason = fmt.Sprintf("长 %d 个字符，超过 %d 的上限", len(name), toolNameMaxLen)
	}
	return &protocol.RequestError{
		Message: fmt.Sprintf("%s 摊平成 %q 后%s，不满足工具名规则 ^[a-zA-Z0-9_-]{1,64}$"+
			"（CC 与 Anthropic 两家共同的上限）：请缩短或改掉命名空间名 / 子工具名后重发。", o, name, reason),
		Code:  CodeInvalidValue,
		Param: o.path + ".name",
	}
}

// restoreReplayNames 是 input 与 tools 都解完之后的一趟后处理（口径层 v1.14 ⑤⑥，#95）：
// 把客户端回放的调用 item 与 tool_choice 点名对回**本轮声明表**里的名字。
//
// 模型看到的工具名是摊平名，历史里的调用名必须与之一致——否则模型会跟着历史改口
// 叫裸名（真 GLM-5.2 实测：回带裸名 + namespace 后，下一次调用就发成了声明表里
// 没有的裸名 orchestrateTask）。规则见 resolveReplayName。
//
// namespace 字段在 decodeToolCall 里落在 ToolCall.Extras，这里读完即删：它已经被
// 消费进名字里，Extras 本来也永不外带。
func restoreReplayNames(req *protocol.Request, table map[string]NamespaceTool) {
	declared := make(map[string]bool, len(req.Tools))
	for _, t := range req.Tools {
		declared[t.Name] = true
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Kind != protocol.BlockToolUse || b.ToolCall == nil {
				continue
			}
			call := b.ToolCall
			ns, _ := call.Extras["namespace"].(string)
			delete(call.Extras, "namespace")
			call.Name = resolveReplayName(call.Name, ns, declared, table)
		}
	}
	if req.ToolChoice.Mode == "tool" {
		req.ToolChoice.Name = resolveReplayName(req.ToolChoice.Name, "", declared, table)
	}
}

// resolveReplayName 决定一个回带的名字在 canonical 里叫什么：
//   - 自带 namespace 字段：套与声明侧同一条摊平规则（默认命名空间裸名，其余加前缀），
//     不看声明表——客户端说了它属于哪个命名空间，就信它。
//   - 没带：名字恰是本轮声明的某个名字（摊平名或顶层名）就原样；否则拿它当裸名在
//     映射表里做唯一查表，恰只在一个非默认命名空间里出现就补前缀；零中或多中原样
//     带过去，不 400——历史 item 被拒客户端无法自救。
//
// tool_choice 点名走同一条（口径层 ⑥）：规范没有点名 namespace 子项的写法，写成
// 摊平名的自然能中，写裸名的靠唯一查表。
func resolveReplayName(name, ns string, declared map[string]bool, table map[string]NamespaceTool) string {
	if ns != "" {
		if isDefaultNamespace(ns) {
			return name
		}
		return flatToolName(ns, name)
	}
	if declared[name] {
		return name
	}
	var hit string
	hits := 0
	for flat, src := range table {
		if src.Name == name {
			hit, hits = flat, hits+1
		}
	}
	if hits == 1 {
		return hit
	}
	return name
}
