// Package declcfg 读声明文件 channels.yaml：业务配置的另一条入口。
//
// 口径层 §2.9 / v0.91：**挂了声明文件，它就是业务配置的唯一事实源**（管理端对业务
// 配置全只读，管理面本身按管理密码的存在性决定注不注册）；没挂则完全是现状——DB
// 为准、管理端可写。闸就是「文件在不在」，全有全无。
//
// 为什么不并进 internal/config（展开层 §7.9）：那个包管的是**进程级**配置，而两份
// 文件的严格度是**刻意不同的**——声明文件开 KnownFields(true)、未知字段即拒，
// config.yaml 保持裸 Unmarshal。并进去之后这条口径没有载体，只剩一句「别搞混」的
// 注释在挡。
package declcfg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/SimonGino/portage/internal/store"
)

// PlaceholderAPIKey 是 channels.example.yaml 里那把出厂 API Key 的值。
//
// 它进闸而不是进注释（口径层 §2.9 #28 ④）：占位**凭证**只是自伤且当场可见——第一个
// 请求就 401，人立刻知道；占位 **API Key** 是公开在仓库里的门钥匙，谁抄了示范文件
// 直接部署，谁的网关就对全世界开着。所以闸只管 API Key 不管上游凭证。
const PlaceholderAPIKey = "sk-ptg-把这里换成你自己生成的-API-Key"

// File 是声明文件的整体形状。字段名逐个对齐 store 的 DDL 列名，不另造一套词表。
//
// 一份文件装完整个业务配置（口径层 §2.9 #34）：presence 闸是全有全无的，拆多份就要
// 重定义闸（挂了三份里的两份算什么），而 apply 的单事务与「一次报全」都以单一输入
// 为前提。**没有顶层 version 字段**（PO 2026-08-19）：导出与 apply 是同一个二进制同
// 一个版本，不存在「新网关读旧文件」的跨版本场景。
type File struct {
	Channels     []Channel     `yaml:"channels"`
	AccessPoints []AccessPoint `yaml:"access_points"`
	APIKeys      []APIKey      `yaml:"api_keys"`
}

// Channel 是一个上游渠道，内嵌它的凭证池与纳管模型（口径层 §2.9 #29）。
//
// 内嵌而不是平铺加外键引用：渠道名是自然键，凭证与纳管模型都只属于一个渠道，
// 平铺会让文件里多出两处「这个 channel_name 拼对了吗」的手写引用。
//
// **没有 protocols 字段**（口径层 v0.96 ②）：支持协议集由「base_url 里给哪些协议
// 填了地址」推导，文件里再写一份就能写出自相矛盾。手写 protocols 会被 KnownFields
// 严格档当场拒掉，正是要的效果。
type Channel struct {
	Name string `yaml:"name"`
	// BaseURL 是每协议出站根地址（口径层 v0.96 ②）：键是协议名，填了即声明该协议，
	// 至少一个。同一上游共用前缀就是几个键填同一个值；地址仍是协议子路径之前的前缀。
	BaseURL        store.BaseURLs `yaml:"base_url"`
	CredentialType string         `yaml:"credential_type"`
	KeyMode        string         `yaml:"key_mode"`
	MaxConcurrency int            `yaml:"max_concurrency"`

	// Provider 是 models.dev 的 provider id 标注（口径层 §2.10，#74）：只服务管理端
	// 的建议价与图标分组，不参与路由。omitempty——没标注的渠道不背一行空串，往返闸
	// 两边（导出不写空值、apply 落空串）对得上。
	Provider string `yaml:"provider,omitempty"`

	SupportsCompaction bool `yaml:"supports_compaction"`
	// SupportsStatefulResponses 是指针，因为它的 DDL 默认值是 **1**，与 Go 的 bool
	// 零值相反（口径层 v0.88 定它默认取「是」，理由是代价不对称的方向与 compaction
	// 位相反）。写成裸 bool 会让「手写文件里没写这一项」静默变成「否」，而位为否
	// 会 400 掉本来能正常续链的请求。nil = 没写 = 跟 DDL 默认走。
	SupportsStatefulResponses *bool `yaml:"supports_stateful_responses"`

	Disabled bool `yaml:"disabled"`

	Credentials []Credential `yaml:"credentials"`
	Models      []Model      `yaml:"models"`
}

// Credential 是凭证池里的一条。**只有名字与值，没有 disabled。**
//
// 这是全文件唯一一处「实体有 disabled 列却不进文件」（口径层 §2.9 #28 ①）：
// channel_keys 的 disabled / disabled_reason / disabled_at 三列**整组**是运行期状态
// （人工停用的现场；v0.95 之前还有 401 自动摘除写它），不进文件。意图列进文件说的是
// **另外四张表**的 disabled（channels、channel_models、access_points、api_keys）加上
// candidates.weight。
//
// 若把它当意图列收进来，每次重启 apply 都会拿文件里那个 disabled: false 把停用的
// key 重新推回轮询池——那正是「必须 upsert、不能 delete-all+insert」这条
// 要防的事，只是换了个更隐蔽的入口。人要停用一把凭证，就把它从文件里删掉。
type Credential struct {
	Name       string `yaml:"name"`
	Credential string `yaml:"credential"`
}

