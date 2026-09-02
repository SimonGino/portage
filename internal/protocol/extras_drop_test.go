package protocol_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
	"github.com/SimonGino/portage/internal/protocol/openaicc"
	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

// 本文件测 Extras 顶层键的**分档登记**（protocol.ClassifyExtrasKey，三个出口共用）。
//
// 放在 protocol_test 而不是各 codec 包里，理由同 effort_test.go：要钉的是「三个出口
// 分类字字一样」，各包一份就是被钉的那个东西自己的失败模式——#15 的重复登记正是同一段
// 逻辑镜像两份、两份一起错。
//
// 一个键只登记一次是硬要求：口径层 §2.6 立跨协议丢弃 WARN 的目的是「不做伪映射也不
// 静默」，明处报一个客户端根本没发的字段，等于把这条日志的可信度打掉——真来一个我们认
// 不得的新字段时，没人分得清是新字段还是这个已知的假阳。

// TestExtrasDropsRegisteredOncePerKind：#15 的回归位。
//
// 入站一律走 anthropic 入口（三种 Extras 组合它都装得下），出口三个各要一组期望；
// want 是**完整**的 dropped 集合，不是「包含」——多记一条幻影正是本票要防的。
func TestExtrasDropsRegisteredOncePerKind(t *testing.T) {
	const tail = `,"messages":[{"role":"user","content":"hi"}]}`
	const (
		metaOnly     = `{"model":"m","max_tokens":16,"metadata":{"user_id":"u"}` + tail
		metaUnknown  = `{"model":"m","max_tokens":16,"metadata":{"user_id":"u"},"没见过的键":1` + tail
		metaThinking = `{"model":"m","max_tokens":16,"metadata":{"user_id":"u"},"thinking":{"type":"enabled","budget_tokens":1024}` + tail
	)

	// anthropic 出口没有 metadata 这一档（Anthropic 原生认这个字段），于是 metadata 落进
	// vendor_request——**错档，但不是重复登记**，与分档收口前的行为一致。改这个口径是
	// #19 的事，改的时候这几行会红，是有意的。
	cases := []struct {
		name string
		body string
		want map[string][]string // 出口名 → 完整 dropped 集合
	}{
		{
			name: "只有 metadata",
			body: metaOnly,
			want: map[string][]string{
				"openaicc":        {"metadata"},
				"openairesponses": {"metadata"},
				"anthropic":       {"vendor_request"},
			},
		},
		{
			name: "metadata + 真·未知顶层键",
			body: metaUnknown,
			want: map[string][]string{
				"openaicc":        {"metadata", "vendor_request"},
				"openairesponses": {"metadata", "vendor_request"},
				"anthropic":       {"vendor_request"}, // 两个键同档，登记去重
			},
		},
		{
			name: "metadata + 思考参数",
			body: metaThinking,
			want: map[string][]string{
				"openaicc":        {"metadata", "thinking_param"},
				"openairesponses": {"metadata", "thinking_param"},
				"anthropic":       {"thinking_param", "vendor_request"},
			},
		},
	}

	exits := []struct {
		name string
		enc  protocol.RequestEncodeReporter
	}{
		{"anthropic", anthropic.NewCodec(anthropic.Options{DefaultMaxTokens: 8192})},
		{"openaicc", openaicc.NewCodec()},
		{"openairesponses", openairesponses.NewCodec()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := decodeWith(t, anthropic.NewCodec(), tc.body)
			for _, exit := range exits {
				t.Run(exit.name, func(t *testing.T) {
					body, dropped := encodeWith(t, exit.enc, req)
					got := dropped.Kinds()
					want := append([]string(nil), tc.want[exit.name]...)
					slices.Sort(got)
					slices.Sort(want)
					if strings.Join(got, ",") != strings.Join(want, ",") {
						t.Errorf("dropped = %v, want %v", got, want)
					}
					// 登记归登记，Extras 一个字节都不许外带（三个出口一致）。
					for _, k := range []string{"metadata", "没见过的键", "thinking"} {
						if _, ok := body[k]; ok {
							t.Errorf("Extras 外带了 %q: %s", k, body[k])
						}
					}
				})
			}
		})
	}
}
