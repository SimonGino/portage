package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// Anthropic 的思考 token 键名与另外两家**都不同形**（口径层 v0.79）：容器名跟
// Responses 一样、字段名跟 CC 一样，两边都不能照抄。真机字节：
//
//	"usage":{"input_tokens":564,"output_tokens":875,
//	         "output_tokens_details":{"thinking_tokens":249}}
//
// 落点还分流式与非流式：非流式在顶层 usage 里，流式**只在 message_delta**——
// message_start 那帧连 output_tokens_details 这个容器都没有。
const (
	reasoningStreamHead = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_r1","usage":{"input_tokens":564,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":3}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":564,"output_tokens":875`
	reasoningStreamTail = `}}

event: message_stop
data: {"type":"message_stop"}

`
)

// reasoningStream 拼一条只在收尾帧带（或不带）思考数的流。details 传空串表示
// 「上游整个容器都不发」。
func reasoningStream(details string) string {
	return reasoningStreamHead + details + reasoningStreamTail
}

func reasoningBody(details string) string {
	return `{"id":"msg_r2","type":"message","role":"assistant","model":"claude-sonnet-5",` +
		`"content":[{"type":"text","text":"你好"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":564,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,` +
		`"output_tokens":1110` + details + `}}`
}

// Tap 侧的三档（口径层 v0.66 ③）：正数 / 报了 0 / 根本不报。
//
// **报 0 那一档最容易掉**：applyUsage 对其余各项是「只覆盖非零值」（否则
// message_delta 会把 message_start 报过的 input/cache 清掉），思考这一格若跟着那条
// 规则走，`thinking_tokens:0` 就会被当成「这一帧没报」，落库退回 NULL——那正是三档
// 里要分开的两档。判据得是**键在不在**，所以用 *int。
func TestTapReasoningTokensThreeTiers(t *testing.T) {
	for _, c := range []struct {
		name    string
		details string
		want    int
		wantHas bool
	}{
		{"报了正数", `,"output_tokens_details":{"thinking_tokens":249}`, 249, true},
		{"报了 0（这次没思考）", `,"output_tokens_details":{"thinking_tokens":0}`, 0, true},
		{"整个容器都不发", "", 0, false},
		{"容器在但字段缺（中转裁剪）", `,"output_tokens_details":{}`, 0, false},
	} {
		t.Run("流式/"+c.name, func(t *testing.T) {
			got := feed(t, NewTap(true), reasoningStream(c.details))
			if got.ReasoningTokens != c.want || got.HasReasoningTokens != c.wantHas {
				t.Errorf("ReasoningTokens/Has = %d/%v, 期望 %d/%v（%+v）",
					got.ReasoningTokens, got.HasReasoningTokens, c.want, c.wantHas, got)
			}
			// 顺带钉住：思考这一格的解析不能把其余各项带歪。
			if got.InputTokens != 564 || got.OutputTokens != 875 {
				t.Errorf("其余 usage 项被带歪了: %+v", got)
			}
		})
		t.Run("非流式/"+c.name, func(t *testing.T) {
			got := feed(t, NewTap(false), reasoningBody(c.details))
			if got.ReasoningTokens != c.want || got.HasReasoningTokens != c.wantHas {
				t.Errorf("ReasoningTokens/Has = %d/%v, 期望 %d/%v（%+v）",
					got.ReasoningTokens, got.HasReasoningTokens, c.want, c.wantHas, got)
			}
		})
	}
}

// 解码侧：思考数要进 canonical，否则 A→CC / A→R 的出口 usage 里这个数就没了
// （Responses 出口的 reasoning_tokens 恒写，丢了会写成 0——比 NULL 更糟，那是假数）。
//
// 流式那半边只有 message_delta 带它，靠 MergeSnapshot 的非零覆盖攒到一起，与
// output_tokens 同一条路径。
func TestDecodeCarriesThinkingTokensIntoCanonical(t *testing.T) {
	t.Run("流式", func(t *testing.T) {
		var merged protocol.Usage
		var seen int
		for _, ev := range collect(t, reasoningStream(`,"output_tokens_details":{"thinking_tokens":249}`)) {
			if ev.Type == protocol.EvUsage {
				seen++
				merged.MergeSnapshot(*ev.Usage)
			}
		}
		if seen != 2 {
			t.Fatalf("放出了 %d 次 usage, 期望 2", seen)
		}
		if merged.ReasoningTokens != 249 {
			t.Errorf("ReasoningTokens = %d, 期望 249（%+v）", merged.ReasoningTokens, merged)
		}
	})

	t.Run("非流式", func(t *testing.T) {
		events, err := NewCodec().DecodeFullBody([]byte(
			reasoningBody(`,"output_tokens_details":{"thinking_tokens":310}`)))
		if err != nil {
			t.Fatal(err)
		}
		var merged protocol.Usage
		for _, ev := range events {
			if ev.Type == protocol.EvUsage {
				merged.MergeSnapshot(*ev.Usage)
			}
		}
		if merged.ReasoningTokens != 310 {
			t.Errorf("ReasoningTokens = %d, 期望 310（%+v）", merged.ReasoningTokens, merged)
		}
	})
}

// 编码侧：**有数才写**，与同一个函数里缓存两项「恒写出来哪怕是 0」相反。
// 那两项的 0 是真的「一次都没命中」，这里的 0 会被读成「这次没思考」，而 canonical
// 侧零值即「上游没报」。理由与形态都同 CC 出口的 completion_tokens_details。
func TestUsageBodyOmitsThinkingWhenAbsent(t *testing.T) {
	for _, c := range []struct {
		name string
		u    protocol.Usage
		want any // nil = 整个 details 不该出现
	}{
		{"有数就写", protocol.Usage{InputTokens: 564, OutputTokens: 1110, ReasoningTokens: 310}, float64(310)},
		{"没数就不写", protocol.Usage{InputTokens: 564, OutputTokens: 1110}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(usageBody(c.u))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			d, ok := got["output_tokens_details"].(map[string]any)
			if c.want == nil {
				if ok {
					t.Errorf("不该出现 output_tokens_details，实得 %v", d)
				}
				return
			}
			if !ok {
				t.Fatalf("缺 output_tokens_details，实得 %v", got)
			}
			if d["thinking_tokens"] != c.want {
				t.Errorf("thinking_tokens = %v, 期望 %v", d["thinking_tokens"], c.want)
			}
			// 缓存两项照旧恒在，不因为多了这一格而漏。
			if _, ok := got["cache_read_input_tokens"]; !ok {
				t.Errorf("cache_read_input_tokens 不见了: %v", got)
			}
		})
	}
}
