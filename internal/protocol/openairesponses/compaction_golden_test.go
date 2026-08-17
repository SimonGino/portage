package openairesponses

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件把 portage-legacy#74 的解码半边直接怼到 **Codex 自己发的字节**上
// （portage-legacy#73 采于 2026-08-13，Codex CLI 0.147 + remote compaction v2）。
// 同包的 compaction_test.go 是手搭 fixture——它钉的是我们的口径自洽，钉不住
// 「Codex 真的这么发包」；这一份补的正是后者。
//
// 三份样本是同一个会话的连续三轮：压缩前 → 压缩 turn → 回带轮。三轮一起摆着才看得出
// 判据的两端都对：压缩 turn 认得出来，而**回带轮不是压缩 turn**（`CompactionTurn()`
// 为假，展开层 §7.7 里那条「丢弃日志不能罩在压缩闸里」的实据就在这儿）。
const compactionGoldenDir = "../../../testdata/golden"

func readGoldenRequest(t *testing.T, sample string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(compactionGoldenDir, sample, "request.json"))
	if err != nil {
		t.Skipf("样本尚未采集：%s（见 testdata/golden/README.md）", sample)
	}
	return body
}

// TestGoldenCompactionTriggerDetected：压缩 turn 的真实请求体两条路都认得出来
// ——透传闸的 HasCompactionTrigger，与转换路径的 DecodeRequest。
func TestGoldenCompactionTriggerDetected(t *testing.T) {
	body := readGoldenRequest(t, "responses-stream-compact-trigger")

	if !HasCompactionTrigger(body) {
		t.Error("HasCompactionTrigger 对真实压缩 turn 返回 false：透传闸会放它过去")
	}

	c := NewCodec()
	req, err := c.DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("真实压缩 turn 解不动: %v", err)
	}
	if !c.CompactionTurn() {
		t.Fatal("CompactionTurn() 为假：转换路径不会去合成 item，Codex 拿到零个就是 Fatal")
	}

	// trigger 项本身不是内容，不该进消息序列；改写要留下压缩 prompt 那条 user 消息。
	last := req.Messages[len(req.Messages)-1]
	if last.Role != protocol.RoleUser || !strings.Contains(last.Content[0].Text, compactPrompt) {
		t.Errorf("末条消息不是压缩 prompt: role=%s text=%.60q", last.Role, last.Content[0].Text)
	}
	for i, m := range req.Messages {
		for _, b := range m.Content {
			if strings.Contains(b.Text, ItemCompactionTrigger) {
				t.Errorf("消息 %d 里混进了 trigger 项的正文", i)
			}
		}
	}
	// 剥工具是必须的：留着上游多半去调工具，这一轮一个字的摘要都没有。真实样本里
	// Codex 把工具声明成 `additional_tools` 输入项，剥的就是它解出来的这一份。
	if len(req.Tools) != 0 {
		t.Errorf("summarizer turn 还留着 %d 个工具", len(req.Tools))
	}
}

// TestGoldenCompactionReplayIsNotACompactionTurn：回带轮**不是**压缩 turn，而它带回来的
// 是**真 OpenAI 密文**——本仓解不开，必须降级成占位并登记，不能把这一段历史整个丢掉。
//
// 这正是混路场景的真身：客户端先经透传渠道压缩成功（拿到上游侧密文），之后被路由到
// 转换渠道。此前只有手搭的假密文能演它。
func TestGoldenCompactionReplayIsNotACompactionTurn(t *testing.T) {
	body := readGoldenRequest(t, "responses-stream-compact-replay")

	if HasCompactionTrigger(body) {
		t.Error("回带轮被判成压缩 turn：透传闸会拒掉一次普通请求")
	}

	c := NewCodec()
	req, err := c.DecodeRequest(body, true)
	if err != nil {
		t.Fatalf("回带轮解不动: %v", err)
	}
	if c.CompactionTurn() {
		t.Error("回带轮的 CompactionTurn() 为真——丢弃日志罩在这道闸里就会全无声")
	}
	if got := c.CompactionDrops(); len(got) != 1 || got[0] != itemCompaction {
		t.Fatalf("CompactionDrops() = %v，期望恰好一条 %q", got, itemCompaction)
	}

	var placed bool
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if strings.Contains(b.Text, opaqueCompactionNote) {
				placed = true
			}
			if strings.Contains(b.Text, "gAAAAAB") {
				t.Error("上游密文以明文进了消息序列")
			}
		}
	}
	if !placed {
		t.Error("解不开的压缩项被整个丢掉了：上游看到的历史会凭空少一段，表现成模型忽然失忆")
	}
}

