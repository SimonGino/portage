// Package store owns the SQLite database: schema creation, the startup
// configuration gate, and resolving an access point to the upstream it routes to.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/SimonGino/portage/internal/protocol"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var (
	// ErrAccessPointNotFound means no enabled access point exposes that model name.
	ErrAccessPointNotFound = errors.New("access point not found")
	// ErrNoUsableCandidate means the access point exists but its candidate,
	// channel or credential is disabled or missing.
	ErrNoUsableCandidate = errors.New("no usable candidate")
)

// Open opens (creating if absent) the SQLite database and applies the schema.
func Open(path string) (*sql.DB, error) {
	dsn := path + "?" + url.Values{
		"_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)"},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single writer avoids SQLITE_BUSY between concurrent relays.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate 补 `CREATE TABLE IF NOT EXISTS` 覆盖不到的形状变化。
//
// schema.sql 对已存在的表是空操作，所以列的增删改一律得在这儿补一手。不建版本表：
// 迁移是不是已经跑过，直接问库里的列长什么样就知道，比维护一个会和实际形状漂移的
// schema_version 更可信。
func migrate(db *sql.DB) error {
	// v0.33：单值 protocol → 支持协议集 protocols。只改列名，值不用动——旧的单值
	// 在新语义下就是一元集合，含义一字不变。
	old, err := hasColumn(db, "channels", "protocol")
	if err != nil {
		return fmt.Errorf("检查 channels.protocol: %w", err)
	}
	if old {
		if _, err := db.Exec(`ALTER TABLE channels RENAME COLUMN protocol TO protocols`); err != nil {
			return fmt.Errorf("迁移 channels.protocol → protocols: %w", err)
		}
	}
	if err := renameOpenAICC(db); err != nil {
		return err
	}
	if err := addCredentialNames(db); err != nil {
		return err
	}
	if err := addModelProtocols(db); err != nil {
		return err
	}
	if err := addKeyPlain(db); err != nil {
		return err
	}
	if err := addConcurrencyColumns(db); err != nil {
		return err
	}
	if err := addErrorDetail(db); err != nil {
		return err
	}
	if err := addIsStream(db); err != nil {
		return err
	}
	if err := addSupportsCompaction(db); err != nil {
		return err
	}
	if err := addUpstreamRequestID(db); err != nil {
		return err
	}
	return addReasoningTokens(db)
}

// addUpstreamRequestID 补 v0.56 的 call_logs.upstream_request_id（#37）。
//
// 不可空、默认空串：存量行一律读作「没有可用的 id」，与「上游没回这个头」同档——
// 这一列上把两者分开没有排障价值，理由见 schema.sql 的列注释。
func addUpstreamRequestID(db *sql.DB) error {
	has, err := hasColumn(db, "call_logs", "upstream_request_id")
	if err != nil {
		return fmt.Errorf("检查 call_logs.upstream_request_id: %w", err)
	}
	if has {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE call_logs ADD COLUMN upstream_request_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("迁移 call_logs.upstream_request_id: %w", err)
	}
	return nil
}

// addReasoningTokens 补 v0.66 的 call_logs.reasoning_tokens（#97）。
//
// **可空**，存量行落 NULL——与上一条正相反。那条的两种情况（没存过 / 上游没回）在
// 排障上是一回事，这条不是：NULL 是「不知道上游报没报」，0 是「上游报了，这次没
// 思考」。默认成 0 会让 Anthropic 渠道与迁移前的所有历史行都显示成确凿的零思考成本，
// 那正是口径层 v0.65「已发生的成本不得静默吞没」要防的静默。
func addReasoningTokens(db *sql.DB) error {
	has, err := hasColumn(db, "call_logs", "reasoning_tokens")
	if err != nil {
		return fmt.Errorf("检查 call_logs.reasoning_tokens: %w", err)
	}
	if has {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE call_logs ADD COLUMN reasoning_tokens INTEGER`); err != nil {
		return fmt.Errorf("迁移 call_logs.reasoning_tokens: %w", err)
	}
	return nil
}

// addIsStream 补 call_logs.is_stream（同步/流式）。
//
// 可空、不给默认值：stream 是解析请求体才知道的，存量行和鉴权失败那类行都停在
// NULL 上，读作「不知道」——补一个 0 默认值会把老流水全部说成同步。
func addIsStream(db *sql.DB) error {
	has, err := hasColumn(db, "call_logs", "is_stream")
	if err != nil {
		return fmt.Errorf("检查 call_logs.is_stream: %w", err)
	}
	if has {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE call_logs ADD COLUMN is_stream INTEGER`); err != nil {
		return fmt.Errorf("迁移 call_logs.is_stream: %w", err)
	}
	return nil
}

// addSupportsCompaction 补 v0.54 的 channels.supports_compaction。
//
// 默认 0（不支持），存量行一律停在 0 上——这是本批唯一一处**行为会变**的迁移：一个
// 今天真的支持压缩的 Responses 透传渠道，迁移后要到管理端把这一位勾上，压缩 turn 才
// 继续放行。选默认否是 PO 2026-08-13 的裁定，理由是代价不对称：位错成否 = 一条点名
// 「去渠道页勾上」的 400，位错成是 = 复现本票要杀掉的那个静默 Fatal。
func addSupportsCompaction(db *sql.DB) error {
	has, err := hasColumn(db, "channels", "supports_compaction")
	if err != nil {
		return fmt.Errorf("检查 channels.supports_compaction: %w", err)
	}
	if has {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE channels ADD COLUMN supports_compaction INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("迁移 channels.supports_compaction: %w", err)
	}
	return nil
}

// addErrorDetail 补 v0.53 的 call_logs.error_detail。
//
// 可空、不给默认值：存量行停在 NULL 上，读作「这行没存过原文」——它与「上游回了
// 4xx 但响应体是空的」（存空串）是两件事，补一个空串默认值会把这两件事抹平。
func addErrorDetail(db *sql.DB) error {
	has, err := hasColumn(db, "call_logs", "error_detail")
	if err != nil {
		return fmt.Errorf("检查 call_logs.error_detail: %w", err)
	}
	if has {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE call_logs ADD COLUMN error_detail TEXT`); err != nil {
		return fmt.Errorf("迁移 call_logs.error_detail: %w", err)
	}
	return nil
}

// addConcurrencyColumns 补渠道限流批（口径层 v0.49/v0.52）的两列：
// channels.max_concurrency 与 call_logs.queue_wait_ms。
//
// 两列的默认值恰好都是老库的既有语义——0 = 不限并发 / 没排过队——所以存量行不用
// 回填，迁移前后行为一字不变。
func addConcurrencyColumns(db *sql.DB) error {
	for _, m := range []struct{ table, col, ddl string }{
		{"channels", "max_concurrency", `ALTER TABLE channels ADD COLUMN max_concurrency INTEGER NOT NULL DEFAULT 0`},
		{"call_logs", "queue_wait_ms", `ALTER TABLE call_logs ADD COLUMN queue_wait_ms INTEGER NOT NULL DEFAULT 0`},
	} {
		has, err := hasColumn(db, m.table, m.col)
		if err != nil {
			return fmt.Errorf("检查 %s.%s: %w", m.table, m.col, err)
		}
		if has {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("迁移 %s.%s: %w", m.table, m.col, err)
		}
	}
	return nil
}

// addKeyPlain 补 v0.47 的 api_keys.key_plain。
//
// 默认空串，存量行就停在空串上——不是「还没回填」，是**回填不了**：老库里只有裸
// SHA-256，没有任何路径能还原出原值。读侧把空串读作「这把的原值没存过」，界面据此
// 提示删了重建，而不是显示一串假的掩码。
func addKeyPlain(db *sql.DB) error {
	has, err := hasColumn(db, "api_keys", "key_plain")
	if err != nil {
		return fmt.Errorf("检查 api_keys.key_plain: %w", err)
	}
	if has {
		return nil
	}
	if _, err := db.Exec(
		`ALTER TABLE api_keys ADD COLUMN key_plain TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("迁移 api_keys.key_plain: %w", err)
	}
	return nil
}

// addModelProtocols 补 v0.40 的 channel_models.protocols。
//
// 默认空串，而空串在读侧就是「继承渠道全集」——所以存量行不用回填，迁移前后行为
// 一字不变。这是这一列敢用 ALTER 加的前提：ALTER 加的列必须有默认值，而这里默认值
// 的语义恰好就是老库当下的语义。
func addModelProtocols(db *sql.DB) error {
	has, err := hasColumn(db, "channel_models", "protocols")
	if err != nil {
		return fmt.Errorf("检查 channel_models.protocols: %w", err)
	}
	if has {
		return nil
	}
	if _, err := db.Exec(
		`ALTER TABLE channel_models ADD COLUMN protocols TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("迁移 channel_models.protocols: %w", err)
	}
	return nil
}

// hasColumn 问库里某张表有没有这一列。迁移是否已跑过全靠它判断。
func hasColumn(db *sql.DB, table, col string) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n)
	return n > 0, err
}

// addCredentialNames 补 v0.38 的两列：channel_keys.name 与 call_logs.channel_key_name。
//
// 顺序不能换：先加列、再补名、最后才建唯一索引。索引建在 schema.sql 里是不行的——
// schema 在 migrate 之前跑，那时老库还没有 name 这一列，CREATE INDEX 当场失败。
// 放在这里则新老库走同一条路，长出同一个形状。
//
// 补名用的是「同渠道内 id 不大于我的有几个」这个相关子查询，而不是全局 id：名字是
// 给人看的，一个只有两把凭证的渠道里冒出「凭证 7」「凭证 12」比没有名字更难读。
// 只补名字为空串的行，所以它天然幂等——补过的不会被第二次改写。（这句别写成带一对
// 单引号的 SQL：gofmt 会把注释里的成对单引号换成中文引号，一格式化就把它改坏。）
func addCredentialNames(db *sql.DB) error {
	for _, m := range []struct{ table, col, ddl string }{
		{"channel_keys", "name", `ALTER TABLE channel_keys ADD COLUMN name TEXT NOT NULL DEFAULT ''`},
		{"call_logs", "channel_key_name", `ALTER TABLE call_logs ADD COLUMN channel_key_name TEXT NOT NULL DEFAULT ''`},
	} {
		has, err := hasColumn(db, m.table, m.col)
		if err != nil {
			return fmt.Errorf("检查 %s.%s: %w", m.table, m.col, err)
		}
		if has {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("迁移 %s.%s: %w", m.table, m.col, err)
		}
	}
	if _, err := db.Exec(`
		UPDATE channel_keys SET name = '凭证 ' || (
			SELECT COUNT(*) FROM channel_keys x
			WHERE x.channel_id = channel_keys.channel_id AND x.id <= channel_keys.id)
		WHERE name = ''`); err != nil {
		return fmt.Errorf("给存量凭证补名: %w", err)
	}
	if _, err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_keys_name
		ON channel_keys(channel_id, name)`); err != nil {
		return fmt.Errorf("建凭证名唯一索引: %w", err)
	}
	return nil
}

// renameOpenAICC 把 v0.36 之前写进库的 `openai_cc` 改写成 `openai`。
//
// 三张列都要改，不能只改 channels：call_logs 的两列存的是同一套取值，漏掉它用量页
// 会把同一个协议劈成两个名字分别聚合，而那是**历史数据**，之后再也没有机会补。
//
// REPLACE 而不是等值比较：channels.protocols 是逗号分隔的集合，`openai_cc` 可能夹在
// 中间。子串替换在这里是安全的——另外两个取值 `anthropic` / `openai_responses` 都不
// 含 `openai_cc`，不会误伤。
//
// 不设「跑过没有」的标记：改完之后库里再没有 `openai_cc`，第二次跑就是零行命中。
// 幂等本身就是它的守卫，比一张会跟实际形状漂移的版本表可信。
func renameOpenAICC(db *sql.DB) error {
	for _, stmt := range []string{
		`UPDATE channels  SET protocols = REPLACE(protocols, 'openai_cc', 'openai')
		   WHERE protocols LIKE '%openai_cc%'`,
		`UPDATE call_logs SET client_protocol = 'openai'   WHERE client_protocol = 'openai_cc'`,
		`UPDATE call_logs SET upstream_protocol = 'openai' WHERE upstream_protocol = 'openai_cc'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("迁移协议名 openai_cc → openai: %w", err)
		}
	}
	return nil
}

// Candidate is the 候选 a request's model name resolved to, carrying the
// connection details of the 渠道 it belongs to — everything a 透传 needs to reach
// the upstream.
type Candidate struct {
	// RequestedModel 是客户端在 `model` 字段里填的那个名字，接入点名或纳管模型
	// 限定名都可能。
	RequestedModel string
	// Direct 记这次走的是纳管模型直连（限定名）而不是接入点。
	Direct        bool
	UpstreamModel string
	// ChannelID 是并发闸（口径层 v0.49）按渠道聚合的 key。用 id 不用名字：渠道
	// 改名不该把在闸上排着的请求劈成两个池子。
	ChannelID   int64
	ChannelName string
	// MaxConcurrency 是渠道级 in-flight 并发上限（口径层 v0.49）：0 = 不限。
	MaxConcurrency int
	// SupportsCompaction 记这个渠道的上游认不认 Codex 的 compaction_trigger
	// （口径层 v0.54）。只在 Responses 透传那条路上被问到——转换路径上 trigger 到不了
	// 上游，与渠道能力无关。
	SupportsCompaction bool
	// Protocol 是**这次请求**选定的上游协议，不是渠道的全部能力——渠道支持协议集
	// （口径层 v0.33）在解析时就按入站协议收成了一个（见 pickProtocol）。下游拿它
	// 拼子路径、挑 codec、挑 tap，都只关心选定的这一个。
	Protocol protocol.Protocol
	BaseURL  string
	// Credentials 是该渠道当下全部**启用**凭证，按 id 升序（口径层 v0.38）。
	// 用哪一份、失败了换不换，由 upstream 的 key 层内环决定——store 只负责把候选
	// 集交出去，选取策略与轮询游标都不落在这一层。
	Credentials []Credential
	// KeyMode 是渠道级的选取模式：polling（默认）/ random。
	KeyMode string
}

// 渠道级的凭证选取模式（口径层 v0.11）。库里的默认值是 polling，认不得的取值一律
// 当 polling——这一列可以是手写 SQL 灌进来的，为一个拼错的模式名让请求失败不值当。
const (
	KeyModePolling = "polling"
	KeyModeRandom  = "random"
)

// Credential 是凭证池里的一份凭证。
//
// Name 是给人看的归因标识（渠道内唯一），会进日志与用量；Value 只在进程内流向
// upstream，永不进任何 JSON 响应——回读走的是管理端那条独立的 CredentialInfo
// （v0.47），热路径这个结构不该是它的出口。ID 是 401 摘除时要改的那一行。
type Credential struct {
	ID    int64
	Name  string
	Value string
}

// QualifiedName 是纳管模型对外的限定名：`渠道名/纳管模型名`。
//
// 渠道名全局唯一、`UNIQUE(channel_id, upstream_model)` 保证渠道内模型名唯一，两者
// 拼起来因此也唯一——这正是口径「两者都列、都可路由」能成立的前提：直连路径不需要
// 任何「重名了选谁」的规则，因为重名压根构造不出来。
func QualifiedName(channel, upstreamModel string) string {
	return channel + "/" + upstreamModel
}

// Resolve maps a client-supplied model name to the 候选 it routes to.
//
// 接入点优先：限定名带 `/`，而接入点名理论上也可以带，撞上时以接入点为准——接入点
// 是显式配出来的对外契约，纳管模型的限定名是自动派生的。
//
// inbound 是入站端点的协议，用来在渠道的支持协议集里选出这次走哪个（口径层 v0.33）。
func Resolve(ctx context.Context, db *sql.DB, model string, inbound protocol.Protocol) (Candidate, error) {
	c, err := resolveAccessPoint(ctx, db, model, inbound)
	// 只有「没有这个接入点」才继续试直连。接入点存在但候选不可用是另一回事，
	// 那时降级去试直连会把「候选停用了」报成「模型不存在」，把人引去查错地方。
	if !errors.Is(err, ErrAccessPointNotFound) {
		return c, err
	}
	return resolveDirect(ctx, db, model, inbound)
}

// usableProtocols 把库里那两列收成「这个纳管模型当下真能走的协议集」。
//
// channelRaw 是渠道的支持协议集，modelRaw 是纳管模型自己声明的子集（口径层 v0.40，
// 空串 = 继承渠道全集）。真正可用的是两者的交集——渠道会说 anthropic 不代表它下面
// 每个模型都在 `/v1/messages` 上存在，而那正是「渠道级探测全通、请求照样 404」的成因。
//
// **路由（pickProtocol）与 /v1/models 的直连清单（ListExposedModels）共用这一个函数**，
// 不是为了省几行：这两处各算各的交集，清单就会列出一个必然 503 的名字，而「列出来的
// 必须调得通」是口径层 v0.32 ③。v0.40 落地时正是漏了清单那一处。
//
// 三种失败分两档，不能混：
//
//   - 两列**解析**失败 → 500。启动闸的 checkChannelFields 扫的是全部未停用渠道，
//     这一列有问题的话进程根本起不来；真走到这儿说明库是在运行中被手写 SQL 改坏的，
//     报错让它回 500，别猜一个协议继续往上游发。
//   - 交集**为空** → ErrNoUsableCandidate（503）。这是合法配置下的正常收场，不是库
//     坏了：渠道协议集缩小之后，模型上那份没跟着改的子集就与它不再重合。它跟「渠道
//     停用」「凭证归零」是同一种「现在用不了」，报 500 会把人引去查数据损坏。
func usableProtocols(channelRaw, modelRaw string) (protocol.Set, error) {
	set, err := protocol.ParseSet(channelRaw)
	if err != nil {
		return nil, fmt.Errorf("渠道的 protocols 列不合法: %w", err)
	}
	// 空串走继承，不进 ParseSet——它对空输入是报错的（「支持协议集不能为空」），
	// 而这一列的空恰恰是最常见的正常值。
	if modelRaw == "" {
		return set, nil
	}
	sub, err := protocol.ParseSet(modelRaw)
	if err != nil {
		return nil, fmt.Errorf("纳管模型的 protocols 列不合法: %w", err)
	}
	if set = set.Intersect(sub); len(set) == 0 {
		return nil, fmt.Errorf(
			"纳管模型声明的协议 %q 与渠道的 %q 没有交集: %w",
			modelRaw, channelRaw, ErrNoUsableCandidate)
	}
	return set, nil
}

// pickProtocol 从可用协议集里定本次请求打上游哪一个。失败分档见 usableProtocols。
func pickProtocol(channelRaw, modelRaw string, inbound protocol.Protocol) (protocol.Protocol, error) {
	set, err := usableProtocols(channelRaw, modelRaw)
	if err != nil {
		return "", err
	}
	p, ok := set.Choose(inbound)
	if !ok {
		return "", fmt.Errorf("渠道的 protocols 列选不出协议: %q", channelRaw)
	}
	return p, nil
}

// resolveAccessPoint 走接入点路径。
//
// M0~M2 的临时闸保证每个接入点只有一个候选，因此这里不做加权抽取；多候选分流在 M4。
//
// **M4 改加权抽取时注意顺序**：模型协议子集与渠道集无交集的候选（v0.40）必须在抽取
// **之前**排除掉，不能像现在这样等 pickProtocol 抽完了才发现。只有一个候选时两者
// 等价，多候选下则不然——死候选会白占一份权重，把请求判死在一个本来有兄弟候选能
// 接的接入点上。
func resolveAccessPoint(ctx context.Context, db *sql.DB, model string, inbound protocol.Protocol) (Candidate, error) {
	var apID int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM access_points WHERE model = ? AND disabled = 0`, model).Scan(&apID)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, ErrAccessPointNotFound
	}
	if err != nil {
		return Candidate{}, err
	}

	c := Candidate{RequestedModel: model}
	var protocols, modelProtocols string
	err = db.QueryRowContext(ctx, `
		SELECT cm.upstream_model, ch.id, ch.name, ch.protocols, cm.protocols, ch.base_url, ch.key_mode, ch.max_concurrency, ch.supports_compaction
		FROM candidates cd
		JOIN channel_models cm ON cm.id = cd.channel_model_id AND cm.disabled = 0
		JOIN channels ch       ON ch.id = cm.channel_id       AND ch.disabled = 0
		WHERE cd.access_point_id = ? AND cd.weight > 0
		  AND EXISTS (SELECT 1 FROM channel_keys ck
		              WHERE ck.channel_id = ch.id AND ck.disabled = 0)
		LIMIT 1`, apID).
		Scan(&c.UpstreamModel, &c.ChannelID, &c.ChannelName, &protocols, &modelProtocols, &c.BaseURL, &c.KeyMode, &c.MaxConcurrency, &c.SupportsCompaction)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, ErrNoUsableCandidate
	}
	if err != nil {
		return Candidate{}, err
	}
	if c.Credentials, err = loadCredentials(ctx, db, c.ChannelID); err != nil {
		return Candidate{}, err
	}
	// EXISTS 与这一趟之间隔着一次 401 摘除的可能——那时凭证刚好归零，与「渠道没有
	// 启用凭证」是同一种收场，交给 503 而不是发一个不带凭证的请求。
	if len(c.Credentials) == 0 {
		return Candidate{}, ErrNoUsableCandidate
	}
	if c.Protocol, err = pickProtocol(protocols, modelProtocols, inbound); err != nil {
		return Candidate{}, fmt.Errorf("接入点 %q: %w", model, err)
	}
	return c, nil
}

