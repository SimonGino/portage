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
	for _, name := range m0Samples {
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
