package declcfg

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/store"
)

// Apply 把声明文件 reconcile 进库：**文件为准，库是它的物化副本**。
//
// 位置是 store.Open 之后、store.Validate 之前（口径层 §2.9 #30）。排这个顺序的理由
// **不是**「空库会被 Validate 拒」——Validate 那六项全是「捞出违规行」式检查，空库
// 全过；真正的理由是反过来的：apply 若排在 Validate 后面，Validate 校的就是一个还没
// 被文件填过的库，声明文件里的错误整个逃出闸外。
//
// 整份 apply 一个事务（#29）。两道闸不合并，但 problems 合并报（#31）——闸一是
// selfCheck（只看文件），闸二是 store.Validate（看库里的行）；闸二在**同一个事务里**
// 对着刚写进去的行跑，任一道有问题就整份回滚。于是「一次报全」不止覆盖文件自身的错，
// 也覆盖那些只有把行摆进库才看得出来的错，而库里一行都不会留下。
func Apply(ctx context.Context, db *sql.DB, f *File, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	if problems := selfCheck(f); len(problems) > 0 {
		// 闸一没过就不 apply，于是闸二无从跑起——它要看的行还不存在，硬跑只会拿上一版
		// 配置的行去回答这一版的问题。这不违背「合并报」：合并的前提是两边都算得出来。
		return problemsErr(problems)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启 apply 事务：%w", err)
	}
	defer tx.Rollback()

	var changes []string
	if changes, err = reconcile(ctx, tx, f); err != nil {
		return err
	}
	if err := store.Validate(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 apply 事务：%w", err)
	}
	// 增删改按实体名打进启动日志（口径层 §2.9 #29）。**不含任何秘密**：这里只写
	// 「哪个实体动了」，凭证值与 key 值一个字都不进来——纯转发形态下日志是唯一的
	// 观测出口，正因如此它更不能变成秘密的第二个副本。
	if len(changes) == 0 {
		log.Info("声明文件已生效，配置无变化")
		return nil
	}
	log.Info("声明文件已生效", "变更", strings.Join(changes, "；"))
	return nil
}

func problemsErr(problems []string) error {
	return fmt.Errorf("声明文件校验未通过：\n  - %s", strings.Join(problems, "\n  - "))
}

// reconcile 逐表对齐，返回按实体名描述的变更清单。
//
// 删按依赖顺序（口径层 §2.9 #29）：candidates 先清空——`candidates.channel_model_id`
// 那条外键**没有 ON DELETE CASCADE**（channels / access_points 那几条有），不先清它，
// 删一个渠道就会被外键顶回来。candidates 整表重建是安全的：它身上没有任何运行期状态
// （weight 是意图、由文件给），也没有第二张表引用它的 id。
func reconcile(ctx context.Context, tx *sql.Tx, f *File) ([]string, error) {
	var changes []string

	if _, err := tx.ExecContext(ctx, `DELETE FROM candidates`); err != nil {
		return nil, fmt.Errorf("清空候选：%w", err)
	}

	chIDs, chChanges, err := applyChannels(ctx, tx, f.Channels)
	if err != nil {
		return nil, err
	}
	changes = append(changes, chChanges...)

	modelIDs := map[string]int64{}
	for _, ch := range f.Channels {
		name := strings.TrimSpace(ch.Name)
		credChanges, err := applyCredentials(ctx, tx, name, chIDs[name], ch.Credentials)
		if err != nil {
			return nil, err
		}
		changes = append(changes, credChanges...)

		mChanges, err := applyModels(ctx, tx, name, chIDs[name], ch.Models, modelIDs)
		if err != nil {
			return nil, err
		}
		changes = append(changes, mChanges...)
	}

	apChanges, err := applyAccessPoints(ctx, tx, f.AccessPoints, modelIDs)
	if err != nil {
		return nil, err
	}
	changes = append(changes, apChanges...)

	keyChanges, err := applyAPIKeys(ctx, tx, f.APIKeys)
	if err != nil {
		return nil, err
	}
	return append(changes, keyChanges...), nil
}

