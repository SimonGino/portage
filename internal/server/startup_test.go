package server_test

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/ai-gateway/internal/gatewaytest"
	"github.com/SimonGino/ai-gateway/internal/store"
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

func TestStartupGateRejectsMultipleCredentials(t *testing.T) {
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "anthropic-official", "anthropic", "https://api.anthropic.com", "sk-first")
	gatewaytest.SeedCredential(t, db, channelID, "sk-second")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "anthropic-official", "2")
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

func TestStartupGateRejectsUnknownProtocol(t *testing.T) {
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "typo-channel", "anthropic_messages", "https://api.anthropic.com", "sk-a")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "claude-sonnet-4-5")
	apID := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, apID, modelID, 100)

	err := store.Validate(t.Context(), db)

	assertRejects(t, err, "typo-channel", "anthropic_messages")
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
