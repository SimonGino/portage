package server_test

import (
	"testing"

	"github.com/SimonGino/portage/internal/store"
)

// v0.36 的迁移：线上取值 `openai_cc` 改名成 `openai`，Open 要把存量行一次性改写。
//
// 三张列都得改。只改 channels 的话渠道还能用，但用量页会把同一个协议劈成 openai 与
// openai_cc 两个名字分别聚合——而 call_logs 是历史数据，漏掉这一次就再也补不回来。
func TestOpenRenamesLegacyProtocolName(t *testing.T) {
	path := t.TempDir() + "/legacy.db"

	// 先让 Open 建出当前形状的库（call_logs 不手写，免得跟 schema.sql 漂移），再把
	// channels 换回 v0.96 之前的老形状——旧取值 openai_cc 只存在于那个形状里，当前
	// 表已经没有 protocols 列可塞。
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		DROP TABLE channels;
		CREATE TABLE channels (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  name TEXT NOT NULL UNIQUE,
		  protocols TEXT NOT NULL,
		  base_url TEXT NOT NULL,
		  credential_type TEXT NOT NULL DEFAULT 'api_key',
		  key_mode TEXT NOT NULL DEFAULT 'polling',
		  disabled INTEGER NOT NULL DEFAULT 0,
		  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO channels (name, protocols, base_url) VALUES
		  ('single', 'openai_cc', 'https://h'),
		  ('mixed',  'openai_cc,openai_responses', 'https://h2'),
		  ('other',  'anthropic', 'https://h3');
		INSERT INTO call_logs
		  (api_key_name, client_protocol, upstream_protocol, model_requested,
		   model_upstream, channel_name, status, total_ms)
		VALUES ('k', 'openai_cc', 'anthropic', 'm', 'mu', 'single', 200, 1),
		       ('k', 'anthropic', 'openai_cc', 'm', 'mu', 'single', 200, 1);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// 跑两遍：迁移没有「跑过没有」的标记，靠的是幂等——第二遍必须是零行命中而不是
	// 把 openai 再折腾一次。
	for round := 1; round <= 2; round++ {
		db, err = store.Open(path)
		if err != nil {
			t.Fatalf("第 %d 遍打开失败: %v", round, err)
		}

		// 改名之后紧跟 v0.96 的展开：openai_cc 折成 openai 的效果落在
		// base_url_openai 那一列上（改错名就落不上）。
		for _, c := range []struct {
			name string
			want store.BaseURLs
		}{
			{"single", store.BaseURLs{OpenAI: "https://h"}},
			// 集合里夹在中间的那一个也要改，且不能误伤 openai_responses。
			{"mixed", store.BaseURLs{OpenAI: "https://h2", OpenAIResponses: "https://h2"}},
			{"other", store.BaseURLs{Anthropic: "https://h3"}},
		} {
			var got store.BaseURLs
			if err := db.QueryRow(
				`SELECT base_url_openai, base_url_openai_responses, base_url_anthropic
				 FROM channels WHERE name = ?`, c.name).
				Scan(&got.OpenAI, &got.OpenAIResponses, &got.Anthropic); err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("第 %d 遍 channels[%s] 迁移后 = %+v，期望 %+v", round, c.name, got, c.want)
			}
		}

		for _, col := range []string{"client_protocol", "upstream_protocol"} {
			var n int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM call_logs WHERE ` + col + ` = 'openai_cc'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("第 %d 遍 call_logs.%s 还剩 %d 行旧名", round, col, n)
			}
		}
		var openai int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM call_logs WHERE client_protocol = 'openai'
			   OR upstream_protocol = 'openai'`).Scan(&openai); err != nil {
			t.Fatal(err)
		}
		if openai != 2 {
			t.Errorf("第 %d 遍改写成 openai 的流水 = %d 行，期望 2", round, openai)
		}
		db.Close()
	}
}

// 空库上跑迁移不能报错：全新安装走的就是这条路。
func TestOpenOnFreshDBIsClean(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/fresh.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM channels`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("新库里居然有 %d 个渠道", n)
	}
}
