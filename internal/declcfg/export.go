package declcfg

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/SimonGino/portage/internal/store"
)

// header 是导出物顶上那段固定说明。
//
// **固定**是硬要求，不是风格：往返闸要的是 `export → 空库 apply → export` 字节相等，
// 而注释每次都要一模一样地再生成一遍才对得上。所以这里没有时间戳、没有版本号、没有
// 主机名——任何「这次导出跟上次不同」的东西都会把那道闸变成永远红的。
const header = `# 由 portage 管理端导出（口径层 §2.9）。这份文件是业务配置的**唯一事实源**：
# 挂上它（-channels 或 PORTAGE_CHANNELS）之后，启动时它会被 apply 进 DB，管理端对业务
# 配置全只读；文件里没有的实体会被删掉。
#
# ⚠ 里面是**明文秘密**：上游凭证与 API Key 都是原值。打了码的文件部署不了，而部署正是
# 它存在的全部理由。所以——权限给 0600，且**永远不要提交进 git**（.gitignore 里已有
# channels.yaml 那一行）。
#
# 改配置的路子是：在本地那台带 UI 的实例上改 → 重新导出 → 重新部署 → 重启。两台之间
# 没有自动同步。
`

// Export 从库读出全部业务配置，序列化成一份声明文件。
//
// 这是主路径的另一半（口径层 §2.9 #32）：本地起一份带 UI 的实例配好 → 导出 → 拿去
// 部署纯转发形态。它不需要任何新机制，只是把 presence 闸两边各用一次——本地那台不挂
// 文件（UI 永远可写），生产那台挂文件（文件为准、管理面按密码闸不存在）。
//
// **含明文不打码**：打了码的文件部署不了。这不引入新的泄露路径——登录者今天已经能
// 批量读到全部秘密（凭证明文回读 v0.47、`GET /keys` 带 key_plain），导出只是把 1+N 次
// 请求压成 1 次。
//
// **输出按自然键字典序排，不按 id**：往返闸的字节相等全靠这一条。sub2api 正是栽在
// 这里——它的导出物没有身份键，只能 append 不能 apply。
//
// **第二个返回值是跳过名单**（口径层 v1.04）：非第一个 admin 用户名下的 key 不进
// 文件，归属表达不了，跳过是裁定过的行为——但跳过必须明说，名单交调用方出口
// （管理端逐把记日志），不让「少了几把」无声发生。
func Export(ctx context.Context, db store.Queryer) ([]byte, []string, error) {
	f, skipped, err := Snapshot(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	raw, err := Marshal(f)
	if err != nil {
		return nil, nil, err
	}
	return raw, skipped, nil
}

// firstAdminOrZero 返回第一个 admin 的 id；没有 admin 的库回 0——AUTOINCREMENT 从
// 1 起，0 匹配不上任何用户。导入闸与导出跳过共用这一条判据，两处对「谁是第一个
// admin」必须说同一句话。
func firstAdminOrZero(ctx context.Context, db store.Queryer) (int64, error) {
	adminID, err := store.FirstAdminID(ctx, db)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("找第一个 admin：%w", err)
	}
	return adminID, nil
}

