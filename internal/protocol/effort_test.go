package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// 本文件测思考档位的**六条路全开**直传（口径层 v0.65 + v0.72/v0.73）。
//
// 放在 protocol_test 而不是某个 codec 包里：要测的是「A 入口发的档位到得了 CC 出口」
// 这种跨包的事，钉在单个 codec 里只能各测半条路，而这一票最容易错的就是「某个出口漏
// 接了某个入口」。
//
// 三个协议的档位落点：
//
//	Anthropic  output_config.effort
//	CC         reasoning_effort（顶层字符串）
//	Responses  reasoning.effort
type effortCodec struct {
	name string
	// req 造一个带指定档位的入站请求体（effort 为空时不带任何思考参数）。
	req func(effort string) string
	// decode / encode 是这个协议的入口与出口。
	decode func(t *testing.T, body string) *protocol.Request
	encode func(t *testing.T, req *protocol.Request) (map[string]json.RawMessage, protocol.Drops)
	// effortKey 是出口方向档位应该落到的顶层键。
	effortKey string
	// want 是那个键的期望字节（%s 换成档位）。
	want func(effort string) string
}

// requestDecoder 是三个 codec 的入口方法的公共形状（protocol.Codec 的一小片）。
type requestDecoder interface {
	DecodeRequest([]byte, bool) (*protocol.Request, error)
}

func decodeWith(t *testing.T, dec requestDecoder, body string) *protocol.Request {
	t.Helper()
	req, err := dec.DecodeRequest([]byte(body), false)
	if err != nil {
		t.Fatalf("解码失败: %v\n%s", err, body)
	}
	return req
}

func encodeWith(t *testing.T, enc protocol.RequestEncodeReporter, req *protocol.Request) (map[string]json.RawMessage, protocol.Drops) {
	t.Helper()
	body, dropped, err := enc.EncodeRequestReport(req, false)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("编出来的不是 JSON 对象: %v\n%s", err, body)
	}
	return keys, dropped
}

