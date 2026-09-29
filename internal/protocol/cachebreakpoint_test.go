package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// 转换路径的 Anthropic 出口自动打缓存断点（#132）：system 末块（没有 system 就最后一个
// 工具）+ 对话最后一条消息里最后一个能打的块；客户端自己带了断点就一个不补。
// 入站字节用 testdata/golden/in-responses-* 与 in-cc-* 真实发包。

// goldenToAnthropic 把一份入站样本经对应入口解码，再按 stream 编成 Anthropic 请求体。
// mutate 在 canonical 上改出样本里没有的形态（无 system、thinking 结尾等）。
func goldenToAnthropic(t *testing.T, sample string, stream bool, mutate func(*protocol.Request)) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", sample, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dec requestDecoder = openaicc.NewCodec()
	if strings.HasPrefix(sample, "in-responses-") {
		dec = openairesponses.NewCodec()
	}
	req := decodeWith(t, dec, string(raw))
	if mutate != nil {
		mutate(req)
	}
	body, err := anthropic.NewCodec(anthropic.Options{DefaultMaxTokens: 8192}).EncodeRequest(req, stream)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// breakpoints 按出现顺序列出带 cache_control 的位置，形如 system[2]、tools[9]、messages[4][1]。
func breakpoints(out map[string]any) []string {
	var at []string
	mark := func(v any, path func(i int) string) {
		list, _ := v.([]any)
		for i, e := range list {
			if m, _ := e.(map[string]any); m["cache_control"] != nil {
				at = append(at, path(i))
			}
		}
	}
	mark(out["system"], func(i int) string { return "system[" + itoa(i) + "]" })
	mark(out["tools"], func(i int) string { return "tools[" + itoa(i) + "]" })
	msgs, _ := out["messages"].([]any)
	for mi, m := range msgs {
		mm, _ := m.(map[string]any)
		mark(mm["content"], func(i int) string { return "messages[" + itoa(mi) + "][" + itoa(i) + "]" })
	}
	return at
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func lenOf(v any) int { l, _ := v.([]any); return len(l) }

// lastContent 是最后一条消息的块数组。
func lastContent(out map[string]any) (int, []any) {
	msgs, _ := out["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	c, _ := last["content"].([]any)
	return len(msgs) - 1, c
}

var goldenInbound = []string{
	"in-cc-consecutive-user", "in-cc-parallel-turn1", "in-cc-parallel-turn2",
	"in-cc-text", "in-cc-tool-turn1", "in-cc-tool-turn2",
	"in-responses-namespace-turn1", "in-responses-namespace-turn2", "in-responses-parallel-turn2",
	"in-responses-text", "in-responses-tool-turn1", "in-responses-tool-turn2",
}

// 全部样本都带 system：断点落 system 末块与最后一条消息的末块，恰好两个；流式与非流式一致。
func TestAnthropicOutletMarksSystemAndConversationTail(t *testing.T) {
	for _, sample := range goldenInbound {
		t.Run(sample, func(t *testing.T) {
			out := goldenToAnthropic(t, sample, false, nil)
			mi, c := lastContent(out)
			want := []string{
				"system[" + itoa(lenOf(out["system"])-1) + "]",
				"messages[" + itoa(mi) + "][" + itoa(len(c)-1) + "]",
			}
			if got := breakpoints(out); !reflect.DeepEqual(got, want) {
				t.Errorf("断点 = %v，期望 %v", got, want)
			}
			stream := goldenToAnthropic(t, sample, true, nil)
			delete(stream, "stream")
			if !reflect.DeepEqual(stream, out) {
				t.Error("流式出口与非流式出口的请求体不一致")
			}
		})
	}
}

// stripSystem 去掉 canonical 里全部 system（顶层与 developer 归一来的 RoleSystem 消息）。
func stripSystem(req *protocol.Request) {
	req.System = nil
	msgs := req.Messages[:0]
	for _, m := range req.Messages {
		if m.Role != protocol.RoleSystem {
			msgs = append(msgs, m)
		}
	}
	req.Messages = msgs
}

// 没有 system：第一个断点挪到最后一个工具上；工具也没有就只剩对话末尾那一个。
func TestAnthropicOutletMarksLastToolWithoutSystem(t *testing.T) {
	t.Run("有工具", func(t *testing.T) {
		out := goldenToAnthropic(t, "in-cc-tool-turn1", false, stripSystem)
		mi, c := lastContent(out)
		want := []string{
			"tools[" + itoa(lenOf(out["tools"])-1) + "]",
			"messages[" + itoa(mi) + "][" + itoa(len(c)-1) + "]",
		}
		if got := breakpoints(out); !reflect.DeepEqual(got, want) {
			t.Errorf("断点 = %v，期望 %v", got, want)
		}
	})
	t.Run("无工具", func(t *testing.T) {
		out := goldenToAnthropic(t, "in-cc-consecutive-user", false, stripSystem)
		if _, ok := out["tools"]; ok {
			t.Fatal("样本前提不成立：in-cc-consecutive-user 应当没有工具")
		}
		mi, c := lastContent(out)
		want := []string{"messages[" + itoa(mi) + "][" + itoa(len(c)-1) + "]"}
		if got := breakpoints(out); !reflect.DeepEqual(got, want) {
			t.Errorf("断点 = %v，期望 %v", got, want)
		}
	})
}

// 最后一条消息以 thinking 收尾：thinking 不收 cache_control，断点往前落到正文块上。
func TestAnthropicOutletSkipsThinkingAtTail(t *testing.T) {
	out := goldenToAnthropic(t, "in-responses-text", false, func(req *protocol.Request) {
		req.Messages = append(req.Messages, protocol.Message{Role: protocol.RoleAssistant, Content: []protocol.Block{
			{Kind: protocol.BlockText, Text: "先说结论"},
			{Kind: protocol.BlockThinking, Text: "再想想"},
		}})
	})
	mi, c := lastContent(out)
	tail, _ := c[len(c)-1].(map[string]any)
	if tail["type"] != "text" || tail["text"] != "先说结论" {
		t.Fatalf("末条消息末块 = %v，期望正文块", tail)
	}
	want := []string{"system[" + itoa(lenOf(out["system"])-1) + "]", "messages[" + itoa(mi) + "][" + itoa(len(c)-1) + "]"}
	if got := breakpoints(out); !reflect.DeepEqual(got, want) {
		t.Errorf("断点 = %v，期望 %v", got, want)
	}
}

// 客户端自己带了断点（CC content part / 工具上的 cache_control 落在 canonical 的 Extras）：
// 原样带给上游，一个不补。
func TestAnthropicOutletKeepsClientBreakpoints(t *testing.T) {
	clientMark := map[string]any{"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}}
	t.Run("消息块", func(t *testing.T) {
		out := goldenToAnthropic(t, "in-cc-parallel-turn2", false, func(req *protocol.Request) {
			req.Messages[1].Content[0].Extras = clientMark // 首条 user 消息
		})
		if got, want := breakpoints(out), []string{"messages[0][0]"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("断点 = %v，期望只有客户端那一个 %v", got, want)
		}
		msgs, _ := out["messages"].([]any)
		first, _ := msgs[0].(map[string]any)
		block, _ := first["content"].([]any)[0].(map[string]any)
		if !reflect.DeepEqual(block["cache_control"], clientMark["cache_control"]) {
			t.Errorf("cache_control = %v，期望客户端原值", block["cache_control"])
		}
	})
	t.Run("工具", func(t *testing.T) {
		out := goldenToAnthropic(t, "in-cc-tool-turn1", false, func(req *protocol.Request) {
			req.Tools[0].Extras = clientMark
		})
		if got, want := breakpoints(out), []string{"tools[0]"}; !reflect.DeepEqual(got, want) {
			t.Errorf("断点 = %v，期望只有客户端那一个 %v", got, want)
		}
	})
}
