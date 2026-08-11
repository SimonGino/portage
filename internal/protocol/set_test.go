package protocol_test

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

func TestParseSetAcceptsASingleValue(t *testing.T) {
	// v0.33 之前库里存的就是单值。它必须原样解成一元集合，否则那些行等于要迁移。
	set, err := protocol.ParseSet("openai")
	if err != nil {
		t.Fatalf("解析单值失败: %v", err)
	}
	if set.String() != "openai" {
		t.Errorf("String() = %q", set.String())
	}
}

func TestParseSetTrimsDedupesAndKeepsOrder(t *testing.T) {
	set, err := protocol.ParseSet(" openai_responses , openai ,openai_responses")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 保序：这个串要原样写回库、原样显示在管理端，重排会让人对不上自己填的次序。
	if set.String() != "openai_responses,openai" {
		t.Errorf("String() = %q，期望去重保序", set.String())
	}
}

func TestParseSetRejectsEmptyAndUnknown(t *testing.T) {
	for _, raw := range []string{"", "   ", ",,", "openai,gemini"} {
		if _, err := protocol.ParseSet(raw); err == nil {
			t.Errorf("ParseSet(%q) 应当报错", raw)
		}
	}
}

// 能透传就透传（口径层 v0.33）：入站协议在集合里就用它。
func TestChoosePrefersPassthrough(t *testing.T) {
	set, _ := protocol.ParseSet("openai,openai_responses")
	for _, inbound := range []protocol.Protocol{protocol.OpenAI, protocol.OpenAIResponses} {
		got, ok := set.Choose(inbound)
		if !ok || got != inbound {
			t.Errorf("Choose(%s) = %s, %v；期望原样透传", inbound, got, ok)
		}
	}
}

// 入站不在集合里才回退，顺序固定 cc > responses > anthropic。
func TestChooseFallsBackInFixedOrder(t *testing.T) {
	cases := []struct {
		set     string
		inbound protocol.Protocol
		want    protocol.Protocol
	}{
		{"openai,openai_responses", protocol.Anthropic, protocol.OpenAI},
		{"openai_responses,anthropic", protocol.OpenAI, protocol.OpenAIResponses},
		{"anthropic", protocol.OpenAIResponses, protocol.Anthropic},
		// 集合里的书写顺序不影响回退顺序——它是口径定的固定优先级，不是「先写的先用」。
		{"openai_responses,openai", protocol.Anthropic, protocol.OpenAI},
	}
	for _, tc := range cases {
		set, err := protocol.ParseSet(tc.set)
		if err != nil {
			t.Fatalf("ParseSet(%q): %v", tc.set, err)
		}
		got, ok := set.Choose(tc.inbound)
		if !ok || got != tc.want {
			t.Errorf("Set(%q).Choose(%s) = %s, %v；期望 %s", tc.set, tc.inbound, got, ok, tc.want)
		}
	}
}
