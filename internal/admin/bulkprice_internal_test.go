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
