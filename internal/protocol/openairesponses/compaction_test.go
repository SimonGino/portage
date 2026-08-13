package openairesponses_test

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol/openairesponses"
)

func TestHasCompactionTrigger(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "尾部带 trigger（Codex 压缩 turn 的实际形态）",
			body: `{"model":"gpt-5","input":[{"type":"message","role":"user","content":"hi"},{"type":"compaction_trigger"}]}`,
			want: true,
		},
		{
			// 位置不当判据：opencodex 见到的是尾项，但没有哪条协议承诺它只能在尾部。
			name: "trigger 不在尾部照样算",
			body: `{"input":[{"type":"compaction_trigger"},{"type":"message","role":"user","content":"hi"}]}`,
			want: true,
		},
		{
			name: "普通 turn",
			body: `{"model":"gpt-5","input":[{"type":"message","role":"user","content":"hi"}]}`,
			want: false,
		},
		{
			name: "input 是字符串",
			body: `{"input":"hello"}`,
			want: false,
		},
		{
			name: "没有 input",
			body: `{"model":"gpt-5"}`,
			want: false,
		},
		{
			// 解不动只让那一项落空：混进来的字符串元素不该把后面的 trigger 遮掉。
			name: "数组里混了解不动的元素",
			body: `{"input":["x",{"type":"compaction_trigger"}]}`,
			want: true,
		},
		{
			name: "整体不是 JSON",
			body: `not json`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := openairesponses.HasCompactionTrigger([]byte(tc.body)); got != tc.want {
				t.Fatalf("HasCompactionTrigger = %v, 期望 %v", got, tc.want)
			}
		})
	}
}