func effortCodecs() []effortCodec {
	return []effortCodec{
		{
			name: "anthropic",
			req: func(effort string) string {
				out := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]`
				if effort != "" {
					out += `,"output_config":{"effort":"` + effort + `"}`
				}
				return out + "}"
			},
			decode: func(t *testing.T, body string) *protocol.Request {
				return decodeWith(t, anthropic.NewCodec(), body)
			},
			encode: func(t *testing.T, req *protocol.Request) (map[string]json.RawMessage, protocol.Drops) {
				return encodeWith(t, anthropic.NewCodec(anthropic.Options{DefaultMaxTokens: 8192}), req)
			},
			effortKey: "output_config",
			want:      func(effort string) string { return `{"effort":"` + effort + `"}` },
		},
		{
			name: "openaicc",
			req: func(effort string) string {
				out := `{"model":"m","messages":[{"role":"user","content":"hi"}]`
				if effort != "" {
					out += `,"reasoning_effort":"` + effort + `"`
				}
				return out + "}"
			},
			decode: func(t *testing.T, body string) *protocol.Request {
				return decodeWith(t, openaicc.NewCodec(), body)
			},
			encode: func(t *testing.T, req *protocol.Request) (map[string]json.RawMessage, protocol.Drops) {
				return encodeWith(t, openaicc.NewCodec(), req)
			},
			effortKey: "reasoning_effort",
			want:      func(effort string) string { return `"` + effort + `"` },
		},
		{
			name: "openairesponses",
			req: func(effort string) string {
				out := `{"model":"m","input":[{"type":"message","role":"user","content":"hi"}]`
				if effort != "" {
					out += `,"reasoning":{"effort":"` + effort + `"}`
				}
				return out + "}"
			},
			decode: func(t *testing.T, body string) *protocol.Request {
				return decodeWith(t, openairesponses.NewCodec(), body)
			},
			encode: func(t *testing.T, req *protocol.Request) (map[string]json.RawMessage, protocol.Drops) {
				return encodeWith(t, openairesponses.NewCodec(), req)
			},
			effortKey: "reasoning",
			want:      func(effort string) string { return `{"effort":"` + effort + `"}` },
		},
	}
}

// 六条路（三入口 × 两个异协议出口）都要把档位原样送到，一条都不许漏接。
func TestEffortPassesAllSixPaths(t *testing.T) {
	codecs := effortCodecs()
	for _, in := range codecs {
		for _, out := range codecs {
			if in.name == out.name {
				// 同协议不经过 codec（透传保真优先），这里没有路要测。
				continue
			}
			t.Run(in.name+"→"+out.name, func(t *testing.T) {
				req := in.decode(t, in.req("high"))
				if req.Effort != "high" {
					t.Fatalf("入口没把档位提成一等字段: Effort=%q Extras=%v", req.Effort, req.Extras)
				}
				keys, _ := out.encode(t, req)
				if got := string(keys[out.effortKey]); got != out.want("high") {
					t.Errorf("%s = %s，期望 %s", out.effortKey, got, out.want("high"))
				}
			})
		}
	}
}

// Anthropic 出口只写 output_config.effort，**不许写 thinking**：写它等于替客户端开思考
// （口径层 v0.65 ④），旧式 thinking:{type:enabled,budget_tokens} 更是替它猜了个预算。
func TestEffortToAnthropicWritesNoThinkingKey(t *testing.T) {
	for _, in := range effortCodecs() {
		if in.name == "anthropic" {
			continue
		}
		t.Run(in.name, func(t *testing.T) {
			req := in.decode(t, in.req("high"))
			keys, _ := encodeWith(t, anthropic.NewCodec(anthropic.Options{DefaultMaxTokens: 8192}), req)
			if got := string(keys["output_config"]); got != `{"effort":"high"}` {
				t.Errorf("output_config = %s", got)
			}
			if _, ok := keys["thinking"]; ok {
				t.Errorf("出向请求里冒出了 thinking 键: %s", keys["thinking"])
			}
		})
	}
}

// 域外取值原样直传、不钳（口径层 v0.65 ⑤）：上游一个看得见的 400 好过网关静默降档。
//
// xhigh / max 是 Anthropic 官方域里的（low|medium|high|xhigh|max），但 CC 与 Responses
// 官方只认到 high——照发，让上游自己说不行。
func TestEffortOutOfDomainIsNotClamped(t *testing.T) {
	codecs := effortCodecs()
	for _, effort := range []string{"xhigh", "max", "没见过的档"} {
		for _, in := range codecs {
			for _, out := range codecs {
				if in.name == out.name {
					continue
				}
				t.Run(effort+"/"+in.name+"→"+out.name, func(t *testing.T) {
					req := in.decode(t, in.req(effort))
					keys, _ := out.encode(t, req)
					if got := string(keys[out.effortKey]); got != out.want(effort) {
						t.Errorf("%s = %s，期望原样 %s（不钳、不降档）", out.effortKey, got, out.want(effort))
					}
				})
			}
		}
	}
}

// 客户端没发档位，出口就一个字节都不加（口径层 v0.65 ⑥：网关不替客户端开思考）。
func TestEffortAbsentAddsNothing(t *testing.T) {
	codecs := effortCodecs()
	for _, in := range codecs {
		for _, out := range codecs {
			if in.name == out.name {
				continue
			}
			t.Run(in.name+"→"+out.name, func(t *testing.T) {
				req := in.decode(t, in.req(""))
				if req.Effort != "" {
					t.Fatalf("没发档位却解出了 Effort=%q", req.Effort)
				}
				keys, dropped := out.encode(t, req)
				if _, ok := keys[out.effortKey]; ok {
					t.Errorf("凭空加了 %s = %s", out.effortKey, keys[out.effortKey])
				}
				for _, d := range dropped {
					if d.Kind == "thinking_param" {
						t.Error("没有思考参数却登记了 thinking_param")
					}
				}
			})
		}
	}
}

// 数值预算不换算成档位（参考实现之间互相矛盾：new-api high=4096 vs
// CLIProxyAPI high=24576），登记进 thinking_param 后丢。thinking 开关本身同理。
func TestThinkingParamsAreRegisteredThenDropped(t *testing.T) {
	for _, tc := range []struct{ name, extra string }{
		{"数值预算", `"thinking_budget":24576`},
		{"思考开关", `"thinking":{"type":"adaptive","display":"omitted"}`},
		{"旧式预算", `"thinking":{"type":"enabled","budget_tokens":4096}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],` +
				`"output_config":{"effort":"high"},` + tc.extra + `}`
			req := decodeWith(t, anthropic.NewCodec(), body)

			for _, out := range []struct {
				name string
				enc  protocol.RequestEncodeReporter
				key  string
			}{
				{"openaicc", openaicc.NewCodec(), "reasoning_effort"},
				{"openairesponses", openairesponses.NewCodec(), "reasoning"},
			} {
				keys, dropped := encodeWith(t, out.enc, req)
				// 档位照走，思考参数照丢：两件事互不影响。
				if _, ok := keys[out.key]; !ok {
					t.Errorf("%s: 档位没送到", out.name)
				}
				if !hasDrop(dropped, "thinking_param") {
					t.Errorf("%s: 思考参数被静默丢了，dropped=%v", out.name, dropped)
				}
				// 也不许作为「不认识的字段」笼统登记——那两件事排障时要分得开。
				if hasDrop(dropped, "vendor_request") {
					t.Errorf("%s: 思考参数被归进了 vendor_request，dropped=%v", out.name, dropped)
				}
				for _, k := range []string{"thinking", "thinking_budget"} {
					if _, ok := keys[k]; ok {
						t.Errorf("%s: %q 漏给了上游", out.name, k)
					}
				}
			}
		})
	}
}

// 档位解不出来时（非字符串、空串），载体整个留在 Extras——那也得记 thinking_param，
// 不许记成 vendor_request：这一档存在的全部理由就是「我的 effort 到底传没传」在日志上
// 看得见（口径层 v0.65 ⑤）。
//
// 这条是评审揪出来的真洞的回归位：`output_config` 三份镜像表一份都没写进去，于是这个
// 形态在三个出口（含 A 出口自己）记的都是 vendor_request。表已上提到 protocol。
func TestUnliftableEffortStillRegistersAsThinkingParam(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"anthropic/非字符串", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":123}}`},
		{"anthropic/空串", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":""}}`},
		{"responses/非字符串", `{"model":"m","input":[{"type":"message","role":"user","content":"hi"}],"reasoning":{"effort":123}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req *protocol.Request
			if strings.HasPrefix(tc.name, "anthropic/") {
				req = decodeWith(t, anthropic.NewCodec(), tc.body)
			} else {
				req = decodeWith(t, openairesponses.NewCodec(), tc.body)
			}
			if req.Effort != "" {
				t.Fatalf("解不出来的值被当成了档位: %q", req.Effort)
			}

			for _, out := range []struct {
				name string
				enc  protocol.RequestEncodeReporter
			}{
				{"anthropic", anthropic.NewCodec(anthropic.Options{DefaultMaxTokens: 8192})},
				{"openaicc", openaicc.NewCodec()},
				{"openairesponses", openairesponses.NewCodec()},
			} {
				keys, dropped := encodeWith(t, out.enc, req)
				if !hasDrop(dropped, "thinking_param") {
					t.Errorf("%s: 没记 thinking_param，dropped=%v", out.name, dropped)
				}
				if hasDrop(dropped, "vendor_request") {
					t.Errorf("%s: 记成了 vendor_request，dropped=%v", out.name, dropped)
				}
				// 解不出来的值一个字节都不许发给上游。
				for _, k := range []string{"output_config", "reasoning", "reasoning_effort"} {
					if _, ok := keys[k]; ok {
						t.Errorf("%s: %q 漏给了上游: %s", out.name, k, keys[k])
					}
				}
			}
		})
	}
}

func hasDrop(dropped protocol.Drops, want string) bool {
	for _, d := range dropped {
		if d.Kind == want {
			return true
		}
	}
	return false
}
