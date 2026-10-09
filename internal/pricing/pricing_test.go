package pricing

import (
	"slices"
	"testing"
)

// 快照是发版资产：解不开、缺了 anthropic 这种一方大厂、或单位明显不对（README
// 说 USD/百万 token，Claude 一档的 input 不可能是 3e-6 或 3e6），都说明生成或
// 提交环节出了错——在测试里逮住，别等管理端页面上一片空建议才发现。
func TestSnapshotLoads(t *testing.T) {
	providers, err := Providers()
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if len(providers) < 100 {
		t.Fatalf("快照里只有 %d 个 provider，明显裁坏了", len(providers))
	}
	for _, p := range providers {
		if p.ID == "" || p.Name == "" {
			t.Fatalf("provider 缺 id 或 name：%+v", p)
		}
	}
}

func TestAnthropicPricesLookSane(t *testing.T) {
	prices, err := ModelPrices("anthropic")
	if err != nil {
		t.Fatalf("ModelPrices: %v", err)
	}
	if len(prices) == 0 {
		t.Fatal("anthropic 名下没有任何有价模型")
	}
	for id, p := range prices {
		if p.Input == nil || p.Output == nil {
			continue
		}
		// 单位是 USD/百万 token：0 是合法的（真免费档），非零则不可能小于
		// 0.001 或大于 10000——越界基本可断定是把单位换算错了。
		for _, v := range []float64{*p.Input, *p.Output} {
			if v != 0 && (v < 0.001 || v > 10000) {
				t.Fatalf("模型 %s 的价 %v 不像 USD/百万 token", id, v)
			}
		}
	}
}

func TestUnknownProviderIsJustEmpty(t *testing.T) {
	prices, err := ModelPrices("no-such-provider")
	if err != nil {
		t.Fatalf("ModelPrices: %v", err)
	}
	if len(prices) != 0 {
		t.Fatalf("查无此家该回空映射，回了 %d 条", len(prices))
	}
}

// v1.75 放宽：快照得带出分档价与模型元数据，否则建议价与模型目录胶囊无源可读。
func TestSnapshotCarriesTierAndLimits(t *testing.T) {
	var tiered, limited, image bool
	for _, pid := range []string{"anthropic", "google", "deepinfra", "openai"} {
		prices, err := ModelPrices(pid)
		if err != nil {
			t.Fatalf("ModelPrices: %v", err)
		}
		for id, p := range prices {
			if p.Tier != nil {
				tiered = true
				if p.Tier.Above <= 0 {
					t.Fatalf("模型 %s 分档阈值 %d 不合法", id, p.Tier.Above)
				}
			}
			limited = limited || p.LimitContext > 0
			image = image || slices.Contains(p.InputModalities, "image")
		}
	}
	if !tiered || !limited || !image {
		t.Fatalf("快照缺放宽字段：分档 %v、limit.context %v、image 模态 %v", tiered, limited, image)
	}
}

// 1h 缓存写建议价（#198）：claude 系模型按「Claude 1h = 2× input」派生（厂商定价
// 事实，docs.anthropic.com prompt-caching#pricing），非 claude 模型恒 nil——它们没有
// 1h TTL 这个档。派生只发生在读取点，快照字节与 gen 的形状都不动。
func TestSuggested1hWritePriceDerivedForClaudeModels(t *testing.T) {
	prices, err := ModelPrices("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for id, p := range prices {
		if p.Input == nil {
			continue
		}
		if p.CacheWrite1H == nil || *p.CacheWrite1H != *p.Input*2 {
			t.Errorf("模型 %s 的 1h 缓存写建议 = %v，期望 2× input = %v", id, p.CacheWrite1H, *p.Input*2)
		}
	}
	// 分档有价时同规则派生：2× 分档 input。
	for id, p := range prices {
		if tier := p.Tier; tier != nil && tier.Input != nil {
			if tier.CacheWrite1H == nil || *tier.CacheWrite1H != *tier.Input*2 {
				t.Errorf("模型 %s 的分档 1h 建议 = %v，期望 %v", id, tier.CacheWrite1H, *tier.Input*2)
			}
		}
	}
	// 非 claude 模型不派生：没有 1h 档的世面价，给建议就是编数。
	if prices, err := ModelPrices("openai"); err != nil {
		t.Fatal(err)
	} else {
		for id, p := range prices {
			if p.CacheWrite1H != nil {
				t.Errorf("模型 %s 不是 claude 系，1h 建议 = %v，期望 nil", id, *p.CacheWrite1H)
			}
		}
	}
}
