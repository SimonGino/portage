package admin

import (
	"testing"

	"github.com/SimonGino/portage/internal/pricing"
)

func fp(v float64) *float64 { return &v }

// 批量填价带分档（口径层 v1.49）：系数同乘基础与分档四价、阈值原样，缺价 nil 穿过。
func TestScaleSuggestionCarriesTier(t *testing.T) {
	got := scaleSuggestion(pricing.ModelPrice{
		Input: fp(2), Output: fp(10),
		Tier: &pricing.PriceTier{Above: 200000, Input: fp(4), CacheRead: fp(0.075)},
	}, 0.8)
	if *got.Input != 1.6 || *got.Output != 8 || got.CacheRead != nil {
		t.Errorf("基础价 = %+v", got)
	}
	if got.TierAbove == nil || *got.TierAbove != 200000 {
		t.Fatalf("阈值该原样，实得 %v", got.TierAbove)
	}
	if *got.TierInput != 3.2 || *got.TierCacheRead != 0.06 || got.TierOutput != nil || got.TierCacheWrite != nil {
		t.Errorf("分档价 = in %v out %v cr %v cw %v", got.TierInput, got.TierOutput, got.TierCacheRead, got.TierCacheWrite)
	}
	if none := scaleSuggestion(pricing.ModelPrice{Input: fp(1)}, 1); none.TierAbove != nil {
		t.Errorf("无分档建议不该落阈值")
	}
}

// 批量填价带 1h 缓存写（#198）：读取点派生的建议（Claude 1h = 2× input）同乘系数，
// 快照缺那一格（非 claude 模型）nil 穿过。
func TestScaleSuggestionCarries1hWrite(t *testing.T) {
	got := scaleSuggestion(pricing.ModelPrice{
		Input: fp(3), CacheWrite1H: fp(6),
		Tier: &pricing.PriceTier{Above: 200000, Input: fp(6), CacheWrite1H: fp(12)},
	}, 2)
	if got.CacheWrite1H == nil || *got.CacheWrite1H != 12 || got.TierCacheWrite1H == nil || *got.TierCacheWrite1H != 24 {
		t.Errorf("1h 建议价 = %v / %v，期望 12 / 24（同乘系数 2）", got.CacheWrite1H, got.TierCacheWrite1H)
	}
	if none := scaleSuggestion(pricing.ModelPrice{Input: fp(1)}, 1); none.CacheWrite1H != nil || none.TierCacheWrite1H != nil {
		t.Errorf("快照缺 1h 建议时该 nil 穿过，实得 %v / %v", none.CacheWrite1H, none.TierCacheWrite1H)
	}
}
