package store

// admin.go 是管理端的读写面。刻意与 Resolve/ListAccessPoints 分开：那两个在**转发
// 热路径**上，形状由「一次请求要什么」决定；这里的形状由「一个页面要展示什么」决定，
// 混在一起会让热路径顺带背上管理端才需要的 JOIN。
//
// 一条约束贯穿全文件：**这里的返回结构都不带 credential 字段**。凭证值从 v0.47 起
// 可回读，但只由凭证池那一个接口发（见 credential.go）——渠道列表、接入点、流水、
// 校验错误一律不带。收口在一个接口上，漏出去才是可控的；散在每个页面结构里，某次
// 改动顺手 SELECT * 就出去了。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
)

// ErrNotFound means the row the caller addressed by id does not exist.
var ErrNotFound = errors.New("not found")

// ErrInvalidInput means the submitted form is malformed in a way the caller can
// fix and should be told about — 400 with the reason, not a blanket 500.
//
// 配套的 InvalidInput 携带给人看的中文原因；这个哨兵只用来分类。
var ErrInvalidInput = errors.New("invalid input")

// InvalidInput 是「表单填错了」的错误，Error() 就是要显示给用户的那句话。
//
// 不用 fmt.Errorf 包哨兵：那样 Error() 会带上 "invalid input: " 这段只有分类意义的
// 英文前缀，而这句话是直接进管理端错误条的。
type InvalidInput struct{ Reason string }

func (e InvalidInput) Error() string        { return e.Reason }
func (e InvalidInput) Is(target error) bool { return target == ErrInvalidInput }

// ErrInUse means the row is still referenced by a candidate, so deleting it
// would break an access point instead of just removing an upstream.
//
// 单独一类而不是让外键自己报错：外键错误只说「约束冲突」，管理端把它翻成
// 「引用了不存在的渠道/模型」——那句话对建/改是对的，对删正好说反了（不是它
// 引用了别人，是别人在引用它）。
var ErrInUse = errors.New("in use")

// InUse 跟 InvalidInput 一样，Error() 就是显示给用户的那句话。
type InUse struct{ Reason string }

func (e InUse) Error() string        { return e.Reason }
func (e InUse) Is(target error) bool { return target == ErrInUse }

// referencingAccessPoints 返回哪些接入点的候选指着这批纳管模型。
//
// where 是加在 channel_models 上的过滤条件，调用方给「整个渠道的」或「这一个
// 模型的」。只回接入点名（也就是对外模型名），不带 base_url。
func referencingAccessPoints(ctx context.Context, db Queryer, where string, arg int64) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT ap.model
		FROM access_points ap
		JOIN candidates cd ON cd.access_point_id = ap.id
		JOIN channel_models cm ON cm.id = cd.channel_model_id
		WHERE `+where+`
		ORDER BY ap.model`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// inUse 把接入点名单拼成给人看的那句话，没人引用时返回 nil。
func inUse(what string, aps []string) error {
	if len(aps) == 0 {
		return nil
	}
	return InUse{Reason: fmt.Sprintf(
		"%s还被接入点 %s 引用。先在这些接入点里把候选改指到别的模型，或者把接入点删掉，再回来删——"+
			"删渠道不顺手带走候选是故意的：接入点空着候选，下次启动闸就过不去。",
		what, strings.Join(aps, "、"))}
}

// ChannelModel 是渠道下的一个纳管模型。
type ChannelModel struct {
	ID            int64  `json:"id"`
	UpstreamModel string `json:"upstream_model"`
	// Protocols 是这个模型自己能走的协议子集（口径层 v0.40）。**空数组 = 继承渠道
	// 全集**，绝大多数模型都该是空的；只有「渠道会说 anthropic，但这个模型不在
	// `/v1/messages` 上」这种例外才填。路由时与渠道集取交集，见 store.pickProtocol。
	Protocols protocol.Set `json:"protocols"`
	Disabled  bool         `json:"disabled"`
}

// Channel 是管理端看到的一个渠道。没有 credential 字段，见文件头。
type Channel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Protocols 是渠道能说的上游协议集——自口径层 v0.96 起是**推导值**：哪些协议
	// 填了出站根地址就是声明了哪些。照发给前端，省得每处消费端自己再推一遍。
	Protocols protocol.Set `json:"protocols"`
	// BaseURLs 是每协议出站根地址（口径层 v0.96 ②）。JSON 字段名沿用 base_url，
	// 只是从单串变成了协议 → 地址的映射。
	BaseURLs BaseURLs `json:"base_url"`
	KeyMode  string   `json:"key_mode"`
	// MaxConcurrency 是渠道级 in-flight 并发上限（口径层 v0.49）：0 = 不限。
	MaxConcurrency int `json:"max_concurrency"`
	// SupportsCompaction 记上游认不认 Codex 的 compaction_trigger（口径层 v0.54）。
	SupportsCompaction bool `json:"supports_compaction"`
	// SupportsStatefulResponses 记上游认不认 Responses 的有状态语义
	// previous_response_id（口径层 v0.88）。默认取是。
	SupportsStatefulResponses bool `json:"supports_stateful_responses"`
	Disabled                  bool `json:"disabled"`
	// 可用/停用凭证计数（口径层 v0.38，原为「有无凭证」一个布尔）：摘光不设特例，
	// 「可用凭证归零」就是渠道从能用变不能用的唯一运行期路径，而列表页是唯一会被
	// 一眼扫过的地方；布尔在 3 把里坏了 2 把时显示的仍是「有凭证」，把最该被看见
	// 的劣化过程整个藏住。
	EnabledKeys  int            `json:"enabled_keys"`
	DisabledKeys int            `json:"disabled_keys"`
	Models       []ChannelModel `json:"models"`
}

// ListChannels 返回全部渠道（含停用的——管理端要能看见并重新启用），每个带上它的
// 纳管模型清单。
func ListChannels(ctx context.Context, db Queryer) ([]Channel, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT ch.id, ch.name, ch.base_url_openai, ch.base_url_openai_responses, ch.base_url_anthropic,
		       ch.key_mode, ch.max_concurrency,
		       ch.supports_compaction, ch.supports_stateful_responses, ch.disabled,
		       (SELECT COUNT(*) FROM channel_keys ck WHERE ck.channel_id = ch.id AND ck.disabled = 0),
		       (SELECT COUNT(*) FROM channel_keys ck WHERE ck.channel_id = ch.id AND ck.disabled <> 0)
		FROM channels ch ORDER BY ch.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := []Channel{}
	byID := map[int64]int{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.Name, &c.BaseURLs.OpenAI, &c.BaseURLs.OpenAIResponses, &c.BaseURLs.Anthropic,
			&c.KeyMode, &c.MaxConcurrency,
			&c.SupportsCompaction, &c.SupportsStatefulResponses, &c.Disabled,
			&c.EnabledKeys, &c.DisabledKeys); err != nil {
			return nil, err
		}
		// 协议集是地址的推导值（口径层 v0.96）：一个地址都没填就是空数组，页面上
		// 显示「无协议」，人去补地址即可。真正拦下它的是启动闸。
		c.Protocols = c.BaseURLs.Protocols()
		if c.Protocols == nil {
			c.Protocols = protocol.Set{}
		}
		c.Models = []ChannelModel{}
		byID[c.ID] = len(channels)
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 纳管模型单独一趟再拼回去，不用 LEFT JOIN 一次拉完：JOIN 出来的行数是
	// 渠道 × 模型，得在 Go 里做一次去重才能还原渠道本身的字段。两趟更短也更难写错。
	mrows, err := db.QueryContext(ctx,
		`SELECT id, channel_id, upstream_model, protocols, disabled FROM channel_models ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var m ChannelModel
		var chID int64
		var mProtocols string
		if err := mrows.Scan(&m.ID, &chID, &m.UpstreamModel, &mProtocols, &m.Disabled); err != nil {
			return nil, err
		}
		// 空串是最常见的正常值（继承渠道全集），ParseSet 对空是报错的，所以不进它。
		// 非空解不动同样留空数组交给页面，理由同上面渠道那一列。
		if mProtocols != "" {
			m.Protocols, _ = protocol.ParseSet(mProtocols)
		}
		if m.Protocols == nil {
			m.Protocols = protocol.Set{}
		}
		if i, ok := byID[chID]; ok {
			channels[i].Models = append(channels[i].Models, m)
		}
	}
	return channels, mrows.Err()
}