// Model 是渠道纳管的一个上游模型。
type Model struct {
	UpstreamModel string   `yaml:"upstream_model"`
	Protocols     []string `yaml:"protocols"`
	// MaxInputTokens 是输入上限（估算）（口径层 v0.99）：0 = 不限。裸 int 不用指针——
	// 0 与「没写」同义（都是不限），没有 weight 那种零值陷阱。
	MaxInputTokens int `yaml:"max_input_tokens"`
	// 四价（口径层 §2.10，#65/#74），USD/百万 token。**指针必须**：0 是真免费、
	// 「没写」是未定价（落 NULL），两态在裸 float64 上长一样——正是 weight 那类
	// 零值陷阱。omitempty 只略过 nil，显式 0 照写，往返闸两边对得上。
	PriceInput      *float64 `yaml:"price_input,omitempty"`
	PriceOutput     *float64 `yaml:"price_output,omitempty"`
	PriceCacheRead  *float64 `yaml:"price_cache_read,omitempty"`
	PriceCacheWrite *float64 `yaml:"price_cache_write,omitempty"`
	Disabled        bool     `yaml:"disabled"`
}

// AccessPoint 是对外模型名，内嵌它的候选。
type AccessPoint struct {
	Model      string      `yaml:"model"`
	Disabled   bool        `yaml:"disabled"`
	Candidates []Candidate `yaml:"candidates"`
}

// Candidate 用**限定名**引用一个渠道纳管模型：`渠道名/上游模型名`。
//
// 跨渠道引用只能靠限定名（口径层 §2.9 #29）：候选是文件里唯一一处跨实体引用，而
// 实体没有显式 id 可挂。限定名必须在 apply 前校验存在，不存在即拒。
type Candidate struct {
	Target string `yaml:"target"`
	// Weight 是指针，同 config.yaml 里 max_retries 那个零值陷阱：**weight=0 是意图
	// 不是缺席**（口径层 §2.9 #29——它表示「临时摘除」，与「这条候选不存在」是两件
	// 事）。写成裸 int，「没写」和「写了 0」都解出 0，补默认值会让写 0 的人摘不掉。
	// nil = 没写 = 跟 DDL 默认 100 走。
	Weight *int `yaml:"weight"`
}

// APIKey 是网关侧的接入 key。
//
// 值必须在文件里**显式给**（口径层 §2.9 #28 ②）：无管理面就没有「生成后看一眼」的
// 地方，写进日志撞 §2.7 的秘密纪律，只落库则让「文件是唯一事实源」当场破功。
type APIKey struct {
	Name          string   `yaml:"name"`
	Key           string   `yaml:"key"`
	AllowedModels []string `yaml:"allowed_models"`
	Disabled      bool     `yaml:"disabled"`
}

// Load 读 path 指向的声明文件。
//
// 路径三档语义（口径层 §2.9 #31 ③），三档各有各的意思，不许合并：
//
//   - **空**       —— 没挂，合法，返回 (nil, nil)，调用方完全走现状形态。
//   - **非空读不到** —— 想挂但挂歪了，**启动即拒**。躲的是 litellm 那个坑：环境变量
//     给的路径不存在时它整个 load 静默跳过、带 0 个模型起来，而同一份配置走命令行
//     参数却 fail fast。store.Validate 空库全过，兜不住它。
//   - **读到但不合法** —— 启动即拒，见 KnownFields 与 Apply 的自校验。
func Load(path string) (*File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("声明文件 %s 不存在。给了路径就必须能读到——"+
			"不挂声明文件是另一种部署形态（不给 -channels / PORTAGE_CHANNELS 即可），"+
			"不是把路径写错", path)
	}
	if err != nil {
		return nil, fmt.Errorf("读声明文件 %s：%w", path, err)
	}
	return Parse(raw, path)
}

// Parse 把一份声明文件的字节解成 File。
//
// **KnownFields(true)：未知字段即拒**（口径层 §2.9 #31 ②）。这一档明确不跟五个参考
// 仓的宽松共识走，理由是 portage 的主路径是「导出 → 部署」——机器生成的文件不会有
// 未知字段，严格档在主路径上零成本，只打手写这条**次要**路径；而纯转发形态下没有
// 任何页面能看「生效配置到底是什么」，静默吞掉一个拼错的字段就再没有第二处能发现它。
//
// config.yaml 保持宽松不动（config.Load 那个裸 Unmarshal）：它是进程级配置、另一份
// 东西，转严会让带历史遗留字段的老部署升级即拒启。**两份文件严格度不同是刻意的。**
func Parse(raw []byte, path string) (*File, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f File
	// 空文件解出 io.EOF。**不在这里拒**：空文件是「挂了文件却什么都没写」，而那件事
	// 由下游那道空 api_keys 的闸报，报得比「YAML 解析失败」准确得多。
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("解析声明文件 %s：%w", path, err)
	}
	return &f, nil
}
