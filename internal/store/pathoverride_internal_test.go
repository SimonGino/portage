package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// ValidatePathOverrides 的形状闸（#175）：首斜杠、无尾斜杠、无查询串、无片段、% 转义合法。
func TestValidatePathOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    PathOverrides
		ok   bool
	}{
		{"空", PathOverrides{}, true},
		{"合法", PathOverrides{OpenAIResponses: "/responses"}, true},
		{"合法的 %23", PathOverrides{OpenAI: "/backend%23codex"}, true},
		{"缺首斜杠", PathOverrides{OpenAI: "responses"}, false},
		{"尾斜杠", PathOverrides{OpenAI: "/responses/"}, false},
		{"带查询串", PathOverrides{OpenAI: "/responses?x=1"}, false},
		{"带片段", PathOverrides{OpenAI: "/backend#codex/responses"}, false},
		{"非法百分号转义", PathOverrides{OpenAI: "/responses%zz"}, false},
		{"被截断的百分号转义", PathOverrides{OpenAI: "/responses%2"}, false},
		{"anthropic 也过闸", PathOverrides{Anthropic: "/custom/"}, false},
	} {
		err := ValidatePathOverrides(tc.o)
		if tc.ok && err != nil {
			t.Errorf("%s：期望通过，得到 %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s：期望拒绝", tc.name)
		}
	}
}

// 老库迁移（#175）：补三个 path_override_* 列，存量行落空串 = 不覆盖。
func TestMigrateAddsPathOverrideColumns(t *testing.T) {
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
		auth_scheme TEXT NOT NULL DEFAULT 'default',
		headers TEXT NOT NULL DEFAULT '',
		max_concurrency INTEGER NOT NULL DEFAULT 0,
		supports_compaction INTEGER NOT NULL DEFAULT 0,
		supports_stateful_responses INTEGER NOT NULL DEFAULT 1,
		provider TEXT NOT NULL DEFAULT '',
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
	var got PathOverrides
	if err := db.QueryRow(`SELECT path_override_openai, path_override_openai_responses, path_override_anthropic
		FROM channels WHERE name = '存量'`).
		Scan(&got.OpenAI, &got.OpenAIResponses, &got.Anthropic); err != nil {
		t.Fatalf("读迁移后的列: %v", err)
	}
	if got != (PathOverrides{}) {
		t.Errorf("存量行 path_override = %+v，期望空", got)
	}
}

// 候选投影带上路径覆盖：Resolve 两条路径与检测目标都得有，转发才打得到正确子路径。
func TestResolveCarriesChannelPathOverride(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	if _, err := db.Exec(`UPDATE channels SET path_override_openai = '/custom/chat'`); err != nil {
		t.Fatalf("改库: %v", err)
	}
	for _, model := range []string{"ap", "ch/gpt-4o"} {
		c, err := Resolve(context.Background(), db, model, protocol.OpenAI)
		if err != nil {
			t.Fatalf("Resolve %s: %v", model, err)
		}
		if c.PathOverride != "/custom/chat" {
			t.Errorf("%s 解析出的候选 PathOverride = %q", model, c.PathOverride)
		}
	}
	target, err := ChannelProbeTarget(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("ChannelProbeTarget: %v", err)
	}
	if target.PathOverrides.OpenAI != "/custom/chat" {
		t.Errorf("检测目标 PathOverrides = %+v", target.PathOverrides)
	}
}

// 意图写（#175）：整组覆盖；形状闸与「覆盖的协议必须有地址」都当场拒；清空即删。
func TestUpdateChannelPathOverrides(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")

	read := func() PathOverrides {
		t.Helper()
		var o PathOverrides
		if err := db.QueryRow(`SELECT path_override_openai, path_override_openai_responses, path_override_anthropic
			FROM channels WHERE id = 1`).Scan(&o.OpenAI, &o.OpenAIResponses, &o.Anthropic); err != nil {
			t.Fatalf("读 path_override: %v", err)
		}
		return o
	}

	if err := UpdateChannelPathOverrides(ctx, db, 1, PathOverrides{OpenAI: " /custom/chat "}); err != nil {
		t.Fatalf("写覆盖: %v", err)
	}
	if got := read(); got.OpenAI != "/custom/chat" {
		t.Fatalf("path_override.OpenAI = %q，期望 trim 后落库", got.OpenAI)
	}

	if err := UpdateChannelPathOverrides(ctx, db, 1, PathOverrides{OpenAI: "custom"}); err == nil {
		t.Error("缺首斜杠该被拒")
	}
	// openai_responses 在 seedChannel 里没有地址，覆盖它该被拒。
	if err := UpdateChannelPathOverrides(ctx, db, 1, PathOverrides{OpenAIResponses: "/responses"}); err == nil {
		t.Error("给没有地址的协议声明覆盖该被拒")
	}
	if got := read(); got.OpenAI != "/custom/chat" {
		t.Errorf("被拒的写入改动了库：%+v", got)
	}

	if err := UpdateChannelPathOverrides(ctx, db, 1, PathOverrides{}); err != nil {
		t.Fatalf("清空: %v", err)
	}
	if got := read(); got != (PathOverrides{}) {
		t.Errorf("清空后 = %+v", got)
	}
}