// ChannelInput 是新建/修改渠道时可写的字段。credential 不在里面，它走凭证池那套逐条
// CRUD（credential.go）——分开是为了让「保存渠道」这个动作不可能顺手清掉凭证。
type ChannelInput struct {
	Name string `json:"name"`
	// BaseURLs 是每协议出站根地址（口径层 v0.96 ②）：填了哪个协议就是声明了哪个，
	// 至少要有一个；从映射里去掉一个协议就是取消声明，服务端拒绝删空（normalized）。
	BaseURLs BaseURLs `json:"base_url"`
	// CredentialType 是渠道级凭证类型 api_key / service_account 二选一。空串是「没提
	// 这个字段」——管理端从不发它（M0 只有 api_key，值域由 Validate 兜），建渠道时落
	// DDL 默认 api_key，改渠道时不动；声明文件那条路（#48 起走这道门）显式给。
	CredentialType string `json:"credential_type"`
	// KeyMode 是凭证选取模式：polling（默认）/ random。空串是「没提这个字段」——它是
	// v0.38 才露到表单上的，老前端与手写的请求体里没有；建渠道时补默认，改渠道时不动。
	KeyMode string `json:"key_mode"`
	// MaxConcurrency 是渠道级并发上限（口径层 v0.49）：0 = 不限。指针的 nil 是
	// 「没提这个字段」——与 KeyMode 的空串同一个陷阱（v0.35⑸ 整体覆盖）：它是并发
	// 闸批才露到表单上的，老请求体里没有，缺省时那一列不动；0 在这里是有意义的
	// 取值（不限），所以哨兵只能是 nil，不能再借零值。
	MaxConcurrency *int `json:"max_concurrency"`
	// SupportsCompaction 是渠道 compaction 能力位（口径层 v0.54）：上游认不认
	// compaction_trigger。指针同 MaxConcurrency——nil = 「没提这个字段」，那一列不动；
	// false 在这里是有意义的取值（默认值就是它），所以哨兵不能借零值。
	SupportsCompaction *bool `json:"supports_compaction"`
	// SupportsStatefulResponses 是渠道有状态续链能力位（口径层 v0.88）：上游认不认
	// previous_response_id。指针同上——nil = 「没提这个字段」，那一列不动。这一位的
	// **默认是 true**，所以借零值当哨兵比上面那位更错不起：false 恰好是有意义的取值，
	// 而拿它当「没提」会让每次保存都把一个本来放行的渠道悄悄关掉。
	SupportsStatefulResponses *bool `json:"supports_stateful_responses"`
	Disabled                  bool  `json:"disabled"`
}

// normalized 校验并归一化每协议地址：去空格，一个协议都没填直接拒——「删掉最后
// 一行地址」造出来的是一个没有任何协议、永远路由不到的渠道（口径层 v0.96）。
//
// 在写库之前拦，不指望启动闸——管理端的保存走的是「写完在同一事务里 Validate，
// 不过就回滚」，那条路能拦住，但报出来的是一句启动闸口吻的话；这里拦能就地说清楚。
func (in ChannelInput) normalized() (BaseURLs, error) {
	if strings.Contains(in.Name, "/") {
		return BaseURLs{}, InvalidInput{Reason: "渠道名不能含 `/`：限定名是 `渠道名/纳管模型名`，而纳管模型名本身常带 `/`" +
			"（`anthropic/claude-3` 这种），两边都能带的话 `a/b/c` 到底是渠道 a 的模型 b/c 还是渠道 a/b 的模型 c 就说不清了"}
	}
	urls := in.BaseURLs.trimmed()
	if len(urls.Protocols()) == 0 {
		return BaseURLs{}, InvalidInput{Reason: "至少给一个协议填上出站根地址：填了哪个协议的地址就是声明了哪个协议，" +
			"一个都不填的渠道永远路由不到"}
	}
	return urls, nil
}

// keyMode 归一化选取模式，**空串原样返回**表示「这次请求没提这个字段」——建渠道时
// 由 CreateChannel 补默认值，改渠道时那一列不动。不在这里补默认，是因为 PUT 是整体
// 覆盖：老前端或手写的请求体里没有 key_mode（它 v0.38 才露到表单上），在这儿补成
// polling 会把一个配好 random 的渠道静默改回轮询，而这种改动在页面上看不出来。
//
// 认不得的取值直接拒——拼错的模式名会静默退化成轮询，而「为什么总是第一把在跑」正是
// 多凭证放开后最难自己想明白的问题。
func (in ChannelInput) keyMode() (string, error) {
	switch mode := strings.TrimSpace(in.KeyMode); mode {
	case "", KeyModePolling, KeyModeRandom:
		return mode, nil
	default:
		return "", InvalidInput{Reason: "凭证选取模式只能是 polling（轮询）或 random（随机）"}
	}
}

// maxConcurrency 校验并发上限。负数直接拒而不是当 0 用：写 -1 的人多半以为它是
// 某种「不限」的暗号，静默当成 0 恰好蒙对了语义，但下次改成 -5 想「更不限」时就
// 该困惑了——说清楚只有 0 表示不限。
func (in ChannelInput) maxConcurrency() (*int, error) {
	if in.MaxConcurrency != nil && *in.MaxConcurrency < 0 {
		return nil, InvalidInput{Reason: "并发上限不能是负数：0 表示不限，正整数表示上限"}
	}
	return in.MaxConcurrency, nil
}