// keysOwnedByOthers 点名「第一个 admin 之外的用户」名下的 API Key，格式
// `名（邮箱）`，按 key 名序。导入闸拿它拒绝，导出拿它点名跳过了谁——判据同源，
// 两处说同一批名字。
func keysOwnedByOthers(ctx context.Context, db store.Queryer) ([]string, error) {
	adminID, err := firstAdminOrZero(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT k.name, COALESCE(u.email, '') FROM api_keys k
		LEFT JOIN users u ON u.id = k.user_id
		WHERE k.user_id IS NOT NULL AND k.user_id <> ?
		ORDER BY k.name`, adminID)
	if err != nil {
		return nil, fmt.Errorf("读 API Key 归属：%w", err)
	}
	defer rows.Close()
	var owned []string
	for rows.Next() {
		var name, email string
		if err := rows.Scan(&name, &email); err != nil {
			return nil, err
		}
		owned = append(owned, fmt.Sprintf("%q（%s）", name, email))
	}
	return owned, rows.Err()
}

// CheckSingleUser 是声明形态**导入闸**的库侧判据（#66 ⑤）：库里存在**第一个 admin
// 之外的用户**名下的 key 时报错并逐把点名。第一个 admin 与无主的 key 不算——
// 单管理员库正是这套形态的主场。
//
// 服务的是 `POST /panel/api/import` 与它的试算：声明文件表达不了归属，导入的覆盖
// 语义会把用户 key 静默清光（#66 ⑤），那不是纪律是事故。**导出不走这道闸**——
// 导出对用户 key 的处置改成了跳过并点名（口径层 v1.04，PO 2026-09-01：不需要导出
// 用户的 key），见 Snapshot。**启动 apply 也不走这道闸**：挂声明文件是显式切事实源
// 的动作，照删文件外的 key、含用户 key（#66 ③）。
//
// 无 admin 却有用户名下 key 的库（只有手写 SQL 造得出）按全违规算：那不是这套
// 形态认识的任何形状。
func CheckSingleUser(ctx context.Context, db store.Queryer) error {
	owned, err := keysOwnedByOthers(ctx, db)
	if err != nil {
		return err
	}
	if len(owned) > 0 {
		return fmt.Errorf("声明形态不支持多用户：这几把 API Key 归属在第一个 admin 之外的用户名下：%s。"+
			"声明文件表达不了归属，覆盖时会把它们静默清光——"+
			"要导入声明文件，先删掉这些 key（或让用户自行删除）", strings.Join(owned, "、"))
	}
	return nil
}

// Snapshot 把库里的业务配置读成一个 File，不做序列化。
//
// 拆出来是为了让往返闸能在结构层面比对，也让导出的取数与排版各自可测。
//
// **只导第一个 admin 与无主的 key**（口径层 v1.04，PO 2026-09-01：不需要导出用户的
// key，取代 #66 ④ 的「多用户库拒绝导出」）：归属在第一个 admin 之外的用户名下的
// key 不进文件——apply 落库一律无主，把它们写进文件等于当场把归属压平，文件还回
// 不去（#66 ④ 的原由）；跳过则什么也不丢，用户 key 留在原库里原封不动。跳过不是
// 静默的：名单走第二个返回值交调用方点名。无 admin 的库没有「第一个 admin」，
// 用户名下的 key 一律算跳过。
func Snapshot(ctx context.Context, db store.Queryer) (*File, []string, error) {
	adminID, err := firstAdminOrZero(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	channels, err := exportChannels(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	aps, err := exportAccessPoints(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	keys, skipped, err := exportAPIKeys(ctx, db, adminID)
	if err != nil {
		return nil, nil, err
	}
	return &File{Channels: channels, AccessPoints: aps, APIKeys: keys}, skipped, nil
}

func exportChannels(ctx context.Context, db store.Queryer) ([]Channel, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, name, base_url_openai, base_url_openai_responses, base_url_anthropic,
		       credential_type, key_mode, auth_scheme,
		       max_concurrency, supports_compaction, supports_stateful_responses, provider, disabled
		FROM channels ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("读渠道：%w", err)
	}
	defer rows.Close()
	var out []Channel
	ids := map[string]int64{}
	for rows.Next() {
		var id int64
		var ch Channel
		var compaction, stateful, disabled int
		if err := rows.Scan(&id, &ch.Name, &ch.BaseURL.OpenAI, &ch.BaseURL.OpenAIResponses,
			&ch.BaseURL.Anthropic, &ch.CredentialType,
			&ch.KeyMode, &ch.AuthScheme, &ch.MaxConcurrency, &compaction, &stateful, &ch.Provider, &disabled); err != nil {
			return nil, err
		}
		// default 不背一行（omitempty 只略过空串）：apply 侧 orDefault 会补回来，
		// 往返闸两边对得上。
		if ch.AuthScheme == store.AuthSchemeDefault {
			ch.AuthScheme = ""
		}
		ch.SupportsCompaction = compaction != 0
		bit := stateful != 0
		ch.SupportsStatefulResponses = &bit
		ch.Disabled = disabled != 0
		out = append(out, ch)
		ids[ch.Name] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Credentials, err = exportCredentials(ctx, db, ids[out[i].Name]); err != nil {
			return nil, err
		}
		if out[i].Models, err = exportModels(ctx, db, ids[out[i].Name]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// exportCredentials 只取 name 与 credential。
//
// **运行期状态列一个都不取**（口径层 §2.9 #32）：disabled / disabled_reason /
// disabled_at 三列是这台机器上的停用现场。带上它们等于把本地的停用判决一起搬到
// 生产机——那台从没打过那把 key，却带着一条「它坏了」的判决上线。
func exportCredentials(ctx context.Context, db store.Queryer, channelID int64) ([]Credential, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, credential FROM channel_keys WHERE channel_id = ? ORDER BY name`, channelID)
	if err != nil {
		return nil, fmt.Errorf("读凭证：%w", err)
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.Name, &c.Credential); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func exportModels(ctx context.Context, db store.Queryer, channelID int64) ([]Model, error) {
	// 四价直接扫进 *float64：NULL → nil → omitempty 不写这一行，未定价的条目在
	// 文件里就是「没有这几个键」——与 apply 侧「没写落 NULL」互为逆运算，往返闸靠它。
	rows, err := db.QueryContext(ctx,
		`SELECT upstream_model, protocols, max_input_tokens,
		        price_input, price_output, price_cache_read, price_cache_write, disabled
		 FROM channel_models WHERE channel_id = ? ORDER BY upstream_model`, channelID)
	if err != nil {
		return nil, fmt.Errorf("读纳管模型：%w", err)
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		var protocols string
		var disabled int
		if err := rows.Scan(&m.UpstreamModel, &protocols, &m.MaxInputTokens,
			&m.PriceInput, &m.PriceOutput, &m.PriceCacheRead, &m.PriceCacheWrite, &disabled); err != nil {
			return nil, err
		}
		m.Protocols = splitProtocols(protocols)
		m.Disabled = disabled != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func exportAccessPoints(ctx context.Context, db store.Queryer) ([]AccessPoint, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, model, disabled FROM access_points ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("读接入点：%w", err)
	}
	defer rows.Close()
	var out []AccessPoint
	ids := map[string]int64{}
	for rows.Next() {
		var id int64
		var ap AccessPoint
		var disabled int
		if err := rows.Scan(&id, &ap.Model, &disabled); err != nil {
			return nil, err
		}
		ap.Disabled = disabled != 0
		out = append(out, ap)
		ids[ap.Model] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Candidates, err = exportCandidates(ctx, db, ids[out[i].Model]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// exportCandidates 把候选写成限定名 + 权重，排序按**限定名**而不是 id。
//
// 排序键必须是文件里那个可见的身份，不是库里的自增列：apply 之后 id 会重排（新库从 1
// 开始），按 id 排的第二次导出就跟第一次对不上了。
func exportCandidates(ctx context.Context, db store.Queryer, apID int64) ([]Candidate, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT ch.name, cm.upstream_model, cd.weight
		FROM candidates cd
		JOIN channel_models cm ON cm.id = cd.channel_model_id
		JOIN channels ch ON ch.id = cm.channel_id
		WHERE cd.access_point_id = ?`, apID)
	if err != nil {
		return nil, fmt.Errorf("读候选：%w", err)
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var channel, model string
		var weight int
		if err := rows.Scan(&channel, &model, &weight); err != nil {
			return nil, err
		}
		w := weight
		out = append(out, Candidate{Target: store.QualifiedName(channel, model), Weight: &w})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Candidate) int { return strings.Compare(a.Target, b.Target) })
	return out, nil
}

// exportAPIKeys 读 API Key，**拿不到原值就当场失败并点名是哪几把**。
//
// key_plain 是 v0.47 才加的列，在那之前建的 key 只有哈希，原值永远补不回来。三种处置
// 里只有「失败」是对的（口径层 §2.9 #32 ④）：
//
//   - 跳过 —— 生产机看着一切正常，而那些客户端拿着旧 key 打过来全是 401，
//     且纯转发形态下没有页面能看出少了几把。
//   - 写占位值 —— 只是把同一个失败推晚一站，还多骗一次人。
//   - 失败 —— 当场知道要去把那几把删了重建。
func exportAPIKeys(ctx context.Context, db store.Queryer, adminID int64) ([]APIKey, []string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT k.name, k.key_plain, k.allowed_models, k.disabled, k.user_id, COALESCE(u.email, '')
		FROM api_keys k LEFT JOIN users u ON u.id = k.user_id
		ORDER BY k.name`)
	if err != nil {
		return nil, nil, fmt.Errorf("读 API Key：%w", err)
	}
	defer rows.Close()
	var out []APIKey
	var lost, skipped []string
	for rows.Next() {
		var k APIKey
		var allowed string
		var disabled int
		var owner sql.NullInt64
		var email string
		if err := rows.Scan(&k.Name, &k.Key, &allowed, &disabled, &owner, &email); err != nil {
			return nil, nil, err
		}
		// 非第一个 admin 名下的 key 不进文件（Snapshot 的注释讲全了为什么是跳过
		// 不是拒绝），但逐把记下名字交调用方点名——静默少几把 key 是这套代码最
		// 反对的失败模式。
		if owner.Valid && owner.Int64 != adminID {
			skipped = append(skipped, fmt.Sprintf("%q（%s）", k.Name, email))
			continue
		}
		if strings.TrimSpace(k.Key) == "" {
			lost = append(lost, k.Name)
			continue
		}
		k.AllowedModels = parseAllowedModels(allowed)
		k.Disabled = disabled != 0
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(lost) > 0 {
		return nil, nil, fmt.Errorf("这几把 API Key 存的只有哈希、拿不到原值，导不出去：%s。"+
			"它们建于 key_plain 这一列之前，哈希不可逆——在管理端把它们删了重建，再导一次",
			strings.Join(quoted(lost), "、"))
	}
	return out, skipped, nil
}

// parseAllowedModels 把库里那一列解回列表形态。
//
// 与写侧同一个格式：逗号分隔，`*` 或空串都是不限（空串出现在手写 SQL 漏填的行上，
// `auth.Key.Allows` 按「没设限制」处理，导出物得跟它说同一句话）。
func parseAllowedModels(raw string) []string {
	if s := strings.TrimSpace(raw); s == "" || s == "*" {
		return []string{"*"}
	}
	out := []string{}
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

func splitProtocols(raw string) []string {
	out := []string{}
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

// Marshal 把 File 排成声明文件的字节。
//
// 缩进定死 2 空格（yaml.v3 默认是 4）：不是审美，是往返闸——编码参数任何一处不确定，
// 两次导出就对不上。字段顺序由结构体定义顺序决定，也是确定的（全篇没有 map）。
func Marshal(f *File) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("序列化声明文件：%w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteFile 把导出物落到磁盘，**权限 0600**（口径层 §2.9 #28 / #32）。
//
// 它里面是明文秘密，跟私钥同级。管理端那条路不经过这里——浏览器下载的文件权限由浏览器
// 决定，网关碰不到；这个函数服务的是仓库里生成 channels.example.yaml 那条路，以及将来
// 任何在服务端落盘的场景。
func WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
