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

	"github.com/SimonGino/portage/internal/calllog"
)

// CredentialInfo 是管理端看到的一份凭证：名字、值、状态、时间。
type CredentialInfo struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Credential 是明文的上游 key（v0.47）。掩码在页面上做，不在这儿做——服务端
	// 掩码等于既发了值又发了个假的，两份都得维护。
	Credential string `json:"credential"`
	Disabled   bool   `json:"disabled"`
	// DisabledReason / DisabledAt 记这把为什么、何时被停用。停用与恢复都只有人工
	// 一条路（口径层 v0.95 去掉 401 自动摘除；老库里可能还留着当年自动摘除写的原因）。
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
// 的凭证，连同停用原因与时刻——那是「这把为什么不转了」的唯一记录。
//
// chatgpt_account 渠道的值是凭证 JSON（#211）：写侧过一遍形状校验，坏 JSON 进不了库——
// 进了也只是把「登录失败」堆到请求时才暴露。
func AddChannelCredentials(ctx context.Context, db Conn, channelID int64, items []NewCredential) error {
	if err := validateCredentialShape(ctx, db, channelID, items); err != nil {
		return err
	}
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

// validateCredentialShape 按渠道的凭证类型校验将要写进去的值（#211）：只有
// chatgpt_account 有形状约束（JSON，见 subcred.go）；其余类型任意非空串照收。
//
// 渠道不存在时静默放过——外键错误由紧随其后的 INSERT 当场报，不在这里枪毙。
func validateCredentialShape(ctx context.Context, db Queryer, channelID int64, items []NewCredential) error {
	var credType string
	err := db.QueryRowContext(ctx,
		`SELECT credential_type FROM channels WHERE id = ?`, channelID).Scan(&credType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if credType != CredentialTypeChatGPTAccount {
		return nil
	}
	for _, it := range items {
		if _, err := ParseChatGPTCredential(it.Value); err != nil {
			return InvalidInput{Reason: err.Error()}
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

// CredentialUpdate 是改一份凭证时可写的东西——一个 patch：指针 nil = 不动那一列。
//
// 三个写点各只碰一个字段（改名、换值、启停），让它们回传自己没编辑的字段等于
// 逼每个调用方替这一行背整份现场：名字框清空再点停用，就会被「凭证名不能为空」
// 拦下一次与名字无关的停用。Value 为空即**不动凭证值**（页面上根本读不到原值，
// 重贴就等于每次都换一把），它是唯一不用指针的：空串本来就不是合法的凭证值。
type CredentialUpdate struct {
	Name     *string
	Value    string
	Disabled *bool
}

// UpdateCredential 改名 / 换值 / 停用 / 启用。全 nil 是调用方的错，拒。
//
// 启用（Disabled=false）时顺手清掉停用原因与时刻：那两列描述的是「当下为什么停
// 着」，凭证恢复了还挂着一句停用原因只会误导下一个看日志的人。
//
// 换值先过形状校验（#211，同 AddChannelCredentials 那道闸）：chatgpt_account 渠道
// 的凭证值是 JSON，坏 JSON 换不进来。
func UpdateCredential(ctx context.Context, db Conn, id int64, in CredentialUpdate) error {
	if value := strings.TrimSpace(in.Value); value != "" {
		if err := validateCredentialValueByID(ctx, db, id, value); err != nil {
			return err
		}
	}
	var sets []string
	var args []any
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" {
			return InvalidInput{Reason: "凭证名不能为空"}
		}
		sets = append(sets, `name = ?`)
		args = append(args, name)
	}
	if value := strings.TrimSpace(in.Value); value != "" {
		sets = append(sets, `credential = ?`)
		args = append(args, value)
	}
	if in.Disabled != nil {
		if *in.Disabled {
			// COALESCE 而不是直接赋值：老库里这份凭证可能还带着 v0.95 之前 401 自动摘除
			// 写下的原因与时刻，人再点一次「停用」不该把那个现场抹成「人工停用」。
			sets = append(sets, `disabled = 1,
			          disabled_reason = COALESCE(disabled_reason, '人工停用'),
			          disabled_at     = COALESCE(disabled_at, CURRENT_TIMESTAMP)`)
		} else {
			sets = append(sets, `disabled = 0, disabled_reason = NULL, disabled_at = NULL`)
		}
	}
	if len(sets) == 0 {
		return InvalidInput{Reason: "没有要改的字段"}
	}
	args = append(args, id)
	res, err := db.ExecContext(ctx,
		`UPDATE channel_keys SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return credentialNameConflict(err, name)
	}
	return affectedOne(res, nil)
}

// validateCredentialValueByID 是 validateCredentialShape 的换值那半边：UpdateCredential
// 只拿凭证 id，渠道的类型要顺着外键找回去。凭证行不存在时静默放过——affectedOne
// 稍后会把「没这行」报出来，不在这里枪毙。
func validateCredentialValueByID(ctx context.Context, db Queryer, id int64, value string) error {
	var credType string
	err := db.QueryRowContext(ctx, `
		SELECT ch.credential_type FROM channel_keys ck
		JOIN channels ch ON ch.id = ck.channel_id WHERE ck.id = ?`, id).Scan(&credType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if credType != CredentialTypeChatGPTAccount {
		return nil
	}
	if _, err := ParseChatGPTCredential(value); err != nil {
		return InvalidInput{Reason: err.Error()}
	}
	return nil
}

// CredentialByID 取一份凭证（含已停用的）。订阅刷新引擎拿锁后的复查读它——「拿锁
// 后复查」要读到的是库里当下那份（refresh token 每刷一次轮换一次），不能拿进门前
// 调用方手里那份旧 JSON 再判一遍（#211）。
func CredentialByID(ctx context.Context, db Queryer, id int64) (CredentialInfo, error) {
	var c CredentialInfo
	err := db.QueryRowContext(ctx, `
		SELECT id, name, credential, disabled,
		       COALESCE(disabled_reason, ''), COALESCE(disabled_at, ''), created_at
		FROM channel_keys WHERE id = ?`, id).Scan(
		&c.ID, &c.Name, &c.Credential, &c.Disabled,
		&c.DisabledReason, &c.DisabledAt, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CredentialInfo{}, ErrNotFound
	}
	return c, err
}

// MarkCredentialReauth 把一份启用中的凭证标成「需要重新登录」的停用（口径层 §2.2
// v1.52：refresh 撞上死亡码即停用、reason 落 reauth_required——v0.95「任何状态码
// 都不改凭证状态」对凭证生命周期事件的唯一例外，那条管的是上游请求状态码）。
//
// 只翻启用中的行：等锁的这会儿被人工停用了的话，现场（原因、时刻）留给人写的那份，
// 网关不覆盖。值也同理（#211 复审）：expectValue 是引擎拿锁复读到的那份原文，
// 飞行中被管理端粘贴换掉时 CAS 落空——新粘的那份不背旧 refresh token 的死亡账。
// 停用原因与流水词 calllog.ReauthRequired 是同一个词、同一处定义。
func MarkCredentialReauth(ctx context.Context, db Conn, id int64, expectValue string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE channel_keys
		SET disabled = 1, disabled_reason = ?, disabled_at = CURRENT_TIMESTAMP
		WHERE id = ? AND disabled = 0 AND credential = ?`,
		calllog.ReauthRequired.String(), id, expectValue)
	return err
}

// RotateCredentialValue 是订阅引擎的轮换写回（#211）：newValue 只在库里的值仍是
// 引擎拿锁复读到的那份 oldRaw 时才落——凭证值在飞行中被管理端粘粘换掉（粘贴
// 迁移走的正是 UpdateCredential 那条路）时 affected 为 0，轮换作废、人粘的那份
// 作数；刚换出的 access token 本次照发，下次请求自会读新值。回 false 只是
// 「没落上」，不是错误。换值同 UpdateCredential 一道形状闸；不翻停用态：轮换
// 落不落与启用无关。
func RotateCredentialValue(ctx context.Context, db Conn, id int64, oldRaw, newValue string) (bool, error) {
	if err := validateCredentialValueByID(ctx, db, id, newValue); err != nil {
		return false, err
	}
	res, err := db.ExecContext(ctx, `
		UPDATE channel_keys SET credential = ?
		WHERE id = ? AND credential = ?`, newValue, id, oldRaw)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteCredential 删一份凭证。
//
// 已落库的流水不动：call_logs.channel_key_name 是**当时**的名字快照，不是外键，
// 删凭证不该让历史用量凭空消失（与 DeleteAPIKey 同一条理由）。
func DeleteCredential(ctx context.Context, db Conn, id int64) error {
	res, err := db.ExecContext(ctx, `DELETE FROM channel_keys WHERE id = ?`, id)
	return affectedOne(res, err)
}

// CredentialForRevocation 取一份凭证的值与它渠道的凭证类型（#212：删凭证前要做
// best-effort 撤销——只有 chatgpt_account 有撤销，且要用到值里的 refresh_token
// 与 client_id）。行不在了回 ErrNotFound，调用方跳过撤销、照删。
func CredentialForRevocation(ctx context.Context, db Queryer, id int64) (value, credentialType string, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT ck.credential, ch.credential_type FROM channel_keys ck
		JOIN channels ch ON ch.id = ck.channel_id WHERE ck.id = ?`, id).
		Scan(&value, &credentialType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return value, credentialType, err
}

// CredentialChannelID 取一份凭证属于哪个渠道（#212：login/start 带 credential_id
// 重新登录时，校验这把凭证就是这个渠道的行）。行不在了回 ErrNotFound。
func CredentialChannelID(ctx context.Context, db Queryer, id int64) (int64, error) {
	var channelID int64
	err := db.QueryRowContext(ctx, `SELECT channel_id FROM channel_keys WHERE id = ?`, id).Scan(&channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return channelID, err
}

// ChannelCredentialType 取渠道的凭证类型（#212：登录弹层按它切形态、只对订阅
// 渠道开放）。渠道不在了回 ErrNotFound。
func ChannelCredentialType(ctx context.Context, db Queryer, id int64) (string, error) {
	var t string
	err := db.QueryRowContext(ctx, `SELECT credential_type FROM channels WHERE id = ?`, id).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
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