// CreateChannel 建一个渠道并返回它的 id。
func CreateChannel(ctx context.Context, db Conn, in ChannelInput) (int64, error) {
	urls, err := in.normalized()
	if err != nil {
		return 0, err
	}
	set := urls.Protocols()
	mode, err := in.keyMode()
	if err != nil {
		return 0, err
	}
	if mode == "" {
		mode = KeyModePolling
	}
	maxConc, err := in.maxConcurrency()
	if err != nil {
		return 0, err
	}
	conc := 0
	if maxConc != nil {
		conc = *maxConc
	}
	// 新建渠道的能力位默认否（PO 2026-08-13 裁定）：勾错成否只是一条明确的 400，
	// 勾错成是会让 Codex 在长会话里静默 Fatal。
	compaction := false
	if in.SupportsCompaction != nil {
		compaction = *in.SupportsCompaction
	}
	// 有状态续链位反过来，新建默认**是**（PO 裁定，口径层 v0.88）：关错的代价是打断
	// 一条本来能用的续链，而开错只会让上游自己回一句客户端读得懂的 not_found。
	stateful := true
	if in.SupportsStatefulResponses != nil {
		stateful = *in.SupportsStatefulResponses
	}
	// 协议不含 Responses 时两个位一律落默认值，请求体里带来的不作数（#33）：这两位
	// 只对 openai_responses 有意义，UI 也只在勾了它时才渲染——不含协议还写着非默认值
	// 的行是一个界面上看不见、也改不掉的状态。守在这里而不是指望前端不发，是因为
	// 换个 UI 或直接打 API 就绕过去了。
	if !set.Has(protocol.OpenAIResponses) {
		compaction = false
		stateful = true
	}
	credType := strings.TrimSpace(in.CredentialType)
	if credType == "" {
		credType = "api_key"
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO channels (name, base_url_openai, base_url_openai_responses, base_url_anthropic,
		                      credential_type, key_mode, max_concurrency,
		                      supports_compaction, supports_stateful_responses, disabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, urls.OpenAI, urls.OpenAIResponses, urls.Anthropic, credType, mode, conc,
		boolInt(compaction), boolInt(stateful), boolInt(in.Disabled))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateChannel 覆盖渠道的可写字段，改名也走这里（合并渠道之后要给它起个不带协议
// 后缀的新名字，口径层 v0.33 不做旧限定名的兼容期）。
func UpdateChannel(ctx context.Context, db Conn, id int64, in ChannelInput) error {
	urls, err := in.normalized()
	if err != nil {
		return err
	}
	set := urls.Protocols()
	mode, err := in.keyMode()
	if err != nil {
		return err
	}
	maxConc, err := in.maxConcurrency()
	if err != nil {
		return err
	}
	// key_mode / max_concurrency 缺省时整列不写（分别见 keyMode 与 MaxConcurrency
	// 的注释）。其余字段仍是整体覆盖——它们从第一版起就在表单里，请求体里没有等于
	// 人真把它清空了。
	sets := `name = ?, base_url_openai = ?, base_url_openai_responses = ?, base_url_anthropic = ?, disabled = ?`
	args := []any{in.Name, urls.OpenAI, urls.OpenAIResponses, urls.Anthropic, boolInt(in.Disabled)}
	if credType := strings.TrimSpace(in.CredentialType); credType != "" {
		sets += `, credential_type = ?`
		args = append(args, credType)
	}
	if mode != "" {
		sets += `, key_mode = ?`
		args = append(args, mode)
	}
	if maxConc != nil {
		sets += `, max_concurrency = ?`
		args = append(args, *maxConc)
	}
	if set.Has(protocol.OpenAIResponses) {
		if in.SupportsCompaction != nil {
			sets += `, supports_compaction = ?`
			args = append(args, boolInt(*in.SupportsCompaction))
		}
		if in.SupportsStatefulResponses != nil {
			sets += `, supports_stateful_responses = ?`
			args = append(args, boolInt(*in.SupportsStatefulResponses))
		}
	} else {
		// 协议不含 Responses 时两个位强制归位默认值（#33，PO 2026-08-19 裁定选 A）：
		// UI 只在勾了 Responses 时才渲染这两位，取消协议后前端整个不发字段，沿用
		// 「nil = 整列不写」会把一个界面上再也够不着的旧值留在库里——哪天把协议勾
		// 回来它原样复活，supports_compaction 残留 1 正是 v0.54 ⑨ 要杀的那格（上游
		// 忽略 compaction_trigger，Codex 收 0 个 compaction item 即 Fatal）。归位值
		// 各按各的默认：compaction 取否（v0.54 ⑨）、stateful 取是（v0.88 ②）。
		sets += `, supports_compaction = 0, supports_stateful_responses = 1`
	}
	args = append(args, id)
	res, err := db.ExecContext(ctx, `UPDATE channels SET `+sets+` WHERE id = ?`, args...)
	return affectedOne(res, err)
}

// ── 按意图的字段写（#48 批2）──────────────────────────────────────────────
//
// 管理端的修改从「整体覆盖 + 哨兵」拆成四笔意图写：页面上哪个控件动了就写哪一笔，
// 「哪些列动」的判断收回服务端，前端不再回传自己没编辑的字段——「回传旧 state 会把
// 别处刚存的覆盖掉」那整类坑随接口形状消失。UpdateChannel 的整体覆盖语义保留给
// declcfg：文件是总量，那条路的语义本来就是覆盖。

// UpdateChannelBaseURLs 单写每协议出站根地址。地址即协议声明（口径层 v0.96 ②），
// 这一笔写的其实是协议集：Responses 被删出集合时两个能力位随手归位默认值（#33）
// ——门多了一扇，规则还是同一条。
func UpdateChannelBaseURLs(ctx context.Context, db Conn, id int64, urls BaseURLs) error {
	trimmed := urls.trimmed()
	if len(trimmed.Protocols()) == 0 {
		return InvalidInput{Reason: "至少给一个协议填上出站根地址：填了哪个协议的地址就是声明了哪个协议，" +
			"一个都不填的渠道永远路由不到"}
	}
	sets := `base_url_openai = ?, base_url_openai_responses = ?, base_url_anthropic = ?`
	args := []any{trimmed.OpenAI, trimmed.OpenAIResponses, trimmed.Anthropic}
	if !trimmed.Protocols().Has(protocol.OpenAIResponses) {
		sets += `, supports_compaction = 0, supports_stateful_responses = 1`
	}
	args = append(args, id)
	res, err := db.ExecContext(ctx, `UPDATE channels SET `+sets+` WHERE id = ?`, args...)
	return affectedOne(res, err)
}

// UpdateChannelKeyMode 单写凭证选取模式。意图写没有「没提这个字段」的形态，空串
// 在这里不再是哨兵，就是一个非法值。
func UpdateChannelKeyMode(ctx context.Context, db Conn, id int64, mode string) error {
	switch mode = strings.TrimSpace(mode); mode {
	case KeyModePolling, KeyModeRandom:
	default:
		return InvalidInput{Reason: "凭证选取模式只能是 polling（轮询）或 random（随机）"}
	}
	res, err := db.ExecContext(ctx, `UPDATE channels SET key_mode = ? WHERE id = ?`, mode, id)
	return affectedOne(res, err)
}

// SetChannelDisabled 渠道启停。
func SetChannelDisabled(ctx context.Context, db Conn, id int64, disabled bool) error {
	res, err := db.ExecContext(ctx, `UPDATE channels SET disabled = ? WHERE id = ?`, boolInt(disabled), id)
	return affectedOne(res, err)
}

// ChannelSettings 是「上游设置」弹框那一笔：改名、并发、能力位。位字段的 nil =
// 「弹框没渲染这个控件」（协议不含 Responses 时它们不在屏幕上），那一列不动。
type ChannelSettings struct {
	Name                      string `json:"name"`
	MaxConcurrency            *int   `json:"max_concurrency"`
	SupportsCompaction        *bool  `json:"supports_compaction"`
	SupportsStatefulResponses *bool  `json:"supports_stateful_responses"`
}

// UpdateChannelSettings 写「上游设置」那一笔。这一笔不碰地址，协议集从库里现值读：
// 不含 Responses 时两个位一律写默认值、请求体里带来的不作数（#33 的 Create 侧同一
// 条立论——UI 造不出这种请求，直接打 API 能，不变式在后端、不信请求体）。
func UpdateChannelSettings(ctx context.Context, db Conn, id int64, s ChannelSettings) error {
	if strings.Contains(s.Name, "/") {
		return InvalidInput{Reason: "渠道名不能含 `/`：限定名是 `渠道名/纳管模型名`，而纳管模型名本身常带 `/`" +
			"（`anthropic/claude-3` 这种），两边都能带的话 `a/b/c` 到底是渠道 a 的模型 b/c 还是渠道 a/b 的模型 c 就说不清了"}
	}
	if s.MaxConcurrency != nil && *s.MaxConcurrency < 0 {
		return InvalidInput{Reason: "并发上限不能是负数：0 表示不限，正整数表示上限"}
	}
	var respURL string
	switch err := db.QueryRowContext(ctx,
		`SELECT base_url_openai_responses FROM channels WHERE id = ?`, id).Scan(&respURL); {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	}
	sets := `name = ?`
	args := []any{s.Name}
	if s.MaxConcurrency != nil {
		sets += `, max_concurrency = ?`
		args = append(args, *s.MaxConcurrency)
	}
	if strings.TrimSpace(respURL) != "" {
		if s.SupportsCompaction != nil {
			sets += `, supports_compaction = ?`
			args = append(args, boolInt(*s.SupportsCompaction))
		}
		if s.SupportsStatefulResponses != nil {
			sets += `, supports_stateful_responses = ?`
			args = append(args, boolInt(*s.SupportsStatefulResponses))
		}
	} else {
		sets += `, supports_compaction = 0, supports_stateful_responses = 1`
	}
	args = append(args, id)
	res, err := db.ExecContext(ctx, `UPDATE channels SET `+sets+` WHERE id = ?`, args...)
	return affectedOne(res, err)
}

// ProbeTarget 是跑一次模型级检测要的东西：打哪儿、声明了哪几个协议、有哪几份凭证、
// 有哪些模型（口径层 v0.96 检测收成一层，子路径可达性探测随层拆除）。
//
// Credentials 是唯一一处凭证值离开 store 的地方，它只流向 upstream.ProbeModel，
// **不进任何 JSON 响应**——「只写不回读」那条约束管的是回读给人看，不是进程内自用。
type ProbeTarget struct {
	Name string
	// BaseURLs 是每协议出站根地址（口径层 v0.96）；Protocols 是它的推导值，顺序
	// 即回退序。检测与拉模型列表按协议各取各的地址。
	BaseURLs  BaseURLs
	Protocols protocol.Set
	// Credentials 含**已停用**的凭证（口径层 v0.96 承接 v0.38 的立论）：恢复是
	// 纯人工的，「这把停用的凭证还坏不坏」除了发一次请求没有别的办法回答——
	// 检测就得能选中它。
	Credentials []ProbeCredential
	// Models 是**启用中**的纳管模型名（口径层 v0.43）。停用的不测：它本来就
	// 路由不到，测出来的结论没有消费者。
	Models []string
}

// ProbeCredential 是检测可选的一份凭证：选中用 ID + 显示用名字 + 进程内自用的值。
type ProbeCredential struct {
	ID       int64
	Name     string
	Value    string
	Disabled bool
}

// ChannelProbeTarget 按 id 取检测目标。
func ChannelProbeTarget(ctx context.Context, db Queryer, id int64) (ProbeTarget, error) {
	var t ProbeTarget
	err := db.QueryRowContext(ctx, `
		SELECT ch.name, ch.base_url_openai, ch.base_url_openai_responses, ch.base_url_anthropic
		FROM channels ch WHERE ch.id = ?`, id).
		Scan(&t.Name, &t.BaseURLs.OpenAI, &t.BaseURLs.OpenAIResponses, &t.BaseURLs.Anthropic)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeTarget{}, ErrNotFound
	}
	if err != nil {
		return ProbeTarget{}, err
	}
	if t.Protocols = t.BaseURLs.Protocols(); len(t.Protocols) == 0 {
		return ProbeTarget{}, InvalidInput{Reason: "这个渠道一个协议的出站根地址都没填，没有可检测的目标"}
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, credential, disabled FROM channel_keys WHERE channel_id = ? ORDER BY id`, id)
	if err != nil {
		return ProbeTarget{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var cp ProbeCredential
		if err := rows.Scan(&cp.ID, &cp.Name, &cp.Value, &cp.Disabled); err != nil {
			return ProbeTarget{}, err
		}
		t.Credentials = append(t.Credentials, cp)
	}
	if err := rows.Err(); err != nil {
		return ProbeTarget{}, err
	}

	mrows, err := db.QueryContext(ctx, `
		SELECT upstream_model FROM channel_models
		WHERE channel_id = ? AND disabled = 0 ORDER BY upstream_model`, id)
	if err != nil {
		return ProbeTarget{}, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var name string
		if err := mrows.Scan(&name); err != nil {
			return ProbeTarget{}, err
		}
		t.Models = append(t.Models, name)
	}
	return t, mrows.Err()
}

// DeleteChannel 删渠道。凭证与纳管模型靠 schema 的 ON DELETE CASCADE 跟着走；
// 指向它的候选不会级联（candidates 引用的是 channel_models 且没有 CASCADE），
// 所以先自己查一遍谁在引用——不查的话外键会在 DELETE 那一步报「约束冲突」，
// 而那条消息说不出「是哪个接入点拦着」，人只能挨个点开看。
func DeleteChannel(ctx context.Context, db Conn, id int64) error {
	aps, err := referencingAccessPoints(ctx, db, `cm.channel_id = ?`, id)
	if err != nil {
		return err
	}
	if err := inUse("这个渠道的纳管模型", aps); err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	return affectedOne(res, err)
}

// AddChannelModel 给渠道加一个纳管模型。重复添加视为幂等成功。
//
// protocols 是这个模型的协议子集（口径层 v0.40），空集合表示继承渠道全集——那是常态。
// 幂等这条对它有个后果：重复添加时协议子集也不会被改写，改子集走 SetChannelModelProtocols。
func AddChannelModel(ctx context.Context, db Conn, channelID int64, upstreamModel string, protocols protocol.Set) error {
	raw, err := normalizeModelProtocols(protocols)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO channel_models (channel_id, upstream_model, protocols) VALUES (?, ?, ?)
		ON CONFLICT(channel_id, upstream_model) DO NOTHING`, channelID, upstreamModel, raw)
	return err
}

// SetChannelModelDisabled 停用/启用一个纳管模型。
func SetChannelModelDisabled(ctx context.Context, db Conn, id int64, disabled bool) error {
	res, err := db.ExecContext(ctx,
		`UPDATE channel_models SET disabled = ? WHERE id = ?`, boolInt(disabled), id)
	return affectedOne(res, err)
}

// SetChannelModelProtocols 改一个纳管模型的协议子集（口径层 v0.40）。
//
// **不校验它是不是渠道协议集的子集**，这是有意的：渠道协议集缩小时级联清理这一列，
// 等于拿「配置任何时刻自洽」换「你改渠道时我替你删配置」，而删掉的填法回不来。存原样，
// 路由时取交集（store.pickProtocol），渠道把协议勾回来这一行自动重新生效。
func SetChannelModelProtocols(ctx context.Context, db Conn, id int64, protocols protocol.Set) error {
	raw, err := normalizeModelProtocols(protocols)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx,
		`UPDATE channel_models SET protocols = ? WHERE id = ?`, raw, id)
	return affectedOne(res, err)
}

