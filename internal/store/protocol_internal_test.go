package store

import (
	"errors"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 空串是这一列最常见的正常值：绝大多数模型不声明子集，继承渠道全集（口径层 v0.40）。
// 它不能进 ParseSet——那边对空输入是报错的。
func TestPickProtocolEmptyModelColumnInherits(t *testing.T) {
	got, err := pickProtocol("anthropic,openai", "", protocol.Anthropic)
	if err != nil {
		t.Fatalf("继承路径不该报错: %v", err)
	}
	if got != protocol.Anthropic {
		t.Errorf("= %q，期望透传 anthropic", got)
	}
}

// 模型把渠道收窄掉入站协议时，回退到能到达的那个，而不是把请求发去一个 404。
// 这正是「渠道级探测全通、请求照样 404」那个成因要被堵掉的地方。
func TestPickProtocolNarrowsToModelSubset(t *testing.T) {
	got, err := pickProtocol("anthropic,openai", "openai", protocol.Anthropic)
	if err != nil {
		t.Fatalf("收窄路径不该报错: %v", err)
	}
	if got != protocol.OpenAI {
		t.Errorf("= %q，期望回退到 openai", got)
	}
}

// 交集为空 → ErrNoUsableCandidate（503），**不是** 500。这是合法配置下的正常收场：
// 渠道协议集缩小之后，模型上那份没跟着改的子集就与它不再重合，跟「渠道停用」「凭证
// 归零」同一档。报 500 会把人引去查一个并不存在的数据损坏。
func TestPickProtocolEmptyIntersectionIsNoUsableCandidate(t *testing.T) {
	_, err := pickProtocol("anthropic", "openai", protocol.Anthropic)
	if !errors.Is(err, ErrNoUsableCandidate) {
		t.Fatalf("err = %v，期望 ErrNoUsableCandidate", err)
	}
}

// 两列解析失败仍是 500 那一档：启动闸扫过全部未停用渠道，真走到这儿说明库是在运行中
// 被手写 SQL 改坏的，不能跟「配置上现在用不了」混成同一种收场。
func TestPickProtocolMalformedColumnsAreNotNoUsableCandidate(t *testing.T) {
	for _, tc := range []struct{ name, channel, model string }{
		{"渠道列坏", "gemini", ""},
		{"模型列坏", "openai", "gemini"},
		{"渠道列空", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pickProtocol(tc.channel, tc.model, protocol.OpenAI)
			if err == nil {
				t.Fatal("应当报错")
			}
			if errors.Is(err, ErrNoUsableCandidate) {
				t.Errorf("err = %v，解析失败不该退化成 503", err)
			}
		})
	}
}

// 旧协议名在模型这一列上也要折成现名，跟渠道那一列共用同一套 ParseSet 规则。
func TestPickProtocolFoldsLegacyNameInModelColumn(t *testing.T) {
	got, err := pickProtocol("openai,anthropic", "openai_cc", protocol.Anthropic)
	if err != nil {
		t.Fatalf("折旧名不该报错: %v", err)
	}
	if got != protocol.OpenAI {
		t.Errorf("= %q，期望 openai_cc 折成 openai 后命中", got)
	}
}