// loadCredentials 取渠道当下全部启用凭证，按 id 升序。
//
// 顺序稳定是选取模式的前提：polling 靠它算「下一份是谁」，random 靠它有个确定的
// 洗牌输入。库里的行序不保证稳定，所以 ORDER BY 不能省。
func loadCredentials(ctx context.Context, db *sql.DB, channelID int64) ([]Credential, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, name, credential FROM channel_keys
		WHERE channel_id = ? AND disabled = 0 ORDER BY id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var cr Credential
		if err := rows.Scan(&cr.ID, &cr.Name, &cr.Value); err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// resolveDirect 走纳管模型直连路径，匹配 `渠道名/纳管模型名` 限定名。
//
// 拼接放在 SQL 里比在 Go 里按 `/` 切开更稳：纳管模型名本身常含 `/`
// （`anthropic/claude-3` 这种 OpenRouter 风格的很常见），切在哪一刀上没有通用
// 答案，而拼起来比对根本不用切。
//
// 唯一性由「渠道名不含 `/`」保证——那条在保存时校验、在启动闸复查。否则渠道 a
// 的模型 b/c 与渠道 a/b 的模型 c 会拼出同一个限定名，下面的 LIMIT 1 静默挑一个。
func resolveDirect(ctx context.Context, db *sql.DB, model string, inbound protocol.Protocol) (Candidate, error) {
	c := Candidate{RequestedModel: model, Direct: true}
	var protocols, modelProtocols string
	err := db.QueryRowContext(ctx, `
		SELECT cm.upstream_model, ch.id, ch.name, ch.protocols, cm.protocols, ch.base_url, ch.key_mode, ch.max_concurrency, ch.supports_compaction
		FROM channel_models cm
		JOIN channels ch ON ch.id = cm.channel_id
		WHERE ch.name || '/' || cm.upstream_model = ?
		  AND cm.disabled = 0 AND ch.disabled = 0
		  AND EXISTS (SELECT 1 FROM channel_keys ck
		              WHERE ck.channel_id = ch.id AND ck.disabled = 0)
		LIMIT 1`, model).
		Scan(&c.UpstreamModel, &c.ChannelID, &c.ChannelName, &protocols, &modelProtocols, &c.BaseURL, &c.KeyMode, &c.MaxConcurrency, &c.SupportsCompaction)
	if err == nil {
		if c.Credentials, err = loadCredentials(ctx, db, c.ChannelID); err != nil {
			return Candidate{}, err
		}
		if len(c.Credentials) == 0 {
			return Candidate{}, ErrNoUsableCandidate
		}
		if c.Protocol, err = pickProtocol(protocols, modelProtocols, inbound); err != nil {
			return Candidate{}, fmt.Errorf("纳管模型 %q: %w", model, err)
		}
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, err
	}

	// 分清「没有这个名字」和「有但现在用不了」。直连路径不进启动闸（它没有
	// candidates 行），停用渠道 / 停用模型 / 没有启用凭证只能在请求时才发现，
	// 一律报 404 会让人以为名字打错了。
	var exists int
	err = db.QueryRowContext(ctx, `
		SELECT 1 FROM channel_models cm
		JOIN channels ch ON ch.id = cm.channel_id
		WHERE ch.name || '/' || cm.upstream_model = ? LIMIT 1`, model).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, ErrAccessPointNotFound
	}
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{}, ErrNoUsableCandidate
}