// normalizeModelProtocols 校验并归一化模型协议子集，空集合归一成空串。
//
// 空串在读侧就是「继承渠道全集」，所以空是正常值不是错误——这正是它不能直接套渠道那
// 条 ParseSet 的原因（那边空集合直接拒，因为渠道必须至少会说一个协议）。非空则照常
// 走 ParseSet：折旧协议名、去重、拒不认识的取值，一套规则两处共用。
func normalizeModelProtocols(s protocol.Set) (string, error) {
	if len(s) == 0 {
		return "", nil
	}
	set, err := protocol.ParseSet(s.String())
	if err != nil {
		return "", InvalidInput{Reason: err.Error()}
	}
	return set.String(), nil
}

// DeleteChannelModel 删一个纳管模型。指向它的候选同样不级联，同样先点名。
func DeleteChannelModel(ctx context.Context, db Conn, id int64) error {
	aps, err := referencingAccessPoints(ctx, db, `cm.id = ?`, id)
	if err != nil {
		return err
	}
	if err := inUse("这个纳管模型", aps); err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `DELETE FROM channel_models WHERE id = ?`, id)
	return affectedOne(res, err)
}

// CandidateRow 是接入点下的一个候选，带上它指向的渠道与纳管模型的名字——页面上
// 要显示「转发到哪儿」，只有 id 没法看。
type CandidateRow struct {
	ID             int64  `json:"id"`
	ChannelModelID int64  `json:"channel_model_id"`
	ChannelID      int64  `json:"channel_id"`
	ChannelName    string `json:"channel_name"`
	UpstreamModel  string `json:"upstream_model"`
	Weight         int    `json:"weight"`
}

