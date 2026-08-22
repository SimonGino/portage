package server_test

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/store"
)

func TestSchemaIsCreatedAndReopenable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("首次开库失败: %v", err)
	}
	db.Close()

	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("重复开库失败: %v", err)
	}
	defer db.Close()

	want := []string{"channels", "channel_keys", "access_points", "channel_models", "candidates", "api_keys", "call_logs"}
	for _, table := range want {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("表 %s 不存在: %v", table, err)
		}
	}
}

func TestStartupGateRejectsMultipleCandidates(t *testing.T) {
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "anthropic-official", "anthropic", "https://api.anthropic.com", "sk-a")
	first := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	second := gatewaytest.SeedChannelModel(t, db, channelID, "claude-opus-4-1")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, first, 100)
	gatewaytest.SeedCandidate(t, db, apID, second, 100)

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "gw-sonnet", "2")
}

// 临时闸的凭证那一半已于口径层 v0.38 放开（凭证池聚合前移到 M3）：多份启用凭证是
// 合法配置，不再是「恰好 1 份」。这条用例钉的正是那次放开——单候选那一半仍在，
// 由 TestStartupGateRejectsMultipleCandidates 守着。
func TestStartupGateAcceptsMultipleCredentials(t *testing.T) {
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "anthropic-official", "anthropic", "https://api.anthropic.com", "sk-first")
	gatewaytest.SeedCredential(t, db, channelID, "sk-second")
	gatewaytest.SeedCredential(t, db, channelID, "sk-third")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)

	if err := store.Validate(t.Context(), db); err != nil {
		t.Fatalf("三份启用凭证被启动闸拒了: %v", err)
	}
}

// 凭证名渠道内唯一（口径层 v0.38）：日志与用量的归因全靠这个名字，库层面不拦的话
// 两行都叫「主号」时归因本身就废了。跨渠道同名照样合法——那是两个上游账号的事。
func TestCredentialNamesAreUniquePerChannel(t *testing.T) {
	db := gatewaytest.NewDB(t)
	a := gatewaytest.SeedChannel(t, db, "chan-a", "anthropic", "https://api.anthropic.com", "sk-a")
	b := gatewaytest.SeedChannel(t, db, "chan-b", "anthropic", "https://api.anthropic.com", "sk-b")
	gatewaytest.SeedNamedCredential(t, db, a, "主号", "sk-1")
	gatewaytest.SeedNamedCredential(t, db, b, "主号", "sk-2")

	if _, err := db.Exec(
		`INSERT INTO channel_keys (channel_id, name, credential) VALUES (?, ?, ?)`, a, "主号", "sk-3"); err == nil {
		t.Fatal("同一渠道内的重名凭证竟然插进去了")
	}
}

func TestStartupGateRejectsAccessPointWithoutCandidate(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedAccessPoint(t, db, "gw-orphan")

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "gw-orphan")
}

func TestStartupGateRejectsChannelWithoutCredential(t *testing.T) {
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "keyless", "anthropic", "https://api.anthropic.com", "")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "keyless")
}

// 网关自己开的连接强制 foreign_keys=ON，插不进悬空候选；但 M0 的配置流程是拿
// sqlite3 CLI 手工 INSERT，而 CLI 默认 foreign_keys=OFF。这里复现 CLI 的宽松写入，
// 证明启动校验能兜住 FK 兜不住的那一半。
func TestStartupGateRejectsDanglingCandidate(t *testing.T) {
	db := gatewaytest.NewDB(t)
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-dangling")

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	gatewaytest.SeedCandidate(t, db, apID, 4242, 100)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "gw-dangling")
}

// 停用渠道时忘了把接入点一起停掉，是手写 SQL 最容易留下的半截状态：启动照样过，
// 接入点还挂在 /v1/models 上，打过去才回 503。这类错该在启动就被点名。
func TestStartupGateRejectsCandidateOnDisabledChannel(t *testing.T) {
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "anthropic-official", "anthropic", "https://api.anthropic.com", "sk-a")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
	mustExec(t, db, `UPDATE channels SET disabled = 1 WHERE id = ?`, channelID)

	err := store.Validate(t.Context(), db)

	// needle 带上接入点名再接 "(id="，否则渠道那一处的 "(id=" 会把断言顶成空转。
	assertRejects(t, err, `接入点 "gw-sonnet" (id=`, "anthropic-official", "该渠道已停用", "接入点要跟着停用")
}

