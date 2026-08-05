package server_test

import (
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
