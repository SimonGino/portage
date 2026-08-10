package server_test

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/ai-gateway/internal/gatewaytest"
	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/store"
)

// 支持协议集（口径层 v0.33）：一个渠道声明它能说的几个上游协议，选哪个由入站端点
// 决定——**能透传就透传**。协议因此不出现在对外模型名里，同一个限定名两条入口都能打。

func seedBothProtocols(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "gpt-luna", "openai_cc,openai_responses", up.URL, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "gpt-5.6-luna")
	return gatewaytest.Start(t, db), up
}

// 同一个渠道、同一个限定名，CC 入口打上游 /v1/chat/completions，Responses 入口打
// 上游 /v1/responses，两条都是透传。这正是拆两个渠道要解决的那件事：v0.33 之前它
// 只能靠两个渠道名（`…-cc` / `…-resp`）区分，而渠道名是对外模型 ID 的最外层。
func TestOneChannelServesBothOpenAIProtocolsByEndpoint(t *testing.T) {
	cases := []struct{ inbound, upstreamPath string }{
		{"/v1/chat/completions", "/v1/chat/completions"},
		{"/v1/responses", "/v1/responses"},
	}
	for _, tc := range cases {
		t.Run(tc.inbound, func(t *testing.T) {
			gw, up := seedBothProtocols(t)
			body := `{"model":"gpt-luna/gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`
			resp := gw.Post(t, tc.inbound, body, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
			}
			got := up.Last(t)
			if got.Path != tc.upstreamPath {
				t.Errorf("上游收到 %s，期望 %s——入站协议在集合里就该原样透传", got.Path, tc.upstreamPath)
			}
			// 透传：body 除 model 之外逐字节不变，没有经过 canonical 转换。
			if !strings.Contains(string(got.Body), `"model":"gpt-5.6-luna"`) {
				t.Errorf("上游收到的 model 不对: %s", got.Body)
			}
		})
	}
}

// 日志里的上游协议记的是**这次选中的**那一个，不是整个集合。
func TestCallLogRecordsTheChosenProtocol(t *testing.T) {
	gw, _ := seedBothProtocols(t)
	gw.Post(t, "/v1/responses", `{"model":"gpt-luna/gpt-5.6-luna","input":"hi"}`, nil)

	row := gw.LastCallRow(t)
	if row.UpstreamProtocol != "openai_responses" {
		t.Errorf("upstream_protocol = %q，期望这次选中的 openai_responses", row.UpstreamProtocol)
	}
}

// 入站协议不在集合里就转换，目标按固定优先级 cc > responses > anthropic 取。
// Anthropic 入口 → 集合 {cc, responses} → 选 CC，走已放开的 A→CC 转换路径。
func TestInboundNotInSetFallsBackToChatCompletions(t *testing.T) {
	gw, up := seedBothProtocols(t)

	body := `{"model":"gpt-luna/gpt-5.6-luna","max_tokens":16,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	resp := gw.Post(t, "/v1/messages", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if got := up.Last(t); got.Path != "/v1/chat/completions" {
		t.Errorf("上游收到 %s，期望回退到 /v1/chat/completions", got.Path)
	}
}

// 只声明 Anthropic 的渠道，CC 入口进来仍报「转换路径尚未实现」——回退顺序不会把
// 一条没落地的路径变成可用的。
func TestFallbackDoesNotOpenAnUnimplementedPath(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "anthropic", "anthropic", up.URL, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "claude-sonnet-4-5")
	gw := gatewaytest.Start(t, db)

	resp := gw.Post(t, "/v1/chat/completions",
		`{"model":"anthropic/claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("状态码 = %d，期望 501；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
}

// count_tokens 是 Anthropic 独有端点、没有转换路径：渠道不说 anthropic 就只能 501，
// 绝不会因为回退顺序被送去 /v1/chat/completions。
func TestCountTokensNeedsAnthropicInTheSet(t *testing.T) {
	gw, up := seedBothProtocols(t)

	resp := gw.Post(t, "/v1/messages/count_tokens",
		`{"model":"gpt-luna/gpt-5.6-luna","messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("状态码 = %d，期望 501；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if up.Count() != 0 {
		t.Error("count_tokens 不该打到上游")
	}
}

// 启动闸：protocols 空了的渠道选不出出站协议，每次请求才 500——按 v0.21 通则
// 启动即报。
func TestStartupGateRejectsAnEmptyProtocolSet(t *testing.T) {
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "broken", "openai_cc", "https://example.invalid", "sk-x")
	if _, err := db.Exec(`UPDATE channels SET protocols = '' WHERE id = ?`, ch); err != nil {
		t.Fatalf("改坏 protocols 失败: %v", err)
	}
	err := store.Validate(t.Context(), db)
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("启动闸没拦下空协议集: %v", err)
	}
}

// v0.33 的迁移：v0.33 之前的库里那一列叫 protocol，Open 要把它改名成 protocols
// 并保住值——不然老库一起来就是「no such column」。
func TestOpenMigratesTheOldProtocolColumn(t *testing.T) {
	path := t.TempDir() + "/old.db"

	// 造一个 v0.33 之前形状的库：先正常建库，再把列名改回去。
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("建旧库失败: %v", err)
	}
	if _, err := old.Exec(`
		CREATE TABLE channels (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  name TEXT NOT NULL UNIQUE,
		  protocol TEXT NOT NULL,
		  base_url TEXT NOT NULL,
		  credential_type TEXT NOT NULL DEFAULT 'api_key',
		  key_mode TEXT NOT NULL DEFAULT 'polling',
		  disabled INTEGER NOT NULL DEFAULT 0,
		  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO channels (name, protocol, base_url) VALUES ('old', 'openai_cc', 'https://h');`); err != nil {
		t.Fatalf("造旧表失败: %v", err)
	}
	old.Close()

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	defer db.Close()

	var protocols string
	if err := db.QueryRow(`SELECT protocols FROM channels WHERE name = 'old'`).Scan(&protocols); err != nil {
		t.Fatalf("迁移后读不到 protocols: %v", err)
	}
	// 值不动：单值在新语义下就是一元集合，含义一字不变。
	if protocols != "openai_cc" {
		t.Errorf("protocols = %q，期望原样保留 openai_cc", protocols)
	}
	set, err := protocol.ParseSet(protocols)
	if err != nil || !set.Has(protocol.OpenAICC) {
		t.Errorf("迁移后的值解不成集合: %v", err)
	}
}
