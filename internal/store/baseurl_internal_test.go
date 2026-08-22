package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 老库迁移（口径层 v0.96）：单一 base_url × 支持协议集，展开成每协议一份地址——
// 旧地址复制给每个已声明协议，迁移前后行为逐字节等价。旧的 protocols / base_url
// 两列随迁移删掉，免得留下一份会漂移的第二事实源。
func TestMigrateExpandsBaseURLPerProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开老库: %v", err)
	}
	// v0.96 之前的形状：channels 存单一 base_url + 逗号分隔的 protocols。
	if _, err := old.Exec(`CREATE TABLE channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		protocols TEXT NOT NULL,
		base_url TEXT NOT NULL,
		credential_type TEXT NOT NULL DEFAULT 'api_key',
		key_mode TEXT NOT NULL DEFAULT 'polling',
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("建老表: %v", err)
	}
	for _, row := range []struct{ name, protocols, url string }{
		{"双协议", "anthropic,openai", "https://mixed.example"},
		{"单协议", "openai_responses", "https://resp.example"},
		// 旧协议名 openai_cc 在 v0.36 的迁移里先被折成 openai，这里也得吃得下。
		{"旧名", "openai_cc", "https://cc.example"},
	} {
		if _, err := old.Exec(`INSERT INTO channels (name, protocols, base_url) VALUES (?,?,?)`,
			row.name, row.protocols, row.url); err != nil {
			t.Fatalf("插存量行 %s: %v", row.name, err)
		}
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("迁移: %v", err)
	}
	defer db.Close()

	for _, want := range []struct {
		name string
		urls BaseURLs
	}{
		{"双协议", BaseURLs{OpenAI: "https://mixed.example", Anthropic: "https://mixed.example"}},
		{"单协议", BaseURLs{OpenAIResponses: "https://resp.example"}},
		{"旧名", BaseURLs{OpenAI: "https://cc.example"}},
	} {
		var got BaseURLs
		if err := db.QueryRow(`SELECT base_url_openai, base_url_openai_responses, base_url_anthropic
			FROM channels WHERE name = ?`, want.name).
			Scan(&got.OpenAI, &got.OpenAIResponses, &got.Anthropic); err != nil {
			t.Fatalf("读迁移后的 %s: %v", want.name, err)
		}
		if got != want.urls {
			t.Errorf("%s 迁移后 = %+v，期望 %+v", want.name, got, want.urls)
		}
	}

	// 旧列必须删掉：留着就是第二份事实源，谁写谁读都会漂。
	for _, col := range []string{"protocols", "base_url"} {
		has, err := hasColumn(db, "channels", col)
		if err != nil {
			t.Fatalf("查列 %s: %v", col, err)
		}
		if has {
			t.Errorf("迁移后 channels.%s 还在，旧列该删", col)
		}
	}
}

// 解析不动 protocols 的存量行（手写 SQL 灌坏的停用渠道）不该把迁移整个掀翻：
// 那种行迁完就是「一个协议都没声明」，启用它时启动闸自然会拦。
func TestMigrateSkipsUnparseableProtocols(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开老库: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		protocols TEXT NOT NULL,
		base_url TEXT NOT NULL,
		disabled INTEGER NOT NULL DEFAULT 1)`); err != nil {
		t.Fatalf("建老表: %v", err)
	}
	if _, err := old.Exec(
		`INSERT INTO channels (name, protocols, base_url, disabled) VALUES ('坏行', 'gemini', 'https://x', 1)`); err != nil {
		t.Fatalf("插坏行: %v", err)
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("迁移不该因一行脏数据失败: %v", err)
	}
	defer db.Close()

	var got BaseURLs
	if err := db.QueryRow(`SELECT base_url_openai, base_url_openai_responses, base_url_anthropic
		FROM channels WHERE name = '坏行'`).
		Scan(&got.OpenAI, &got.OpenAIResponses, &got.Anthropic); err != nil {
		t.Fatalf("读坏行: %v", err)
	}
	if got != (BaseURLs{}) {
		t.Errorf("坏行迁移后 = %+v，期望三列全空", got)
	}
}

// 启动闸（v0.21 通则逐份适用）：每份已填协议地址各过一遍绝对 http/https 校验，
// 一个协议都没填的启用渠道直接拒。
func TestValidateChecksPerProtocolBaseURLs(t *testing.T) {
	t.Run("一份地址都没有即拒", func(t *testing.T) {
		db := openTestDB(t)
		seedChannel(t, db, "openai", "")
		if _, err := db.Exec(`UPDATE channels SET base_url_openai = '' WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
		err := Validate(context.Background(), db)
		if err == nil || !strings.Contains(err.Error(), "ch") {
			t.Fatalf("零地址渠道该被拦并点名: %v", err)
		}
	})

	t.Run("坏地址逐份拦且不回显值", func(t *testing.T) {
		db := openTestDB(t)
		seedChannel(t, db, "openai,anthropic", "")
		if _, err := db.Exec(
			`UPDATE channels SET base_url_anthropic = 'ftp://secret-host' WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
		err := Validate(context.Background(), db)
		if err == nil {
			t.Fatal("坏地址该被拦")
		}
		if !strings.Contains(err.Error(), "anthropic") {
			t.Errorf("该点名是哪个协议的地址: %v", err)
		}
		if strings.Contains(err.Error(), "secret-host") {
			t.Errorf("错误回显泄露了地址值: %v", err)
		}
	})
}

// 派生协议集的顺序是固定的回退序（openai → openai_responses → anthropic），
// First 取第一个已填协议——fetch-models 选根地址用的就是它。
func TestBaseURLsDeriveProtocols(t *testing.T) {
	u := BaseURLs{Anthropic: "https://a.example", OpenAI: "https://o.example"}
	if got := u.Protocols().String(); got != "openai,anthropic" {
		t.Errorf("Protocols() = %q，期望按回退序 openai,anthropic", got)
	}
	if got := u.Get(protocol.Anthropic); got != "https://a.example" {
		t.Errorf("Get(anthropic) = %q", got)
	}
	p, url := u.First()
	if p != protocol.OpenAI || url != "https://o.example" {
		t.Errorf("First() = %q %q，期望 openai 的地址", p, url)
	}
}
