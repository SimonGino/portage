package protocol_test

import (
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 毛值减回净值：Anthropic 出口的 input_tokens 与两项缓存互不相交（portage-legacy#72）。
func TestUsageNetInput(t *testing.T) {
	cases := []struct {
		name string
		u    protocol.Usage
		want int
	}{
		{"减掉缓存两项", protocol.Usage{InputTokens: 1000, CacheReadTokens: 800, CacheWriteTokens: 150}, 50},
		{"没有缓存时等于毛值", protocol.Usage{InputTokens: 69}, 69},
		{"缓存大于毛值时钳零", protocol.Usage{InputTokens: 10, CacheReadTokens: 99}, 0},
		{"全零", protocol.Usage{}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.u.NetInput(); got != c.want {
				t.Errorf("NetInput() = %d, 期望 %d", got, c.want)
			}
		})
	}
}

// EvUsage 的语义是**累计快照**，后来者的非零字段覆盖先前值（event.go）。整结构体
// 覆盖会让「只报 output_tokens」的兼容上游把 input 清零（portage-legacy#72）。
func TestUsageMergeSnapshot(t *testing.T) {
	cases := []struct {
		name       string
		base, next protocol.Usage
		want       protocol.Usage
	}{{
		name: "只报 output 时保住先前的 input 与缓存",
		base: protocol.Usage{InputTokens: 100, CacheReadTokens: 80, CacheWriteTokens: 10},
		next: protocol.Usage{OutputTokens: 42},
		want: protocol.Usage{InputTokens: 100, OutputTokens: 42, CacheReadTokens: 80, CacheWriteTokens: 10},
	}, {
		name: "全套快照逐字段覆盖",
		base: protocol.Usage{InputTokens: 100, OutputTokens: 1, CacheReadTokens: 80},
		next: protocol.Usage{InputTokens: 120, OutputTokens: 42, CacheReadTokens: 90, CacheWriteTokens: 5},
		want: protocol.Usage{InputTokens: 120, OutputTokens: 42, CacheReadTokens: 90, CacheWriteTokens: 5},
	}, {
		name: "空快照什么都不动",
		base: protocol.Usage{InputTokens: 100, OutputTokens: 42},
		next: protocol.Usage{},
		want: protocol.Usage{InputTokens: 100, OutputTokens: 42},
	}, {
		name: "起手就是全套时照单全收",
		base: protocol.Usage{},
		next: protocol.Usage{InputTokens: 7, OutputTokens: 3},
		want: protocol.Usage{InputTokens: 7, OutputTokens: 3},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.base
			got.MergeSnapshot(c.next)
			if got != c.want {
				t.Errorf("合并后 = %+v, 期望 %+v", got, c.want)
			}
		})
	}
}
