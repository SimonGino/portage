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

// Candidate is the 候选 an 接入点 resolved to, carrying the connection details of
// the 渠道 it belongs to — everything a 透传 needs to reach the upstream.
type Candidate struct {
	AccessPointModel string
	UpstreamModel    string
	ChannelName      string
	Protocol         protocol.Protocol
	BaseURL          string
	Credential       string
}

// Resolve maps an 接入点 public model name to its single 候选.
//
// M0~M2 的临时闸保证每个接入点只有一个候选，因此这里不做加权抽取；多候选分流在 M4。
func Resolve(ctx context.Context, db *sql.DB, model string) (Candidate, error) {
	var apID int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM access_points WHERE model = ? AND disabled = 0`, model).Scan(&apID)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, ErrAccessPointNotFound
	}
	if err != nil {
		return Candidate{}, err
	}

	c := Candidate{AccessPointModel: model}
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

// AccessPoint is one 接入点 as the models list exposes it.
type AccessPoint struct {
	Model     string
	CreatedAt int64
}

// ListAccessPoints returns every enabled 接入点, in insertion order.
//
// created_at 在 SQL 里就换算成 unix 秒，免得依赖驱动对 DATETIME 文本的解析。手写
// SQL 塞进来的 created_at 未必是 strftime 认得的格式，那时它返回 NULL——COALESCE
// 兜住，免得一行脏数据把整个 /v1/models 打成 500。
func ListAccessPoints(ctx context.Context, db *sql.DB) ([]AccessPoint, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT model, COALESCE(CAST(strftime('%s', created_at) AS INTEGER), 0)
		FROM access_points WHERE disabled = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AccessPoint
	for rows.Next() {
		var ap AccessPoint
		if err := rows.Scan(&ap.Model, &ap.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ap)
	}
	return out, rows.Err()
}

// Validate is the startup gate. It reports every violation it finds, naming the
// offending record, so a hand-written SQL row can be fixed in one pass.
//
// 临时闸（M0~M2）：单候选、单凭证。多候选分流与凭证池聚合在 M4。
func Validate(ctx context.Context, db *sql.DB) error {
	var problems []string
	for _, check := range []func(context.Context, *sql.DB) ([]string, error){
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

func collect(ctx context.Context, db *sql.DB, query string, format func(*sql.Rows) (string, error)) ([]string, error) {
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

func checkSingleCandidate(ctx context.Context, db *sql.DB) ([]string, error) {
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

func checkSingleCredential(ctx context.Context, db *sql.DB) ([]string, error) {
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

func checkDanglingCandidate(ctx context.Context, db *sql.DB) ([]string, error) {
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

// checkCandidateReachable catches the half-finished state left by disabling a
// channel and forgetting its access point: checkSingleCandidate only counts
// candidates, so the access point still passes the gate, still shows up in
// /v1/models, and only fails at request time with a 503.
//
// 停用渠道时接入点要跟着停——这条把「跟着停」从口头约定变成启动就报。
func checkCandidateReachable(ctx context.Context, db *sql.DB) ([]string, error) {
	return collect(ctx, db, `
		SELECT ap.model, ch.id, ch.name, ch.disabled
		FROM candidates cd
		JOIN access_points ap  ON ap.id = cd.access_point_id AND ap.disabled = 0
		JOIN channel_models cm ON cm.id = cd.channel_model_id
		JOIN channels ch       ON ch.id = cm.channel_id
		WHERE cd.weight > 0
		  AND (ch.disabled <> 0
		       OR NOT EXISTS (SELECT 1 FROM channel_keys ck
		                       WHERE ck.channel_id = ch.id AND ck.disabled = 0))`,
		func(rows *sql.Rows) (string, error) {
			var chID int64
			var apModel, chName string
			var chDisabled bool
			if err := rows.Scan(&apModel, &chID, &chName, &chDisabled); err != nil {
				return "", err
			}
			reason := "该渠道没有启用凭证"
			if chDisabled {
				reason = "该渠道已停用"
			}
			return fmt.Sprintf("接入点 %q 的候选指向渠道 %q (id=%d)，但%s；接入点要跟着停用",
				apModel, chName, chID, reason), nil
		})
}

// checkChannelFields catches typos in hand-written SQL, which is how channels
// are maintained until the admin UI lands in M3.
func checkChannelFields(ctx context.Context, db *sql.DB) ([]string, error) {
	return collect(ctx, db, `
		SELECT id, name, protocol, credential_type
		FROM channels WHERE disabled = 0`,
		func(rows *sql.Rows) (string, error) {
			var id int64
			var name, proto, credType string
			if err := rows.Scan(&id, &name, &proto, &credType); err != nil {
				return "", err
			}
			switch {
			case !protocol.Protocol(proto).Valid():
				return fmt.Sprintf("渠道 %q (id=%d) 的 protocol=%q 不是 anthropic/openai_cc/openai_responses 之一", name, id, proto), nil
			case credType != "api_key":
				return fmt.Sprintf("渠道 %q (id=%d) 的 credential_type=%q，M0 只支持 api_key", name, id, credType), nil
			}
			return "", nil
		})
}
