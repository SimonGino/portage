// gen 从 https://models.dev/api.json 裁出 portage 要的那一小片，gzip 后写进
// internal/pricing/snapshot.json.gz（#68/#74，口径层 §2.10 计价）。
//
// 全量 api.json 约 5MB：绝大部分是 reasoning、release_date 这类本项目用不上的
// 字段。这里只留 provider 的 id/name 与每个模型的：四价；`cost.tiers` 中
// `tier.type = context` 的第一档（分档价，阈值 = `tier.size`，档内缺价 = 沿用
// 基础价）；`limit.context`、`limit.output`、`modalities.input`（模型目录胶囊）。
// v1.75 放宽（#185/#189）前只留四价。多留一个字段就是白背一份会过期的数据，
// 所以清单以外的一律不留。**没有任何一个价的模型直接丢掉**：它给不出建议价，
// 留着只占体积（上游 434/7483 条无 cost，调研 #68）。
//
// 随发版更新：`make update-models-snapshot`（即 `go generate ./internal/pricing`）。
// 输出必须确定：同一份 api.json 两次生成字节相等——map 序列化按键排序是
// encoding/json 的既有保证，gzip 头不带时间戳（ModTime 留零值）。
package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const sourceURL = "https://models.dev/api.json"

// upstreamProvider / upstreamModel 只声明要读的字段，未知字段一律忽略——
// 上游 schema 每小时都在演进（#68 调研 ⑧），对多出来的键宽容即可免疫。
type upstreamProvider struct {
	ID     string                   `json:"id"`
	Name   string                   `json:"name"`
	Models map[string]upstreamModel `json:"models"`
}

type fourPrices struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

func (p fourPrices) empty() bool {
	return p.Input == nil && p.Output == nil && p.CacheRead == nil && p.CacheWrite == nil
}

type upstreamModel struct {
	Cost *struct {
		fourPrices
		Tiers []struct {
			fourPrices
			Tier struct {
				Type string `json:"type"`
				Size int64  `json:"size"`
			} `json:"tier"`
		} `json:"tiers"`
	} `json:"cost"`
	Limit struct {
		Context int64 `json:"context"`
		Output  int64 `json:"output"`
	} `json:"limit"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
}

// snapshotProvider / snapshotModel 是落进快照的形状，与 internal/pricing 读侧
// 一一对应。指针 + omitempty：缺哪个价就不写哪个键，NULL 与 0（真免费）分得开。
type snapshotProvider struct {
	Name   string                   `json:"name"`
	Models map[string]snapshotModel `json:"models,omitempty"`
}

type snapshotModel struct {
	fourPrices
	Tier            *snapshotTier `json:"tier,omitempty"`
	LimitContext    int64         `json:"limit_context,omitempty"`
	LimitOutput     int64         `json:"limit_output,omitempty"`
	InputModalities []string      `json:"input_modalities,omitempty"`
}

// snapshotTier 是分档价：毛输入超过 Above 时整笔按这四价（缺价沿用基础价）。
type snapshotTier struct {
	Above int64 `json:"above"`
	fourPrices
}

func main() {
	log.SetFlags(0)
	raw, err := fetch()
	if err != nil {
		log.Fatalf("拉取 %s：%v", sourceURL, err)
	}
	var full map[string]upstreamProvider
	if err := json.Unmarshal(raw, &full); err != nil {
		log.Fatalf("解析 api.json：%v", err)
	}

	pruned := make(map[string]snapshotProvider, len(full))
	models := 0
	for id, p := range full {
		sp := snapshotProvider{Name: p.Name}
		for mid, m := range p.Models {
			if m.Cost == nil {
				continue
			}
			if m.Cost.empty() {
				continue
			}
			sm := snapshotModel{
				fourPrices:      m.Cost.fourPrices,
				LimitContext:    m.Limit.Context,
				LimitOutput:     m.Limit.Output,
				InputModalities: m.Modalities.Input,
			}
			// 只要第一档 context 档：本项目分档价只有一档（口径层 v1.49），
			// 上游 tiers 按 size 升序，第一档即最低阈值。
			for _, t := range m.Cost.Tiers {
				if t.Tier.Type != "context" {
					continue
				}
				if t.Tier.Size > 0 && !t.empty() {
					sm.Tier = &snapshotTier{Above: t.Tier.Size, fourPrices: t.fourPrices}
				}
				break
			}
			if sp.Models == nil {
				sp.Models = map[string]snapshotModel{}
			}
			sp.Models[mid] = sm
			models++
		}
		pruned[id] = sp
	}

	out, err := json.Marshal(pruned)
	if err != nil {
		log.Fatalf("序列化快照：%v", err)
	}
	f, err := os.Create("snapshot.json.gz")
	if err != nil {
		log.Fatalf("写快照：%v", err)
	}
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		log.Fatalf("开 gzip：%v", err)
	}
	if _, err := zw.Write(out); err != nil {
		log.Fatalf("写快照：%v", err)
	}
	if err := zw.Close(); err != nil {
		log.Fatalf("写快照：%v", err)
	}
	if err := f.Close(); err != nil {
		log.Fatalf("写快照：%v", err)
	}
	st, _ := os.Stat("snapshot.json.gz")
	fmt.Printf("models.dev 快照已更新：%d 个 provider、%d 个有价模型，gzip %d 字节\n",
		len(pruned), models, st.Size())
}

func fetch() ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(sourceURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游回 %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}
