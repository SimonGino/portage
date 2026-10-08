package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

func ip(v int64) *int64 { return &v }

// 分档价（口径层 v1.49，#185）：阈值 + 分档四价随四价一起整组写入，并沿两条
// 读点（Resolve 计价、ListChannels 管理端）原样带出；分档某价 nil 原样穿过——
// 那是「沿用基础价」，补成 0 就成了「分档免费」。
func TestTierPricesRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	if err := UpdateChannelModel(ctx, db, 1, ChannelModelPatch{Prices: &ChannelModelPrices{
		Input: fp(3), Output: fp(15),
		TierAbove: ip(200000), TierInput: fp(6), TierCacheRead: fp(0),
	}}); err != nil {
		t.Fatalf("填分档价: %v", err)
	}

	for _, model := range []string{"ap", "ch/gpt-4o"} {
		cand, err := Resolve(ctx, db, model, protocol.OpenAI)
		if err != nil {
			t.Fatalf("解析 %s: %v", model, err)
		}
		p := cand.Prices
		if p.TierAbove == nil || *p.TierAbove != 200000 || p.TierInput == nil || *p.TierInput != 6 ||
			p.TierCacheRead == nil || *p.TierCacheRead != 0 || p.TierOutput != nil || p.TierCacheWrite != nil {
			t.Errorf("%s 的分档价没带对：%+v", model, p)
		}
	}

	chs, err := ListChannels(ctx, db)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	m := chs[0].Models[0]
	if m.PriceTierAbove == nil || *m.PriceTierAbove != 200000 || m.PriceTierInput == nil ||
		*m.PriceTierInput != 6 || m.PriceTierOutput != nil {
		t.Errorf("ListChannels 分档价 = above %v input %v output %v", m.PriceTierAbove, m.PriceTierInput, m.PriceTierOutput)
	}
}

// 清空阈值 = 去分档：分档四价跟着清回 NULL，不留一组没有阈值的孤儿价。
func TestClearingTierThresholdDropsTierPrices(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	if err := UpdateChannelModel(ctx, db, 1, ChannelModelPatch{Prices: &ChannelModelPrices{
		Input: fp(3), TierAbove: ip(1000), TierInput: fp(6),
	}}); err != nil {
		t.Fatalf("填分档价: %v", err)
	}
	if err := UpdateChannelModel(ctx, db, 1, ChannelModelPatch{Prices: &ChannelModelPrices{
		Input: fp(3), TierInput: fp(6),
	}}); err != nil {
		t.Fatalf("去分档: %v", err)
	}
	var above sql.NullInt64
	var tin sql.NullFloat64
	if err := db.QueryRow(`SELECT price_tier_above, price_tier_input FROM channel_models WHERE id = 1`).
		Scan(&above, &tin); err != nil {
		t.Fatalf("读条目: %v", err)
	}
	if above.Valid || tin.Valid {
		t.Errorf("清空阈值后 above=%v tier_input=%v，期望都 NULL", above, tin)
	}
}

// 阈值要正整数（0 = 「超过 0 token」即恒按分档价，只能是填错）；分档价同基础价拒负数。
func TestTierPricesValidation(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	for name, p := range map[string]ChannelModelPrices{
		"阈值 0":  {TierAbove: ip(0)},
		"阈值负数":  {TierAbove: ip(-1)},
		"分档价负数": {TierAbove: ip(10), TierOutput: fp(-1)},
	} {
		err := UpdateChannelModel(context.Background(), db, 1, ChannelModelPatch{Prices: &p})
		var bad InvalidInput
		if !errors.As(err, &bad) {
			t.Errorf("%s：err = %v，期望 InvalidInput", name, err)
		}
	}
}

// 批量填价带分档：fill 里的阈值与分档四价一起落。
func TestBulkPriceCarriesTier(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	fill := map[string]ChannelModelPrices{
		"gpt-4o": {Input: fp(1), TierAbove: ip(128000), TierInput: fp(2), TierOutput: fp(8)},
	}
	if _, err := BulkPriceChannelModels(ctx, db, 1, false, fill); err != nil {
		t.Fatalf("批量填价: %v", err)
	}
	var above int64
	var tout float64
	if err := db.QueryRow(`SELECT price_tier_above, price_tier_output FROM channel_models WHERE id = 1`).
		Scan(&above, &tout); err != nil {
		t.Fatalf("读条目: %v", err)
	}
	if above != 128000 || tout != 8 {
		t.Errorf("分档落成 above=%d tier_output=%v，期望 128000 / 8", above, tout)
	}
}

// 老库迁移补五列，存量条目落 NULL（全程一档），幂等。
func TestMigrateAddsTierColumns(t *testing.T) {
	old, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("开老库: %v", err)
	}
	defer old.Close()
	for _, ddl := range []string{
		`CREATE TABLE channels (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`,
		`CREATE TABLE channel_models (id INTEGER PRIMARY KEY AUTOINCREMENT,
		 channel_id INTEGER NOT NULL, upstream_model TEXT NOT NULL)`,
		`CREATE TABLE call_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, status INTEGER NOT NULL)`,
		`INSERT INTO channel_models (channel_id, upstream_model) VALUES (1, 'legacy-m')`,
	} {
		if _, err := old.Exec(ddl); err != nil {
			t.Fatalf("建老表: %v", err)
		}
	}
	for range 2 {
		if err := addPricingColumns(old); err != nil {
			t.Fatalf("迁移: %v", err)
		}
	}
	var above sql.NullInt64
	var tcw sql.NullFloat64
	if err := old.QueryRow(`SELECT price_tier_above, price_tier_cache_write FROM channel_models WHERE id = 1`).
		Scan(&above, &tcw); err != nil {
		t.Fatalf("读存量条目: %v", err)
	}
	if above.Valid || tcw.Valid {
		t.Errorf("存量条目分档列 = %v / %v，期望 NULL", above, tcw)
	}
}
