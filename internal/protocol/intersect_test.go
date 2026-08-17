package protocol_test

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

func mustSet(t *testing.T, raw string) protocol.Set {
	t.Helper()
	set, err := protocol.ParseSet(raw)
	if err != nil {
		t.Fatalf("ParseSet(%q): %v", raw, err)
	}
	return set
}

// 交集保 receiver 的顺序：调用方传的 receiver 是渠道集，它才是存回库、显示在管理端
// 的那一份，跟着它走读起来最不意外。
func TestIntersectKeepsReceiverOrder(t *testing.T) {
	channel := mustSet(t, "anthropic,openai,openai_responses")
	model := mustSet(t, "openai_responses,anthropic")
	if got := channel.Intersect(model).String(); got != "anthropic,openai_responses" {
		t.Errorf("Intersect = %q，期望按渠道集的顺序", got)
	}
}

// 空交集是**合法结果**不是错误：渠道协议集缩小之后，模型上那份没跟着改的子集就会与
// 它不再重合。调用方据此把候选当「现在用不了」，不是当库坏了（见 store.pickProtocol）。
func TestIntersectCanBeEmpty(t *testing.T) {
	if got := mustSet(t, "anthropic").Intersect(mustSet(t, "openai")); len(got) != 0 {
		t.Errorf("Intersect = %v，期望空集合", got)
	}
}

func TestIntersectWithSelfIsIdentity(t *testing.T) {
	set := mustSet(t, "openai,anthropic")
	if got := set.Intersect(set).String(); got != set.String() {
		t.Errorf("Intersect(self) = %q，期望 %q", got, set.String())
	}
}

// 交集之后仍然走同一套 Choose：模型把渠道收窄到 {openai} 之后，anthropic 入站的请求
// 不再透传，而是按 fallbackOrder 落到 openai 去走转换——这正是要的效果，它换来的是
// 一个能到达的上游，而不是一个 404。
func TestChooseOnIntersectFallsBackInsteadOf404(t *testing.T) {
	effective := mustSet(t, "anthropic,openai").Intersect(mustSet(t, "openai"))
	got, ok := effective.Choose(protocol.Anthropic)
	if !ok {
		t.Fatal("Choose 应当选得出协议")
	}
	if got != protocol.OpenAI {
		t.Errorf("Choose = %q，期望回退到 openai", got)
	}
}
