package protocol_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// 本文件拿**真实 Anthropic 上游转录**驱动 A→CC / A→R 的推理合成（口径层 v0.62）。
//
// 两份样本（anthropic-{stream-thinking,thinking-high}）是 output_config.effort=high 单发
// 触发的思考，thinking 正文整段为空、真内容只有那串 1 KB 的 signature。所以这里钉的正是
// 最危险的那一格：**签名不许当正文写出去**，而空正文也不许凭空开出一个推理块。
//
// 「推理正文要到得了客户端」那一半钉不到——A 源侧带正文的真机转录本库还没有（见样本
// meta.json 的 source）。那一半由手写帧覆盖（各 codec 与 server 的用例）。

func goldenSignature(t *testing.T, raw []byte) string {
	t.Helper()
	// 直接从字节里抠签名，不复用 codec 的解析：断言要查的东西不能由被测代码提供。
	//
	// 取**最长**的那个而不是第一个：流式样本里 content_block_start 先带一个
	// `"signature": ""` 占位，真签名在后面那条 signature_delta 里。
	var sig string
	for _, key := range []string{`"signature": "`, `"signature":"`} {
		rest := raw
		for {
			i := bytes.Index(rest, []byte(key))
			if i < 0 {
				break
			}
			rest = rest[i+len(key):]
			j := bytes.IndexByte(rest, '"')
			if j < 0 {
				break
			}
			if cand := string(rest[:j]); len(cand) > len(sig) {
				sig = cand
			}
			rest = rest[j:]
		}
	}
	if len(sig) < 64 {
		t.Fatalf("抠出来的 signature 只有 %d 字符，不像真签名: %q", len(sig), sig)
	}
	return sig
}

func decodeGoldenThinking(t *testing.T, sample string, stream bool) ([]protocol.Event, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir, sample, "response.raw"))
	if os.IsNotExist(err) {
		t.Skipf("样本尚未采集：%s", sample)
	}
	if err != nil {
		t.Fatal(err)
	}
	sig := goldenSignature(t, raw)

	codec := anthropic.NewCodec()
	if !stream {
		events, err := codec.DecodeFullBody(raw)
		if err != nil {
			t.Fatalf("DecodeFullBody: %v", err)
		}
		return events, sig
	}
	ch, err := codec.DecodeStream(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var events []protocol.Event
	for ev := range ch {
		events = append(events, ev)
	}
	return events, sig
}

// 解码侧：签名走 ThinkingSignature 通道，正文那条是空串（样本形态）。
func TestGoldenAnthropicThinkingChannels(t *testing.T) {
	for _, tc := range []struct {
		sample string
		stream bool
	}{
		{"anthropic-stream-thinking", true},
		{"anthropic-thinking-high", false},
	} {
		t.Run(tc.sample, func(t *testing.T) {
			events, sig := decodeGoldenThinking(t, tc.sample, tc.stream)

			var sawSignature bool
			for _, ev := range events {
				if ev.Type != protocol.EvThinkingDelta {
					continue
				}
				switch ev.Channel {
				case protocol.ThinkingSignature:
					sawSignature = true
					if ev.Text != sig {
						t.Errorf("签名事件的正文与样本里那串不一致")
					}
				case protocol.ThinkingBody, protocol.ThinkingSummary:
					// 这两份样本的正文恒空——真有正文的样本进来时这条不该拦它，
					// 所以只在非空时报「正文冒出来了」是错的；这里只记形态。
					if ev.Text != "" {
						t.Logf("样本里出现了非空推理正文（形态变了，样本 meta 要更新）: %.40q", ev.Text)
					}
				}
			}
			if !sawSignature {
				t.Error("签名没解成 ThinkingSignature 事件")
			}
		})
	}
}

// 出口侧：三个出口都不许把签名写进下行字节，也不许为空正文开出推理块。
//
// 三个出口都跑（含 A→A 那个形态——虽然线上同协议走透传不进 codec，出口实现本身
// 不该有「源是谁」的分支，口径层 v0.73 ⓪）。
func TestGoldenAnthropicThinkingSignatureNeverReachesAnyOutlet(t *testing.T) {
	outlets := []struct {
		name string
		// enc 走流式，full 走非流式，同一份断言跑两遍（口径层 v0.62 ①）。
		enc  func(w *bytes.Buffer, ch <-chan protocol.Event) error
		full func(events []protocol.Event) ([]byte, error)
		// blockMarks 是「这个出口开了一个推理块」的记号。要精确到不会被 usage 里的
		// reasoning_tokens 一类误伤，所以带上引号与冒号。
		blockMarks []string
	}{
		{
			name:       "anthropic",
			enc:        func(w *bytes.Buffer, ch <-chan protocol.Event) error { return anthropic.NewCodec().EncodeStream(w, ch) },
			full:       func(e []protocol.Event) ([]byte, error) { return anthropic.NewCodec().EncodeFullBody(e) },
			blockMarks: []string{`"type":"thinking"`, "thinking_delta"},
		},
		{
			name:       "openaicc",
			enc:        func(w *bytes.Buffer, ch <-chan protocol.Event) error { return openaicc.NewCodec().EncodeStream(w, ch) },
			full:       func(e []protocol.Event) ([]byte, error) { return openaicc.NewCodec().EncodeFullBody(e) },
			blockMarks: []string{"reasoning_content"},
		},
		{
			name: "openairesponses",
			enc: func(w *bytes.Buffer, ch <-chan protocol.Event) error {
				return openairesponses.NewCodec().EncodeStream(w, ch)
			},
			full:       func(e []protocol.Event) ([]byte, error) { return openairesponses.NewCodec().EncodeFullBody(e) },
			blockMarks: []string{`"type":"reasoning"`, "reasoning_summary"},
		},
	}

	for _, tc := range []struct {
		sample string
		stream bool
	}{
		{"anthropic-stream-thinking", true},
		{"anthropic-thinking-high", false},
	} {
		events, sig := decodeGoldenThinking(t, tc.sample, tc.stream)
		for _, out := range outlets {
			t.Run(tc.sample+"→"+out.name, func(t *testing.T) {
				check := func(path, wire string) {
					if strings.Contains(wire, sig) {
						t.Errorf("%s: 签名整串漏进了下行字节", path)
					}
					// 前 32 字符也查一遍：分片截断的签名同样是密文。
					if strings.Contains(wire, sig[:32]) {
						t.Errorf("%s: 签名的前段漏进了下行字节", path)
					}
					// 样本的推理正文恒空 → 出口不该开出任何推理块。
					for _, mark := range out.blockMarks {
						if strings.Contains(wire, mark) {
							t.Errorf("%s: 正文为空却开出了推理块（%s）:\n%s", path, mark, wire)
						}
					}
				}

				ch := make(chan protocol.Event, len(events))
				for _, ev := range events {
					ch <- ev
				}
				close(ch)
				var buf bytes.Buffer
				if err := out.enc(&buf, ch); err != nil {
					t.Fatalf("EncodeStream: %v", err)
				}
				check("流式", buf.String())

				body, err := out.full(events)
				if err != nil {
					t.Fatalf("EncodeFullBody: %v", err)
				}
				check("非流式", string(body))
			})
		}
	}
}
