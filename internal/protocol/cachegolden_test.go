package protocol_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/anthropic"
)

// 本文件是 #13 的 canonical 半边：cacheSamples（golden_test.go）走 Tap 断的是
// 上游原样的**净值**，这里把同一批真字节喂给 anthropic 解码，断 canonical Usage
// 的 InputTokens 是**加回缓存两项后的毛值**（口径层 v0.71）。两边各有断言、数在
// 缓存非零时必然不同——这正是构造样本时代（cachehit_fixture_test.go）就立下的
// 分工，如今真字节也各走一遍。
//
// 毛值在表里**手写成数**（37 + 7059 = 7096），不调 protocol.GrossInput 现算：
// 用被测函数给自己出期望值，加法错了两边一起错，断言就白断了。
var cacheGoldenGross = map[string]int{
	"anthropic-cache-write":        7096,
	"anthropic-cache-hit":          7096,
	"anthropic-stream-cache-write": 7096,
	"anthropic-stream-cache-hit":   7096,
}

// TestCacheGoldenThroughCanonical：真实转录 → Anthropic 解码 → canonical Usage。
// 缓存两项原样透出、净值加法归一成毛值、NetInput 能减回上游原样，三件一起断。
func TestCacheGoldenThroughCanonical(t *testing.T) {
	for _, name := range cacheSamples {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(goldenDir, name)
			metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			var meta sampleMeta
			if err := json.Unmarshal(metaRaw, &meta); err != nil {
				t.Fatalf("meta.json 解析失败: %v", err)
			}
			if !meta.Verified {
				t.Fatalf("%s 的 meta.json 仍是 verified:false", name)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "response.raw"))
			if err != nil {
				t.Fatal(err)
			}

			codec := anthropic.NewCodec()
			var got protocol.Usage
			var seen int
			collect := func(ev protocol.Event) {
				if ev.Type == protocol.EvUsage && ev.Usage != nil {
					seen++
					got.MergeSnapshot(*ev.Usage)
				}
			}
			if meta.Stream {
				ch, err := codec.DecodeStream(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				for ev := range ch {
					collect(ev)
				}
			} else {
				events, err := codec.DecodeFullBody(raw)
				if err != nil {
					t.Fatal(err)
				}
				for _, ev := range events {
					collect(ev)
				}
			}
			if seen == 0 {
				t.Fatal("一个 EvUsage 都没解出来")
			}

			gross := cacheGoldenGross[name]
			if got.InputTokens != gross {
				t.Errorf("canonical InputTokens = %d, 期望毛值 %d（净值 %d + 缓存两项）",
					got.InputTokens, gross, meta.Expect.InputTokens)
			}
			if got.CacheReadTokens != meta.Expect.CacheReadTokens ||
				got.CacheWriteTokens != meta.Expect.CacheWriteTokens {
				t.Errorf("缓存两项 = (read %d, write %d), 期望 (read %d, write %d)——明细不因归一而丢",
					got.CacheReadTokens, got.CacheWriteTokens,
					meta.Expect.CacheReadTokens, meta.Expect.CacheWriteTokens)
			}
			// 编码回 Anthropic 线上格式时要能减回去，与解码侧的加法互为逆向。
			if net := got.NetInput(); net != meta.Expect.InputTokens {
				t.Errorf("NetInput = %d, 期望回到上游原样的 %d", net, meta.Expect.InputTokens)
			}
		})
	}
}