// applyChannels upsert 渠道并删掉文件里没有的，返回 name → id。
//
// 按自然键 upsert 而不是 delete-all + insert（口径层 §2.9 #29）：整表重建会换掉
// channels.id，而 channel_keys 的停用现场挂在 channel_keys.id 上、靠 channel_id
// 串着——删父行 CASCADE 一走，那份现场就没了。
func applyChannels(ctx context.Context, tx *sql.Tx, list []Channel) (map[string]int64, []string, error) {
	before, err := namesOf(ctx, tx, `SELECT name FROM channels`)
	if err != nil {
		return nil, nil, err
	}
	ids := map[string]int64{}
	var changes []string
	keep := map[string]bool{}
	for _, ch := range list {
		name := strings.TrimSpace(ch.Name)
		keep[name] = true
		stateful := 1
		if ch.SupportsStatefulResponses != nil && !*ch.SupportsStatefulResponses {
			stateful = 0
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channels (name, base_url_openai, base_url_openai_responses, base_url_anthropic,
			                      credential_type, key_mode,
			                      max_concurrency, supports_compaction, supports_stateful_responses, disabled)
			VALUES (?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(name) DO UPDATE SET
			  base_url_openai = excluded.base_url_openai,
			  base_url_openai_responses = excluded.base_url_openai_responses,
			  base_url_anthropic = excluded.base_url_anthropic,
			  credential_type = excluded.credential_type,
			  key_mode = excluded.key_mode,
			  max_concurrency = excluded.max_concurrency,
			  supports_compaction = excluded.supports_compaction,
			  supports_stateful_responses = excluded.supports_stateful_responses,
			  disabled = excluded.disabled`,
			name, strings.TrimSpace(ch.BaseURL.OpenAI), strings.TrimSpace(ch.BaseURL.OpenAIResponses),
			strings.TrimSpace(ch.BaseURL.Anthropic),
			orDefault(ch.CredentialType, "api_key"), orDefault(ch.KeyMode, store.KeyModePolling),
			ch.MaxConcurrency, boolInt(ch.SupportsCompaction), stateful, boolInt(ch.Disabled),
		); err != nil {
			return nil, nil, fmt.Errorf("写入渠道 %q：%w", name, err)
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE name = ?`, name).Scan(&id); err != nil {
			return nil, nil, fmt.Errorf("读回渠道 %q 的 id：%w", name, err)
		}
		ids[name] = id
		if !before[name] {
			changes = append(changes, "新增渠道 "+name)
		}
	}
	gone, err := deleteMissing(ctx, tx, `DELETE FROM channels WHERE name = ?`, before, keep)
	if err != nil {
		return nil, nil, fmt.Errorf("删除渠道：%w", err)
	}
	for _, n := range gone {
		changes = append(changes, "删除渠道 "+n)
	}
	return ids, changes, nil
}

// applyCredentials 是全套 reconcile 里**唯一一处刻意只写一列**的地方。
//
// ON CONFLICT 只覆盖 credential，**不碰 disabled / disabled_reason / disabled_at**：
// 那三列整组是停用的运行期现场（口径层 §2.9 #28 ①），不进文件也就没有值可写。
// 若跟着别的表一起把 disabled 覆成 0，每次重启都会把停用的 key 推回轮询池——
// 那正是「必须 upsert、不能 delete-all + insert」要防的事，只是换了个更隐蔽的入口。
func applyCredentials(ctx context.Context, tx *sql.Tx, channel string, channelID int64, list []Credential) ([]string, error) {
	before, err := namesOf(ctx, tx, `SELECT name FROM channel_keys WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, err
	}
	var changes []string
	keep := map[string]bool{}
	for _, c := range list {
		name := strings.TrimSpace(c.Name)
		keep[name] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channel_keys (channel_id, name, credential) VALUES (?,?,?)
			ON CONFLICT(channel_id, name) DO UPDATE SET credential = excluded.credential`,
			channelID, name, strings.TrimSpace(c.Credential)); err != nil {
			return nil, fmt.Errorf("写入渠道 %q 的凭证 %q：%w", channel, name, err)
		}
		if !before[name] {
			changes = append(changes, fmt.Sprintf("新增凭证 %s/%s", channel, name))
		}
	}
	gone, err := deleteMissing(ctx, tx,
		`DELETE FROM channel_keys WHERE channel_id = ? AND name = ?`, before, keep, channelID)
	if err != nil {
		return nil, fmt.Errorf("删除渠道 %q 的凭证：%w", channel, err)
	}
	for _, n := range gone {
		changes = append(changes, fmt.Sprintf("删除凭证 %s/%s", channel, n))
	}
	return changes, nil
}

