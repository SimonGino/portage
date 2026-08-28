package declcfg_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/declcfg"
	"github.com/SimonGino/portage/internal/store"
)

// exampleFile 是仓库根那份示范文件的路径（用例工作目录是包目录）。
const exampleFile = "../../channels.example.yaml"

// roundtripFixture 是往返闸的输入：在示范数据上再压几样示范里放不下的东西。
//
// 把示范当底子而不是另写一份，是因为示范本身就得过两道闸，省一份要维护的样本；
// 加的那几样都是**只有在往返里才会翻车**的形状：
//
//   - 停用渠道且**一份凭证都没有**（`checkChannelHasCredential` 放行停用渠道）——
//     导出侧给出 nil 切片，序列化成 `null`，要能被解回 nil 再导出成同样的 `null`。
//   - 停用接入点挂**两个**候选、其中一个 `weight: 0`——`checkSingleCandidate` 只扫
//     未停用接入点，于是这是今天唯一能表达多候选与零权重的地方（口径层 §2.9 #29）。
//   - 候选**故意逆序写**，钉住导出按限定名排而不是按插入序/id。
func roundtripFixture() *declcfg.File {
	f := declcfg.Example()
	// 示范里那把是出厂占位 key，直接 apply 会被 #28 的闸拒掉。往返要的是**结构**
	// 字节相等，不是那一个值，换成一把真值即可。
	f.APIKeys[0].Key = "sk-ptg-roundtrip-用的真值"

	zero, hundred := 0, 100
	f.Channels = append(f.Channels, declcfg.Channel{
		Name:           "停用的旧渠道",
		BaseURL:        store.BaseURLs{OpenAI: "https://old.example.internal"},
		CredentialType: "api_key",
		KeyMode:        "polling",
		Disabled:       true,
	})
	f.AccessPoints = append(f.AccessPoints, declcfg.AccessPoint{
		Model:    "停用的接入点",
		Disabled: true,
		Candidates: []declcfg.Candidate{
			{Target: "自建-vllm/Qwen3-32B", Weight: &zero},
			{Target: "anthropic/claude-sonnet-4-20250514", Weight: &hundred},
		},
	})
	return f
}

