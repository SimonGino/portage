package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 老库迁移（口径层 v1.13，#82）：补 channels.auth_scheme，存量行落 default——
// 即现行为，迁移前后一个字节都不变。
func TestMigrateAddsAuthScheme(t *testing.T) {
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
		`INSERT INTO channels (name, base_url_anthropic) VALUES ('存量', 'https://up.example')`); err != nil {
		t.Fatalf("插存量行: %v", err)
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("迁移: %v", err)
	}
	defer db.Close()

	var got string
	if err := db.QueryRow(`SELECT auth_scheme FROM channels WHERE name = '存量'`).Scan(&got); err != nil {
		t.Fatalf("读迁移后的列: %v", err)
	}
	if got != AuthSchemeDefault {
		t.Errorf("存量行 auth_scheme = %q，期望 %q", got, AuthSchemeDefault)
	}
}

// 值域闸：建渠道不提就是 default，意图写能改档，拼错的档位当场 400 而不是静默
// 退化成 default——「配了 raw 怎么还是 401」比一条点名的错误难查得多。
func TestAuthSchemeWrites(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	id, err := CreateChannel(ctx, db, ChannelInput{
		Name: "ch", BaseURLs: BaseURLs{Anthropic: "https://up.example"}})
	if err != nil {
		t.Fatalf("建渠道: %v", err)
	}

	read := func() string {
		t.Helper()
		var got string
		if err := db.QueryRow(`SELECT auth_scheme FROM channels WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("读列: %v", err)
		}
		return got
	}
	if got := read(); got != AuthSchemeDefault {
		t.Fatalf("不提字段建出来 = %q，期望 default", got)
	}

	raw := AuthSchemeRaw
	if err := UpdateChannelSettings(ctx, db, id, ChannelSettings{Name: "ch", AuthScheme: &raw}); err != nil {
		t.Fatalf("设置写 raw: %v", err)
	}
	if got := read(); got != AuthSchemeRaw {
		t.Fatalf("设置写后 = %q，期望 raw", got)
	}

	// nil = 弹框没提这个字段，那一列不动。
	if err := UpdateChannelSettings(ctx, db, id, ChannelSettings{Name: "ch"}); err != nil {
		t.Fatalf("设置不提字段: %v", err)
	}
	if got := read(); got != AuthSchemeRaw {
		t.Fatalf("不提字段把列改了：%q", got)
	}

	bad := "Bearer"
	if err := UpdateChannelSettings(ctx, db, id, ChannelSettings{Name: "ch", AuthScheme: &bad}); err == nil {
		t.Error("拼错的档位该被拒")
	}
	if _, err := CreateChannel(ctx, db, ChannelInput{
		Name: "ch2", BaseURLs: BaseURLs{Anthropic: "https://up.example"},
		AuthScheme: "x-api-key"}); err == nil {
		t.Error("建渠道带拼错的档位该被拒")
	}
}