// ExposedModel is one entry of `GET /v1/models`.
type ExposedModel struct {
	ID        string
	CreatedAt int64
	// Direct 区分这一条是接入点还是纳管模型限定名。对外的 OpenAI 格式里不体现，
	// 管理端和用例靠它分辨。
	Direct bool
}

// ListExposedModels returns everything the gateway can route to: 启用的接入点，
// 加上每个可用纳管模型的限定名（口径层 v0.32「两者都列、都可路由」）。
//
// 直连那半边只列**当下真能打通**的——渠道启用、模型启用、渠道有启用凭证，**且协议
// 集与渠道的有交集**（v0.38，口径层 v0.40）。列表与可路由集合必须一致：harness 拉到
// 清单就直接照着打，列一个必然 503 的名字等于给它挖个坑。判据走 usableProtocols，与
// pickProtocol 同一个函数——各算各的正是这一处漏过的原因。
//
// 解析失败的行也不列：那种行请求打过去会回 500（见 usableProtocols 的分档），同样不
// 属于「当下真能打通」。整个清单不因一行脏数据而 500，理由同下面的 COALESCE。
//
// **接入点那半边只对交集这一项过滤**（v0.38 二修）。它别的项不用管——渠道停用、模型
// 停用、凭证归零都归启动闸（v0.18/v0.21），配置能起来就说明它通；而交集为空**不进启动
// 闸**（口径层 v0.40 ②：它是运行期状态不是数据损坏），启动闸这一项兜不住，于是「列出来
// 的必须调得通」在这半边只能靠运行期过滤。口径层 v0.32 ③ 当初把过滤限定在直连半边，
// 给的理由正是「启动闸兜不住它」——同一条理由现在覆盖两边。
//
// created_at 在 SQL 里就换算成 unix 秒，免得依赖驱动对 DATETIME 文本的解析。手写
// SQL 塞进来的 created_at 未必是 strftime 认得的格式，那时它返回 NULL——COALESCE
// 兜住，免得一行脏数据把整个 /v1/models 打成 500。
func ListExposedModels(ctx context.Context, db *sql.DB) ([]ExposedModel, error) {
	dead, err := deadAccessPoints(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT model, COALESCE(CAST(strftime('%s', created_at) AS INTEGER), 0), 0 AS direct, id, '', ''
		FROM access_points WHERE disabled = 0
		UNION ALL
		SELECT ch.name || '/' || cm.upstream_model,
		       COALESCE(CAST(strftime('%s', cm.created_at) AS INTEGER), 0), 1, cm.id,
		       ch.protocols, cm.protocols
		FROM channel_models cm
		JOIN channels ch ON ch.id = cm.channel_id
		WHERE cm.disabled = 0 AND ch.disabled = 0
		  AND EXISTS (SELECT 1 FROM channel_keys ck
		              WHERE ck.channel_id = ch.id AND ck.disabled = 0)
		ORDER BY direct, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 接入点名理论上可以长得和某个限定名一模一样。ORDER BY direct 把接入点排在前面，
	// 于是这里先到先得就等于「接入点优先」——与 Resolve 的优先级一致，列表里那一条
	// 才指向请求真正会去的地方。
	seen := make(map[string]bool)
	var out []ExposedModel
	for rows.Next() {
		var m ExposedModel
		var id int64
		var channelProtocols, modelProtocols string
		if err := rows.Scan(
			&m.ID, &m.CreatedAt, &m.Direct, &id, &channelProtocols, &modelProtocols); err != nil {
			return nil, err
		}
		if m.Direct {
			if _, err := usableProtocols(channelProtocols, modelProtocols); err != nil {
				continue
			}
		} else if dead[id] {
			continue
		}
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out, rows.Err()
}

// deadAccessPoints 找出「候选一个都活不了」的接入点——它们的每个候选，协议子集与所在
// 渠道的支持协议集都没有交集，于是打过去必 503。
//
// **判据是「有没有一个候选活着」而不是「有没有一个候选死了」**：M4 放开多候选之后，
// 一个死候选不该把整个接入点从清单上抹掉，它还有兄弟候选能接。M0~M2 临时闸下每个接入
// 点只有一个候选，两种写法等价，但等价的时候正是把它写对的时候。
//
// 一个候选都没有的接入点不在返回的集合里（照列）：那种形状启动闸本来就拒（
// checkSingleCandidate），这里不替它改判。
func deadAccessPoints(ctx context.Context, db *sql.DB) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT cd.access_point_id, ch.protocols, cm.protocols
		FROM candidates cd
		JOIN channel_models cm ON cm.id = cd.channel_model_id
		JOIN channels ch       ON ch.id = cm.channel_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	alive, dead := make(map[int64]bool), make(map[int64]bool)
	for rows.Next() {
		var apID int64
		var channelProtocols, modelProtocols string
		if err := rows.Scan(&apID, &channelProtocols, &modelProtocols); err != nil {
			return nil, err
		}
		if _, err := usableProtocols(channelProtocols, modelProtocols); err == nil {
			alive[apID] = true
		} else {
			dead[apID] = true
		}
	}
	for id := range alive {
		delete(dead, id)
	}
	return dead, rows.Err()
}

// Queryer 是 *sql.DB 与 *sql.Tx 的公共只读面。
//
// Validate 收它而不是 *sql.DB，是为了能在**尚未提交的事务里**跑：管理端每次写完
// 都要先自校验再决定提交还是回滚（M3）。而 Open 把连接池设成了 1，事务开着的时候
// 拿 *sql.DB 再查一次会等一条永远回不来的连接——自锁，不是报错，表现是管理端
// 保存请求直接挂住。
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Validate is the startup gate. It reports every violation it finds, naming the
// offending record, so a hand-written SQL row can be fixed in one pass.
//
// 临时闸只剩单候选那一半（口径层 v0.38）：多候选分流仍在 M4，凭证池聚合已前移
// 到 M3，于是凭证只剩「至少一份」这条可达性下限。
func Validate(ctx context.Context, db Queryer) error {
	var problems []string
	for _, check := range []func(context.Context, Queryer) ([]string, error){
		checkSingleCandidate,
		checkChannelHasCredential,
		checkDanglingCandidate,
		checkCandidateReachable,
		checkChannelFields,
		checkModelProtocols,
	} {
		found, err := check(ctx, db)
		if err != nil {
			return err
		}
		problems = append(problems, found...)
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("配置校验未通过：\n  - %s", strings.Join(problems, "\n  - "))
}

func collect(ctx context.Context, db Queryer, query string, format func(*sql.Rows) (string, error)) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		msg, err := format(rows)
		if err != nil {
			return nil, err
		}
		if msg != "" {
			out = append(out, msg)
		}
	}
	return out, rows.Err()
}

