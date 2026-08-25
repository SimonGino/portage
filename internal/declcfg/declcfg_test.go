package declcfg_test

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/declcfg"
	"github.com/SimonGino/portage/internal/store"
)

// goodFile 是一份能过两道闸的最小声明文件，各用例在它上面改一处。
const goodFile = `
channels:
  - name: qwen
    base_url:
      openai: https://example.internal/v1
    credentials:
      - name: 主号
        credential: sk-upstream-1
    models:
      - upstream_model: Qwen3-27B
access_points:
  - model: claude-sonnet-4
    candidates:
      - target: qwen/Qwen3-27B
api_keys:
  - name: laptop
    key: sk-ptg-real-one
`

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustApply(t *testing.T, db *sql.DB, yaml string) {
	t.Helper()
	f, err := declcfg.Parse([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := declcfg.Apply(context.Background(), db, f, discardLogger()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func applyErr(t *testing.T, db *sql.DB, yaml string) string {
	t.Helper()
	f, err := declcfg.Parse([]byte(yaml), "test.yaml")
	if err != nil {
		return err.Error()
	}
	err = declcfg.Apply(context.Background(), db, f, discardLogger())
	if err == nil {
		t.Fatal("期望 Apply 拒绝这份文件，实际过了")
	}
	return err.Error()
}

// TestLoadPathHasThreeDistinctMeanings 钉住路径三档（口径层 §2.9 #31 ③）。
//
// 中间那一档是这条口径的全部要点：**给了路径却读不到必须拒启**。litellm 在这里静默
// 跳过整个 load、带 0 个模型起来，而 store.Validate 空库全过、兜不住它。
func TestLoadPathHasThreeDistinctMeanings(t *testing.T) {
	t.Run("空路径就是没挂，合法", func(t *testing.T) {
		f, err := declcfg.Load("")
		if err != nil {
			t.Fatalf("空路径应当合法，得到 %v", err)
		}
		if f != nil {
			t.Fatalf("空路径应当返回 nil File，得到 %+v", f)
		}
	})
	t.Run("路径非空却读不到就拒启", func(t *testing.T) {
		_, err := declcfg.Load(filepath.Join(t.TempDir(), "channel.yaml"))
		if err == nil {
			t.Fatal("拼错的文件名必须拒启，否则会静默退回库里的旧配置")
		}
	})
	t.Run("读得到就解出来", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "channels.yaml")
		if err := os.WriteFile(p, []byte(goodFile), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := declcfg.Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(f.Channels) != 1 || f.Channels[0].Name != "qwen" {
			t.Fatalf("解出来的内容不对：%+v", f)
		}
	})
}

// TestUnknownFieldIsRejected 钉住严格档（口径层 §2.9 #31 ②）。
func TestUnknownFieldIsRejected(t *testing.T) {
	bad := strings.Replace(goodFile, "    base_url:", "    base_urls:", 1)
	if _, err := declcfg.Parse([]byte(bad), "test.yaml"); err == nil {
		t.Fatal("未知字段必须拒——纯转发形态下没有第二处能发现拼错的字段")
	}
}

// TestConfigYAMLStaysLenient 钉住「两份文件严格度不同是刻意的」。
//
// 这条用例守的是一个**反向**约束：将来有人觉得两份文件该一致，把 config.Load 也转严，
// 带历史遗留字段的老部署会升级即拒启。
func TestConfigYAMLStaysLenient(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("listen: \"127.0.0.1:1\"\nsome_removed_field: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProcessConfig(p); err != nil {
		t.Fatalf("config.yaml 必须保持宽松，遗留字段不该拒启：%v", err)
	}
}

// TestReportsEveryProblemAtOnce 钉住「一次报全」（口径层 §2.9 #31 ①）。
//
// 首错即停会把「改文件重启」这唯一的修复通道从一次变成五次。
func TestReportsEveryProblemAtOnce(t *testing.T) {
	db := openDB(t)
	msg := applyErr(t, db, `
channels:
  - name: a
    base_url:
      openai: https://example.internal/v1
    key_mode: 乱写
    credentials:
      - name: ""
        credential: ""
    models:
      - upstream_model: m
access_points:
  - model: ap
    candidates:
      - target: a/不存在
api_keys: []
`)
	for _, want := range []string{"key_mode", "没有 name", "没有值", "找不到对应的渠道纳管模型", "一把 API Key 都没有"} {
		if !strings.Contains(msg, want) {
			t.Errorf("一次报全里缺了 %q。实际报文：\n%s", want, msg)
		}
	}
}

// TestPlaceholderAPIKeyIsRejected 钉住占位闸（口径层 §2.9 #28 ④）。
//
// 闸只管 API Key 不管上游凭证：占位凭证只是自伤且当场可见（第一个请求就 401），
// 占位 API Key 是公开在仓库里的门钥匙。
func TestPlaceholderAPIKeyIsRejected(t *testing.T) {
	db := openDB(t)
	bad := strings.Replace(goodFile, "sk-ptg-real-one", declcfg.PlaceholderAPIKey, 1)
	if msg := applyErr(t, db, bad); !strings.Contains(msg, "出厂占位值") {
		t.Fatalf("占位 API Key 必须拒启，实际报文：%s", msg)
	}
	t.Run("同一个占位串当上游凭证则放行", func(t *testing.T) {
		ok := strings.Replace(goodFile, "sk-upstream-1", declcfg.PlaceholderAPIKey, 1)
		mustApply(t, openDB(t), ok)
	})
}

// TestDuplicateKeyPlaintextNamesBothKeys 钉住 key_hash 那一格（展开层 §7.9）。
//
// 自然键（name）不冲突而 key_hash 冲突，留给 SQLite 的约束错误就只有索引名、没有
// 实体名——而纯转发形态下没有第二个地方能查是哪两把。
func TestDuplicateKeyPlaintextNamesBothKeys(t *testing.T) {
	db := openDB(t)
	msg := applyErr(t, db, strings.Replace(goodFile,
		"  - name: laptop\n    key: sk-ptg-real-one\n",
		"  - name: laptop\n    key: sk-ptg-same\n  - name: desktop\n    key: sk-ptg-same\n", 1))
	for _, want := range []string{`"desktop"`, `"laptop"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("报文必须点名是哪两把 key，缺了 %s：\n%s", want, msg)
		}
	}
}

// TestApplyIsOneTransaction 钉住「整个 apply 一个事务」（口径层 §2.9 #29）。
//
// 闸二（store.Validate）在同一个事务里跑，所以它拦下的东西同样一行都不该落库。
func TestApplyIsOneTransaction(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, goodFile)
	// base_url 缺 scheme：selfCheck 放行（那是 Validate 的判据），Validate 拦下。
	msg := applyErr(t, db, strings.Replace(goodFile, "https://example.internal/v1", "example.internal", 1))
	if !strings.Contains(msg, "协议地址") {
		t.Fatalf("期望被 store.Validate 拦下，实际：%s", msg)
	}
	var got string
	if err := db.QueryRow(`SELECT base_url_openai FROM channels WHERE name='qwen'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "https://example.internal/v1" {
		t.Fatalf("闸二拦下之后库里不该留下半份配置，base_url_openai 已变成 %q", got)
	}
}

// TestApplyKeepsDisabledCredential 是本票最要紧的一条回归。
//
// 停用现场挂在 channel_keys 的 disabled / disabled_reason / disabled_at 上，那三列
// **整组**是运行期状态、不进文件（口径层 §2.9 #28 ①；v0.95 之后只有人工停用会写它，
// 老库里还可能躺着当年 401 自动摘除写的行）。delete-all + insert 会把停用的 key
// 拉回轮询池；把 disabled 当意图列收进文件也会——只是入口更隐蔽。
func TestApplyKeepsDisabledCredential(t *testing.T) {
	db := openDB(t)
	// 用**双凭证**而不是 goodFile 的单凭证，因为 store.Validate 的
	// checkChannelHasCredential 要求每个未停用渠道至少留一份启用凭证：单凭证渠道停掉
	// 唯一那把之后，下次启动会被那道闸拒——**那是既有闸与本形态的真实交互，不是这条
	// 用例要测的东西**（已单独报给 PO）。这里要钉的是「停用现场跨 apply 活着」。
	pool := strings.Replace(goodFile,
		"      - name: 主号\n        credential: sk-upstream-1\n",
		"      - name: 主号\n        credential: sk-upstream-1\n      - name: 备号\n        credential: sk-upstream-2\n", 1)
	mustApply(t, db, pool)

	var credID int64
	if err := db.QueryRow(`SELECT id FROM channel_keys WHERE name='主号'`).Scan(&credID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1,
		disabled_reason = '人工停用', disabled_at = CURRENT_TIMESTAMP
		WHERE id = ?`, credID); err != nil {
		t.Fatalf("停用凭证: %v", err)
	}

	// 同一份文件再 apply 一遍，模拟一次重启。
	mustApply(t, db, pool)

	var disabled int
	var reason sql.NullString
	if err := db.QueryRow(`SELECT disabled, disabled_reason FROM channel_keys WHERE id=?`, credID).
		Scan(&disabled, &reason); err != nil {
		t.Fatal(err)
	}
	if disabled != 1 {
		t.Error("重启 apply 之后停用的 key 被拉回了轮询池——停用现场丢了")
	}
	if !reason.Valid || reason.String == "" {
		t.Error("停用原因被 apply 抹掉了；那三列整组是运行期状态，apply 不该碰")
	}
}

// TestApplyDeletesEntitiesMissingFromFile 钉住「文件里没有的一律删」与删除顺序。
//
// candidates.channel_model_id 那条外键**没有** ON DELETE CASCADE，删渠道前不先清候选
// 就会被外键顶回来。
func TestApplyDeletesEntitiesMissingFromFile(t *testing.T) {
	db := openDB(t)
	two := strings.Replace(goodFile, "access_points:", `  - name: spare
    base_url:
      anthropic: https://spare.internal
    credentials:
      - name: 备号
        credential: sk-upstream-2
    models:
      - upstream_model: claude-x
access_points:`, 1)
	// spare 自带一个接入点，不跟 qwen 挤同一个：临时闸要求每个未停用接入点**恰好**
	// 1 个 weight>0 的候选（多候选分流在 M4）。
	two = strings.Replace(two, "api_keys:", `  - model: claude-x-direct
    candidates:
      - target: spare/claude-x
api_keys:`, 1)
	mustApply(t, db, two)
	mustApply(t, db, goodFile) // spare 从文件里消失

	for _, q := range []struct{ what, query string }{
		{"渠道", `SELECT COUNT(*) FROM channels WHERE name='spare'`},
		{"纳管模型", `SELECT COUNT(*) FROM channel_models WHERE upstream_model='claude-x'`},
		{"候选", `SELECT COUNT(*) FROM candidates`},
	} {
		var n int
		if err := db.QueryRow(q.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if q.what == "候选" {
			if n != 1 {
				t.Errorf("候选应当只剩文件里那一条，实得 %d", n)
			}
			continue
		}
		if n != 0 {
			t.Errorf("文件里没有的%s没被删掉，还剩 %d 条", q.what, n)
		}
	}
}

// TestWeightZeroIsIntentNotAbsence 钉住零值陷阱（口径层 §2.9 #29）。
//
// weight=0 是「临时摘除」这个意图，不是「没写」。补默认值会让写 0 的人摘不掉。
func TestWeightZeroIsIntentNotAbsence(t *testing.T) {
	db := openDB(t)
	// weight: 0 只能挂在**停用**的接入点上——M4 之前 checkSingleCandidate 要求每个
	// 未停用接入点恰好 1 个 weight>0 的候选，于是「唯一那条候选摘掉」这个形态今天
	// 表达不出来（已单独报给 PO）。零值处理本身与接入点停没停用无关，这里照样钉得住。
	mustApply(t, db, strings.Replace(goodFile, "api_keys:", `  - model: 备用口
    disabled: true
    candidates:
      - target: qwen/Qwen3-27B
        weight: 0
api_keys:`, 1))
	var w int
	if err := db.QueryRow(`SELECT weight FROM candidates c
		JOIN access_points ap ON ap.id = c.access_point_id WHERE ap.model = '备用口'`).Scan(&w); err != nil {
		t.Fatal(err)
	}
	if w != 0 {
		t.Fatalf("显式写的 weight: 0 被补成了 %d，人就摘不掉这条候选了", w)
	}

	t.Run("没写则跟 DDL 默认 100 走", func(t *testing.T) {
		db := openDB(t)
		mustApply(t, db, goodFile)
		var w int
		if err := db.QueryRow(`SELECT weight FROM candidates`).Scan(&w); err != nil {
			t.Fatal(err)
		}
		if w != 100 {
			t.Fatalf("缺席的 weight 应当是 DDL 默认 100，实得 %d", w)
		}
	})
}

// TestStatefulBitDefaultsToTrue 钉住那个与 Go 零值相反的 DDL 默认（口径层 v0.88）。
//
// 位为否会 400 掉本来能正常续链的请求，所以「手写文件里没写这一项」不能静默变成否。
func TestStatefulBitDefaultsToTrue(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, goodFile)
	var bit int
	if err := db.QueryRow(`SELECT supports_stateful_responses FROM channels WHERE name='qwen'`).Scan(&bit); err != nil {
		t.Fatal(err)
	}
	if bit != 1 {
		t.Fatal("没写 supports_stateful_responses 时该跟 DDL 默认取「是」，实得「否」")
	}
	t.Run("显式写 false 则是 false", func(t *testing.T) {
		// 渠道得声明 Responses 协议这一位才轮得到人写：不含协议时 writer 强制归位
		// 默认值（#33，见 TestResponsesBitsResetWhenProtocolAbsent）。
		db := openDB(t)
		mustApply(t, db, strings.Replace(goodFile, "    base_url:",
			"    supports_stateful_responses: false\n    base_url:\n      openai_responses: https://example.internal", 1))
		var bit int
		if err := db.QueryRow(`SELECT supports_stateful_responses FROM channels WHERE name='qwen'`).Scan(&bit); err != nil {
			t.Fatal(err)
		}
		if bit != 0 {
			t.Fatal("显式写的 false 没被写进去")
		}
	})
}

// TestResponsesBitsResetWhenProtocolAbsent 钉住 #48 收门要闭合的那个缺口：apply 走
// store 的渠道 writer 之后，#33 的归位不变式对声明文件同样生效。
//
// 收门前的 raw upsert 会把 supports_compaction: true 原样写给一个没声明 Responses
// 的渠道——正是 #33 要杀的那种行：界面上看不见、也改不掉，哪天把协议勾回来残值
// 原样复活（supports_compaction 残留 1 会让 Codex 静默 Fatal，v0.54 ⑨）。
func TestResponsesBitsResetWhenProtocolAbsent(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, strings.Replace(goodFile, "    base_url:",
		"    supports_compaction: true\n    supports_stateful_responses: false\n    base_url:", 1))
	var compaction, stateful int
	if err := db.QueryRow(`SELECT supports_compaction, supports_stateful_responses
		FROM channels WHERE name='qwen'`).Scan(&compaction, &stateful); err != nil {
		t.Fatal(err)
	}
	if compaction != 0 || stateful != 1 {
		t.Fatalf("不含 Responses 协议的渠道两个能力位该归位默认值（0/1），实得 %d/%d", compaction, stateful)
	}

	t.Run("改渠道那条路同样归位", func(t *testing.T) {
		// 先按含 Responses 的形态落一次非默认值，再去掉协议重新 apply——走的是
		// UpdateChannel 那支。收门前这正是残值复活的入口。
		db := openDB(t)
		withResponses := strings.Replace(goodFile, "    base_url:",
			"    supports_compaction: true\n    base_url:\n      openai_responses: https://example.internal", 1)
		mustApply(t, db, withResponses)
		mustApply(t, db, goodFile)
		var compaction int
		if err := db.QueryRow(`SELECT supports_compaction FROM channels WHERE name='qwen'`).Scan(&compaction); err != nil {
			t.Fatal(err)
		}
		if compaction != 0 {
			t.Fatal("去掉 Responses 协议再 apply，supports_compaction 残值没被归位")
		}
	})
}

// TestAPIKeyIsUsableAfterApply 钉住 key_hash / key_plain 两列都填对了。
//
// 只填一列的话闸全过、库看着正常，而客户端拿着文件里那把 key 打过来是 401——
// 这正是纯转发形态下最难查的一类失败。
func TestAPIKeyIsUsableAfterApply(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, goodFile)
	var hash, plain string
	if err := db.QueryRow(`SELECT key_hash, key_plain FROM api_keys WHERE name='laptop'`).Scan(&hash, &plain); err != nil {
		t.Fatal(err)
	}
	if hash != auth.Hash("sk-ptg-real-one") {
		t.Error("key_hash 不是明文的 SHA-256，鉴权会全 401")
	}
	if plain != "sk-ptg-real-one" {
		t.Errorf("key_plain 没落上，导出那半边会拿不到原值：%q", plain)
	}
}

// TestAllowedModelsMatchesWhatAuthReads 钉住白名单写进去的格式就是转发端读的格式。
//
// `api_keys.allowed_models` 是**逗号分隔**的，读它的是 `auth.Key.Allows`
// （`strings.Split(list, ",")` 逐段比）。声明文件这条入口一度把它写成 JSON 数组：
// 两道闸全过、库里那一行看着也像回事，而每一段都带着引号和方括号，跟请求里那个裸
// 模型名永远比不上——一把限了模型的 key 变成谁都放不过去的死 key。纯转发形态下没有
// 页面能看出这件事，只能从 401 往回猜。
//
// 断言直接落在 `Allows` 上而不是那个字符串上：格式将来真要换（换成 JSON 也不是不
// 行），要跟着换的是**两边一起**，而这条用例问的正是「两边还对得上吗」。
func TestAllowedModelsMatchesWhatAuthReads(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, strings.Replace(goodFile,
		"    key: sk-ptg-real-one",
		"    key: sk-ptg-real-one\n    allowed_models: [claude-sonnet-4, qwen/Qwen3-27B]", 1))

	k, err := auth.Resolve(context.Background(), db,
		http.Header{"X-Api-Key": []string{"sk-ptg-real-one"}})
	if err != nil {
		t.Fatalf("鉴权解不出这把 key：%v", err)
	}
	for _, model := range []string{"claude-sonnet-4", "qwen/Qwen3-27B"} {
		if !k.Allows(model) {
			t.Errorf("白名单里写了 %q 却放不过去；allowed_models=%q 与 auth 读的格式对不上",
				model, k.AllowedModels)
		}
	}
	if k.Allows("别的接入点") {
		t.Errorf("白名单外的模型不该放行；allowed_models=%q", k.AllowedModels)
	}
}

// TestAllowedModelsRejectsCommaInName 钉住带逗号的名字当场被拦。
//
// 这一列的分隔符就是逗号，带逗号的名字在里面表达不出来——存进去会被切成两段谁都
// 对不上，而闸全过、页面上也看着正常。
func TestAllowedModelsRejectsCommaInName(t *testing.T) {
	got := applyErr(t, openDB(t), strings.Replace(goodFile,
		"    key: sk-ptg-real-one",
		"    key: sk-ptg-real-one\n    allowed_models: [\"a,b\"]", 1))
	if !strings.Contains(got, "逗号") {
		t.Errorf("带逗号的白名单项该被点名，实得：%s", got)
	}
}

// TestApplyLogsChangesWithoutSecrets 钉住「变更进日志、秘密不进日志」。
//
// 纯转发形态下日志是唯一的观测出口，正因如此它更不能变成秘密的第二个副本。
func TestApplyLogsChangesWithoutSecrets(t *testing.T) {
	db := openDB(t)
	var buf strings.Builder
	f, err := declcfg.Parse([]byte(goodFile), "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := declcfg.Apply(context.Background(), db, f, textLogger(&buf)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"新增渠道 qwen", "新增凭证 qwen/主号", "新增接入点 claude-sonnet-4", "新增 API Key laptop"} {
		if !strings.Contains(out, want) {
			t.Errorf("启动日志缺了 %q：\n%s", want, out)
		}
	}
	for _, secret := range []string{"sk-upstream-1", "sk-ptg-real-one"} {
		if strings.Contains(out, secret) {
			t.Errorf("秘密 %q 进了日志", secret)
		}
	}
}
