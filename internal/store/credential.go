package store

// credential.go 是渠道凭证池的读写面（口径层 v0.38 把它从 M4 前移到 M3）。
//
// **凭证值可回读**（v0.47 推翻 v0.28 的「只写不回读」与 v0.38 的「不派生显示字符」）。
// PO 裁定：管理端要能看见、能复制，否则「这把到底是哪一把」在页面上没有任何直观表达。
// 值原本就是明文存库的，所以这一版只是把它读出来，没有降低任何既有强度。
//
// 仍然成立的一条：**名字才是归因依据**。日志与用量按名字认凭证，名字渠道内唯一——
// 两行都叫「主号」就废掉了归因本身。回读只是让人对得上号，不是让别处改用值来指代它。
//
// 注意这跟错误回显那条纪律不冲突：上游 key 与 base_url 一律不进错误信息（CLAUDE.md），
// 那条管的是**转发链路吐给客户端的东西**，跟管理端登录后自己看自己的配置是两码事。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// CredentialInfo 是管理端看到的一份凭证：名字、值、状态、时间。
type CredentialInfo struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Credential 是明文的上游 key（v0.47）。掩码在页面上做，不在这儿做——服务端
	// 掩码等于既发了值又发了个假的，两份都得维护。
	Credential string `json:"credential"`
	Disabled   bool   `json:"disabled"`
	// DisabledReason / DisabledAt 是 401 摘除的现场（口径层 v0.38 只摘 401）。
	// 恢复是纯人工的，所以这两项就是「这把为什么不转了」的唯一记录。
	DisabledReason string `json:"disabled_reason"`
	DisabledAt     string `json:"disabled_at"`
	CreatedAt      string `json:"created_at"`
}