func applyModels(ctx context.Context, tx *sql.Tx, channel string, channelID int64, list []Model, ids map[string]int64) ([]string, error) {
	before, err := namesOf(ctx, tx, `SELECT upstream_model FROM channel_models WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, err
	}
	var changes []string
	keep := map[string]bool{}
	for _, m := range list {
		name := strings.TrimSpace(m.UpstreamModel)
		keep[name] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channel_models (channel_id, upstream_model, protocols, disabled) VALUES (?,?,?,?)
			ON CONFLICT(channel_id, upstream_model) DO UPDATE SET
			  protocols = excluded.protocols, disabled = excluded.disabled`,
			channelID, name, parseProtocols(m.Protocols), boolInt(m.Disabled)); err != nil {
			return nil, fmt.Errorf("写入渠道 %q 的纳管模型 %q：%w", channel, name, err)
		}
		var id int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM channel_models WHERE channel_id = ? AND upstream_model = ?`,
			channelID, name).Scan(&id); err != nil {
			return nil, fmt.Errorf("读回纳管模型 %q 的 id：%w", name, err)
		}
		ids[store.QualifiedName(channel, name)] = id
		if !before[name] {
			changes = append(changes, "新增纳管模型 "+store.QualifiedName(channel, name))
		}
	}
	gone, err := deleteMissing(ctx, tx,
		`DELETE FROM channel_models WHERE channel_id = ? AND upstream_model = ?`, before, keep, channelID)
	if err != nil {
		return nil, fmt.Errorf("删除渠道 %q 的纳管模型：%w", channel, err)
	}
	for _, n := range gone {
		changes = append(changes, "删除纳管模型 "+store.QualifiedName(channel, n))
	}
	return changes, nil
}

func applyAccessPoints(ctx context.Context, tx *sql.Tx, list []AccessPoint, modelIDs map[string]int64) ([]string, error) {
	before, err := namesOf(ctx, tx, `SELECT model FROM access_points`)
	if err != nil {
		return nil, err
	}
	var changes []string
	keep := map[string]bool{}
	for _, ap := range list {
		model := strings.TrimSpace(ap.Model)
		keep[model] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO access_points (model, disabled) VALUES (?,?)
			ON CONFLICT(model) DO UPDATE SET disabled = excluded.disabled`,
			model, boolInt(ap.Disabled)); err != nil {
			return nil, fmt.Errorf("写入接入点 %q：%w", model, err)
		}
		var apID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM access_points WHERE model = ?`, model).Scan(&apID); err != nil {
			return nil, fmt.Errorf("读回接入点 %q 的 id：%w", model, err)
		}
		for _, c := range ap.Candidates {
			target := strings.TrimSpace(c.Target)
			// weight 缺席跟 DDL 默认 100 走，显式写 0 就是 0——**0 是意图（临时摘除）
			// 不是缺席**，补默认值会让写 0 的人摘不掉（同 config.yaml 里 max_retries
			// 那个零值陷阱）。selfCheck 已经确认过 target 在文件里找得到。
			weight := 100
			if c.Weight != nil {
				weight = *c.Weight
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (?,?,?)`,
				apID, modelIDs[target], weight); err != nil {
				return nil, fmt.Errorf("写入接入点 %q 的候选 %q：%w", model, target, err)
			}
		}
		if !before[model] {
			changes = append(changes, "新增接入点 "+model)
		}
	}
	gone, err := deleteMissing(ctx, tx, `DELETE FROM access_points WHERE model = ?`, before, keep)
	if err != nil {
		return nil, fmt.Errorf("删除接入点：%w", err)
	}
	for _, n := range gone {
		changes = append(changes, "删除接入点 "+n)
	}
	return changes, nil
}