// 渠道还开着但唯一那份凭证被停用，等价于没凭证：checkSingleCredential 数的是启用
// 凭证，所以这一条已经被它挡住；这里钉的是**接入点也要被点名**，否则只知道渠道坏了，
// 不知道哪个对外模型受影响。同时钉住补救建议——渠道还开着时正解是补凭证，
// 不是「接入点跟着停用」。
func TestStartupGateRejectsCandidateOnChannelWithDisabledCredential(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "gw-sonnet", "anthropic", "https://api.anthropic.com", "claude-sonnet-4-5", "sk-a")
	mustExec(t, db, `UPDATE channel_keys SET disabled = 1`)

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "gw-sonnet", "该渠道没有启用凭证", "补一份启用凭证")
	if strings.Contains(err.Error(), "接入点要跟着停用") {
		t.Errorf("渠道还开着，却建议停用接入点: %v", err)
	}
}

// 只停纳管模型是「过校验、请求时 503」的第三种写法：Resolve 过滤 cm.disabled，
// 启动校验也得过滤，否则同一类错三种写法拦两种，分界线讲不出道理。
func TestStartupGateRejectsCandidateOnDisabledChannelModel(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "gw-sonnet", "anthropic", "https://api.anthropic.com", "claude-sonnet-4-5", "sk-a")
	mustExec(t, db, `UPDATE channel_models SET disabled = 1`)

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "gw-sonnet", "test-anthropic", "该纳管模型已停用")
}

// 渠道与接入点一起停用是干净状态，不该报错——否则「手头只有一边的 key」这个
// 最常见的场景会被校验逼疯。
func TestStartupGateAcceptsDisabledChannelWithDisabledAccessPoint(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "gw-sonnet", "anthropic", "https://api.anthropic.com", "claude-sonnet-4-5", "sk-a")
	mustExec(t, db, `UPDATE channels SET disabled = 1`)
	mustExec(t, db, `UPDATE access_points SET disabled = 1 WHERE model = 'gw-sonnet'`)

	if err := store.Validate(t.Context(), db); err != nil {
		t.Fatalf("渠道与接入点一起停用被拒: %v", err)
	}
}

// weight=0 的候选在临时闸下是死的（checkSingleCandidate 只数 weight>0），它指向
// 哪条渠道都不该报错——否则「先把候选权重清零、渠道留着待用」这种写法会被误伤。
func TestStartupGateIgnoresZeroWeightCandidateOnDisabledChannel(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "gw-sonnet", "anthropic", "https://api.anthropic.com", "claude-sonnet-4-5", "sk-a")
	spare := gatewaytest.SeedChannel(t, db, "spare", "anthropic", "https://api.anthropic.com", "sk-b")
	spareModel := gatewaytest.SeedChannelModel(t, db, spare, "claude-opus-4-1")
	var apID int64
	if err := db.QueryRow(`SELECT id FROM access_points WHERE model = 'gw-sonnet'`).Scan(&apID); err != nil {
		t.Fatal(err)
	}
	gatewaytest.SeedCandidate(t, db, apID, spareModel, 0)
	mustExec(t, db, `UPDATE channels SET disabled = 1 WHERE id = ?`, spare)

	if err := store.Validate(t.Context(), db); err != nil {
		t.Fatalf("weight=0 的候选指向停用渠道被拒: %v", err)
	}
}

// 渠道侧的协议名拼错在 v0.96 之后表达不出来了（协议集从每协议地址列派生），但
// channel_models.protocols 还是手写 SQL 能灌坏的一列，拼错的名字仍要启动即拒。
func TestStartupGateRejectsUnknownProtocol(t *testing.T) {
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "typo-channel", "anthropic", "https://api.anthropic.com", "sk-a")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
	mustExec(t, db, `UPDATE channel_models SET protocols = 'anthropic_messages' WHERE id = ?`, modelID)

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "typo-channel", "anthropic_messages")
}