// ListChannelCredentials 列一个渠道的全部凭证，含已停用的——停用的那些正是要看
// 原因、要人工恢复的那些。
func ListChannelCredentials(ctx context.Context, db Queryer, channelID int64) ([]CredentialInfo, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, name, credential, disabled,
		       COALESCE(disabled_reason, ''), COALESCE(disabled_at, ''), created_at
		FROM channel_keys WHERE channel_id = ? ORDER BY id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CredentialInfo{}
	for rows.Next() {
		var c CredentialInfo
		if err := rows.Scan(&c.ID, &c.Name, &c.Credential, &c.Disabled,
			&c.DisabledReason, &c.DisabledAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NewCredential 是要加进池子的一份凭证。Name 为空时由 defaultCredentialName 给一个
// 渠道内不重名的 `凭证 N`。
type NewCredential struct {
	Name  string
	Value string
}

// AddChannelCredentials 往渠道的凭证池里**追加**若干份。
//
// 语义是追加而不是整把替换（口径层 v0.38 改写 v0.28 的写入形态）。v0.47 让值可回读之后
// 「页面上对不齐」那半条理由没了，但另半条还在，而且是决定性的：覆盖会连带清掉已停用
// 的凭证，那是 401 摘除的现场，也是「这把为什么不转了」的唯一记录。
func AddChannelCredentials(ctx context.Context, db Conn, channelID int64, items []NewCredential) error {
	for _, it := range items {
		value := strings.TrimSpace(it.Value)
		if value == "" {
			return InvalidInput{Reason: "凭证不能为空"}
		}
		name := strings.TrimSpace(it.Name)
		if name == "" {
			var err error
			if name, err = defaultCredentialName(ctx, db, channelID); err != nil {
				return err
			}
		}
		// channel_id 指向不存在的渠道时这一句会报外键错误——Open 里 PRAGMA
		// foreign_keys(1) 是开着的，所以这里不需要再自己查一次渠道在不在。
		if _, err := db.ExecContext(ctx,
			`INSERT INTO channel_keys (channel_id, name, credential) VALUES (?, ?, ?)`,
			channelID, name, value); err != nil {
			return credentialNameConflict(err, name)
		}
	}
	return nil
}

// defaultCredentialName 给一个渠道内不重名的 `凭证 N`。
//
// 从「现有份数 + 1」开始往上找而不是直接用它：删过凭证之后份数会和最大编号对不上，
// 那时 `凭证 2` 可能已经被占着。循环有上界，纯防呆——真有人手写了一万个同名前缀，
// 报错也比死循环强。
func defaultCredentialName(ctx context.Context, db Conn, channelID int64) (string, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM channel_keys WHERE channel_id = ?`, channelID).Scan(&n); err != nil {
		return "", err
	}
	for i := n + 1; i <= n+1000; i++ {
		name := fmt.Sprintf("凭证 %d", i)
		var exists int
		err := db.QueryRowContext(ctx,
			`SELECT 1 FROM channel_keys WHERE channel_id = ? AND name = ?`, channelID, name).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", InvalidInput{Reason: "自动起名失败，请自己给这份凭证起个名字"}
}

// CredentialUpdate 是改一份凭证时可写的东西。
//
// Value 为空即**不动凭证值**——改名和停用是最常见的两种改动，让它们必须重贴一遍
// key 是荒唐的（页面上根本读不到原值，重贴就等于每次都换一把）。
type CredentialUpdate struct {
	Name     string
	Value    string
	Disabled bool
}

// UpdateCredential 改名 / 换值 / 停用 / 启用。
//
// 启用（Disabled=false）时顺手清掉停用原因与时刻：那两列描述的是「当下为什么停
// 着」，留着一个已经恢复的凭证挂着一句「上游回 401」只会误导下一个看日志的人。
// 这也正是口径层 v0.38 说的「只人工恢复」——恢复的动作只有这一个。
func UpdateCredential(ctx context.Context, db Conn, id int64, in CredentialUpdate) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return InvalidInput{Reason: "凭证名不能为空"}
	}
	// COALESCE 而不是直接赋值：这份凭证可能是被 401 自动摘掉的，人再点一次「停用」
	// 不该把那个原因与时刻抹成「人工停用」——现场比动作重要。
	state := `disabled = 1,
	          disabled_reason = COALESCE(disabled_reason, '人工停用'),
	          disabled_at     = COALESCE(disabled_at, CURRENT_TIMESTAMP)`
	if !in.Disabled {
		state = `disabled = 0, disabled_reason = NULL, disabled_at = NULL`
	}
	args := []any{name}
	set := `name = ?`
	if value := strings.TrimSpace(in.Value); value != "" {
		set += `, credential = ?`
		args = append(args, value)
	}
	args = append(args, id)
	res, err := db.ExecContext(ctx,
		`UPDATE channel_keys SET `+set+`, `+state+` WHERE id = ?`, args...)
	if err != nil {
		return credentialNameConflict(err, name)
	}
	return affectedOne(res, nil)
}

// DeleteCredential 删一份凭证。
//
// 已落库的流水不动：call_logs.channel_key_name 是**当时**的名字快照，不是外键，
// 删凭证不该让历史用量凭空消失（与 DeleteAPIKey 同一条理由）。
func DeleteCredential(ctx context.Context, db Conn, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM channel_keys WHERE id = ?`, id)
	return affectedOne(res, err)
}

// DisableCredential 摘掉一份凭证，记下原因与时刻。转发端在收到 401 时调它
// （口径层 v0.38：**只有 401 摘**，403 换而不摘，429 换而不摘也不冷却）。
//
// 只在还启用着的时候写，这样 disabled_at 记的是**第一次**被摘的时刻，不会被后续
// 并发进来的请求一次次刷新。
func DisableCredential(ctx context.Context, db Conn, id int64, reason string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE channel_keys
		SET disabled = 1, disabled_reason = ?, disabled_at = CURRENT_TIMESTAMP
		WHERE id = ? AND disabled = 0`, reason, id)
	return err
}

// credentialNameConflict 把唯一索引的冲突翻成一句人话。
//
// 不靠上层那条通用的「名称重复，或引用了不存在的渠道/模型」：凭证名重复是这里最
// 常见的失败，而那句话既没点名是哪一个，也没说清楚重名的范围是「同一个渠道内」。
func credentialNameConflict(err error, name string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE") && strings.Contains(err.Error(), "channel_keys") {
		return InvalidInput{Reason: fmt.Sprintf("这个渠道里已经有一份叫 %q 的凭证了，换个名字——"+
			"名字是日志与用量里认凭证的唯一依据，重名等于没名字", name)}
	}
	return err
}
