// Package pricing 内嵌 models.dev 的裁剪快照，给管理端两样东西：渠道 provider
// 标注的取值域，与填价时的建议价（#68/#74，口径层 §2.10 计价）。
//
// **只做建议，不进任何闸**：provider 标注不参与路由，建议价不自动落库——上游快照
// 是发版那一刻的世面价，网关真正记账用的永远是人在纳管条目上确认过的四价。快照
// 过期、缺价（上游 434/7483 条无 cost）都只是「没有建议」，不是错误。
//
// 数据 MIT 许可（sst/models.dev），版权声明与许可证文本见同目录 LICENSE-models.dev。
// 随发版更新走 `make update-models-snapshot`，见 gen/main.go。
package pricing

import (
	"bytes"
	"cmp"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

//go:generate go run ./gen

//go:embed snapshot.json.gz
var snapshotGz []byte

// Provider 是 models.dev 的一个 provider：kebab-case id（`anthropic`、
// `amazon-bedrock`…）加给人看的名字。
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ModelPrice 是快照里一个模型的四价，单位与渠道口径一致：USD / 百万 token
// （models.dev README 明文，无需换算）。指针的 nil = 快照里没有这个价——
// 与纳管条目那边一样，「没有」与 0（真免费）必须分得开。
type ModelPrice struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

type provider struct {
	Name   string                `json:"name"`
	Models map[string]ModelPrice `json:"models"`
}

// load 启动后第一次被问到才解压解析，且只做一次。快照坏了（只可能是生成或提交
// 环节出错）不 panic：管理端少一份建议不该影响转发，错误延迟到读点报出去。
var load = sync.OnceValues(func() (map[string]provider, error) {
	zr, err := gzip.NewReader(bytes.NewReader(snapshotGz))
	if err != nil {
		return nil, fmt.Errorf("解压 models.dev 快照：%w", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("解压 models.dev 快照：%w", err)
	}
	var m map[string]provider
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 models.dev 快照：%w", err)
	}
	return m, nil
})

// Providers 返回全部 provider，按名字排序（同名极少，退回按 id）——这份列表直接
// 灌管理端的下拉，排序在这儿做一次比每个消费端各排各的稳。
func Providers() ([]Provider, error) {
	m, err := load()
	if err != nil {
		return nil, err
	}
	out := make([]Provider, 0, len(m))
	for id, p := range m {
		out = append(out, Provider{ID: id, Name: p.Name})
	}
	slices.SortFunc(out, func(a, b Provider) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}

// ModelPrices 返回一个 provider 名下全部有价模型：模型 id → 四价。
// provider 不认识时返回空映射不报错——标注是自由文本，快照又随发版才更新，
// 「查无此家」在这里就是「没有建议」。
func ModelPrices(providerID string) (map[string]ModelPrice, error) {
	m, err := load()
	if err != nil {
		return nil, err
	}
	return m[providerID].Models, nil
}
