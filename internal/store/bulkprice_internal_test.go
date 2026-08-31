package store

import (
	"context"
	"errors"
	"testing"
)

func fp(v float64) *float64 { return &v }

// 批量填价（口径层 v1.10，#81）：有建议的填、已定价的默认跳过、没建议的跳过并计数。
func TestBulkPriceChannelModels(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	// 第二条纳管模型没有建议价，钉住「无建议价跳过」那一档。
	if _, err := db.Exec(
		`INSERT INTO channel_models (id, channel_id, upstream_model) VALUES (2, 1, 'no-suggest')`); err != nil {
		t.Fatalf("插第二条模型: %v", err)
	}
	fill := map[string]ChannelModelPrices{
		"gpt-4o": {Input: fp(1.5), Output: fp(7.5), CacheRead: fp(0)},
	}

	got, err := BulkPriceChannelModels(context.Background(), db, 1, false, fill)
	if err != nil {
		t.Fatalf("批量填价: %v", err)
	}
	if got.Filled != 1 || got.SkippedPriced != 0 || got.SkippedNoSuggestion != 1 {
		t.Errorf("首轮 = %+v，期望 已填 1 / 已定价跳过 0 / 无建议跳过 1", got)
	}
	// 落成的是普通四价：建议缺 cache_write 就落 NULL，0 原样是真免费。
	m := mustChannelModel(t, db, 1)
	if m.Input == nil || *m.Input != 1.5 || m.CacheRead == nil || *m.CacheRead != 0 {
		t.Errorf("四价 = %+v，期望 input 1.5、cache_read 0", m)
	}
	if m.CacheWrite != nil {
		t.Errorf("建议里没有的价该落 NULL，实得 %v", *m.CacheWrite)
	}

	// 再跑一遍：已定价条目默认跳过，价不动。
	fill["gpt-4o"] = ChannelModelPrices{Input: fp(9)}
	got, err = BulkPriceChannelModels(context.Background(), db, 1, false, fill)
	if err != nil {
		t.Fatalf("二轮: %v", err)
	}
	if got.Filled != 0 || got.SkippedPriced != 1 {
		t.Errorf("二轮 = %+v，期望 已填 0 / 已定价跳过 1", got)
	}
	if m := mustChannelModel(t, db, 1); *m.Input != 1.5 {
		t.Errorf("默认跳过却改了价：%v", *m.Input)
	}

	// 勾了覆盖才动，整组覆盖——旧的 cache_read 0 被新建议的 NULL 顶掉。
	got, err = BulkPriceChannelModels(context.Background(), db, 1, true, fill)
	if err != nil {
		t.Fatalf("覆盖轮: %v", err)
	}
	if got.Filled != 1 {
		t.Errorf("覆盖轮 = %+v，期望 已填 1", got)
	}
	m = mustChannelModel(t, db, 1)
	if m.Input == nil || *m.Input != 9 || m.CacheRead != nil {
		t.Errorf("覆盖后 = %+v，期望 input 9、cache_read NULL", m)
	}
}

func mustChannelModel(t *testing.T, db Queryer, id int64) ChannelModelPrices {
	t.Helper()
	var p ChannelModelPrices
	if err := db.QueryRowContext(context.Background(), `
		SELECT price_input, price_output, price_cache_read, price_cache_write
		FROM channel_models WHERE id = ?`, id).
		Scan(&p.Input, &p.Output, &p.CacheRead, &p.CacheWrite); err != nil {
		t.Fatalf("读条目 %d: %v", id, err)
	}
	return p
}

// ChannelProvider：拿标注给建议价定位，渠道不存在要 ErrNotFound 而不是空串——
// 空串是「未标注」的正常答案，两者不能混。
func TestChannelProvider(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	p, err := ChannelProvider(context.Background(), db, 1)
	if err != nil || p != "" {
		t.Fatalf("未标注渠道 = (%q, %v)，期望空串无错", p, err)
	}
	if _, err := db.Exec(`UPDATE channels SET provider = 'openai' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if p, _ = ChannelProvider(context.Background(), db, 1); p != "openai" {
		t.Errorf("provider = %q，期望 openai", p)
	}
	if _, err := ChannelProvider(context.Background(), db, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("查无此渠道 err = %v，期望 ErrNotFound", err)
	}
}

// /my/models 的单价映射（口径层 v1.10）：接入点按唯一候选取价，限定名直连取自身，
// 同名撞车接入点优先——与 ListExposedModels 的先到先得同一条秩序。
func TestListExposedModelPrices(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	if _, err := db.Exec(`UPDATE channel_models SET price_input = 3, price_output = 15 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	prices, err := ListExposedModelPrices(context.Background(), db)
	if err != nil {
		t.Fatalf("ListExposedModelPrices: %v", err)
	}
	for _, name := range []string{"ap", "ch/gpt-4o"} {
		p, ok := prices[name]
		if !ok || p.Input == nil || *p.Input != 3 || p.CacheRead != nil {
			t.Errorf("%s = (%+v, %v)，期望 input 3、cache_read NULL", name, p, ok)
		}
	}

	// 停用的接入点不进映射——它也不会出现在 /my/models 清单里，映射多留一条只会
	// 掩盖 join 条件写错。
	if _, err := db.Exec(`UPDATE access_points SET disabled = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	prices, err = ListExposedModelPrices(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prices["ap"]; ok {
		t.Error("停用接入点不该出现在单价映射里")
	}
}