func checkSingleCandidate(ctx context.Context, db Queryer) ([]string, error) {
	return collect(ctx, db, `
		SELECT ap.id, ap.model, COUNT(cd.id)
		FROM access_points ap
		LEFT JOIN candidates cd ON cd.access_point_id = ap.id AND cd.weight > 0
		WHERE ap.disabled = 0
		GROUP BY ap.id
		HAVING COUNT(cd.id) <> 1`,
		func(rows *sql.Rows) (string, error) {
			var id, n int64
			var model string
			if err := rows.Scan(&id, &model, &n); err != nil {
				return "", err
			}
			return fmt.Sprintf("接入点 %q (id=%d) 有 %d 个 weight>0 的候选，临时闸要求恰好 1 个", model, id, n), nil
		})
}

// checkChannelHasCredential 要求每个未停用渠道至少有一份启用凭证。
//
// 上限那一半（临时闸的「恰好 1 份」）已于口径层 v0.38 放开，凭证池聚合前移到 M3；
// 下限留着，它不是临时闸而是 v0.18 的可达性通则：零凭证的渠道每个请求都会失败，
// 而这是启动时就判定得了的。
func checkChannelHasCredential(ctx context.Context, db Queryer) ([]string, error) {
	return collect(ctx, db, `
		SELECT ch.id, ch.name
		FROM channels ch
		LEFT JOIN channel_keys ck ON ck.channel_id = ch.id AND ck.disabled = 0
		WHERE ch.disabled = 0
		GROUP BY ch.id
		HAVING COUNT(ck.id) = 0`,
		func(rows *sql.Rows) (string, error) {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return "", err
			}
			return fmt.Sprintf("渠道 %q (id=%d) 没有启用凭证；补一份，或者把渠道停用", name, id), nil
		})
}

