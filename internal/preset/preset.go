// Package preset 是渠道预设清单（口径层 v1.47 §2.2，展开层 §7.12，#181）：建渠道
// 用的模板，选中即预填新建表单、全部可改，建成即与预设脱钩——预设不是实体、不进库、
// 不进路由。
//
// 清单是手写 JSON（presets.json），go:embed 进二进制、不启动拉取。地址一律对厂商
// 文档逐条核过，存的是协议子路径之前的前缀（展开层 §6.1）；models.dev 的 `api`
// 字段不能直通（它是 AI SDK 的 baseURL 约定，也没有 Responses 地址）。magpie
// `internal/provider/presets.go`（MIT）只当核对底稿，没有复制。
package preset

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed presets.json
var raw []byte

// Preset 是一条预设。有 Plans 时 Protocols 与 ModelsDev 只写在各 plan 里，第一个
// plan 是默认；没有 Plans 时写在顶层。
type Preset struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"` // vendor（厂商 API）/ relay（中转）
	// Icon 是 web/src/icons/svg 下的图标键。
	Icon string `json:"icon"`
	// ModelsDev 是 models.dev 快照的 provider id，即渠道 provider 标注的键；空 = 不标注。
	ModelsDev string            `json:"models_dev,omitempty"`
	Protocols map[string]string `json:"protocols,omitempty"`
	KeysURL   string            `json:"keys_url"`
	Note      string            `json:"note,omitempty"`
	// AuthScheme 预填「认证头」：default / bearer / raw，空 = default。
	AuthScheme string `json:"auth_scheme,omitempty"`
	Plans      []Plan `json:"plans,omitempty"`
}

// Plan 是同一家 key 不通用的一套地址（国际 / 中国、按量 / Coding Plan）。
type Plan struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	ModelsDev string            `json:"models_dev,omitempty"`
	Protocols map[string]string `json:"protocols"`
	// KeysURL 是这套地址的 key 在另一个页面申请时的地址；空 = 同预设。
	KeysURL string `json:"keys_url,omitempty"`
}

// List 返回清单，顺序即目录里的顺序。
var List = sync.OnceValues(func() ([]Preset, error) {
	var ps []Preset
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, fmt.Errorf("解析渠道预设清单：%w", err)
	}
	return ps, nil
})
