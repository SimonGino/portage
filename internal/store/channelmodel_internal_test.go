package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 纳管模型只有一扇写门（#141）：管理端一次保存与声明文件一次 apply 都从这里过，
// 所以负价 / 负上限 / 协议归一 / nil 不动这几条不变量只在这一张表里钉一次。
func TestUpdateChannelModel(t *testing.T) {
	ctx := context.Background()
	ptr := func(v float64) *float64 { return &v }
	set := func(ps ...string) *protocol.Set {
		s := make(protocol.Set, 0, len(ps))
		for _, p := range ps {
			s = append(s, protocol.Protocol(p))
		}
		return &s
	}
	intp := func(v int) *int { return &v }
	boolp := func(v bool) *bool { return &v }

	type row struct {
		protocols string
		limit     int
		priceIn   sql.NullFloat64
		disabled  int
	}
	read := func(t *testing.T, db *sql.DB) row {
		t.Helper()
		var r row
		if err := db.QueryRow(`SELECT protocols, max_input_tokens, price_input, disabled
			FROM channel_models WHERE id = 1`).Scan(&r.protocols, &r.limit, &r.priceIn, &r.disabled); err != nil {
			t.Fatalf("回读: %v", err)
		}
		return r
	}

	cases := []struct {
		name    string
		patch   ChannelModelPatch
		wantErr bool
		check   func(t *testing.T, r row)
	}{
		{"负价拒", ChannelModelPatch{Prices: &ChannelModelPrices{Input: ptr(-0.1)}}, true, nil},
		{"负上限拒", ChannelModelPatch{MaxInputTokens: intp(-1)}, true, nil},
		{"认不得的协议拒", ChannelModelPatch{Protocols: set("gemini")}, true, nil},
		{"空 patch 拒", ChannelModelPatch{}, true, nil},
		// 空集合是显式改回「继承渠道全集」，不是错误；折旧名归一、去重。
		{"协议归一", ChannelModelPatch{Protocols: set("openai_cc", "openai")}, false, func(t *testing.T, r row) {
			if r.protocols != "openai" {
				t.Errorf("protocols = %q，期望折旧名后去重成 openai", r.protocols)
			}
		}},
		{"协议清空成继承", ChannelModelPatch{Protocols: set()}, false, func(t *testing.T, r row) {
			if r.protocols != "" {
				t.Errorf("protocols = %q，期望空串", r.protocols)
			}
		}},
		// 整组覆盖：一次写俩、再一次全清回 NULL，都得是一笔到位；且只碰价，别的列不动。
		{"填价不动其余列", ChannelModelPatch{Prices: &ChannelModelPrices{Input: ptr(3), Output: ptr(15)}}, false, func(t *testing.T, r row) {
			if !r.priceIn.Valid || r.priceIn.Float64 != 3 || r.limit != 200000 || r.disabled != 1 {
				t.Errorf("填价后 %+v，期望 price_input=3、上限与停用位不动", r)
			}
		}},
		{"清回未定价", ChannelModelPatch{Prices: &ChannelModelPrices{}}, false, func(t *testing.T, r row) {
			if r.priceIn.Valid {
				t.Errorf("清空后 price_input = %v，期望 NULL", r.priceIn.Float64)
			}
		}},
		{"上限清成不限", ChannelModelPatch{MaxInputTokens: intp(0)}, false, func(t *testing.T, r row) {
			if r.limit != 0 {
				t.Errorf("max_input_tokens = %d，期望 0", r.limit)
			}
		}},
		{"启用", ChannelModelPatch{Disabled: boolp(false)}, false, func(t *testing.T, r row) {
			if r.disabled != 0 {
				t.Errorf("disabled = %d，期望 0", r.disabled)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			seedChannel(t, db, "anthropic,openai", "")
			if _, err := db.Exec(`UPDATE channel_models SET max_input_tokens = 200000, disabled = 1 WHERE id = 1`); err != nil {
				t.Fatalf("预置: %v", err)
			}
			err := UpdateChannelModel(ctx, db, 1, tc.patch)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("err = %v, 期望 ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("写入失败: %v", err)
			}
			tc.check(t, read(t, db))
		})
	}

	// 不存在的行是 404 不是静默成功。
	db := openTestDB(t)
	if err := UpdateChannelModel(ctx, db, 99, ChannelModelPatch{Disabled: boolp(true)}); !errors.Is(err, ErrNotFound) {
		t.Errorf("改不存在的行 err = %v，期望 ErrNotFound", err)
	}
}

// TestAddChannelModelZeroPriceOnSubscription：订阅渠道的纳管条目建成时四价自动落 0
// （#215，口径层 §2.2 v1.52「真免费」态）：0 与 NULL 在价列上是两种话——NULL 是
// 「还没记账依据」，0 是「记过了，账是 0」。普通渠道默认仍是 NULL；渠道不存在时
// 照旧让 INSERT 自己撞外键，不在半道改判另一种错。
func TestAddChannelModelZeroPriceOnSubscription(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		credTyp string
		want    sql.NullFloat64
	}{
		{"chatgpt_account", "chatgpt_account", sql.NullFloat64{Float64: 0, Valid: true}},
		{"copilot_account 同为订阅", "copilot_account", sql.NullFloat64{Float64: 0, Valid: true}},
		{"普通渠道仍 NULL", "api_key", sql.NullFloat64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			if _, err := db.Exec(
				`INSERT INTO channels (id, name, credential_type) VALUES (1, 'ch', ?)`, tc.credTyp); err != nil {
				t.Fatalf("插渠道: %v", err)
			}
			if err := AddChannelModel(ctx, db, 1, "gpt-6.1-sol", nil); err != nil {
				t.Fatalf("加纳管模型: %v", err)
			}
			var in, out, cr, cw sql.NullFloat64
			if err := db.QueryRow(`SELECT price_input, price_output, price_cache_read, price_cache_write
				FROM channel_models WHERE channel_id = 1`).Scan(&in, &out, &cr, &cw); err != nil {
				t.Fatalf("回读: %v", err)
			}
			for _, got := range []struct {
				name string
				col  sql.NullFloat64
			}{{"price_input", in}, {"price_output", out}, {"price_cache_read", cr}, {"price_cache_write", cw}} {
				if got.col != tc.want {
					t.Errorf("%s = %+v, 期望 %+v", got.name, got.col, tc.want)
				}
			}
			// 幂等重添加：价格不被二次改写。
			if err := AddChannelModel(ctx, db, 1, "gpt-6.1-sol", nil); err != nil {
				t.Fatalf("重复添加: %v", err)
			}
			if err := db.QueryRow(`SELECT price_input FROM channel_models WHERE channel_id = 1`).Scan(&in); err != nil {
				t.Fatal(err)
			}
			if in != tc.want {
				t.Errorf("幂等重添加后 price_input = %+v, 期望不变", in)
			}
		})
	}

	// 渠道不存在：外键照旧拦（与 AddChannelCredentials 同一条纪律），不因多了一次
	// 读渠道类型的查询改判另一种错。
	db := openTestDB(t)
	if err := AddChannelModel(ctx, db, 999, "gpt-x", nil); err == nil {
		t.Error("渠道不存在该报错，得到 nil")
	}
}
