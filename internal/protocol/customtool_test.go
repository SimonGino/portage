package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 这三件事对称与否此前没有任何一处用例钉着——包装在 openaicc、拆包在
// openairesponses、声明根本没写，谁也验不到另一边。搬到一个包里的**主要收益**就是
// 这条往返用例。
func TestCustomToolArgsRoundTrip(t *testing.T) {
	cases := map[string]string{
		"JS 源码":     `const r = await tools.exec_command({cmd:"cat b.txt"}); text(r.output)`,
		"含引号与反斜杠":   `console.log("a\"b\\c")`,
		"含换行":       "line1\nline2\n",
		"看起来像 JSON": `{"cmd":"ls"}`,
		"单个字符":      "x",
		"全是空白":      "   ",
	}
	for name, original := range cases {
		t.Run(name, func(t *testing.T) {
			wrapped, err := protocol.WrapCustomToolArgs(original)
			if err != nil {
				t.Fatalf("包装失败: %v", err)
			}
			// 包装结果必须是合法 JSON——这正是包装存在的理由（CC 的 arguments 与
			// Anthropic 的 input 都只收 JSON）。
			if !json.Valid([]byte(wrapped)) {
				t.Fatalf("包装结果不是合法 JSON: %s", wrapped)
			}
			if got := protocol.UnwrapCustomToolArgs(wrapped); got != original {
				t.Errorf("往返后 = %q, 原文 %q（包装成 %s）", got, original, wrapped)
			}
		})
	}
}

// 空入参给 {} 而不是空串：目标协议不收空串，而 {} 在两边都是「没参数」的合法说法。
func TestWrapCustomToolArgsEmptyGivesEmptyObject(t *testing.T) {
	wrapped, err := protocol.WrapCustomToolArgs("")
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != "{}" {
		t.Errorf("空入参包成了 %q, 期望 {}", wrapped)
	}
}

// 拆不动就原样返回，不报错也不吞掉：上游未必按我们发过去的形状回话。
func TestUnwrapCustomToolArgsPassesThroughWhatItCannotUnwrap(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"根本不是 JSON":     {`just some text`, `just some text`},
		"没有 input 键":    {`{"cmd":"ls"}`, `{"cmd":"ls"}`},
		"input 不是字符串":   {`{"input":{"cmd":"ls"}}`, `{"input":{"cmd":"ls"}}`},
		"input 是数字":     {`{"input":42}`, `{"input":42}`},
		"JSON 数组":       {`[1,2]`, `[1,2]`},
		"空串":            {``, ``},
		"只有空白":          {"  \n ", ``},
		"两侧空白要修掉":       {"  {\"input\":\"js\"}  ", `js`},
		"input 是空字符串":   {`{"input":""}`, ``},
		"input 旁边还有别的键": {`{"input":"js","extra":1}`, `js`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := protocol.UnwrapCustomToolArgs(c.in); got != c.want {
				t.Errorf("拆 %q = %q, 期望 %q", c.in, got, c.want)
			}
		})
	}
}

// 声明侧与包装侧必须用同一个键。分头写死字符串就是漂移的起点，而漂移的症状是
// 工具结果对不回去且**不报错**。
func TestCustomToolSchemaAgreesWithWrapKey(t *testing.T) {
	schema := protocol.CustomToolSchema()
	if !json.Valid(schema) {
		t.Fatalf("合成的 schema 不是合法 JSON: %s", schema)
	}
	var parsed struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Type != "object" {
		t.Errorf("schema.type = %q, 期望 object", parsed.Type)
	}
	if _, ok := parsed.Properties[protocol.CustomToolArgsKey]; !ok {
		t.Errorf("schema 里没有 %q 这个属性: %s", protocol.CustomToolArgsKey, schema)
	}
	if len(parsed.Required) != 1 || parsed.Required[0] != protocol.CustomToolArgsKey {
		t.Errorf("schema.required = %v, 期望只含 %q", parsed.Required, protocol.CustomToolArgsKey)
	}

	// 模型照这份 schema 填出来的东西，拆包侧必须认得。
	filled, err := json.Marshal(map[string]string{protocol.CustomToolArgsKey: "console.log(1)"})
	if err != nil {
		t.Fatal(err)
	}
	if got := protocol.UnwrapCustomToolArgs(string(filled)); got != "console.log(1)" {
		t.Errorf("照 schema 填的入参拆出来是 %q", got)
	}
	if strings.Count(string(schema), protocol.CustomToolArgsKey) != 2 {
		t.Errorf("schema 里 %q 应恰好出现两次（properties + required）: %s",
			protocol.CustomToolArgsKey, schema)
	}
}
