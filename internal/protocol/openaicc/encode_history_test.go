package openaicc_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// 本文件钉 CC 出口对**历史**的两处整形：system 归位（口径层 v1.25 ②，#114）与
// reasoning_content 回带（口径层 v1.27 ①，#118）。入口一律走真 codec 解码，A 与 R
// 各一路；golden 样本能覆盖的格子用样本，样本里没有的形态（中段 developer、A 入口
// 的 thinking + tool_use）手写请求体，并在用例注释里说明是构造的。

type ccHistory struct {
	Messages []struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent *string         `json:"reasoning_content"`
		ToolCalls        []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
	} `json:"messages"`
}

func encodeHistory(t *testing.T, req *protocol.Request) (string, protocol.Drops, ccHistory) {
	t.Helper()
	body, dropped, err := openaicc.NewCodec().EncodeRequestReport(req, false)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var out ccHistory
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("编出来的不是合法 JSON: %v\n%s", err, body)
	}
	return string(body), dropped, out
}

// decodeResponsesSample 把真实 Responses 入站样本解成 canonical（同 decodeSample 的闸）。
func decodeResponsesSample(t *testing.T, name string) *protocol.Request {
	t.Helper()
	dir := filepath.Join(goldenDir, name)
	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Verified bool `json:"verified"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.Verified {
		t.Fatalf("%s 的 meta.json 仍是 verified:false", name)
	}
	body, err := os.ReadFile(filepath.Join(dir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	return decodeResponses(t, string(body))
}

func decodeResponses(t *testing.T, body string) *protocol.Request {
	t.Helper()
	req, err := openairesponses.NewCodec().DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return req
}

func contentText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("content 不是字符串: %s", raw)
	}
	return s
}

// assertSystemOnlyAtHead：role=system 恰好一条且在最前。
func assertSystemOnlyAtHead(t *testing.T, out ccHistory) {
	t.Helper()
	for i, m := range out.Messages {
		if (m.Role == "system") != (i == 0) {
			t.Errorf("messages[%d].role = %q：system 只许是开头那一条", i, m.Role)
		}
	}
}

// A 入口 mid-conversation-system（golden in-anthropic-tool-turn2：user → system →
// assistant → user）：中段那条改发 user、原位不动，开头只有顶层 System 那一条。
func TestEncodeMidConversationSystemBecomesUser(t *testing.T) {
	req := decodeSample(t, "in-anthropic-tool-turn2")
	const mid = "[redacted mid-conversation-system len=9769]"
	_, _, out := encodeHistory(t, req)

	assertSystemOnlyAtHead(t, out)
	// 顶层 System 之后依次是 user、中段 system（现为 user）、assistant。
	if len(out.Messages) < 4 {
		t.Fatalf("只编出 %d 条消息", len(out.Messages))
	}
	if m := out.Messages[2]; m.Role != "user" || contentText(t, m.Content) != mid {
		t.Errorf("messages[2] = %s %s，期望中段 system 原位改发 user", m.Role, m.Content)
	}
	if out.Messages[3].Role != "assistant" {
		t.Errorf("messages[3].role = %q，中段那条的位置变了", out.Messages[3].Role)
	}
}

// R 入口 instructions + 开头 developer（golden in-responses-namespace-turn2，ADE 实采）：
// 并成开头一条 system，instructions 在前、developer 在后。
func TestEncodeInstructionsAndLeadingDeveloperMerge(t *testing.T) {
	req := decodeResponsesSample(t, "in-responses-namespace-turn2")
	_, _, out := encodeHistory(t, req)

	assertSystemOnlyAtHead(t, out)
	head := contentText(t, out.Messages[0].Content)
	ins := strings.Index(head, "[redacted instructions")
	dev := strings.Index(head, "[redacted developer text")
	if ins < 0 || dev < 0 || ins > dev {
		t.Errorf("开头 system 没按原序并入 instructions 与 developer:\n%.300s", head)
	}
}

// 中段 developer 通知（Codex 换模型时插的那种）改发 user。**构造样本**：真实采样里
// developer 全在最前（MVP §9.2），形状照 sub2api normalizeResponsesDerivedChatMessageRoles
// 的描述手写。
func TestEncodeMidConversationDeveloperBecomesUser(t *testing.T) {
	req := decodeResponses(t, `{"model":"m","instructions":"ins",`+
		`"input":[`+
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev-head"}]},`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"u1"}]},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]},`+
		`{"type":"message","role":"developer","content":[{"type":"input_text","text":"model switched"}]},`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"u2"}]}]}`)
	_, _, out := encodeHistory(t, req)

	assertSystemOnlyAtHead(t, out)
	var got []string
	for _, m := range out.Messages {
		got = append(got, m.Role+":"+contentText(t, m.Content))
	}
	want := []string{"system:ins\ndev-head", "user:u1", "assistant:a1", "user:model switched", "user:u2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("messages = %q\n期望 %q", got, want)
	}
}

