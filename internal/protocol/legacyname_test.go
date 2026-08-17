package protocol_test

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// v0.36 把线上取值 `openai_cc` 改成了 `openai`。旧名在**读**侧永远收（手写配置、
// GOLDENREC_PROTOCOL 这类不经过库迁移的入口），在**写**侧永远不吐。

func TestNormalizeFoldsLegacyName(t *testing.T) {
	if got := protocol.Normalize("openai_cc"); got != protocol.OpenAI {
		t.Errorf("Normalize(openai_cc) = %q，期望 %q", got, protocol.OpenAI)
	}
	for _, p := range []protocol.Protocol{protocol.OpenAI, protocol.OpenAIResponses, protocol.Anthropic} {
		if got := protocol.Normalize(p); got != p {
			t.Errorf("Normalize(%q) = %q，现名该原样返回", p, got)
		}
	}
	// 未知值原样透出去，由 Valid 去拒——在这儿吞掉只会让错误信息说不清是哪个值。
	if got := protocol.Normalize("gemini"); got != "gemini" {
		t.Errorf("Normalize(gemini) = %q，期望原样返回", got)
	}
}

// 旧名不进枚举：Valid 只认现名，别名单独走 Normalize。两者混在一起的话，某天
// String() 就会把旧名重新写回库里。
func TestLegacyNameIsNotValid(t *testing.T) {
	if protocol.Protocol("openai_cc").Valid() {
		t.Error("openai_cc 不该是合法取值，它只是 Normalize 认的别名")
	}
	if protocol.Protocol("gemini").Valid() {
		t.Error("gemini 还没有 codec，不该是合法取值")
	}
}

func TestParseSetAcceptsLegacyNameAndEmitsCurrent(t *testing.T) {
	set, err := protocol.ParseSet("openai_cc,anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if got := set.String(); got != "openai,anthropic" {
		t.Errorf("String() = %q，期望 %q——读收旧名，写只吐现名", got, "openai,anthropic")
	}
}

// 折完重名要去重：`openai,openai_cc` 是同一个协议写了两遍，解出来该是一元集合。
func TestParseSetDedupesAcrossLegacyName(t *testing.T) {
	set, err := protocol.ParseSet("openai,openai_cc")
	if err != nil {
		t.Fatal(err)
	}
	if got := set.String(); got != "openai" {
		t.Errorf("String() = %q，期望 %q", got, "openai")
	}
}

// goldenrec 的 GOLDENREC_PROTOCOL 是别名要兜的读侧入口之一（它手写、不经过库迁移）。
// 这条钉的是 cmd/goldenrec 那边「先 Normalize 再 Valid」的顺序：反过来的话 Valid
// 故意不收旧名，已有的采集环境会当场被打死。
func TestLegacyNameSurvivesNormalizeThenValid(t *testing.T) {
	if !protocol.Normalize("openai_cc").Valid() {
		t.Error("折完就该是合法取值——顺序反了 goldenrec 会拒收旧的 GOLDENREC_PROTOCOL")
	}
}
