package openairesponses

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// fixtureDir 放的是**构造样本**，与 goldenDir 的真实转录分属两档：golden 认
// `verified: true`，这边认 `synthetic: true`（钉死它不是转录）。缘由与升格规矩见
// testdata/fixtures/README.md。
const fixtureDir = "../../../testdata/fixtures"

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	dir := filepath.Join(fixtureDir, name)
	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Synthetic bool `json:"synthetic"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.Synthetic {
		t.Fatalf("%s 的 meta.json 没有 synthetic:true——真录到的样本请放进 testdata/golden/ 走 verified 那道闸", name)
	}
	body, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// ADE 实采（in-responses-namespace-turn1）：顶层 tools 里 10 个 function + 1 个 web_search
// + 8 个具名 namespace 共 45 个子工具。口径层 v1.14 的触发案例——此前那 45 个整体落成
// 一个个 ToolServer 被两个出口丢光，模型无工具可调。
func TestDecodeRequestFlattensNamespaceTools(t *testing.T) {
	c := NewCodec()
	req, err := c.DecodeRequest(loadSample(t, "in-responses-namespace-turn1"), true)
	if err != nil {
		t.Fatalf("ADE 样本解不动: %v", err)
	}

	if len(req.Tools) != 56 {
		t.Fatalf("解出 %d 个工具, 期望 56（10 顶层 function + 45 摊平 + 1 web_search）", len(req.Tools))
	}
	kinds := map[protocol.ToolKind]int{}
	byName := map[string]protocol.Tool{}
	for _, tool := range req.Tools {
		kinds[tool.Kind]++
		byName[tool.Name] = tool
		// 老 bug 的形状：外壳整个当工具、子项塞在 Extras["tools"] 里没人读。
		if _, ok := tool.Extras["tools"]; ok {
			t.Errorf("工具 %q 的 Extras 里还留着 tools 子项——namespace 没被展开", tool.Name)
		}
	}
	if kinds[protocol.ToolFunction] != 55 || kinds[protocol.ToolServer] != 1 || kinds[protocol.ToolCustom] != 0 {
		t.Errorf("种类计数 = %v, 期望 function 55 / server 1 / custom 0", kinds)
	}

	// 顶层的照旧裸名——request_user_input 在 ADE 是顶层 function（Codex 里它是
	// functions 子项），这正是「全裸名」方案会撞名的那个例子。
	for _, name := range []string{"shell_command", "request_user_input", "update_plan", "get_goal"} {
		if tool, ok := byName[name]; !ok || tool.Kind != protocol.ToolFunction {
			t.Errorf("顶层工具 %q 没以裸名解出来（或种类不对）: %+v", name, tool)
		}
	}

	// 摊平名 = <命名空间>__<子工具名>；命名空间名自身含 __ 也只是拼接，不做任何解释。
	table := c.NamespaceTools()
	if len(table) != 45 {
		t.Fatalf("映射表 %d 项, 期望 45", len(table))
	}
	const flat = "mcp__ade_asset_knowledge__readKnowledgeIndexFile"
	tool, ok := byName[flat]
	if !ok {
		t.Fatalf("摊平工具 %q 没解出来", flat)
	}
	if tool.Kind != protocol.ToolFunction || len(tool.Schema) == 0 || tool.Description == "" {
		t.Errorf("摊平工具丢了 schema / 描述 / 种类: kind=%q schema=%d desc=%q", tool.Kind, len(tool.Schema), tool.Description)
	}
	if _, ok := tool.Extras["strict"]; !ok {
		t.Errorf("摊平工具的 strict 没进 Extras: %v", tool.Extras)
	}
	if got := table[flat]; got != (NamespaceTool{Namespace: "mcp__ade_asset_knowledge", Name: "readKnowledgeIndexFile"}) {
		t.Errorf("映射表[%s] = %+v, 期望 {mcp__ade_asset_knowledge readKnowledgeIndexFile}", flat, got)
	}

	// 表里每一项都对得上一个解出来的工具，且名字全合规（CC 与 Anthropic 共同上限）。
	for name, origin := range table {
		if _, ok := byName[name]; !ok {
			t.Errorf("映射表里的 %q 不在 req.Tools 里", name)
		}
		if !toolNamePattern.MatchString(name) {
			t.Errorf("摊平名 %q 不满足 %s", name, toolNamePattern)
		}
		if flatToolName(origin.Namespace, origin.Name) != name {
			t.Errorf("映射表[%s] 的来处 %+v 拼不回这个名字", name, origin)
		}
	}
	// 顶层与服务端工具不在表里。
	for _, name := range []string{"request_user_input", "shell_command", ""} {
		if _, ok := table[name]; ok {
			t.Errorf("%q 不该进映射表（顶层 / 服务端工具对外就是裸名）", name)
		}
	}
	if c.customTools != nil {
		t.Errorf("ADE 样本没有 custom 工具, customTools = %v", c.customTools)
	}
}

// Codex 把工具裹在 `functions` 命名空间里放在 additional_tools 项（responses-stream-*
// 六份真实发包）。默认命名空间免前缀，canonical 里出来的名字与展开前一模一样——
// 这是 Codex 主路径字节不变的验收线。
func TestDecodeRequestKeepsDefaultNamespaceBare(t *testing.T) {
	c := NewCodec()
	req, err := c.DecodeRequest(readGoldenRequest(t, "responses-stream-tool-turn1"), true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	kinds := map[string]protocol.ToolKind{}
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
		kinds[tool.Name] = tool.Kind
	}
	if strings.Join(names, ",") != "exec,wait,request_user_input" {
		t.Fatalf("工具名序列 = %v, 期望 exec,wait,request_user_input（functions 子项裸名、顺序不变）", names)
	}
	if kinds["exec"] != protocol.ToolCustom || kinds["wait"] != protocol.ToolFunction {
		t.Errorf("子项种类变了: %v", kinds)
	}
	if !c.customTools["exec"] || len(c.customTools) != 1 {
		t.Errorf("customTools = %v, 期望只有 exec", c.customTools)
	}
	if c.NamespaceTools() != nil {
		t.Errorf("默认命名空间不进映射表, 实得 %v", c.NamespaceTools())
	}
}

// 子项种类不变（口径层 v1.14 ②）：namespace 里的 custom 与顶层 custom 完全同规——
// 摊平名进 customTools（编码侧靠它发 custom_tool_call 并拆包装），format 照进 Extras。
// sub2api 那条「原生摊平时跳过 custom 子项」被否决：它会丢掉 Codex 的 exec。
func TestDecodeRequestFlattensCustomChildInNamespace(t *testing.T) {
	body := `{"model":"m","tools":[{"type":"namespace","name":"ns","tools":[
		{"type":"custom","name":"exec","format":{"type":"grammar","syntax":"lark","definition":"start: SOURCE"}},
		{"type":"function","name":"wait","parameters":{"type":"object"}}
	]}]}`
	c := NewCodec()
	req, err := c.DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 2 || req.Tools[0].Name != "ns__exec" || req.Tools[1].Name != "ns__wait" {
		t.Fatalf("工具 = %+v, 期望 ns__exec / ns__wait", req.Tools)
	}
	exec := req.Tools[0]
	if exec.Kind != protocol.ToolCustom || exec.Schema != nil || exec.Extras["format"] == nil {
		t.Errorf("custom 子项变形了: kind=%q schema=%s extras=%v", exec.Kind, exec.Schema, exec.Extras)
	}
	if !c.customTools["ns__exec"] || c.customTools["exec"] {
		t.Errorf("customTools = %v, 期望记的是摊平名 ns__exec", c.customTools)
	}
	if got := c.NamespaceTools()["ns__exec"]; got != (NamespaceTool{Namespace: "ns", Name: "exec"}) {
		t.Errorf("映射表[ns__exec] = %+v", got)
	}
}

// 样本没覆盖但协议允许的形态，一个都不许解不动——decode 仍是全函数。
func TestDecodeRequestToleratesNamespaceShapes(t *testing.T) {
	cases := map[string]struct {
		body      string
		wantNames []string
	}{
		"空壳（没有 tools）": {
			`{"model":"m","tools":[{"type":"namespace","name":"ns"},{"type":"function","name":"f"}]}`,
			[]string{"f"},
		},
		"空数组": {
			`{"model":"m","tools":[{"type":"namespace","name":"ns","tools":[]}]}`,
			nil,
		},
		"没有 name 的壳等于默认命名空间": {
			`{"model":"m","tools":[{"type":"namespace","tools":[{"type":"function","name":"f"}]}]}`,
			[]string{"f"},
		},
		"空串 name 等于默认命名空间": {
			`{"model":"m","tools":[{"type":"namespace","name":"","tools":[{"type":"function","name":"f"}]}]}`,
			[]string{"f"},
		},
		"functions 与其它命名空间并存": {
			`{"model":"m","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"f"}]},` +
				`{"type":"namespace","name":"g","tools":[{"type":"function","name":"f"}]}]}`,
			[]string{"f", "g__f"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req, err := NewCodec().DecodeRequest([]byte(tc.body), false)
			if err != nil {
				t.Fatalf("解不动: %v", err)
			}
			var names []string
			for _, tool := range req.Tools {
				names = append(names, tool.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.wantNames, ",") {
				t.Errorf("工具名 = %v, 期望 %v", names, tc.wantNames)
			}
		})
	}

	// 规范说 namespace 不嵌套。真嵌了不特判：内层壳按认不得的种类归 ToolServer，由出口
	// 当服务端工具丢弃并登记——不炸，也不假装展开了。
	req, err := NewCodec().DecodeRequest([]byte(`{"model":"m","tools":[{"type":"namespace","name":"outer","tools":[
		{"type":"namespace","name":"inner","tools":[{"type":"function","name":"f"}]}]}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Kind != protocol.ToolServer || req.Tools[0].Name != "outer__inner" {
		t.Errorf("嵌套 namespace 解成 %+v, 期望一个名为 outer__inner 的 ToolServer", req.Tools)
	}
}

func requestError(t *testing.T, err error) *protocol.RequestError {
	t.Helper()
	if err == nil {
		t.Fatal("应当被拒，却解成功了")
	}
	reqErr, ok := errors.AsType[*protocol.RequestError](err)
	if !ok {
		t.Fatalf("错误不是 *protocol.RequestError（回不成可逐字回显的 400）: %T %v", err, err)
	}
	if reqErr.Code != CodeInvalidValue {
		t.Errorf("code = %q, 期望 %q", reqErr.Code, CodeInvalidValue)
	}
	return reqErr
}

// 撞名即 400（口径层 v1.14 ③）：摊平后一名两源，回 400 且**点名两个来源**。构造样本是
// ③ 点名的那种——request_user_input 既是顶层 function（ADE）又是 functions 子项（Codex）。
func TestDecodeRequestRejectsFlatNameCollision(t *testing.T) {
	reqErr := requestError(t, must2(NewCodec().DecodeRequest(loadFixture(t, "in-responses-namespace-collision"), true)))
	if reqErr.Param != "tools[1].tools[0].name" {
		t.Errorf("param = %q, 期望指到后来的那个来源 tools[1].tools[0].name", reqErr.Param)
	}
	for _, want := range []string{`"request_user_input"`, "tools[0]（顶层工具 request_user_input）", "tools[1].tools[0]（默认命名空间的子工具 request_user_input）", "重发"} {
		if !strings.Contains(reqErr.Message, want) {
			t.Errorf("文案没点名 %q: %s", want, reqErr.Message)
		}
	}

	cases := map[string]struct {
		body      string
		wantParam string
		wantIn    []string
	}{
		// 命名空间名自身可含 __：a 的子项 b__c 与 a__b 的子项 c 摊平后是同一个名字。
		// 拆串还原在这种形状上必拆错，正是「只能查表」那条的反例。
		"跨命名空间摊平后撞": {
			`{"model":"m","tools":[{"type":"namespace","name":"a","tools":[{"type":"function","name":"b__c"}]},` +
				`{"type":"namespace","name":"a__b","tools":[{"type":"function","name":"c"}]}]}`,
			"tools[1].tools[0].name",
			[]string{`"a__b__c"`, "命名空间 a 的子工具 b__c", "命名空间 a__b 的子工具 c"},
		},
		"同名 namespace": {
			`{"model":"m","tools":[{"type":"namespace","name":"ade_git","tools":[{"type":"function","name":"commit"}]},` +
				`{"type":"namespace","name":"ade_git","tools":[{"type":"function","name":"commit"}]}]}`,
			"tools[1].tools[0].name",
			[]string{`"ade_git__commit"`, "tools[0].tools[0]", "tools[1].tools[0]"},
		},
		// 两个容器：顶层 tools 先解，additional_tools 里的 functions 子项后到。
		"顶层撞 additional_tools 里的 functions 子项": {
			`{"model":"m","tools":[{"type":"function","name":"wait"}],` +
				`"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"wait"}]}]}]}`,
			"input[0].tools[0].tools[0].name",
			[]string{"tools[0]（顶层工具 wait）", "input[0].tools[0].tools[0]（默认命名空间的子工具 wait）"},
		},
		"顶层撞非默认命名空间摊平名": {
			`{"model":"m","tools":[{"type":"function","name":"ns__f"},{"type":"namespace","name":"ns","tools":[{"type":"function","name":"f"}]}]}`,
			"tools[1].tools[0].name",
			[]string{`"ns__f"`, "顶层工具 ns__f", "命名空间 ns 的子工具 f"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reqErr := requestError(t, must2(NewCodec().DecodeRequest([]byte(tc.body), false)))
			if reqErr.Param != tc.wantParam {
				t.Errorf("param = %q, 期望 %q", reqErr.Param, tc.wantParam)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(reqErr.Message, want) {
					t.Errorf("文案没点名 %q: %s", want, reqErr.Message)
				}
			}
		})
	}
}