// base_url 是手写 SQL 灌进来的，schema 只要求非 NULL。校验不看它的话，空串、漏了
// scheme 的裸域名、ftp:// 全都能过启动，接入点照常挂在 /v1/models 上，每次请求才在
// http.Client.Do 里失败回 502——正是口径层 v0.18 判过的「配置能过校验但请求时才炸」。
func TestStartupGateRejectsUnusableBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
	}{
		{"空串", ""},
		{"漏了 scheme 的裸域名", "api.anthropic.com"},
		{"只有路径", "/v1"},
		{"scheme 不是 http(s)", "ftp://api.anthropic.com"},
		{"有 scheme 无 host", "https://"},
		// buildURL 是字符串拼接：带查询串或 fragment 时，协议子路径会被拼到
		// ? / # 之后，Go 解出来的 path 仍是前缀那一段，请求永远打错地方。
		{"带查询串", "https://api.anthropic.com/prefix?x=1"},
		{"带空查询串", "https://api.anthropic.com/prefix?"},
		{"带 fragment", "https://api.anthropic.com/prefix#frag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := gatewaytest.NewDB(t)
			channelID := gatewaytest.SeedChannel(t, db, "bad-url", "anthropic", tc.baseURL, "sk-a")
			modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
			apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
			gatewaytest.SeedCandidate(t, db, apID, modelID, 100)

			err := store.Validate(t.Context(), db)

			// 空串在 v0.96 之后落到「一个协议地址都没填」那条报文，其余落到
			// 「哪个协议的地址哪里不对」，公共词是「地址」。
			assertRejects(t, err, "bad-url", "地址")
		})
	}
}

// base_url 可能带 userinfo，把它回显进错误信息就是把上游密码打进 stderr——
// cmd/portage 会把 Validate 的错误直接落日志。CLAUDE.md：错误回显严禁泄露 base_url。
func TestStartupGateDoesNotEchoBaseURL(t *testing.T) {
	const secret = "https://alice:hunter2@internal.example.invalid/private?x=1"
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "leaky", "anthropic", secret, "sk-a")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)

	err := store.Validate(t.Context(), db)
	if err == nil {
		t.Fatal("带查询串的 base_url 该被拒")
	}

	for _, leak := range []string{"hunter2", "alice", "internal.example.invalid", "/private", secret} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("错误信息泄露了 %q：%v", leak, err)
		}
	}
	// 不回显值不等于不可诊断：仍要点名是哪条渠道、哪个协议的地址、哪里不对。
	for _, want := range []string{"leaky", "anthropic", "协议地址", "查询串"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺少 %q，无法定位：%v", want, err)
		}
	}
}

// 停用渠道不参与该校验——与既有几条 checkChannelFields 的语义一致，否则「手头没有
// 这家的 key，先把渠道连同接入点一起停掉」这条示例 SQL 里写明的做法就走不通了。
func TestStartupGateIgnoresBaseURLOfDisabledChannel(t *testing.T) {
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "bad-url", "anthropic", "", "sk-a")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
	mustExec(t, db, `UPDATE channels SET disabled = 1 WHERE id = ?`, channelID)
	mustExec(t, db, `UPDATE access_points SET disabled = 1 WHERE id = ?`, apID)

	if err := store.Validate(t.Context(), db); err != nil {
		t.Fatalf("停用渠道的 base_url 不该参与校验: %v", err)
	}
}

func TestStartupGateAcceptsSingleCandidateSingleCredential(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "gw-sonnet", "anthropic", "https://api.anthropic.com", "claude-sonnet-4-5", "sk-a")

	if err := store.Validate(t.Context(), db); err != nil {
		t.Fatalf("合法配置被拒: %v", err)
	}
}

func TestHealthz(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	resp := gw.Get(t, "/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("库正常时 /healthz = %d, 期望 200", resp.StatusCode)
	}

	gw.DB.Close()

	resp = gw.Get(t, "/healthz")
	if resp.StatusCode == http.StatusOK {
		t.Error("库不可用时 /healthz 仍回 200")
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("执行 %q 失败: %v", query, err)
	}
}

// assertRejects checks the gate failed and that its message names every needle,
// so a hand-written SQL row can be located without guessing.
func assertRejects(t *testing.T, err error, needles ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("非法配置未被启动校验拒绝")
	}
	for _, needle := range needles {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("校验错误未点名 %q: %v", needle, err)
		}
	}
}