// AccessPointDetail 是管理端看到的一个接入点。
type AccessPointDetail struct {
	ID         int64          `json:"id"`
	Model      string         `json:"model"`
	Disabled   bool           `json:"disabled"`
	Candidates []CandidateRow `json:"candidates"`
}

// ListAccessPointsDetail 返回全部接入点（含停用的）及其候选。
func ListAccessPointsDetail(ctx context.Context, db Queryer) ([]AccessPointDetail, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, model, disabled FROM access_points ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := []AccessPointDetail{}
	byID := map[int64]int{}
	for rows.Next() {
		var ap AccessPointDetail
		if err := rows.Scan(&ap.ID, &ap.Model, &ap.Disabled); err != nil {
			return nil, err
		}
		ap.Candidates = []CandidateRow{}
		byID[ap.ID] = len(points)
		points = append(points, ap)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// LEFT JOIN 而不是 JOIN：候选可能指向已被删掉的纳管模型（悬空候选），那种行
	// 用 JOIN 会直接消失，页面上看不见——而它恰恰是最需要被看见、被删掉的一行。
	crows, err := db.QueryContext(ctx, `
		SELECT cd.id, cd.access_point_id, cd.channel_model_id, cd.weight,
		       COALESCE(ch.id, 0), COALESCE(ch.name, ''), COALESCE(cm.upstream_model, '')
		FROM candidates cd
		LEFT JOIN channel_models cm ON cm.id = cd.channel_model_id
		LEFT JOIN channels ch       ON ch.id = cm.channel_id
		ORDER BY cd.id`)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var c CandidateRow
		var apID int64
		if err := crows.Scan(&c.ID, &apID, &c.ChannelModelID, &c.Weight,
			&c.ChannelID, &c.ChannelName, &c.UpstreamModel); err != nil {
			return nil, err
		}
		if i, ok := byID[apID]; ok {
			points[i].Candidates = append(points[i].Candidates, c)
		}
	}
	return points, crows.Err()
}

// CreateAccessPoint 建接入点并挂上它的唯一候选。
//
// 两件事一起做而不是分两个接口：临时闸要求接入点恰好一个候选，分开做意味着中间必然
// 存在一个「零候选」的瞬间——而每次写完都要跑 Validate，那个瞬间会被判为非法，
// 于是第一步永远保存不了。
func CreateAccessPoint(ctx context.Context, db Conn, model string, channelModelID int64, weight int) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO access_points (model) VALUES (?)`, model)
	if err != nil {
		return 0, err
	}
	apID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (?, ?, ?)`,
		apID, channelModelID, weight); err != nil {
		return 0, err
	}
	return apID, nil
}

// UpdateAccessPoint 改接入点的对外模型名、启用状态，以及它指向的候选。
func UpdateAccessPoint(ctx context.Context, db Conn, id int64, model string, disabled bool, channelModelID int64, weight int) error {
	res, err := db.ExecContext(ctx,
		`UPDATE access_points SET model = ?, disabled = ? WHERE id = ?`, model, boolInt(disabled), id)
	if err := affectedOne(res, err); err != nil {
		return err
	}
	// 候选整条换掉。临时闸只允许一个，所以「改指向」在数据上就是删了重建。
	if _, err := db.ExecContext(ctx, `DELETE FROM candidates WHERE access_point_id = ?`, id); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (?, ?, ?)`,
		id, channelModelID, weight)
	return err
}

// DeleteAccessPoint 删接入点，候选跟着级联。
func DeleteAccessPoint(ctx context.Context, db Conn, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM access_points WHERE id = ?`, id)
	return affectedOne(res, err)
}

// APIKey 是管理端看到的一把网关 key。
//
// 带明文（Key，口径层 v0.47：PO 裁定管理端要能看能复制），**不带 key_hash**——
// 后者仍是转发热路径的校验依据，把它发给页面没有任何用处，只是多一个泄露面。
// Key 为空串表示这把是加 key_plain 那一列之前建的：原值只留了哈希，还原不了。
type APIKey struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Key 是明文。空串 = 原值没存过（存量 key），不是「这把 key 是空的」。
	Key           string `json:"key"`
	AllowedModels string `json:"allowed_models"`
	Disabled      bool   `json:"disabled"`
	CreatedAt     string `json:"created_at"`
}