func checkDanglingCandidate(ctx context.Context, db Queryer) ([]string, error) {
	return collect(ctx, db, `
		SELECT cd.id, ap.model
		FROM candidates cd
		JOIN access_points ap    ON ap.id = cd.access_point_id
		LEFT JOIN channel_models cm ON cm.id = cd.channel_model_id
		LEFT JOIN channels ch       ON ch.id = cm.channel_id
		WHERE cm.id IS NULL OR ch.id IS NULL`,
		func(rows *sql.Rows) (string, error) {
			var id int64
			var model string
			if err := rows.Scan(&id, &model); err != nil {
				return "", err
			}
			return fmt.Sprintf("接入点 %q 的候选 (id=%d) 引用了不存在的纳管模型或渠道", model, id), nil
		})
}

// checkCandidateReachable catches the half-finished states left by disabling
// something and forgetting the access point in front of it: checkSingleCandidate
// only counts candidates, so the access point still passes the gate, still shows
// up in /v1/models, and only fails at request time with a 503.
//
// 判定条件与 Resolve 的 JOIN 逐条对齐——渠道、纳管模型、凭证三者任一停用，Resolve
// 就取不到候选。少对齐一条，那一种写法就会漏到 503 才暴露。
func checkCandidateReachable(ctx context.Context, db Queryer) ([]string, error) {
	return collect(ctx, db, `
		SELECT ap.id, ap.model, ch.name, ch.id, cm.upstream_model, ch.disabled, cm.disabled
		FROM candidates cd
		JOIN access_points ap  ON ap.id = cd.access_point_id AND ap.disabled = 0
		JOIN channel_models cm ON cm.id = cd.channel_model_id
		JOIN channels ch       ON ch.id = cm.channel_id
		WHERE cd.weight > 0
		  AND (ch.disabled <> 0 OR cm.disabled <> 0
		       OR NOT EXISTS (SELECT 1 FROM channel_keys ck
		                       WHERE ck.channel_id = ch.id AND ck.disabled = 0))`,
		func(rows *sql.Rows) (string, error) {
			var apID, chID int64
			var apModel, chName, upstreamModel string
			var chDisabled, cmDisabled bool
			if err := rows.Scan(&apID, &apModel, &chName, &chID, &upstreamModel, &chDisabled, &cmDisabled); err != nil {
				return "", err
			}
			// 补救建议随原因走：渠道还开着时正解是补凭证，不是把接入点也停掉。
			reason, remedy := "该渠道没有启用凭证", "补一份启用凭证，或把渠道与接入点一起停用"
			switch {
			case chDisabled:
				reason, remedy = "该渠道已停用", "接入点要跟着停用"
			case cmDisabled:
				reason, remedy = "该纳管模型已停用", "接入点要跟着停用"
			}
			return fmt.Sprintf("接入点 %q (id=%d) 的候选指向渠道 %q (id=%d) 的纳管模型 %q，但%s；%s",
				apModel, apID, chName, chID, upstreamModel, reason, remedy), nil
		})
}

