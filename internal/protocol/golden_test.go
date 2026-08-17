package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/taps"
)

// goldenDir 是全仓共用的转录库。它不放在某个包的 testdata/ 下，是因为同一份样本
// 到 P1 还要喂给 codec 的跨协议用例（§9 样本 1~6 刻意选的「同语义、双协议」场景）。
const goldenDir = "../../testdata/golden"

// m0Samples 是展开层 §9 的「M0 必抓子集」。列在这里而不是靠扫目录，是为了让
// **没采集的样本也看得见**：缺哪个就 skip 哪个子测试，而不是目录空着一路绿灯。
var m0Samples = []string{
	"anthropic-stream-text",
	"anthropic-stream-tool",
	"anthropic-stream-parallel-tools",
	"anthropic-text",
	"anthropic-tool",
	"anthropic-parallel-tools",
	"cc-stream-text",
	"cc-stream-tool",
	"cc-stream-parallel-tools",
	"cc-text",
	"cc-tool",
	"cc-parallel-tools",
}

// compactionSamples 是 Codex remote compaction v2 的三段真实转录（#73）。
//
// 单开一张表而不是并进 m0Samples：§9 的 M0 必抓子集是「六场景 × 双协议」那张网，
// Responses 样本本就排在 M1；这三份是为 #74 的压缩口径采的，进度归属不同，混在一起
// 会让「M0 齐了没有」这个问题答不出来。
//
// 它们喂给 Tap 的部分与其余 upstream 样本毫无二致（也正好补上 Responses Tap 的
// cached_tokens 真实覆盖）；真正只有这三份才有的东西在 request.json 与 compaction
// item 上，那部分由 openairesponses 包的用例消费。
var compactionSamples = []string{
	"responses-stream-compact-turn1",
	"responses-stream-compact-trigger",
	"responses-stream-compact-replay",
}

// reasoningSamples 是 thinking/reasoning 跨协议口径那批的真实转录（#93）。
//
// 又单开一张表，理由同 compactionSamples：它们既不属 §9 的「六场景 × 双协议」M0 网，
// 也不是压缩那一批，是为口径层 v0.62（出向合成）采的前置样本，进度归属独立。
//
// 喂给 Tap 的部分与其余 upstream 样本一样；只有这一批才有的东西在 reasoning_content
// 的增量序、Responses 的 reasoning item 生命周期，以及回带那两份请求体上——那部分等
// codec 实现落地后由各自的包消费。回带样本 in-anthropic-thinking-replay 是 inbound
// 方向、没有 response.raw，不进这张表（inbound 样本由 canonical_coverage_test 那道闸看）。
var reasoningSamples = []string{
	"cc-stream-reasoning-text",
	"cc-stream-reasoning-tool-turn1",
	"cc-stream-reasoning-tool-turn2",
	"responses-stream-reasoning-turn1",
	"responses-stream-reasoning-replay",
}

// responsesUpstreamSamples 是 Responses 出口半边的基础转录（#79）。
//
// 第三张单开的表，理由同上两张：它们既不在 §9 的「六场景 × 双协议」M0 网里（Responses
// 样本排在 M1），也不属压缩批或 reasoning 批，是为 openairesponses 出口半边采的样本前提。
//
// 五份覆盖三档：纯文本、custom 工具整轮（turn1 发起 + turn2 回带）、code-mode 并行整轮。
// 喂给 Tap 的部分与其余 upstream 样本一样；只有这一批才有的东西在工具 item 的线格形状与
// 回带请求体上，那部分由 openairesponses 包的用例消费。
var responsesUpstreamSamples = []string{
	"responses-stream-text",
	"responses-stream-tool-turn1",
	"responses-stream-tool-turn2",
	"responses-stream-parallel-turn1",
	"responses-stream-parallel-turn2",
}

type sampleMeta struct {
	Protocol string           `json:"protocol"`
	Stream   bool             `json:"stream"`
	Endpoint string           `json:"endpoint"`
	Status   int              `json:"status"`
	Expect   protocol.Summary `json:"expect"`
	Verified bool             `json:"verified"`
	// Source 记这份样本采自哪个上游，不参与任何断言。声明在这里是为了让它可被发现：
	// 同一个目录树里迟早会同时躺着中转采的和官方直连采的样本（#37），到那时
	// 「这个数是谁报的」只能靠它区分。
	Source string `json:"source"`
}

// TestGoldenSamples 用真实转录驱动 Tap：样本 → Tap → Summary，与人工核过的 expect 比对。
func TestGoldenSamples(t *testing.T) {
	all := append([]string{}, m0Samples...)
	all = append(all, compactionSamples...)
	all = append(all, reasoningSamples...)
	all = append(all, responsesUpstreamSamples...)
	for _, name := range all {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(goldenDir, name)
			metaRaw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			if os.IsNotExist(err) {
				t.Skipf("样本尚未采集：%s（用 cmd/goldenrec 录，脱敏核对后放进来）", dir)
			}
			if err != nil {
				t.Fatal(err)
			}
			var meta sampleMeta
			if err := json.Unmarshal(metaRaw, &meta); err != nil {
				t.Fatalf("meta.json 解析失败: %v", err)
			}
			// goldenrec 预填的 expect 出自被测代码本身，没人核过就等于让实现
			// 给自己判卷。这道闸不能绕。
			if !meta.Verified {
				t.Fatalf("%s 的 meta.json 仍是 verified:false——脱敏并核对 expect 后再置 true", name)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "response.raw"))
			if err != nil {
				t.Fatal(err)
			}

			tap := taps.New(protocol.Protocol(meta.Protocol), meta.Stream)
			if tap == nil {
				t.Fatalf("meta.json 里的 protocol=%q 无效", meta.Protocol)
			}
			// 按 4 KB 切块喂，不整块灌：整块灌永远碰不到跨块的帧边界。
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