// ListAPIKeys 返回全部网关 key。
func ListAPIKeys(ctx context.Context, db Queryer) ([]APIKey, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, COALESCE(key_plain, ''), allowed_models, disabled, created_at
		 FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Key, &k.AllowedModels, &k.Disabled, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// CreateAPIKey 存一把新 key：哈希给转发热路径查，明文给管理端回读（v0.47）。
// 明文仍由调用方生成——这里只负责落库，不决定 key 长什么样。
func CreateAPIKey(ctx context.Context, db Conn, name, keyHash, keyPlain, allowedModels string) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO api_keys (name, key_hash, key_plain, allowed_models) VALUES (?, ?, ?, ?)`,
		name, keyHash, keyPlain, allowedModels)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAPIKey 改名字、模型白名单与启用状态。改不了 key 本身——要换就删了重建。
// v0.47 之后服务端确实留着明文了，但「改 key」这个动作依然不提供：它跟「删了重建」
// 的效果一字不差，多一条路只是多一处能把线上客户端改死的地方。
func UpdateAPIKey(ctx context.Context, db Conn, id int64, name, allowedModels string, disabled bool) error {
	res, err := db.ExecContext(ctx,
		`UPDATE api_keys SET name = ?, allowed_models = ?, disabled = ? WHERE id = ?`,
		name, allowedModels, boolInt(disabled), id)
	return affectedOne(res, err)
}

// DeleteAPIKey 删一把 key。已落库的流水不动：call_logs.api_key_name 是**当时**的
// 名字快照，不是外键，删 key 不该让历史用量凭空消失。
func DeleteAPIKey(ctx context.Context, db Conn, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	return affectedOne(res, err)
}

// CallLogRow 是用量页的一行。
type CallLogRow struct {
	ID         int64  `json:"id"`
	CreatedAt  string `json:"created_at"`
	APIKeyName string `json:"api_key_name"`
	// Endpoint 是这次打的转发端点路径（#17）。
	//
	// string 而不是指针：新行一律有值（callLog 中间件在任何事情失败之前就写死它），
	// 空串只有一个意思——加列之前的老流水，与 UpstreamRequestID 同款。
	Endpoint string `json:"endpoint"`
	// UpstreamEndpoint 是这次打到上游的那条路径（#20），与 Endpoint 成对显示。
	//
	// string 而不是指针，同 Endpoint。空串在这一列上有两个意思，靠 Endpoint 分辨：
	// 两列皆空 = 加列之前的老流水，只有这列空 = 那次请求从没发到上游（401 / 429 /
	// count_tokens 撞非 anthropic 渠道的 501 / 并发闸队满）。
	UpstreamEndpoint string `json:"upstream_endpoint"`
	ClientProtocol   string `json:"client_protocol"`
	UpstreamProtocol string `json:"upstream_protocol"`
	ModelRequested   string `json:"model_requested"`
	ModelUpstream    string `json:"model_upstream"`
	ChannelName      string `json:"channel_name"`
	ChannelKeyName   string `json:"channel_key_name"`
	Status           int    `json:"status"`
	RetryCount       int    `json:"retry_count"`
	// IsStream：同步/流式。指针而不是 bool：NULL 是「不知道」（鉴权失败那类行没
	// 解析到请求体，迁移前的老行同），false 才是「同步」，两者不能抹成一个。
	IsStream *bool  `json:"is_stream"`
	TTFTMs   *int64 `json:"ttft_ms"`
	TotalMs  int64  `json:"total_ms"`
	// QueueWaitMs 是并发闸排队耗时（口径层 v0.52，露出见 #7）。int64 而不是指针：
	// 写侧每行必落且列不可空——「没排队」与「排了 0ms」在这一列上同档，接口照原样
	// 给 0 不转 null。真排过队（> 0）才值得摆到人眼前，那是前端的判据，不是这里的。
	QueueWaitMs      int64  `json:"queue_wait_ms"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	CacheReadTokens  *int64 `json:"cache_read_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
	// ReasoningTokens 是思考 token（口径层 v0.66），output_tokens 的明细而非另一笔。
	// null 是「上游不报这个数」，0 是「这次没思考」——前端据此决定露不露这一格。
	ReasoningTokens *int64 `json:"reasoning_tokens"`
	// Error 是网关自己的固定词表（`calllog.Outcome`）。string 而不是指针：库里
	// NULL 即「这行没有错误词」，对外统一给空串（口径层 v0.67 ⑤，同
	// UpstreamRequestID 的成例）。可空性规则的唯一定义处是 `calllog.Outcome.Column`
	// 与它的反向 `calllog.ErrorWord`，这里不再复述一遍。
	Error string `json:"error"`
	// ErrorDetail 是上游错误原文（口径层 v0.53），只在失败行有值。
	//
	// 指针而不是 string：null 是「没存过」，空串是「上游回了 4xx 但响应体是空的」
	// ——后者本身就是排障信息（某些 LB 就这么干），COALESCE 成空串会把这两件事
	// 抹平，而这一列可空的全部理由就是要分开它们。
	//
	// **只走管理端接口**：它是不可控的上游文本，转发路径回给客户端的永远是我方的
	// 固定词表与 error.message 一句。
	ErrorDetail *string `json:"error_detail"`
	// UpstreamRequestID 是上游响应头 request-id 的原样快照（口径层 v0.56），拿它去
	// 上游那边对账。**成功行也有**，与 ErrorDetail 只在失败行有值不同。
	//
	// string 而不是指针：库里这一列不可空（默认空串），「没走到上游」「上游没回这个
	// 头」「v0.56 之前的老流水」三种情况在对账上是同一件事——都没有可用的 id。
	UpstreamRequestID string `json:"upstream_request_id"`
}

// CallLogFilter 是流水列表的筛选与翻页条件。
//
// **Before 与 Offset 一起用才是完整的翻页**（v0.61）。单用 offset 会错位：流水是时间序、
// 新行不断插到头部，翻到第二页时 offset 已经被新写入的行推着往后错，同一条出现两次。
// 单用游标则只能一页一页地挪——它没有逆向形式，也没有「往前数 60 条」这种形式，跳到
// 第 7 页算不出来。管理端因此进页时记下当时的最大 id 当 Before 把窗口的上沿钉死，Offset
// 在这个窗口里定位：新行都落在线上边，既挤不进任何一页，也不会让总页数在手底下变。
// 两个都不给就是「最新的那一页」。
type CallLogFilter struct {
	Limit  int
	Offset int
	// Before 只取 id 严格小于它的行；0 = 从最新一条开始。
	Before int64
	// Model 精确匹配 model_requested（客户端请求的那个名，不是上游模型名）。
	// 取 UnknownModelLabel 时筛的是「没解析到模型名」那一档，即 model_requested = ''：
	// 用量页的模型下拉是拿 by=model 的聚合 label 填的，那一档在那里就叫这个名，
	// 筛不动的话它在下拉里就是个点了没反应的死选项。
	Model string
	// APIKeyName 精确匹配网关 key 的名字快照。
	APIKeyName string
	// Endpoint 精确匹配端点路径（#17），取值就是 protocol.Endpoint 那四条之一。
	//
	// 不设「(未记录端点)」那种哨兵：加列前的老行确实没有这个信息，而本票要的是把
	// count_tokens 从 messages 里择出来，不是把历史行挑出来看。
	//
	// 筛的只有**入站**端点：出站那一列（#20）只展示不筛，问「这批 count_tokens 怎么
	// 样」用的是客户端打的那条路径，出站是同一批行的属性、不是另一个入口。
	Endpoint string
	// FailedOnly 只留 status >= 400 的行。判据是状态码而非 error 列非空——上游透传
	// 4xx 的 error 列是空的（v0.28 纪律），漏掉它「只看失败」就名不副实。
	FailedOnly bool
}

// callLogWhere 把筛选条件拼成 WHERE 子句。列表与计数必须共用它——两处各拼一份的话，
// 某天加了个筛选只改了一处，页码就会按另一组条件算出来（比如「只看失败」把总数算成
// 全部流水，末页翻过去是空的）。
func callLogWhere(f CallLogFilter) (string, []any) {
	where, args := []string{}, []any{}
	if f.Before > 0 {
		where, args = append(where, "id < ?"), append(args, f.Before)
	}
	if f.Model == UnknownModelLabel {
		where = append(where, "model_requested = ''")
	} else if f.Model != "" {
		where, args = append(where, "model_requested = ?"), append(args, f.Model)
	}
	if f.APIKeyName != "" {
		where, args = append(where, "api_key_name = ?"), append(args, f.APIKeyName)
	}
	if f.Endpoint != "" {
		where, args = append(where, "endpoint = ?"), append(args, f.Endpoint)
	}
	if f.FailedOnly {
		where = append(where, "status >= 400")
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// CountCallLogs 数这组筛选下总共有多少行，供管理端算总页数。
//
// 它与 ListCallLogs 认同一个 Before：管理端翻页时把 Before 钉在进页那一刻的最大 id 上，
// 总数与行都只看这条线以下的部分，于是翻页过程中新写进来的流水既不会挤进某一页，也不会
// 让总页数在手底下变。不加 Before 时数的就是当下的全部。
func CountCallLogs(ctx context.Context, db Queryer, f CallLogFilter) (int64, error) {
	clause, args := callLogWhere(f)
	var n int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_logs`+clause, args...).Scan(&n)
	return n, err
}