// 不在本闸内的形态照常放行：两个顶层同名工具（今天 CC 编码侧也不查重，那条另裁），
// 以及顶层 x 与命名空间 n 的子项 x——摊平后是 x 与 n__x，根本不撞。
func TestDecodeRequestCollisionGateOnlyCoversNamespaces(t *testing.T) {
	for name, body := range map[string]string{
		"两个顶层同名":        `{"model":"m","tools":[{"type":"function","name":"x"},{"type":"function","name":"x"}]}`,
		"顶层与非默认命名空间同裸名": `{"model":"m","tools":[{"type":"function","name":"x"},{"type":"namespace","name":"n","tools":[{"type":"function","name":"x"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewCodec().DecodeRequest([]byte(body), false); err != nil {
				t.Errorf("不该被拒: %v", err)
			}
		})
	}
}

// 摊平名须满足 ^[a-zA-Z0-9_-]{1,64}$（口径层 v1.14 ④），不满足即 400 点名那个工具，不截断
// 不转义。构造样本：ADE 真名 mcp__ade_asset_knowledge 加 46 字符子工具名，摊平 72 字符。
func TestDecodeRequestRejectsInvalidFlatName(t *testing.T) {
	reqErr := requestError(t, must2(NewCodec().DecodeRequest(loadFixture(t, "in-responses-namespace-badname"), true)))
	if reqErr.Param != "tools[0].tools[1].name" {
		t.Errorf("param = %q, 期望 tools[0].tools[1].name（点名超限那一个，不是合规的邻居）", reqErr.Param)
	}
	for _, want := range []string{
		`"mcp__ade_asset_knowledge__readKnowledgeIndexFileFromCorporateSharedDrive"`,
		"命名空间 mcp__ade_asset_knowledge 的子工具 readKnowledgeIndexFileFromCorporateSharedDrive",
		"长 72 个字符", "64", "重发",
	} {
		if !strings.Contains(reqErr.Message, want) {
			t.Errorf("文案没点名 %q: %s", want, reqErr.Message)
		}
	}

	// 非法字符是另一种不合规，文案要说清是字符而不是长度。
	reqErr = requestError(t, must2(NewCodec().DecodeRequest(
		[]byte(`{"model":"m","tools":[{"type":"namespace","name":"mcp.x","tools":[{"type":"function","name":"y"}]}]}`), false)))
	if reqErr.Param != "tools[0].tools[0].name" || !strings.Contains(reqErr.Message, "之外的字符") {
		t.Errorf("非法字符那条不对: param=%q msg=%s", reqErr.Param, reqErr.Message)
	}

	// 校验只对**我们拼出来的**名字做：顶层工具与默认命名空间子项的名字格式是内容格式、
	// 上游的能力面，不在这道闸里（口径层 ④ 与「格式不设白名单」那条的分界）。
	for name, body := range map[string]string{
		"顶层工具名含点":         `{"model":"m","tools":[{"type":"function","name":"bad.name"}]}`,
		"functions 子项名含点": `{"model":"m","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"bad.name"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewCodec().DecodeRequest([]byte(body), false); err != nil {
				t.Errorf("不该被拒: %v", err)
			}
		})
	}
}

