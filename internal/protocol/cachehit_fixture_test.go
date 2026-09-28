package protocol_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/codecs"
	"github.com/SimonGino/portage/internal/protocol/taps"
)

// fixtureDir 放的是**构造样本**，与 goldenDir 的真实转录分属两档，别混——两边的
// 闸门也不同：golden 认 `verified: true`（人核过的转录），这边认 `synthetic: true`
// （钉死它不是转录）。缘由与升格规矩见 testdata/fixtures/README.md。
const fixtureDir = "../../testdata/fixtures"

// cacheHitFixtures 补的是 #2 第 2 项：六份真实 anthropic-* 样本的 cache 计数全是 0
// （中转那侧不回），于是缓存两项的**Anthropic 侧**解析路径此前没有任何样本走到。
//
// 这两份从真实转录派生、只改 usage 数字，形状依据官方文档：input_tokens 只算最后一个
// 缓存断点**之后**的 token，与两项缓存互不相交（platform.claude.com/docs/en/api/rate-limits，
// 2026-08-13 核对）。它证明不了「官方真的这么发」——那是 #2 拿官方 key 才能收的口。
//
// 后五份是 #111 补的**缓存写入**形状：OpenAI 官方把写入量报在 CC 的
// prompt_tokens_details.cache_write_tokens / R 的 input_tokens_details.cache_write_tokens，
// 百炼报在 prompt_tokens_details.cache_creation_input_tokens，此前 CC 两侧与 R Tap
// 都不读，写入部分被按普通输入计价。
var cacheHitFixtures = []string{
	"anthropic-cache-hit",
	"anthropic-stream-cache-hit",
	"cc-cache-write",
	"cc-stream-cache-write",
	"cc-stream-cache-write-bailian",
	"cc-cache-write-both-keys",
	"responses-cache-write",
}

type fixtureMeta struct {
	Protocol string           `json:"protocol"`
	Stream   bool             `json:"stream"`
	Expect   protocol.Summary `json:"expect"`
	// Synthetic 必须为 true。这道闸是反着开的：golden 那边拦的是「还没人核过就当
	// 事实源」，这边拦的是「构造样本被搬进 golden 冒充转录」。
	Synthetic bool   `json:"synthetic"`
	Source    string `json:"source"`
	// ExpectCanonicalInputTokens 是归一成**毛值**之后的 input（含缓存两项）。
	// 与 Expect.InputTokens 刻意分开：Tap 保留上游原始语义（Anthropic 的净值），
	// canonical 归一为毛值，两个数在缓存非零时必然不同——这正是此前没样本能验的那段
	// 加法（protocol/event.go 的 Usage 约定）。
	ExpectCanonicalInputTokens int `json:"expectCanonicalInputTokens"`
}

func loadFixture(t *testing.T, name string) (fixtureMeta, []byte) {
	t.Helper()
	dir := filepath.Join(fixtureDir, name)
	metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta fixtureMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("meta.json 解析失败: %v", err)
	}
	if !meta.Synthetic {
		t.Fatalf("%s 的 meta.json 没有 synthetic:true——真录到的样本请放进 testdata/golden/ 走 verified 那道闸", name)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "response.raw"))
	if err != nil {
		t.Fatal(err)
	}
	return meta, raw
}

// TestCacheHitFixturesThroughTap：样本 → Tap → Summary，保留上游原始语义
// （Anthropic 的 input_tokens 是净值，不含缓存两项）。
func TestCacheHitFixturesThroughTap(t *testing.T) {
	for _, name := range cacheHitFixtures {
		t.Run(name, func(t *testing.T) {
			meta, raw := loadFixture(t, name)

			tap := taps.New(protocol.Protocol(meta.Protocol), meta.Stream)
			if tap == nil {
				t.Fatalf("meta.json 里的 protocol=%q 无效", meta.Protocol)
			}
			// 按 4 KB 切块喂，理由同 golden_test.go：整块灌碰不到跨块的帧边界。
			for i := 0; i < len(raw); i += 4096 {
				end := min(i+4096, len(raw))
				if n, err := tap.Write(raw[i:end]); n != end-i || err != nil {
					t.Fatalf("Tap.Write 返回 (%d, %v)，必须是 (%d, nil)", n, err, end-i)
				}
			}
			if got := tap.Summary(); got != meta.Expect {
				t.Errorf("Summary = %+v\n期望 = %+v", got, meta.Expect)
			}
		})
	}
}