// TestGoldenPreCompactionTurnIsOrdinary：压缩前那一轮是普通请求，两道判据都得放行。
// 它挡的是「判据认位置」一类的退化——真实 trigger 恰好在尾项，光看尾项也能过 trigger
// 那条用例，但会在这里把普通请求也判成压缩。
func TestGoldenPreCompactionTurnIsOrdinary(t *testing.T) {
	body := readGoldenRequest(t, "responses-stream-compact-turn1")

	if HasCompactionTrigger(body) {
		t.Error("压缩前的普通一轮被判成压缩 turn")
	}
	c := NewCodec()
	if _, err := c.DecodeRequest(body, true); err != nil {
		t.Fatalf("普通一轮解不动: %v", err)
	}
	if c.CompactionTurn() || len(c.CompactionDrops()) != 0 {
		t.Errorf("普通一轮被当成压缩相关: turn=%v drops=%v", c.CompactionTurn(), c.CompactionDrops())
	}
}

// TestGoldenCompactionItemShape 把真实 compaction item 的线格形状钉死。
//
// 钉它是因为我们**自己也要发一个**，而形状上的每一处差异都只能靠真转录发现：
// 上游发 added + done 两个事件且两份 encrypted_content **不同**（added 那份短得多），
// `response.completed.output` 却是**空数组**。客户端取的是 done 那份——回带轮里那个
// item 的 id 与密文与 done 逐字节相等，而 codex-rs `collect_compaction_output` 只数
// `OutputItemDone`、判据是 `compaction_count == 1`（Apache-2.0，2026-08-13 核）。
// 我们「只发 done、不发 added」因此是对的（展开层 §7.7）。
func TestGoldenCompactionItemShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(compactionGoldenDir, "responses-stream-compact-trigger", "response.raw"))
	if err != nil {
		t.Skipf("样本尚未采集：%v", err)
	}

	type item struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Encrypted string `json:"encrypted_content"`
	}
	var added, done *item
	var completedOutput []json.RawMessage
	for _, line := range strings.Split(string(raw), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			Item     *item  `json:"item"`
			Response *struct {
				Output []json.RawMessage `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_item.added":
			added = ev.Item
		case "response.output_item.done":
			done = ev.Item
		case "response.completed":
			if ev.Response != nil {
				completedOutput = ev.Response.Output
			}
		}
	}

	if added == nil || done == nil {
		t.Fatal("样本里没有成对的 output_item.added / .done")
	}
	if added.Type != itemCompaction || done.Type != itemCompaction {
		t.Fatalf("item type = %q / %q，期望 %q", added.Type, done.Type, itemCompaction)
	}
	if added.Encrypted == done.Encrypted {
		t.Error("added 与 done 的 encrypted_content 相同——样本变了，§7.7 里那段推论要重核")
	}
	if len(completedOutput) != 0 {
		t.Errorf("response.completed.output 不是空数组（%d 项）：客户端若改从这里收集，"+
			"我们只发 done 的做法就不成立了", len(completedOutput))
	}

	// 回带轮拿回来的是 done 那份，不是 added 那份。
	var replay struct {
		Input []item `json:"input"`
	}
	if err := json.Unmarshal(readGoldenRequest(t, "responses-stream-compact-replay"), &replay); err != nil {
		t.Fatal(err)
	}
	var carried *item
	for i := range replay.Input {
		if replay.Input[i].Type == itemCompaction {
			carried = &replay.Input[i]
		}
	}
	if carried == nil {
		t.Fatal("回带轮里没有 compaction 项")
	}
	if carried.ID != done.ID || carried.Encrypted != done.Encrypted {
		t.Errorf("回带的 item 与 done 不一致: id %q vs %q", carried.ID, done.ID)
	}
	if carried.Encrypted == added.Encrypted {
		t.Error("回带的是 added 那份——「只发 done」的前提被推翻了")
	}
}