// checkChannelFields catches typos in hand-written SQL, which is how channels
// are maintained until the admin UI lands in M3.
func checkChannelFields(ctx context.Context, db Queryer) ([]string, error) {
	return collect(ctx, db, `
		SELECT id, name, protocols, credential_type, base_url
		FROM channels WHERE disabled = 0`,
		func(rows *sql.Rows) (string, error) {
			var id int64
			var name, protocols, credType, baseURL string
			if err := rows.Scan(&id, &name, &protocols, &credType, &baseURL); err != nil {
				return "", err
			}
			// 渠道名不能含 `/`：限定名 `渠道名/纳管模型名` 是拼起来比对的，两边都
			// 允许 `/` 的话 `a/b/c` 有两种拆法，直连路径的 LIMIT 1 会静默挑一个——
			// 这正是 v0.21 通则说的「静态就能判定不可能对」的配置。
			if strings.Contains(name, "/") {
				return fmt.Sprintf("渠道 %q (id=%d) 的名字含 `/`，会让限定名 `渠道名/纳管模型名` 产生歧义"+
					"（纳管模型名本身常带 `/`）；改个不带 `/` 的渠道名", name, id), nil
			}
			// 支持协议集非空且逐项合法（v0.33）。这一列是逗号分隔的集合，不再是单值：
			// 空集合的渠道选不出出站协议，每次请求才 500——正是 v0.21 通则要拦的形态。
			if _, err := protocol.ParseSet(protocols); err != nil {
				return fmt.Sprintf("渠道 %q (id=%d) 的 protocols=%q 不合法：%v（逗号分隔，取值 anthropic/openai/openai_responses）",
					name, id, protocols, err), nil
			}
			switch {
			case credType != "api_key":
				return fmt.Sprintf("渠道 %q (id=%d) 的 credential_type=%q，M0 只支持 api_key", name, id, credType), nil
			}
			// 只报「哪里不对」，不回显 base_url 本身——它可能带 userinfo，
			// 那就是把上游密码打进 stderr（CLAUDE.md：错误回显严禁泄露 base_url）。
			if why := badBaseURL(baseURL); why != "" {
				return fmt.Sprintf("渠道 %q (id=%d) 的 base_url %s；它要填到「协议子路径之前」，"+
					"例如 https://api.anthropic.com。按不泄露上游地址的约定这里不回显实际值，"+
					"请查 channels 表核对", name, id, why), nil
			}
			return "", nil
		})
}