// TestCacheHitFixturesThroughCanonical：样本 → 按 meta.protocol 解码 → canonical Usage。
// Anthropic 样本验的是「净值加回缓存两项」那段归一（protocol/event.go）；OpenAI 系
// 样本验的是缓存两项明细读到了 canonical（毛值直映，不加减）。
func TestCacheHitFixturesThroughCanonical(t *testing.T) {
	for _, name := range cacheHitFixtures {
		t.Run(name, func(t *testing.T) {
			meta, raw := loadFixture(t, name)
			codec := codecs.New(protocol.Protocol(meta.Protocol), codecs.Options{})

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

			want := protocol.Usage{
				InputTokens:      meta.ExpectCanonicalInputTokens,
				OutputTokens:     meta.Expect.OutputTokens,
				CacheReadTokens:  meta.Expect.CacheReadTokens,
				CacheWriteTokens: meta.Expect.CacheWriteTokens,
			}
			if got != want {
				t.Errorf("canonical Usage = %+v\n期望 = %+v", got, want)
			}
			// 编码回 Anthropic 线上格式时要能减回去，与解码侧那个加法互为逆向；
			// 对不上说明毛值/净值在某一侧串了档，客户端看到的 input_tokens 就会错。
			// 只对 Anthropic 样本成立：OpenAI 系的 Summary 本就是毛值。
			if meta.Protocol != string(protocol.Anthropic) {
				return
			}
			if net := got.NetInput(); net != meta.Expect.InputTokens {
				t.Errorf("NetInput = %d, 期望回到上游原样的 %d", net, meta.Expect.InputTokens)
			}
		})
	}
}

// TestCacheWriteCrossesExits：缓存写入量跨协议带到出口（#111）。上游样本按自家
// codec 解码，再用另一协议的 codec 编成非流式响应，读出口 usage 里的写入键。
//
// cc-stream-cache-write 的 cached + write > prompt（new-api 的取值）：A 出口
// input_tokens 照实钳成 0、两项缓存照原数写，不按比例压缩（PO 2026-09-28 裁决 4）。
func TestCacheWriteCrossesExits(t *testing.T) {
	cases := []struct {
		fixture   string
		exit      protocol.Protocol
		wantWrite float64
		wantInput float64 // 出口 usage 顶层的输入键（A 是净值，CC / R 是毛值）
	}{
		{"cc-stream-cache-write", protocol.Anthropic, 3616, 0},
		{"cc-stream-cache-write", protocol.OpenAIResponses, 3616, 3619},
		{"anthropic-cache-hit", protocol.OpenAI, 1024, 4881},
		{"responses-cache-write", protocol.OpenAI, 10240, 14521},
	}
	for _, c := range cases {
		t.Run(c.fixture+"→"+string(c.exit), func(t *testing.T) {
			meta, raw := loadFixture(t, c.fixture)
			in := codecs.New(protocol.Protocol(meta.Protocol), codecs.Options{})
			var events []protocol.Event
			if meta.Stream {
				ch, err := in.DecodeStream(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				for ev := range ch {
					events = append(events, ev)
				}
			} else {
				var err error
				if events, err = in.DecodeFullBody(raw); err != nil {
					t.Fatal(err)
				}
			}
			body, err := codecs.New(c.exit, codecs.Options{}).EncodeFullBody(events)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Usage map[string]any `json:"usage"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			u := out.Usage
			var write, input any
			switch c.exit {
			case protocol.Anthropic:
				write, input = u["cache_creation_input_tokens"], u["input_tokens"]
			case protocol.OpenAI:
				d, _ := u["prompt_tokens_details"].(map[string]any)
				write, input = d["cache_write_tokens"], u["prompt_tokens"]
			case protocol.OpenAIResponses:
				d, _ := u["input_tokens_details"].(map[string]any)
				write, input = d["cache_write_tokens"], u["input_tokens"]
			}
			if write != c.wantWrite || input != c.wantInput {
				t.Errorf("出口 usage 写入 = %v、输入 = %v，期望 %v、%v\nusage = %v",
					write, input, c.wantWrite, c.wantInput, u)
			}
		})
	}
}
