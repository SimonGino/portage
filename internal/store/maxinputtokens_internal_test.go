package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 两条解析路径都要带上这一列——接入点路径和限定名直连路径各走各的 SQL，漏掉一处
// 的话直连就绕过了上限（同 protocols 那列的既有立论）。
func TestResolveCarriesMaxInputTokensOnBothPaths(t *testing.T) {
	for _, tc := range []struct{ name, model string }{
		{"接入点", "ap"},
		{"限定名直连", "ch/gpt-4o"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			seedChannel(t, db, "openai", "")
			if _, err := db.Exec(`UPDATE channel_models SET max_input_tokens = 200000`); err != nil {
				t.Fatalf("设上限: %v", err)
			}

			cand, err := Resolve(context.Background(), db, tc.model, protocol.OpenAI)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if cand.MaxInputTokens != 200000 {
				t.Errorf("MaxInputTokens = %d, 期望 200000", cand.MaxInputTokens)
			}
		})
	}
}

// 负数拒而不是当 0 用：0 已经是「不限」，负数只能是填错（同 max_concurrency 立论）。
func TestSetChannelModelMaxInputTokensRejectsNegative(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")

	err := SetChannelModelMaxInputTokens(context.Background(), db, 1, -1)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, 期望 ErrInvalidInput", err)
	}
	if err := SetChannelModelMaxInputTokens(context.Background(), db, 1, 0); err != nil {
		t.Fatalf("清成不限失败: %v", err)
	}
}

// 老库迁移：没有 max_input_tokens 列的 channel_models 加完列之后，存量行拿到的是 0，
// 也就是「不限」——迁移前后行为一字不变，所以不需要回填。
func TestMigrateAddsModelMaxInputTokensUnlimitedDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开老库: %v", err)
	}
	// v0.99 之前的形状：channel_models 没有 max_input_tokens 这一列。
	if _, err := old.Exec(`CREATE TABLE channel_models (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id INTEGER NOT NULL,
		upstream_model TEXT NOT NULL,
		protocols TEXT NOT NULL DEFAULT '',
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(channel_id, upstream_model))`); err != nil {
		t.Fatalf("建老表: %v", err)
	}
	if _, err := old.Exec(
		`INSERT INTO channel_models (channel_id, upstream_model) VALUES (1, 'legacy')`); err != nil {
		t.Fatalf("插存量行: %v", err)
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("迁移: %v", err)
	}
	defer db.Close()

	var got int
	if err := db.QueryRow(
		`SELECT max_input_tokens FROM channel_models WHERE upstream_model = 'legacy'`).Scan(&got); err != nil {
		t.Fatalf("读存量行: %v", err)
	}
	if got != 0 {
		t.Errorf("存量行 max_input_tokens = %d，期望 0（不限）", got)
	}
}
