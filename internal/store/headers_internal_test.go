package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 老库迁移（#137）：补 channels.headers，存量行落空串 = 不带额外头，行为零变化。
func TestMigrateAddsChannelHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开老库: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		base_url_openai TEXT NOT NULL DEFAULT '',
		base_url_openai_responses TEXT NOT NULL DEFAULT '',
		base_url_anthropic TEXT NOT NULL DEFAULT '',
		credential_type TEXT NOT NULL DEFAULT 'api_key',
		key_mode TEXT NOT NULL DEFAULT 'polling',
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("建老表: %v", err)
	}
	if _, err := old.Exec(
		`INSERT INTO channels (name, base_url_openai) VALUES ('存量', 'https://up.example')`); err != nil {
		t.Fatalf("插存量行: %v", err)
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("迁移: %v", err)
	}
	defer db.Close()
	var got string
	if err := db.QueryRow(`SELECT headers FROM channels WHERE name = '存量'`).Scan(&got); err != nil {
		t.Fatalf("读迁移后的列: %v", err)
	}
	if got != "" {
		t.Errorf("存量行 headers = %q，期望空串", got)
	}
}

// 写侧：落排序 JSON；nil = 没提不动（管理端那几笔字段写不带它）；空 map = 清掉。
func TestChannelHeadersWrites(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	urls := BaseURLs{OpenAI: "https://up.example"}
	id, err := CreateChannel(ctx, db, ChannelInput{Name: "ch", BaseURLs: urls,
		Headers: map[string]string{"x-opencode-session": "s1", "User-Agent": "portage/1"}})
	if err != nil {
		t.Fatalf("建渠道: %v", err)
	}
	read := func() string {
		t.Helper()
		var got string
		if err := db.QueryRow(`SELECT headers FROM channels WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("读列: %v", err)
		}
		return got
	}
	const want = `{"User-Agent":"portage/1","x-opencode-session":"s1"}`
	if got := read(); got != want {
		t.Fatalf("建出来 = %s，期望 %s", got, want)
	}
	if err := UpdateChannel(ctx, db, id, ChannelInput{Name: "ch", BaseURLs: urls}); err != nil {
		t.Fatalf("不提 headers 改渠道: %v", err)
	}
	if got := read(); got != want {
		t.Fatalf("不提字段把列改了：%s", got)
	}
	if err := UpdateChannel(ctx, db, id, ChannelInput{Name: "ch", BaseURLs: urls, Headers: map[string]string{}}); err != nil {
		t.Fatalf("清 headers: %v", err)
	}
	if got := read(); got != "" {
		t.Fatalf("空 map 该清成空串，实得 %s", got)
	}
}

// 保留头名两类（PO 2026-09-29）：网关自己的 7 个 + Go 会忽略/重算的 Host、
// Content-Length、逐跳头；大小写不敏感。User-Agent 放行。
func TestValidateHeaders(t *testing.T) {
	for _, name := range []string{"Authorization", "X-API-KEY", "content-type", "Accept", "Accept-Encoding",
		"anthropic-version", "Anthropic-Beta", "Host", "Content-Length", "Connection", "Transfer-Encoding",
		"Keep-Alive", "TE", "Trailer", "Upgrade", "Proxy-Authorization", "Proxy-Connection"} {
		if ValidateHeaders(map[string]string{name: "v"}) == nil {
			t.Errorf("保留头名 %q 该被拒", name)
		}
	}
	bad := []map[string]string{
		{"x-a": "1", "X-A": "2"},    // 只差大小写
		{"x-a": ""},                 // 空值
		{"x-a": "v\r\nInjected: 1"}, // 头注入
		{"bad name": "v"},           // 头名非法
	}
	for _, h := range bad {
		if ValidateHeaders(h) == nil {
			t.Errorf("%v 该被拒", h)
		}
	}
	if err := ValidateHeaders(map[string]string{"User-Agent": "portage/1", "x-opencode-session": "a,b"}); err != nil {
		t.Errorf("User-Agent 与带逗号的值该放行：%v", err)
	}
	if _, err := CreateChannel(context.Background(), openTestDB(t), ChannelInput{Name: "ch",
		BaseURLs: BaseURLs{OpenAI: "https://up.example"}, Headers: map[string]string{"Host": "x"}}); err == nil {
		t.Error("writer 该拦保留头名")
	}
}

// 候选投影带上 headers：Resolve 两条路径都得有，转发才发得出去。
func TestResolveCarriesChannelHeaders(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	if _, err := db.Exec(`UPDATE channels SET headers = '{"x-opencode-session":"s1"}'`); err != nil {
		t.Fatalf("改库: %v", err)
	}
	for _, model := range []string{"ap", "ch/gpt-4o"} {
		c, err := Resolve(context.Background(), db, model, protocol.OpenAI)
		if err != nil {
			t.Fatalf("Resolve %s: %v", model, err)
		}
		if c.Headers["x-opencode-session"] != "s1" {
			t.Errorf("%s 解析出的候选 Headers = %v", model, c.Headers)
		}
	}
	target, err := ChannelProbeTarget(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("ChannelProbeTarget: %v", err)
	}
	if target.Headers["x-opencode-session"] != "s1" {
		t.Errorf("检测目标 Headers = %v", target.Headers)
	}
}