// R→CC 多轮工具调用，有明文（golden in-responses-namespace-turn2：reasoning 带
// summary 文本，与 function_call 同属一条 assistant 消息）：reasoning_content 回带。
func TestEncodeResponsesReasoningSummaryReplaysAsReasoningContent(t *testing.T) {
	req := decodeResponsesSample(t, "in-responses-namespace-turn2")
	_, _, out := encodeHistory(t, req)

	var carried int
	for i, m := range out.Messages {
		if m.ReasoningContent == nil {
			continue
		}
		if len(m.ToolCalls) == 0 {
			t.Errorf("messages[%d] 没有 tool_calls 却带了 reasoning_content", i)
		}
		if !strings.Contains(*m.ReasoningContent, "[redacted reasoning summary") {
			t.Errorf("messages[%d].reasoning_content = %q，不是 summary 明文", i, *m.ReasoningContent)
		}
		carried++
	}
	if carried == 0 {
		t.Error("样本里的 reasoning 带 summary 明文，却一条 reasoning_content 都没回带")
	}
}

// R→CC 无明文（golden in-responses-tool-turn2：summary 为空、只有密文）：不发
// reasoning_content、不填占位，密文不外带且登记丢弃。
func TestEncodeResponsesEncryptedOnlyReasoningNotReplayed(t *testing.T) {
	req := decodeResponsesSample(t, "in-responses-tool-turn2")
	body, dropped, _ := encodeHistory(t, req)

	if strings.Contains(body, "reasoning_content") {
		t.Errorf("无明文却发了 reasoning_content:\n%s", body)
	}
	if strings.Contains(body, "encrypted_content") {
		t.Errorf("密文漏进了 CC 请求:\n%s", body)
	}
	if !dropped.Has(openaicc.DropThinking) {
		t.Errorf("丢了密文却没登记: %v", dropped.Kinds())
	}
}

// A→CC 多轮工具调用。**构造样本**：Claude Code 带 thinking 的工具轮尚无实采
// （in-anthropic-thinking-replay 那轮没有 tool_use）。三条 assistant：有明文 + 工具调用
// → 回带；只有签名 + 工具调用 → 不带；有明文但没工具调用 → 不带（范围只在 tool_calls 上）。
func TestEncodeAnthropicThinkingReplaysOnToolCallMessages(t *testing.T) {
	req := decodeAnthropic(t, `{"model":"m","max_tokens":10,"messages":[`+
		`{"role":"user","content":"读 a"},`+
		`{"role":"assistant","content":[`+
		`{"type":"thinking","thinking":"先读 a","signature":"sig-a"},`+
		`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"p":"a"}}]},`+
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"A"}]},`+
		`{"role":"assistant","content":[`+
		`{"type":"thinking","thinking":"","signature":"sig-b"},`+
		`{"type":"tool_use","id":"toolu_2","name":"Read","input":{"p":"b"}}]},`+
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"B"}]},`+
		`{"role":"assistant","content":[`+
		`{"type":"thinking","thinking":"读完了","signature":"sig-c"},`+
		`{"type":"text","text":"好了"}]},`+
		`{"role":"user","content":"继续"}]}`)
	body, dropped, out := encodeHistory(t, req)

	var got []string
	for _, m := range out.Messages {
		if m.Role != "assistant" {
			continue
		}
		rc := "<none>"
		if m.ReasoningContent != nil {
			rc = *m.ReasoningContent
		}
		got = append(got, rc)
	}
	want := []string{"先读 a", "<none>", "<none>"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("三条 assistant 的 reasoning_content = %q，期望 %q", got, want)
	}
	if strings.Contains(body, "sig-") || strings.Contains(body, "signature") {
		t.Errorf("签名漏进了 CC 请求:\n%s", body)
	}
	if !dropped.Has(openaicc.DropThinking) {
		t.Errorf("签名被丢却没登记: %v", dropped.Kinds())
	}
}