// checkModelProtocols 扫纳管模型那一列协议子集（v0.38，口径层 v0.40）。
//
// 拦的只有「**值本身不合法**」——空串是继承渠道全集（最常见的正常值），交集为空则
// **刻意不拦**：那是运行期状态不是数据损坏，口径层 v0.40 ② 明确把它挡在启动闸外，
// 拦了等于让「渠道少勾一个协议」把整个进程掀翻。
//
// 管理端写这一列时已经过 ParseSet，所以不合法的值只能来自手写 SQL 或导入的库。但
// 那正是 v0.21 通则要拦的形态：不拦的话进程照常起来，第一个打到这个模型的请求才
// 500，而管理端把解不动的值显示成「继承」（ListChannels 吞掉解析错误），页面上根本
// 看不出哪里不对。
func checkModelProtocols(ctx context.Context, db Queryer) ([]string, error) {
	return collect(ctx, db, `
		SELECT cm.id, cm.upstream_model, cm.protocols, ch.name
		FROM channel_models cm
		JOIN channels ch ON ch.id = cm.channel_id
		WHERE cm.disabled = 0 AND ch.disabled = 0 AND cm.protocols <> ''`,
		func(rows *sql.Rows) (string, error) {
			var id int64
			var model, protocols, channel string
			if err := rows.Scan(&id, &model, &protocols, &channel); err != nil {
				return "", err
			}
			if _, err := protocol.ParseSet(protocols); err != nil {
				return fmt.Sprintf("渠道 %q 的纳管模型 %q (id=%d) 的 protocols=%q 不合法：%v"+
					"（逗号分隔，取值 anthropic/openai/openai_responses；留空表示继承渠道全集）",
					channel, model, id, protocols, err), nil
			}
			return "", nil
		})
}

