package store

// admin.go 是管理端的读写面。刻意与 Resolve/ListAccessPoints 分开：那两个在**转发
// 热路径**上，形状由「一次请求要什么」决定；这里的形状由「一个页面要展示什么」决定，
// 混在一起会让热路径顺带背上管理端才需要的 JOIN。
//
// 一条硬约束贯穿全文件：**上游凭证只写不回读**（PO 于 M3 裁定）。因此所有返回结构里
// 都没有 credential 字段，只有 HasCredential 这个布尔。加一个「掩码回读」都不行——
// 掩码本身是信息，且实现上很容易某次改动漏掉掩码把全串吐出去。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/SimonGino/ai-gateway/internal/protocol"
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
	Disabled      bool   `json:"disabled"`
}

// Channel 是管理端看到的一个渠道。没有 credential 字段，见文件头。
type Channel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Protocols 是渠道能说的上游协议集（口径层 v0.33）。对前端是个字符串数组；
	// 库里是逗号分隔的一列。
	Protocols protocol.Set `json:"protocols"`
	BaseURL   string       `json:"base_url"`
	KeyMode   string       `json:"key_mode"`
	Disabled  bool         `json:"disabled"`
	// HasCredential 是「这个渠道能不能用」在页面上唯一看得见的凭证信息。
	HasCredential bool           `json:"has_credential"`
	Models        []ChannelModel `json:"models"`
}

// ListChannels 返回全部渠道（含停用的——管理端要能看见并重新启用），每个带上它的
// 纳管模型清单。
func ListChannels(ctx context.Context, db Queryer) ([]Channel, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT ch.id, ch.name, ch.protocols, ch.base_url, ch.key_mode, ch.disabled,
		       EXISTS(SELECT 1 FROM channel_keys ck WHERE ck.channel_id = ch.id AND ck.disabled = 0)
		FROM channels ch ORDER BY ch.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	channels := []Channel{}
	byID := map[int64]int{}
	for rows.Next() {
		var c Channel
		var protocols string
		if err := rows.Scan(&c.ID, &c.Name, &protocols, &c.BaseURL, &c.KeyMode, &c.Disabled, &c.HasCredential); err != nil {
			return nil, err
		}
		// 解不动就留空数组交给页面显示，不让整张列表 500：这一列可以是手写 SQL
		// 灌坏的，而管理端恰恰是去修它的地方。真正拦下它的是启动闸。
		c.Protocols, _ = protocol.ParseSet(protocols)
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
		`SELECT id, channel_id, upstream_model, disabled FROM channel_models ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var m ChannelModel
		var chID int64
		if err := mrows.Scan(&m.ID, &chID, &m.UpstreamModel, &m.Disabled); err != nil {
			return nil, err
		}
		if i, ok := byID[chID]; ok {
			channels[i].Models = append(channels[i].Models, m)
		}
	}
	return channels, mrows.Err()
}

// ChannelInput 是新建/修改渠道时可写的字段。credential 不在里面，它走
// SetChannelCredential——分开是为了让「保存渠道」这个动作不可能顺手清掉凭证。
type ChannelInput struct {
	Name      string       `json:"name"`
	Protocols protocol.Set `json:"protocols"`
	BaseURL   string       `json:"base_url"`
	Disabled  bool         `json:"disabled"`
}

// normalized 校验并归一化支持协议集：去空格、去重、保序，空集合直接拒。
//
// 在写库之前拦，不指望启动闸——管理端的保存走的是「写完在同一事务里 Validate，
// 不过就回滚」，那条路能拦住，但报出来的是一句启动闸口吻的话；这里拦能就地说清楚。
func (in ChannelInput) normalized() (string, error) {
	set, err := protocol.ParseSet(in.Protocols.String())
	if err != nil {
		return "", InvalidInput{Reason: err.Error()}
	}
	return set.String(), nil
}

// CreateChannel 建一个渠道并返回它的 id。
func CreateChannel(ctx context.Context, db Conn, in ChannelInput) (int64, error) {
	protocols, err := in.normalized()
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO channels (name, protocols, base_url, disabled) VALUES (?, ?, ?, ?)`,
		in.Name, protocols, in.BaseURL, boolInt(in.Disabled))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateChannel 覆盖渠道的可写字段，改名也走这里（合并渠道之后要给它起个不带协议
// 后缀的新名字，口径层 v0.33 不做旧限定名的兼容期）。
func UpdateChannel(ctx context.Context, db Conn, id int64, in ChannelInput) error {
	protocols, err := in.normalized()
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `
		UPDATE channels SET name = ?, protocols = ?, base_url = ?, disabled = ? WHERE id = ?`,
		in.Name, protocols, in.BaseURL, boolInt(in.Disabled), id)
	return affectedOne(res, err)
}