func applyFile(t *testing.T, db *sql.DB, f *declcfg.File) {
	t.Helper()
	if _, err := declcfg.Apply(context.Background(), db, f, discardLogger()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func mustExport(t *testing.T, db *sql.DB) []byte {
	t.Helper()
	raw, err := declcfg.Export(context.Background(), db)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return raw
}

// TestExportRoundtripsByteForByte 是本票的核心闸（口径层 §2.9 #32）。
//
// `export → 空库 apply → export` 逐字节相同。它一条顶三条：排序不稳、字段漏导、
// 零值被吞、id 泄进排序键——任何一样都会让第二次输出跟第一次对不上。
//
// sub2api 正是栽在导出物没有身份键上：它的输出只能 append 不能 apply，于是「导出→
// 部署」这条主路径根本走不通。往返相等是那条主路径成立的充要条件。
func TestExportRoundtripsByteForByte(t *testing.T) {
	first := openDB(t)
	applyFile(t, first, roundtripFixture())
	one := mustExport(t, first)

	// 第二次走的是**文件**这条路：解析导出物 → 灌进一个全新的空库 → 再导出。
	// 不复用第一次那个 *File，否则解析这一段就没被覆盖到。
	parsed, err := declcfg.Parse(one, "export.yaml")
	if err != nil {
		t.Fatalf("导出物自己解不动：%v\n---\n%s", err, one)
	}
	second := openDB(t)
	applyFile(t, second, parsed)
	two := mustExport(t, second)

	if string(one) != string(two) {
		t.Errorf("两次导出不相等\n--- 第一次 ---\n%s\n--- 第二次 ---\n%s", one, two)
	}
	// 防呆：往返相等这条断言在两边都是空文件时也成立。
	for _, want := range []string{"anthropic", "自建-vllm", "停用的接入点", "sk-ptg-roundtrip"} {
		if !strings.Contains(string(one), want) {
			t.Errorf("导出物里没有 %q，往返闸测的是两份空文件相等", want)
		}
	}
}

// TestExportSortsByNaturalKeyNotID 钉住排序键是文件里那个可见身份。
//
// 按 id 排在**第一次**导出时看不出任何问题——库是按字母序建的，两者恰好一致。
// 只有当插入序与字母序相反时才露馅，所以这里刻意反着建。
func TestExportSortsByNaturalKeyNotID(t *testing.T) {
	db := openDB(t)
	f := &declcfg.File{
		Channels: []declcfg.Channel{
			{
				Name: "zeta", BaseURL: store.BaseURLs{OpenAI: "https://z.example.internal"},
				CredentialType: "api_key", KeyMode: "polling",
				Credentials: []declcfg.Credential{
					{Name: "z2", Credential: "sk-z2"},
					{Name: "z1", Credential: "sk-z1"},
				},
				Models: []declcfg.Model{{UpstreamModel: "m-z2"}, {UpstreamModel: "m-z1"}},
			},
			{
				Name: "alpha", BaseURL: store.BaseURLs{OpenAI: "https://a.example.internal"},
				CredentialType: "api_key", KeyMode: "polling",
				Credentials: []declcfg.Credential{{Name: "a1", Credential: "sk-a1"}},
				Models:      []declcfg.Model{{UpstreamModel: "m-a1"}},
			},
		},
		APIKeys: []declcfg.APIKey{
			{Name: "zzz", Key: "sk-ptg-zzz"},
			{Name: "aaa", Key: "sk-ptg-aaa"},
		},
	}
	applyFile(t, db, f)

	out, err := declcfg.Snapshot(context.Background(), db)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := []string{out.Channels[0].Name, out.Channels[1].Name}; got[0] != "alpha" || got[1] != "zeta" {
		t.Errorf("渠道该按 name 排，得到 %v", got)
	}
	if got := out.Channels[1].Credentials; got[0].Name != "z1" || got[1].Name != "z2" {
		t.Errorf("凭证该按 name 排，得到 %+v", got)
	}
	if got := out.Channels[1].Models; got[0].UpstreamModel != "m-z1" || got[1].UpstreamModel != "m-z2" {
		t.Errorf("纳管模型该按 upstream_model 排，得到 %+v", got)
	}
	if got := []string{out.APIKeys[0].Name, out.APIKeys[1].Name}; got[0] != "aaa" || got[1] != "zzz" {
		t.Errorf("API Key 该按 name 排，得到 %v", got)
	}
}

// TestExportDropsCredentialRuntimeState 钉住 401 摘除现场不进导出物。
//
// channel_keys 的 disabled / disabled_reason / disabled_at 三列只由网关写。带上它们
// 等于把本地这台撞过的 401 一起搬去生产——那台从没打过这把 key，却带着一条「它坏了」
// 的判决上线。读侧那条 `TestApplyKeepsDisabledCredential` 守着反方向：文件不能把死
// key 推回轮询池。两条合起来才是「这三列跟文件无关」。
func TestExportDropsCredentialRuntimeState(t *testing.T) {
	db := openDB(t)
	applyFile(t, db, roundtripFixture())

	// 模拟一次 401 摘除：凭证池里两把，摘掉一把（摘光会撞 checkChannelHasCredential）。
	if _, err := db.Exec(
		`UPDATE channel_keys SET disabled = 1, disabled_reason = '上游 401：invalid x-api-key',
		 disabled_at = '2026-08-19T10:00:00Z' WHERE name = '备号'`); err != nil {
		t.Fatal(err)
	}

	raw := string(mustExport(t, db))
	if !strings.Contains(raw, "备号") {
		t.Error("被摘除的凭证本身要留在文件里——它是配置，摘除是运行期状态")
	}
	for _, leak := range []string{"disabled_reason", "disabled_at", "invalid x-api-key", "2026-08-19T10:00:00Z"} {
		if strings.Contains(raw, leak) {
			t.Errorf("导出物里出现了运行期状态 %q", leak)
		}
	}
	// 再往返一次：摘除现场不该改变导出物，否则「摘了一把 key 之后导出物就变了」
	// 会让往返闸在真实使用中失效。
	clean := openDB(t)
	applyFile(t, clean, roundtripFixture())
	if raw != string(mustExport(t, clean)) {
		t.Error("摘除一把凭证之后导出物变了，说明运行期状态漏进了导出物")
	}
}

// TestExportFailsNamingKeysWithoutPlaintext 钉住存量 key 的处置是**失败并点名**。
//
// key_plain 是 v0.47 才加的列，更早建的 key 只有哈希、原值补不回来。跳过会让生产机
// 看着一切正常而那些客户端全是 401，且纯转发形态下没有页面能看出少了几把；写占位值
// 只是把同一个失败推晚一站。
func TestExportFailsNamingKeysWithoutPlaintext(t *testing.T) {
	db := openDB(t)
	applyFile(t, db, roundtripFixture())
	if _, err := db.Exec(`UPDATE api_keys SET key_plain = '' WHERE name = '默认'`); err != nil {
		t.Fatal(err)
	}
	_, err := declcfg.Export(context.Background(), db)
	if err == nil {
		t.Fatal("拿不到原值的 API Key 必须让导出当场失败")
	}
	if !strings.Contains(err.Error(), "默认") {
		t.Errorf("报错要点名是哪几把，实得：%v", err)
	}
}

// TestExportHasNoTopLevelVersion 钉住顶层 version 字段不做（PO 2026-08-19）。
//
// 加了就得一直维护它的语义，而今天没有任何东西会读它。代价记在 §7.9：
// KnownFields(true) 之下加字段安全、删字段与改名不安全。
func TestExportHasNoTopLevelVersion(t *testing.T) {
	db := openDB(t)
	applyFile(t, db, roundtripFixture())
	for _, line := range strings.Split(string(mustExport(t, db)), "\n") {
		if strings.HasPrefix(line, "version:") {
			t.Errorf("导出物有顶层 version 字段：%q", line)
		}
	}
}

// TestExampleFileIsGeneratedByExporter 钉住 channels.example.yaml 不是手写的。
//
// 手写的示范没有闸护着，字段改名时会静默过期——而在纯转发形态下它是次要路径唯一的
// 参考，过期的参考比没有参考更坏（口径层 §2.9 #34）。
//
// 改了字段之后重新生成：`PORTAGE_UPDATE_EXAMPLE=1 go test ./internal/declcfg`
func TestExampleFileIsGeneratedByExporter(t *testing.T) {
	want, err := declcfg.Marshal(declcfg.Example())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if os.Getenv("PORTAGE_UPDATE_EXAMPLE") != "" {
		if err := declcfg.WriteFile(exampleFile, want); err != nil {
			t.Fatal(err)
		}
		t.Log("已重新生成 " + exampleFile)
	}
	got, err := os.ReadFile(exampleFile)
	if err != nil {
		t.Fatalf("读示范文件：%v（用 PORTAGE_UPDATE_EXAMPLE=1 go test ./internal/declcfg 生成）", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s 与导出器的输出不一致；用 PORTAGE_UPDATE_EXAMPLE=1 go test ./internal/declcfg 重新生成", exampleFile)
	}

	// 示范文件里那把是出厂占位 key，于是它**照原样挂上去会被拒启**——这是要的效果，
	// 它公开在仓库里。用例顺带把这条钉住。
	if !strings.Contains(string(got), declcfg.PlaceholderAPIKey) {
		t.Error("示范文件里应当是出厂占位 API Key")
	}
	f, err := declcfg.Parse(got, exampleFile)
	if err != nil {
		t.Fatalf("示范文件自己解不动：%v", err)
	}
	if _, err := declcfg.Apply(context.Background(), openDB(t), f, discardLogger()); err == nil {
		t.Error("示范文件照原样 apply 必须被占位闸拒掉")
	}
}

// TestExportWritesFile0600 钉住落盘权限（口径层 §2.9 #28）。
//
// 文件里是明文上游凭证与明文 API Key，跟私钥同级。管理端下载那条路不经过这里
// ——浏览器写盘的权限网关碰不到——这个函数守的是服务端落盘的场景。
func TestExportWritesFile0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "channels.yaml")
	if err := declcfg.WriteFile(p, []byte("channels:\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("声明文件权限该是 0600，实得 %o", got)
	}
}