// badBaseURL 说明 base_url 为什么拼不出一个能用的上游地址；能用则返回空串。
//
// schema 只要求非 NULL，而配置是手写 SQL 灌进来的：空串、漏了 scheme 的
// api.anthropic.com、ftp:// 全都存得进去。这类配置过得了校验、接入点照常挂在
// /v1/models 上，每次请求才在 http.Client.Do 里失败回 502——正是口径层 v0.18
// 判过的「配置能过校验但请求时才炸」，v0.21 已把它升为通则。
//
// 查询串与 fragment 单独拦：buildURL 是字符串拼接，`https://h/p?x=1` 接上
// /v1/messages 之后 Go 解出来是 path=/p、query=x=1/v1/messages——协议子路径被
// 整个吞进查询串，请求永远打到 /p 上，而且启动、日志、响应全都看不出异常。
func badBaseURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "无法解析为 URL"
	case u.Scheme != "http" && u.Scheme != "https":
		return "的 scheme 必须是 http 或 https"
	case u.Host == "":
		return "缺少 host"
	case u.RawQuery != "" || u.ForceQuery:
		return "不能带查询串——协议子路径会被拼到 ? 之后，被整个吞进查询串"
	case u.Fragment != "":
		return "不能带 fragment——协议子路径会被拼到 # 之后，不会进入请求路径"
	}
	return ""
}