// must2 把 (值, error) 收成 error，喂给只看错误的断言。
func must2[T any](_ T, err error) error { return err }

// 回程还原（口径层 v1.14 ⑤，#95）：模型调的是摊平名，客户端认的是 namespace 字段 +
// 裸子名。只查表——命名空间名 mcp__ade_asset_knowledge 自身含 `__`，按分隔符拆必错。
// 顶层工具不带 namespace 字段（客户端本来就没这个键）。
func TestEncodeStreamRestoresNamespaceToolCall(t *testing.T) {
	c := NewCodec()
	if _, err := c.DecodeRequest(loadSample(t, "in-responses-namespace-turn1"), true); err != nil {
		t.Fatal(err)
	}
	frames := encodeStream(t, c,
		protocol.Event{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		protocol.Event{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_a", ToolName: "mcp__ade_asset_knowledge__readKnowledgeIndexFile"},
		protocol.Event{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{"path":"x"}`},
		protocol.Event{Type: protocol.EvToolCallEnd, Index: 0},
		protocol.Event{Type: protocol.EvToolCallStart, Index: 1, ToolID: "call_b", ToolName: "shell_command"},
		protocol.Event{Type: protocol.EvToolArgsDelta, Index: 1, Text: `{"command":"ls"}`},
		protocol.Event{Type: protocol.EvToolCallEnd, Index: 1},
		protocol.Event{Type: protocol.EvDone, StopReason: "tool_calls"},
	)
	assertEventOrder(t, frames,
		"response.created", "response.in_progress",
		"response.output_item.added", "response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.output_item.added", "response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.completed",
	)
	// 命名空间子工具：added / arguments.done / output_item.done 三处都还原。
	for _, i := range []int{2, 4, 5} {
		m := frames[i].data
		if item, ok := m["item"].(map[string]any); ok {
			m = item
		}
		if m["name"] != "readKnowledgeIndexFile" || m["namespace"] != "mcp__ade_asset_knowledge" {
			t.Errorf("帧 %d（%s）没还原: name=%v namespace=%v", i, frames[i].event, m["name"], m["namespace"])
		}
	}
	// 顶层工具：裸名照旧，且没有 namespace 键。
	for _, i := range []int{6, 8, 9} {
		m := frames[i].data
		if item, ok := m["item"].(map[string]any); ok {
			m = item
		}
		if _, has := m["namespace"]; m["name"] != "shell_command" || has {
			t.Errorf("帧 %d（%s）顶层工具不该带 namespace: %v", i, frames[i].event, m)
		}
	}

	// 非流式同源（EncodeFullBody 复用流编码器）。
	body, err := c.EncodeFullBody([]protocol.Event{
		{Type: protocol.EvMessageStart, ID: "chatcmpl-abc", Model: "m"},
		{Type: protocol.EvToolCallStart, Index: 0, ToolID: "call_a", ToolName: "ade_task__orchestrateTask"},
		{Type: protocol.EvToolArgsDelta, Index: 0, Text: `{}`},
		{Type: protocol.EvToolCallEnd, Index: 0},
		{Type: protocol.EvDone, StopReason: "tool_calls"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(body, &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Output) != 1 || full.Output[0]["name"] != "orchestrateTask" || full.Output[0]["namespace"] != "ade_task" {
		t.Errorf("非流式 output 没还原: %v", full.Output)
	}
}

// 回带对回声明表（口径层 v1.14 ⑤⑥，#95）。真 GLM-5.2 实测：历史里的调用名若与模型
// 眼前的摊平名不一致，下一轮模型就跟着历史叫裸名，调到声明表里没有的工具。
// 三种回带形态 + tool_choice 点名，一张表钉住；零中/多中原样带过去、不 400。
func TestDecodeRequestRestoresReplayNames(t *testing.T) {
	const tools = `[
		{"type":"function","name":"top","parameters":{}},
		{"type":"namespace","name":"ade_task","tools":[
			{"type":"function","name":"orchestrateTask","parameters":{}},
			{"type":"function","name":"list","parameters":{}}]},
		{"type":"namespace","name":"mcp__x","tools":[
			{"type":"function","name":"list","parameters":{}},
			{"type":"function","name":"top","parameters":{}}]},
		{"type":"namespace","name":"functions","tools":[
			{"type":"function","name":"bare","parameters":{}}]}
	]`
	cases := []struct {
		label string
		item  string // 回带的 function_call item（不含 call_id/arguments）
		want  string
	}{
		{"自带 namespace：套摊平规则", `"name":"orchestrateTask","namespace":"ade_task"`, "ade_task__orchestrateTask"},
		{"自带 namespace 且是默认命名空间：裸名", `"name":"bare","namespace":"functions"`, "bare"},
		{"自带 namespace 撞了顶层名：信客户端", `"name":"top","namespace":"mcp__x"`, "mcp__x__top"},
		{"摊平名恰中：原样", `"name":"ade_task__orchestrateTask"`, "ade_task__orchestrateTask"},
		{"顶层名恰中：原样（不去查表）", `"name":"top"`, "top"},
		{"裸名唯一查表：补前缀", `"name":"orchestrateTask"`, "ade_task__orchestrateTask"},
		{"裸名多中：原样，不 400", `"name":"list"`, "list"},
		{"根本没声明：原样，不 400", `"name":"nope"`, "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			body := `{"model":"m","tools":` + tools + `,"input":[
				{"role":"user","content":[{"type":"input_text","text":"go"}]},
				{"type":"function_call","call_id":"call_1",` + tc.item + `,"arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`
			req, err := NewCodec().DecodeRequest([]byte(body), true)
			if err != nil {
				t.Fatalf("解不动: %v", err)
			}
			var calls []*protocol.ToolCall
			for _, m := range req.Messages {
				for _, b := range m.Content {
					if b.Kind == protocol.BlockToolUse {
						calls = append(calls, b.ToolCall)
					}
				}
			}
			if len(calls) != 1 {
				t.Fatalf("解出 %d 个调用, 期望 1", len(calls))
			}
			if calls[0].Name != tc.want {
				t.Errorf("回带名 = %q, 期望 %q", calls[0].Name, tc.want)
			}
			if _, left := calls[0].Extras["namespace"]; left {
				t.Errorf("namespace 读完该从 Extras 删掉: %v", calls[0].Extras)
			}
		})
	}

	// tool_choice 点名同一条规则：摊平名恰中、裸名唯一查表、多中原样（后面出口自己判）。
	for _, tc := range []struct{ name, want string }{
		{"ade_task__orchestrateTask", "ade_task__orchestrateTask"},
		{"orchestrateTask", "ade_task__orchestrateTask"},
		{"top", "top"},
		{"list", "list"},
	} {
		body := `{"model":"m","tools":` + tools + `,"tool_choice":{"type":"function","name":"` + tc.name + `"},
			"input":[{"role":"user","content":[{"type":"input_text","text":"go"}]}]}`
		req, err := NewCodec().DecodeRequest([]byte(body), true)
		if err != nil {
			t.Fatalf("tool_choice %q: %v", tc.name, err)
		}
		if req.ToolChoice.Mode != "tool" || req.ToolChoice.Name != tc.want {
			t.Errorf("tool_choice %q → %+v, 期望 Name=%q", tc.name, req.ToolChoice, tc.want)
		}
	}
}

// ADE 实采第二轮（in-responses-namespace-turn2）：**回程形态的真实证据**（口径层
// v1.14 ⑤，#95）。真实 harness 把命名空间子工具的历史调用写成裸子名 + `namespace`
// 字段，17 次回带里 4 次如此，一次摊平名都没有——三种设想的形态里它用的是这一种。
//
// 钉住的是「历史里的名字必须与模型眼前的声明名一致」：不还原的话这 4 次回带就成了
// 声明表里没有的裸名，模型跟着历史改口（真 GLM-5.2 实测会调出 orchestrateTask）。
func TestDecodeRequestRestoresRealADEReplay(t *testing.T) {
	c := NewCodec()
	req, err := c.DecodeRequest(loadSample(t, "in-responses-namespace-turn2"), true)
	if err != nil {
		t.Fatalf("ADE 第二轮样本解不动: %v", err)
	}

	declared := make(map[string]bool, len(req.Tools))
	for _, tool := range req.Tools {
		declared[tool.Name] = true
	}
	if len(req.Tools) != 72 || len(c.NamespaceTools()) != 61 {
		t.Fatalf("解出 %d 个工具 / 映射表 %d 项, 期望 72 / 61", len(req.Tools), len(c.NamespaceTools()))
	}

	var names []string
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Kind != protocol.BlockToolUse || b.ToolCall == nil {
				continue
			}
			names = append(names, b.ToolCall.Name)
			// 每一次回带都必须落回本轮声明表里的某个名字，一个都不能漏。
			if !declared[b.ToolCall.Name] {
				t.Errorf("回带调用 %q 不在本轮声明的工具里——模型会跟着历史改口", b.ToolCall.Name)
			}
			if _, left := b.ToolCall.Extras["namespace"]; left {
				t.Errorf("%q 的 namespace 读完该从 Extras 删掉", b.ToolCall.Name)
			}
		}
	}
	if len(names) != 17 {
		t.Fatalf("解出 %d 次回带调用, 期望 17", len(names))
	}
	// 4 次命名空间子工具全部补上了前缀；其余 13 次是顶层 shell_command，原样。
	got := map[string]int{}
	for _, n := range names {
		got[n]++
	}
	want := map[string]int{
		"ade_task__taskProgressUpdated":    3,
		"ade_task__humanDecisionRequested": 1,
		"shell_command":                    13,
	}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("回带名 %q 出现 %d 次, 期望 %d 次（全部计数: %v）", name, got[name], n, got)
		}
	}
}