// ListCallLogs 返回最近的调用流水，最新在前。limit 由调用方兜上限。
func ListCallLogs(ctx context.Context, db Queryer, f CallLogFilter) ([]CallLogRow, error) {
	clause, args := callLogWhere(f)
	args = append(args, f.Limit, f.Offset)

	rows, err := db.QueryContext(ctx, `
		SELECT id, created_at, api_key_name, endpoint, upstream_endpoint, client_protocol, upstream_protocol,
		       model_requested, model_upstream, channel_name, channel_key_name, status, retry_count,
		       is_stream, ttft_ms, total_ms, queue_wait_ms, input_tokens, output_tokens,
		       cache_read_tokens, cache_write_tokens, reasoning_tokens,
		       error, error_detail, upstream_request_id
		FROM call_logs`+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallLogRow{}
	for rows.Next() {
		var r CallLogRow
		var ttft, in, outTok, cr, cw, reasoning sql.NullInt64
		var stream sql.NullBool
		var errWord, detail sql.NullString
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.APIKeyName, &r.Endpoint, &r.UpstreamEndpoint,
			&r.ClientProtocol, &r.UpstreamProtocol,
			&r.ModelRequested, &r.ModelUpstream, &r.ChannelName, &r.ChannelKeyName, &r.Status, &r.RetryCount,
			&stream, &ttft, &r.TotalMs, &r.QueueWaitMs, &in, &outTok, &cr, &cw, &reasoning,
			&errWord, &detail, &r.UpstreamRequestID); err != nil {
			return nil, err
		}
		// NULL → 空串在 Go 侧抹，不在 SQL 里 COALESCE：这一列的可空性规则只该有
		// 一个定义处（calllog.Outcome.Column 的反向），写在 SQL 里等于把它复制到
		// 一处类型系统看不见、也没有单测的地方。
		r.Error = calllog.ErrorWord(errWord)
		r.TTFTMs, r.InputTokens, r.OutputTokens = nullable(ttft), nullable(in), nullable(outTok)
		r.CacheReadTokens, r.CacheWriteTokens = nullable(cr), nullable(cw)
		r.ReasoningTokens = nullable(reasoning)
		r.IsStream = nullableBool(stream)
		r.ErrorDetail = nullableStr(detail)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageRow 是用量汇总的一行。Label 按维度取值：接入点名，或上游凭证名。
type UsageRow struct {
	Label        string `json:"label"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read_tokens"`
	CacheWrite   int64  `json:"cache_write_tokens"`
}

// 用量聚合的三个维度（v0.38 加了按上游凭证，v0.53 加了按网关 key）。
const (
	UsageByModel      = "model"
	UsageByKey        = "key"
	UsageByCredential = "credential"
)

// UnknownModelLabel 是「请求没解析到模型名」那一档的显示名。401 鉴权失败、请求体
// 不是合法 JSON、缺 model 字段这几条路径都在赋值 model_requested 之前就返回了
// （server.go relay），流水里那一列是空串。
//
// 不能让空串原样出现在模型维度里：用量页的模型下拉用「空串 = 全部模型」表示不筛，
// 空 label 撞上这个取值，两行会同时显示成选中。给它一个括号哨兵，取名法与另外两个
// 维度的 (未鉴权)/(未走到上游) 一样，并让 CallLogFilter.Model 认它——聚合里露得出来
// 的档位就该点得进去，点了没反应的死选项比没有这个选项更糟。
//
// **与那两个不同的是它还当查询参数值**（口径层 v0.64）：那两个只出不进，这个是第一个
// 双向哨兵。代价是客户端真请求一个叫 (未记录模型) 的模型就会撞上——届时**哨兵优先**，
// 那个模型既筛不出来，也会被 UsageBy 并进这一档的聚合行。判为可接受：单人网关、模型名
// 是自己配的，全角括号是刻意选的。真撞上再改成 label 与查询值分离的双字段形状，那要
// 三个维度一起动。
const UnknownModelLabel = "(未记录模型)"

// UsageBy 汇总一段时间的用量，按 dim 指定的维度聚合。时间范围由 r 定：默认是「近
// r.Days 个自然日」（本地时区，见 windowStart），r 的两个端点都给时则是那个半开
// 区间（口径层 v0.86，排行页点中节律带上某一格之后按那一格重算）。
//
// 按凭证聚合是 v0.38 加的：「这个号跑了多少、还剩多少」是个聚合问题，只给逐行的
// 日志表等于把 group by 留给人的肉眼做。凭证名为空串的行不丢掉——藏起来会让两个维度
// 的总次数对不上——但也不能一股脑归成「没走到上游」，那里面混着两种行：
//
//   - 渠道名也为空：确实没走到上游（鉴权失败、模型不存在、限流），归「(未走到上游)」。
//   - 渠道名不空：选出了候选却没留下凭证名。绝大多数是 v0.38 迁移之前的老流水（那时
//     还没有这一列），少数是选出候选后、发出请求前就失败的（比如请求体转换失败）。
//     这些行走到过上游或差一步就到，归「(未走到上游)」是错的，单独一档。
//
// Errors 数的是 status >= 400 的行，包括上游自己回的 4xx——用量页要回答的是
// 「有多少次调用没拿到东西」，而不是「网关有没有出错」，两者对使用者是一回事。
// 按网关 key 聚合是 v0.53 加的：PO 想问的「我在网关新建的这把 key 跑了多少」，此前
// 唯一沾边的维度是**上游**凭证——名字像、含义完全是另一件事。空 key 名归「(未鉴权)」：
// 鉴权失败的请求也落流水（口径层 §2.5），把它们混进某把 key 的账里是错的。
// windowStart 拼出「近 days 天」的下界表达式：**本地时区的 days 个自然日，今天算一天**
// （口径层 v0.55）。原先是滚动的 days×24 小时，用量页把这段时间画成按天的柱子之后
// 那个口径藏不住了——最老的那根只覆盖大半天，每次看都矮一截，而那是窗口切得巧，
// 不是那天真的少。
//
// `created_at` 存的是 UTC（`CURRENT_TIMESTAMP`），所以边界在本地日历上算完再折回 UTC
// 去比：写成 `date(created_at,'localtime') >= …` 一样对，但那样整列都要过一遍函数，
// `idx_call_logs_created_at` 就用不上了。
func windowStart(days int) string {
	return fmt.Sprintf("datetime('now', 'localtime', 'start of day', '-%d days', 'utc')", days-1)
}

// UsageRange 定用量查询的**记账范围**：要么「近 Days 个自然日」（windowStart），
// 要么一段 unix 秒的半开区间 [From, To)。两端都非零时走后者，Days 不参与
// （口径层 v0.86）——排行页点中节律带上的某一格之后，要按那一格重新聚合。
type UsageRange struct {
	// Days 至少是 1（windowStart 里「今天算一天」，0 会拼出一条 SQLite 认不得的
	// 时间修饰符、筛出空结果而不报错）。走 From/To 那条路时它不参与，可以留零值。
	Days int
	From time.Time
	To   time.Time
}

// Spanned 报这个范围走不走 From/To 那条路。只给一端当没给：一端定不出区间。
func (r UsageRange) Spanned() bool { return !r.From.IsZero() && !r.To.IsZero() }

// where 拼时间条件与它的参数（不含 WHERE 关键字）。
//
// From/To 在 Go 侧折成 UTC datetime 串再比，**不在 SQL 里对 created_at 套函数**：
// 理由与 windowStart 那条同一个——整列过一遍函数，idx_call_logs_created_at 就废了。
func (r UsageRange) where() (string, []any) {
	if r.Spanned() {
		return "created_at >= ? AND created_at < ?", []any{sqlTime(r.From), sqlTime(r.To)}
	}
	return "created_at >= " + windowStart(r.Days), nil
}

// sqlTime 把时刻折成 created_at 那一列的存储形态：UTC，CURRENT_TIMESTAMP 的格式。
func sqlTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// 分桶粒度（口径层 v0.86）。1 天窗口要 24 个小时桶、7 天窗口要 168 个。
const (
	BucketDay  = "day"
	BucketHour = "hour"
)

// BucketUsage 是节律带上的一格：某个区间里的调用数、失败数与四个 token 列。
//
// errors 与缓存两列必须给：切片井摆的是缓存 / 净输入 / 输出三段，而**净输入 =
// 毛值 − 缓存两项**（口径层 v0.71）；节律带每格的 tooltip 要报失败数。
type BucketUsage struct {
	// Bucket 是本地时区的桶起点：unit=day 为 YYYY-MM-DD，unit=hour 为 YYYY-MM-DD HH:00。
	Bucket       string `json:"bucket"`
	Calls        int64  `json:"calls"`
	Errors       int64  `json:"errors"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CacheRead    int64  `json:"cache_read_tokens"`
	CacheWrite   int64  `json:"cache_write_tokens"`
}

// UsageBuckets 把最近 days 天（本地自然日，见 windowStart）按 unit 分桶。
//
// **空桶补零**：SQL 的 GROUP BY 只吐有行的桶，照那份结果画图，空着的那几格会从横轴上
// 消失、剩下的挤在一起，看起来像是一直在用——而「什么时候没在用」正是这张图要回答的
// 问题之一。小时粒度下这条只会更硬：一天 24 格里空着十几格是常态。
//
// **未到的区间不给**（口径层 v0.86 ③）：桶只补到 now，不补到窗口右端。今天 15 点看
// 「今天」，活跃区间的分母是 16 不是 24；把 24 格都吐出来的话，前端要么自己再算一次
// 「哪些是未来」，要么就把分母算错。格子怎么排是前端的事，这里只答「已经发生了什么」。
//
// now 补的是桶序列，SQL 里的窗口下界仍走 datetime('now')——两者必须是**同一天**
// （测试里可以钉死钟点，但别钉到别的日期上，否则补齐序列与窗口对不上）。
func UsageBuckets(ctx context.Context, db Queryer, days int, unit string, now time.Time) ([]BucketUsage, error) {
	expr, layout := `date(created_at, 'localtime')`, "2006-01-02"
	if unit == BucketHour {
		expr, layout = `strftime('%Y-%m-%d %H:00', created_at, 'localtime')`, "2006-01-02 15:00"
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS bucket,
		       COUNT(*),
		       SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0)
		FROM call_logs
		WHERE created_at >= %s
		GROUP BY bucket ORDER BY bucket`, expr, windowStart(days)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string]BucketUsage{}
	for rows.Next() {
		var u BucketUsage
		if err := rows.Scan(&u.Bucket, &u.Calls, &u.Errors,
			&u.InputTokens, &u.OutputTokens, &u.CacheRead, &u.CacheWrite); err != nil {
			return nil, err
		}
		got[u.Bucket] = u
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 补齐用 now 的本地日历，与 SQL 里那两个 'localtime' 同源（都取进程所在时区）。
	out := []BucketUsage{}
	for t := bucketStart(now, days); !t.After(now); t = nextBucket(t, unit) {
		key := t.Format(layout)
		if u, ok := got[key]; ok {
			out = append(out, u)
			continue
		}
		out = append(out, BucketUsage{Bucket: key})
	}
	return out, nil
}

// bucketStart 是补齐序列的第一格：本地时区的「days 个自然日前那天的 00:00」，
// 与 windowStart 拼出来的下界同一个时刻。
func bucketStart(now time.Time, days int) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		AddDate(0, 0, -(days - 1))
}

// nextBucket 往后挪一格。天用 AddDate 而不是加 24 小时：有夏令时的时区里那两者不等，
// 会把某一天的标签挪到前一天上去。
func nextBucket(t time.Time, unit string) time.Time {
	if unit == BucketHour {
		return t.Add(time.Hour)
	}
	return t.AddDate(0, 0, 1)
}

func UsageBy(ctx context.Context, db Queryer, r UsageRange, dim string) ([]UsageRow, error) {
	// 模型这一档的兜底文案走参数而不是拼进 SQL：它在 Go 侧还要给 CallLogFilter 用，
	// 两边照着同一个常量走才不会一边改了另一边筛不到。
	label, args := `CASE WHEN model_requested <> '' THEN model_requested ELSE ? END`,
		[]any{UnknownModelLabel}
	if dim == UsageByKey {
		label, args = `CASE WHEN api_key_name <> '' THEN api_key_name ELSE '(未鉴权)' END`, nil
	}
	if dim == UsageByCredential {
		label, args = `CASE
		           WHEN channel_key_name <> '' THEN channel_key_name
		           WHEN channel_name <> ''     THEN '(未记录凭证)'
		           ELSE '(未走到上游)'
		         END`, nil
	}
	// 范围的参数排在标签之后：SELECT 里那个 ? 先于 WHERE 里的两个出现。
	where, rangeArgs := r.where()
	args = append(args, rangeArgs...)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS label,
		       COUNT(*),
		       SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0)
		FROM call_logs
		WHERE %s
		GROUP BY label ORDER BY COUNT(*) DESC`, label, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.Label, &u.Calls, &u.Errors,
			&u.InputTokens, &u.OutputTokens, &u.CacheRead, &u.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullable(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// nullableStr 同上，给那些**空串本身有含义**的列用——JSON 里 null 与 "" 得是两个答案。
func nullableStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

// nullableBool 同上——is_stream 的 null 是「不知道」，false 是「同步」，不能抹成一个。
func nullableBool(n sql.NullBool) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Bool
	return &v
}

// affectedOne 把「按 id 改了 0 行」翻译成 ErrNotFound。
//
// 没有这一步，改一个不存在的 id 会静默成功，管理端上表现为「保存了但没变化」。
func affectedOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