func applyAPIKeys(ctx context.Context, tx *sql.Tx, list []APIKey) ([]string, error) {
	before, err := namesOf(ctx, tx, `SELECT name FROM api_keys`)
	if err != nil {
		return nil, err
	}
	var changes []string
	keep := map[string]bool{}
	for _, k := range list {
		name := strings.TrimSpace(k.Name)
		keep[name] = true
		plain := strings.TrimSpace(k.Key)
		// 哈希与明文各存一列（口径层 v0.47）：鉴权走 key_hash 的唯一索引，key_plain
		// 是给人看的那一份。声明文件这条路上明文本来就在手上，两列都填得出来。
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_keys (name, key_hash, key_plain, allowed_models, disabled) VALUES (?,?,?,?,?)
			ON CONFLICT(name) DO UPDATE SET
			  key_hash = excluded.key_hash,
			  key_plain = excluded.key_plain,
			  allowed_models = excluded.allowed_models,
			  disabled = excluded.disabled`,
			name, auth.Hash(plain), plain, allowedModels(k.AllowedModels), boolInt(k.Disabled)); err != nil {
			return nil, fmt.Errorf("写入 API Key %q：%w", name, err)
		}
		if !before[name] {
			changes = append(changes, "新增 API Key "+name)
		}
	}
	gone, err := deleteMissing(ctx, tx, `DELETE FROM api_keys WHERE name = ?`, before, keep)
	if err != nil {
		return nil, fmt.Errorf("删除 API Key：%w", err)
	}
	for _, n := range gone {
		changes = append(changes, "删除 API Key "+n)
	}
	return changes, nil
}

// allowedModels 把白名单拼成 `api_keys.allowed_models` 那一列的形态：**逗号分隔**，
// 或者 `*` 表示不限。
//
// 逗号而不是 JSON：读这一列的是 `auth.Key.Allows`，它 `strings.Split(list, ",")`
// 逐段比。写成 JSON 数组的话每一段都带着引号和方括号，跟请求里那个裸模型名永远比不
// 上——一把限了模型的 key 会变成谁都放不过去的死 key，而闸全过、页面上也看着正常。
// 管理端与前端一直用的就是逗号（`Keys.tsx` 直接 `.split(',')` 显示），文件这条入口
// 得跟它们写进同一个格式。
//
// **不做存在性校验**（口径层 §2.9 #29）：白名单只在转发端生效，写一个还没建的接入点
// 名不是配置损坏——先写 key 后加模型是完全正常的顺序。名字里带逗号则由自校验拦下，
// 那种名字在这个格式里根本表达不出来。
func allowedModels(list []string) string {
	models := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s == "*" {
			return "*"
		} else if s != "" {
			models = append(models, s)
		}
	}
	if len(models) == 0 {
		return "*"
	}
	return strings.Join(models, ",")
}

func namesOf(ctx context.Context, tx *sql.Tx, query string, args ...any) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// deleteMissing 删掉 before 里有、keep 里没有的那些，返回被删的名字（已排序）。
//
// 排序不是为了好看：变更清单会进启动日志，而 map 遍历顺序每次都不同，不排的话同一份
// 文件两次启动打出两种顺序的日志，diff 起来全是噪音。
func deleteMissing(ctx context.Context, tx *sql.Tx, stmt string, before, keep map[string]bool, prefixArgs ...any) ([]string, error) {
	var gone []string
	for name := range before {
		if !keep[name] {
			gone = append(gone, name)
		}
	}
	sort.Strings(gone)
	for _, name := range gone {
		if _, err := tx.ExecContext(ctx, stmt, append(append([]any{}, prefixArgs...), name)...); err != nil {
			return nil, err
		}
	}
	return gone, nil
}

func orDefault(v, fallback string) string {
	if v = strings.TrimSpace(v); v == "" {
		return fallback
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
