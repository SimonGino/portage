package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 两条解析路径都要带上四价——接入点路径和限定名直连路径各走各的 SQL，漏掉一处
// 的话直连的调用就全记不上成本（同 max_input_tokens 那列的既有立论）。
func TestResolveCarriesPricesOnBothPaths(t *testing.T) {
	for _, tc := range []struct{ name, model string }{
		{"接入点", "ap"},
		{"限定名直连", "ch/gpt-4o"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			seedChannel(t, db, "openai", "")
			// 刻意只填两价：nil 要原样穿过 Resolve 到 Recorder，被 SQL 默认值或
			// 扫描环节补成 0 的话，「未定价」就静默变成了「真免费」。
			if _, err := db.Exec(
				`UPDATE channel_models SET price_input = 3, price_cache_read = 0`); err != nil {
				t.Fatalf("设价: %v", err)
			}

			cand, err := Resolve(context.Background(), db, tc.model, protocol.OpenAI)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			p := cand.Prices
			if p.Input == nil || *p.Input != 3 {
				t.Errorf("Prices.Input = %v, 期望 3", p.Input)
			}
			if p.CacheRead == nil || *p.CacheRead != 0 {
				t.Errorf("Prices.CacheRead = %v, 期望显式 0（真免费）", p.CacheRead)
			}
			if p.Output != nil || p.CacheWrite != nil {
				t.Errorf("没定价的两项该是 nil，实得 Output=%v CacheWrite=%v", p.Output, p.CacheWrite)
			}
		})
	}
}

// 负数拒而不是收下：0 已经是「真免费」，清回未定价走 nil，负价只能是填错。
func TestSetChannelModelPricesRejectsNegative(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")

	bad := -0.1
	err := SetChannelModelPrices(context.Background(), db, 1, ChannelModelPrices{Input: &bad})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, 期望 ErrInvalidInput", err)
	}

	// 整组覆盖：一次写俩、再一次全清回 NULL，都得是一笔到位。
	in, out := 3.0, 15.0
	if err := SetChannelModelPrices(context.Background(), db, 1,
		ChannelModelPrices{Input: &in, Output: &out}); err != nil {
		t.Fatalf("填价失败: %v", err)
	}
	if err := SetChannelModelPrices(context.Background(), db, 1, ChannelModelPrices{}); err != nil {
		t.Fatalf("清回未定价失败: %v", err)
	}
	var got sql.NullFloat64
	if err := db.QueryRow(`SELECT price_input FROM channel_models WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("读价: %v", err)
	}
	if got.Valid {
		t.Errorf("清空后 price_input = %v，期望 NULL", got.Float64)
	}
}

// ListChannels 要把四价与「有没有用量」一起端给管理端——未定价提醒的判据是
// 「四价全 NULL 且有用量」（口径层 §2.10），两个输入缺一个前端就判不了。
func TestListChannelsCarriesPricesAndHasUsage(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	in := 3.0
	if err := SetChannelModelPrices(context.Background(), db, 1,
		ChannelModelPrices{Input: &in}); err != nil {
		t.Fatalf("填价: %v", err)
	}
	// 一条报了 usage 的流水（input_tokens 非 NULL）；另插一条**没报 usage** 的
	// 同条目流水，钉住判据看的是「有用量」而不是「有流水」。
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name, client_protocol,
		upstream_protocol, model_requested, model_upstream, channel_name, status, total_ms, input_tokens)
		VALUES ('k', 'openai', 'openai', 'ap', 'gpt-4o', 'ch', 200, 5, 120),
		       ('k', 'openai', 'openai', 'ap', 'gpt-4o', 'ch', 500, 5, NULL)`); err != nil {
		t.Fatalf("插流水: %v", err)
	}

	chs, err := ListChannels(context.Background(), db)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	m := chs[0].Models[0]
	if m.PriceInput == nil || *m.PriceInput != 3 {
		t.Errorf("PriceInput = %v, 期望 3", m.PriceInput)
	}
	if m.PriceOutput != nil {
		t.Errorf("PriceOutput = %v, 期望 nil（未定价）", m.PriceOutput)
	}
	if !m.HasUsage {
		t.Error("有报 usage 的流水，HasUsage 该是 true")
	}
}

// 用量记在别的条目名下不算这条的用量：判据键是渠道名 × 上游模型名。
func TestHasUsageIsPerEntryNotPerChannel(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name, client_protocol,
		upstream_protocol, model_requested, model_upstream, channel_name, status, total_ms, input_tokens)
		VALUES ('k', 'openai', 'openai', 'ap', '别的模型', 'ch', 200, 5, 120)`); err != nil {
		t.Fatalf("插流水: %v", err)
	}
	chs, err := ListChannels(context.Background(), db)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if chs[0].Models[0].HasUsage {
		t.Error("用量记在别的上游模型名下，这条的 HasUsage 该是 false")
	}
}

// 老库迁移：六列补齐且幂等；存量流水的 cost 落在 NULL 上——那些调用打上游时网关还
// 不认识价，「无量可计」不能记成确凿的 0（同 reasoning_tokens 那条立论）。
func TestMigrateAddsPricingColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开老库: %v", err)
	}
	// #74 之前的形状：三张表都没有计价批的列。
	for _, ddl := range []string{
		`CREATE TABLE channels (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
		 base_url_openai TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE channel_models (id INTEGER PRIMARY KEY AUTOINCREMENT,
		 channel_id INTEGER NOT NULL, upstream_model TEXT NOT NULL)`,
		`CREATE TABLE call_logs (id INTEGER PRIMARY KEY AUTOINCREMENT,
		 api_key_name TEXT NOT NULL, channel_name TEXT NOT NULL, status INTEGER NOT NULL)`,
	} {
		if _, err := old.Exec(ddl); err != nil {
			t.Fatalf("建老表: %v", err)
		}
	}
	if _, err := old.Exec(`INSERT INTO channels (name) VALUES ('legacy');
		INSERT INTO channel_models (channel_id, upstream_model) VALUES (1, 'legacy-m');
		INSERT INTO call_logs (api_key_name, channel_name, status) VALUES ('k', 'legacy', 200)`); err != nil {
		t.Fatalf("插存量行: %v", err)
	}

	if err := addPricingColumns(old); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 幂等：网关每次启动都会跑一遍。
	if err := addPricingColumns(old); err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}

	var price, cost sql.NullFloat64
	var provider string
	if err := old.QueryRow(`SELECT price_input FROM channel_models WHERE id = 1`).Scan(&price); err != nil {
		t.Fatalf("读存量条目: %v", err)
	}
	if err := old.QueryRow(`SELECT provider FROM channels WHERE id = 1`).Scan(&provider); err != nil {
		t.Fatalf("读存量渠道: %v", err)
	}
	if err := old.QueryRow(`SELECT cost FROM call_logs WHERE id = 1`).Scan(&cost); err != nil {
		t.Fatalf("读存量流水: %v", err)
	}
	if price.Valid {
		t.Errorf("存量条目 price_input = %v，期望 NULL（未定价）", price.Float64)
	}
	if provider != "" {
		t.Errorf("存量渠道 provider = %q，期望空串（未标注）", provider)
	}
	if cost.Valid {
		t.Errorf("存量流水 cost = %v，期望 NULL（无量可计）", cost.Float64)
	}
	old.Close()
}