// ProbeTarget 是跑一次协议可达性探测要的东西：打哪儿、试哪几个协议、用哪把凭证。
//
// Credential 是唯一一处凭证离开 store 的地方，它只流向 upstream.Probe，**不进任何
// JSON 响应**——「上游凭证只写不回读」那条约束管的是回读给人看，不是进程内自用。
type ProbeTarget struct {
	Name       string
	BaseURL    string
	Protocols  protocol.Set
	Credential string
}

// ChannelProbeTarget 按 id 取探测目标。渠道没有启用凭证时 Credential 为空串——照样
// 探，只是上游多半回 401，而 401 同样证明子路径存在，这正是探测要问的。
func ChannelProbeTarget(ctx context.Context, db Queryer, id int64) (ProbeTarget, error) {
	var t ProbeTarget
	var protocols string
	var credential sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT ch.name, ch.base_url, ch.protocols,
		       (SELECT ck.credential FROM channel_keys ck
		        WHERE ck.channel_id = ch.id AND ck.disabled = 0 LIMIT 1)
		FROM channels ch WHERE ch.id = ?`, id).
		Scan(&t.Name, &t.BaseURL, &protocols, &credential)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeTarget{}, ErrNotFound
	}
	if err != nil {
		return ProbeTarget{}, err
	}
	t.Credential = credential.String
	if t.Protocols, err = protocol.ParseSet(protocols); err != nil {
		return ProbeTarget{}, InvalidInput{Reason: err.Error()}
	}
	return t, nil
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

// SetChannelCredential 换掉渠道的上游凭证。
//
// 先删后插而不是 UPDATE：临时闸要求「恰好 1 份启用凭证」，UPDATE 在有 0 份或 2 份
// 时都会悄悄走偏。已停用的旧凭证留着不动——那是 key 熔断的现场，删了就查不到
// 「哪一把什么时候被停的」。
func SetChannelCredential(ctx context.Context, db Conn, channelID int64, credential string) error {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM channel_keys WHERE channel_id = ? AND disabled = 0`, channelID); err != nil {
		return err
	}
	// channel_id 指向不存在的渠道时这一句会报外键错误——Open 里 PRAGMA
	// foreign_keys(1) 是开着的，所以这里不需要再自己查一次渠道在不在。
	_, err := db.ExecContext(ctx,
		`INSERT INTO channel_keys (channel_id, credential) VALUES (?, ?)`, channelID, credential)
	return err
}

