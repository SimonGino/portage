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

	"github.com/SimonGino/ai-gateway/internal/protocol"

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
	return db, nil
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
	ChannelName   string
	Protocol      protocol.Protocol
	BaseURL       string
	Credential    string
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
func Resolve(ctx context.Context, db *sql.DB, model string) (Candidate, error) {
	c, err := resolveAccessPoint(ctx, db, model)
	// 只有「没有这个接入点」才继续试直连。接入点存在但候选不可用是另一回事，
	// 那时降级去试直连会把「候选停用了」报成「模型不存在」，把人引去查错地方。
	if !errors.Is(err, ErrAccessPointNotFound) {
		return c, err
	}
	return resolveDirect(ctx, db, model)
}

// resolveAccessPoint 走接入点路径。
//
// M0~M2 的临时闸保证每个接入点只有一个候选，因此这里不做加权抽取；多候选分流在 M4。
func resolveAccessPoint(ctx context.Context, db *sql.DB, model string) (Candidate, error) {
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
	err = db.QueryRowContext(ctx, `
		SELECT cm.upstream_model, ch.name, ch.protocol, ch.base_url, ck.credential
		FROM candidates cd
		JOIN channel_models cm ON cm.id = cd.channel_model_id AND cm.disabled = 0
		JOIN channels ch       ON ch.id = cm.channel_id       AND ch.disabled = 0
		JOIN channel_keys ck   ON ck.channel_id = ch.id       AND ck.disabled = 0
		WHERE cd.access_point_id = ? AND cd.weight > 0
		LIMIT 1`, apID).
		Scan(&c.UpstreamModel, &c.ChannelName, &c.Protocol, &c.BaseURL, &c.Credential)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, ErrNoUsableCandidate
	}
	if err != nil {
		return Candidate{}, err
	}
	return c, nil
}

// resolveDirect 走纳管模型直连路径，匹配 `渠道名/纳管模型名` 限定名。
//
// 拼接放在 SQL 里比在 Go 里按 `/` 切开更稳：渠道名和纳管模型名本身都可能含 `/`
// （`anthropic/claude-3` 这种 OpenRouter 风格的模型名很常见），切在哪一刀上没有
// 通用答案，而拼起来比对根本不用切。
func resolveDirect(ctx context.Context, db *sql.DB, model string) (Candidate, error) {
	c := Candidate{RequestedModel: model, Direct: true}
	err := db.QueryRowContext(ctx, `
		SELECT cm.upstream_model, ch.name, ch.protocol, ch.base_url, ck.credential
		FROM channel_models cm
		JOIN channels ch     ON ch.id = cm.channel_id
		JOIN channel_keys ck ON ck.channel_id = ch.id AND ck.disabled = 0
		WHERE ch.name || '/' || cm.upstream_model = ?
		  AND cm.disabled = 0 AND ch.disabled = 0
		LIMIT 1`, model).
		Scan(&c.UpstreamModel, &c.ChannelName, &c.Protocol, &c.BaseURL, &c.Credential)
	if err == nil {
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
// 直连那半边只列**当下真能打通**的——渠道启用、模型启用、渠道有启用凭证。列表与可
// 路由集合必须一致：harness 拉到清单就直接照着打，列一个必然 503 的名字等于给它挖
// 个坑。接入点那半边不做这层过滤，因为它归启动闸管（v0.18/v0.21），配置能起来就说
// 明它通。
//
// created_at 在 SQL 里就换算成 unix 秒，免得依赖驱动对 DATETIME 文本的解析。手写
// SQL 塞进来的 created_at 未必是 strftime 认得的格式，那时它返回 NULL——COALESCE
// 兜住，免得一行脏数据把整个 /v1/models 打成 500。
func ListExposedModels(ctx context.Context, db *sql.DB) ([]ExposedModel, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT model, COALESCE(CAST(strftime('%s', created_at) AS INTEGER), 0), 0 AS direct, id
		FROM access_points WHERE disabled = 0
		UNION ALL
		SELECT ch.name || '/' || cm.upstream_model,
		       COALESCE(CAST(strftime('%s', cm.created_at) AS INTEGER), 0), 1, cm.id
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
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.Direct, &id); err != nil {
			return nil, err
		}
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out, rows.Err()
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
// 临时闸（M0~M2）：单候选、单凭证。多候选分流与凭证池聚合在 M4。
func Validate(ctx context.Context, db Queryer) error {
	var problems []string
	for _, check := range []func(context.Context, Queryer) ([]string, error){
		checkSingleCandidate,
		checkSingleCredential,
		checkDanglingCandidate,
		checkCandidateReachable,
		checkChannelFields,
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

func checkSingleCredential(ctx context.Context, db Queryer) ([]string, error) {
	return collect(ctx, db, `
		SELECT ch.id, ch.name, COUNT(ck.id)
		FROM channels ch
		LEFT JOIN channel_keys ck ON ck.channel_id = ch.id AND ck.disabled = 0
		WHERE ch.disabled = 0
		GROUP BY ch.id
		HAVING COUNT(ck.id) <> 1`,
		func(rows *sql.Rows) (string, error) {
			var id, n int64
			var name string
			if err := rows.Scan(&id, &name, &n); err != nil {
				return "", err
			}
			return fmt.Sprintf("渠道 %q (id=%d) 有 %d 份启用凭证，临时闸要求恰好 1 份", name, id, n), nil
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
		SELECT id, name, protocol, credential_type, base_url
		FROM channels WHERE disabled = 0`,
		func(rows *sql.Rows) (string, error) {
			var id int64
			var name, proto, credType, baseURL string
			if err := rows.Scan(&id, &name, &proto, &credType, &baseURL); err != nil {
				return "", err
			}
			switch {
			case !protocol.Protocol(proto).Valid():
				return fmt.Sprintf("渠道 %q (id=%d) 的 protocol=%q 不是 anthropic/openai_cc/openai_responses 之一", name, id, proto), nil
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