// AddChannelModel 给渠道加一个纳管模型。重复添加视为幂等成功。
func AddChannelModel(ctx context.Context, db Conn, channelID int64, upstreamModel string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO channel_models (channel_id, upstream_model) VALUES (?, ?)
		ON CONFLICT(channel_id, upstream_model) DO NOTHING`, channelID, upstreamModel)
	return err
}

// SetChannelModelDisabled 停用/启用一个纳管模型。
func SetChannelModelDisabled(ctx context.Context, db Conn, id int64, disabled bool) error {
	res, err := db.ExecContext(ctx,
		`UPDATE channel_models SET disabled = ? WHERE id = ?`, boolInt(disabled), id)
	return affectedOne(res, err)
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

// APIKey 是管理端看到的一把网关 key。没有 key_hash——它虽然不是明文，但它是**唯一
// 的校验依据**，泄露等于把离线爆破的靶子交出去，而 key 是低熵的人造串。
type APIKey struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	AllowedModels string `json:"allowed_models"`
	Disabled      bool   `json:"disabled"`
	CreatedAt     string `json:"created_at"`
}

// ListAPIKeys 返回全部网关 key。
func ListAPIKeys(ctx context.Context, db Queryer) ([]APIKey, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, allowed_models, disabled, created_at FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.AllowedModels, &k.Disabled, &k.CreatedAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// CreateAPIKey 存一把新 key 的哈希。明文由调用方生成并**只回显一次**，这里不经手。
func CreateAPIKey(ctx context.Context, db Conn, name, keyHash, allowedModels string) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO api_keys (name, key_hash, allowed_models) VALUES (?, ?, ?)`,
		name, keyHash, allowedModels)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAPIKey 改名字、模型白名单与启用状态。改不了 key 本身——要换就删了重建，
// 因为服务端只有哈希，没法「在原 key 上改」。
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
	ID               int64  `json:"id"`
	CreatedAt        string `json:"created_at"`
	APIKeyName       string `json:"api_key_name"`
	ClientProtocol   string `json:"client_protocol"`
	UpstreamProtocol string `json:"upstream_protocol"`
	ModelRequested   string `json:"model_requested"`
	ModelUpstream    string `json:"model_upstream"`
	ChannelName      string `json:"channel_name"`
	Status           int    `json:"status"`
	RetryCount       int    `json:"retry_count"`
	TTFTMs           *int64 `json:"ttft_ms"`
	TotalMs          int64  `json:"total_ms"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	CacheReadTokens  *int64 `json:"cache_read_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
	Error            string `json:"error"`
}

// ListCallLogs 返回最近的调用流水，最新在前。limit 由调用方兜上限。
func ListCallLogs(ctx context.Context, db Queryer, limit, offset int) ([]CallLogRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, created_at, api_key_name, client_protocol, upstream_protocol,
		       model_requested, model_upstream, channel_name, status, retry_count,
		       ttft_ms, total_ms, input_tokens, output_tokens,
		       cache_read_tokens, cache_write_tokens, COALESCE(error, '')
		FROM call_logs ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallLogRow{}
	for rows.Next() {
		var r CallLogRow
		var ttft, in, outTok, cr, cw sql.NullInt64
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.APIKeyName, &r.ClientProtocol, &r.UpstreamProtocol,
			&r.ModelRequested, &r.ModelUpstream, &r.ChannelName, &r.Status, &r.RetryCount,
			&ttft, &r.TotalMs, &in, &outTok, &cr, &cw, &r.Error); err != nil {
			return nil, err
		}
		r.TTFTMs, r.InputTokens, r.OutputTokens = nullable(ttft), nullable(in), nullable(outTok)
		r.CacheReadTokens, r.CacheWriteTokens = nullable(cr), nullable(cw)
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageRow 是用量汇总的一行：按接入点聚合。
type UsageRow struct {
	ModelRequested string `json:"model_requested"`
	Calls          int64  `json:"calls"`
	Errors         int64  `json:"errors"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	CacheRead      int64  `json:"cache_read_tokens"`
	CacheWrite     int64  `json:"cache_write_tokens"`
}

// UsageByModel 汇总最近 days 天的用量。
//
// Errors 数的是 status >= 400 的行，包括上游自己回的 4xx——用量页要回答的是
// 「有多少次调用没拿到东西」，而不是「网关有没有出错」，两者对使用者是一回事。
func UsageByModel(ctx context.Context, db Queryer, days int) ([]UsageRow, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT model_requested,
		       COUNT(*),
		       SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0)
		FROM call_logs
		WHERE created_at >= datetime('now', '-%d days')
		GROUP BY model_requested ORDER BY COUNT(*) DESC`, days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.ModelRequested, &u.Calls, &u.Errors,
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
