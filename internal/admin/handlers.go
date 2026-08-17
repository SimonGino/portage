package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/upstream"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// loginFailDelay 是密码错误时的固定延时。
//
// 不做锁定、不做计数：单管理员的自用网关，锁定只会把自己关在门外。一个固定延时把
// 在线爆破从「每秒几千次」压到「每秒两次」，对付局域网里的脚本足够；真要防住，
// 靠的是别把 8317 暴露到公网（口径层 §2.7 的 TLS + 限流是另一件事）。
const loginFailDelay = 500 * time.Millisecond

// ── 会话 ────────────────────────────────────────────────────────────────

func (h *Handler) login(c *gin.Context) {
	var in struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}

	hash, err := store.GetSetting(c.Request.Context(), h.db, store.SettingAdminPasswordHash)
	if err != nil {
		h.log.Error("读管理端密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "登录失败")
		return
	}
	if hash == "" {
		// 说清楚是「还没设」而不是「密码错」：这两种情况的补救动作完全不同，
		// 含糊其辞会让人对着配置文件里明明写着的密码反复重试。
		fail(c, http.StatusServiceUnavailable, "尚未设置管理密码：在 config.yaml 里填 admin_password 后重启")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		time.Sleep(loginFailDelay)
		h.log.Warn("管理端登录失败", "remote", c.ClientIP())
		fail(c, http.StatusUnauthorized, "密码不对")
		return
	}

	token, err := h.sessions.create()
	if err != nil {
		h.log.Error("生成会话失败", "err", err)
		fail(c, http.StatusInternalServerError, "登录失败")
		return
	}
	h.setSessionCookie(c, token, int(sessionTTL.Seconds()))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) logout(c *gin.Context) {
	if token, _ := c.Cookie(cookieName); token != "" {
		h.sessions.drop(token)
	}
	h.setSessionCookie(c, "", -1)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// session 让前端在加载时问一句「我还登着吗」，免得每个页面各自靠 401 去发现。
func (h *Handler) session(c *gin.Context) {
	token, _ := c.Cookie(cookieName)
	hash, err := store.GetSetting(c.Request.Context(), h.db, store.SettingAdminPasswordHash)
	if err != nil {
		h.log.Error("读管理端密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取状态失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"authenticated": h.sessions.valid(token),
		"password_set":  hash != "",
	})
}

// setSessionCookie 统一设置会话 cookie。
//
// HttpOnly：JS 读不到，XSS 也偷不走会话。
// SameSite=Strict：跨站过来的请求一律不带这个 cookie，CSRF 因此不成立——管理端的
// 写接口全是同源 fetch，不受影响。这也是这里不再另做 CSRF token 的原因。
// Secure 跟着实际协议走：局域网里是明文 HTTP，硬写 Secure 会让 cookie 根本存不下，
// 表现成「登录成功但立刻又变成未登录」。
func (h *Handler) setSessionCookie(c *gin.Context, token string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) changePassword(c *gin.Context) {
	var in struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(in.New) < 8 {
		fail(c, http.StatusBadRequest, "新密码至少 8 位")
		return
	}
	ctx := c.Request.Context()
	hash, err := store.GetSetting(ctx, h.db, store.SettingAdminPasswordHash)
	if err != nil {
		h.log.Error("读管理端密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "修改失败")
		return
	}
	// 已经登录了还要验旧密码：cookie 可能是别人在这台机器上留下的。
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Old)) != nil {
		time.Sleep(loginFailDelay)
		fail(c, http.StatusUnauthorized, "原密码不对")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(in.New), bcrypt.DefaultCost)
	if err != nil {
		h.log.Error("哈希新密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "修改失败")
		return
	}
	if err := store.SetSetting(ctx, h.db, store.SettingAdminPasswordHash, string(newHash)); err != nil {
		h.log.Error("写管理端密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "修改失败")
		return
	}
	// 全部会话作废，包括发起这次修改的这一个——改完要重登。
	h.sessions.dropAll()
	h.setSessionCookie(c, "", -1)
	h.log.Info("管理端密码已修改，全部会话已吊销")
	c.JSON(http.StatusOK, gin.H{"ok": true, "relogin": true})
}

// ── 渠道 ────────────────────────────────────────────────────────────────

func (h *Handler) listChannels(c *gin.Context) {
	list, err := store.ListChannels(c.Request.Context(), h.db)
	if err != nil {
		h.log.Error("列渠道失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, list)
}

// channelInput 是渠道表单。credential 只在**创建**时可选带一把（省去「建完再去加
// 凭证」这一步）——之后凭证走 /channels/:id/credentials 那套逐条 CRUD，而修改渠道的
// 接口根本不看这个字段，因此「改个名字」不可能顺手把凭证清空。
type channelInput struct {
	Name string `json:"name"`
	// Protocols 是支持协议集（口径层 v0.33），至少一个。
	Protocols []string `json:"protocols"`
	BaseURL   string   `json:"base_url"`
	// KeyMode 是凭证选取模式 polling/random（口径层 v0.44 起露在凭证池弹窗里，不在渠道表单）。
	KeyMode string `json:"key_mode"`
	// MaxConcurrency 是渠道级并发上限（口径层 v0.49）：0 = 不限。指针留 nil 表示
	// 「请求体没提」，缺省不动库里那一列（同 KeyMode 的整体覆盖陷阱）。
	MaxConcurrency *int `json:"max_concurrency"`
	// SupportsCompaction 是 compaction 能力位（口径层 v0.54）：上游认不认 Codex 的
	// compaction_trigger。指针的 nil 同 MaxConcurrency——缺省不动那一列。
	SupportsCompaction *bool  `json:"supports_compaction"`
	Disabled           bool   `json:"disabled"`
	Credential         string `json:"credential"`
}

func (in channelInput) toStore() store.ChannelInput {
	set := make(protocol.Set, 0, len(in.Protocols))
	for _, p := range in.Protocols {
		set = append(set, protocol.Protocol(strings.TrimSpace(p)))
	}
	return store.ChannelInput{
		Name:               strings.TrimSpace(in.Name),
		Protocols:          set,
		BaseURL:            strings.TrimSpace(in.BaseURL),
		KeyMode:            strings.TrimSpace(in.KeyMode),
		MaxConcurrency:     in.MaxConcurrency,
		SupportsCompaction: in.SupportsCompaction,
		Disabled:           in.Disabled,
	}
}

func (h *Handler) createChannel(c *gin.Context) {
	var in channelInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if in.Name == "" {
		fail(c, http.StatusBadRequest, "渠道名不能为空")
		return
	}
	h.writeResult(c, func(ctx context.Context, tx *sql.Tx) (any, error) {
		id, err := store.CreateChannel(ctx, tx, in.toStore())
		if err != nil {
			return nil, err
		}
		if cred := strings.TrimSpace(in.Credential); cred != "" {
			if err := store.AddChannelCredentials(ctx, tx, id, []store.NewCredential{{Value: cred}}); err != nil {
				return nil, err
			}
		}
		return gin.H{"id": id}, nil
	})
}

func (h *Handler) updateChannel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in channelInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.UpdateChannel(ctx, tx, id, in.toStore())
	})
}

// probeChannel 逐个协议问上游「你提供这个子路径吗」，回一组结果给页面显示。
//
// **只提示、不做闸**（口径层 v0.33）：不落库、不参与路由、不影响保存成败——所以它是
// 独立的一次 POST，而不是缝在保存事务里。前端在保存成功之后调它，勾错协议集的人当场
// 就能看见，而这条信息不会变成一份会过期的缓存躺在库里。
//
// 串行不并发：最多三个协议，而并发起来时上游那边看到的是三个几乎同时到达的请求，
// 有些中转会按这个判限流。
func (h *Handler) probeChannel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	target, err := store.ChannelProbeTarget(c.Request.Context(), h.db, id)
	if err != nil {
		h.writeError(c, err)
		return
	}
	// 逐把凭证探（口径层 v0.38），含已停用的——恢复既然是纯人工的，「这把被摘的凭证
	// 现在还坏不坏」除了删掉重配就没有别的办法回答。一份凭证都没有时仍探一轮空凭证：
	// 401 同样证明子路径存在，而那正是探测要问的。
	creds := target.Credentials
	if len(creds) == 0 {
		creds = []store.ProbeCredential{{Name: ""}}
	}
	groups := make([]probeGroup, 0, len(creds))
	for _, cred := range creds {
		g := probeGroup{Credential: cred.Name, Disabled: cred.Disabled}
		for _, p := range target.Protocols {
			g.Results = append(g.Results, upstream.Probe(c.Request.Context(), target.BaseURL, p, cred.Value))
		}
		groups = append(groups, g)
	}

	// 模型矩阵要花钱，所以**默认不跑**，由调用方显式 `?models=1` 要（口径层 v0.43 ①
	// 「只由人手点」）。缺省站在不花钱那一侧：上面那层是保存渠道后自动跑的（v0.33），
	// 把矩阵默认挂上去等于每改一次 base_url 就静默打出「模型数 × 协议数」次真实推理。
	// 写成 opt-out（`?models=0`）的话，将来漏传参数的代价是花钱，opt-in 漏传只是少一层提示。
	var models []modelProbeRow
	var modelCred string
	if c.Query("models") == "1" {
		models, modelCred = probeModelMatrix(c.Request.Context(), target)
	}

	// 只报渠道名，不报 base_url，更不报凭证值。
	h.log.Info("渠道协议探测", "channel", target.Name,
		"credentials", len(groups), "models", len(models))
	c.JSON(http.StatusOK, gin.H{
		"credentials":      groups,
		"models":           models,
		"model_credential": modelCred,
	})
}

// probeModelMatrix 跑模型级探测（口径层 v0.43）：启用中的纳管模型 × 各自的有效协议
// 集（自己声明的子集，空则继承渠道全集），每格发一个带模型名的最小真实请求。
//
// 只用第一把**启用**凭证：这一层每格都是要花钱的真请求，逐把凭证轰全矩阵是
// 模型数 × 协议数 × 凭证数的立方爆炸；「这把凭证还活不活」的问题上面那层已经
// 逐把答过了。403 的格子固定词表里写明「可能是这把凭证没开通」——那正是 403 的
// 凭证相关含义，所以响应里带上探的是哪把（model_credential），页面照实标注。
//
// 并发跑但压到 4：与上面那层「串行防中转按并发判限流」的顾虑相对，这一层的请求
// 形状就是普通推理流量，4 路并发是任何客户端都会有的样子；串行在 20 个模型 ×
// 8 秒超时的最坏情形下要等三分钟，人在对话框前面等不了那么久。
func probeModelMatrix(ctx context.Context, target store.ProbeTarget) ([]modelProbeRow, string) {
	if len(target.Models) == 0 {
		return nil, ""
	}
	var cred store.ProbeCredential
	for _, x := range target.Credentials {
		if !x.Disabled {
			cred = x
			break
		}
	}
	if cred.Value == "" {
		// 一把启用的凭证都没有：拿空凭证发请求只会攒一屏 401 说不清，不如不发。
		// 上面那层同样会是空的，页面只剩子路径探测不到东西的提示。
		return nil, ""
	}

	rows := make([]modelProbeRow, len(target.Models))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, m := range target.Models {
		protos := m.Protocols
		if len(protos) == 0 {
			protos = target.Protocols
		}
		rows[i] = modelProbeRow{Model: m.Name, Results: make([]upstream.ModelProbeResult, len(protos))}
		for j, p := range protos {
			wg.Add(1)
			go func(i, j int, p protocol.Protocol, model string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				rows[i].Results[j] = upstream.ProbeModel(ctx, target.BaseURL, p, cred.Value, model)
			}(i, j, p, m.Name)
		}
	}
	wg.Wait()
	return rows, cred.Name
}

// modelProbeRow 是一个纳管模型的探测结论行。凭证值不在里面，也不会有掩码。
type modelProbeRow struct {
	Model   string                      `json:"model"`
	Results []upstream.ModelProbeResult `json:"results"`
}

func (h *Handler) deleteChannel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.DeleteChannel(ctx, tx, id)
	})
}

// probeGroup 是一份凭证的探测结果分组。凭证值不在里面，也不会有掩码。
type probeGroup struct {
	Credential string                 `json:"credential"`
	Disabled   bool                   `json:"disabled"`
	Results    []upstream.ProbeResult `json:"results"`
}

// fetchChannelModels 朝渠道声明的每个协议侧拉一次模型列表，回给表单做预勾选
// （口径层 v0.40）。
//
// **拉回来的东西不落库、不参与路由**：它只是替人把「这个模型在哪个协议侧存在」看一眼，
// 落库的仍是人在表单上确认过的配置。中转站的 `/v1/models` 返回一份写死的大列表是常态，
// 直接采信等于把一份会撒谎的缓存放进请求路径——那正是 §2.2 拒绝把探测做成闸的理由。
//
// 只用第一把**启用**凭证，不像 Probe 那样逐把跑：那边逐把是因为「这把被摘的凭证还坏
// 不坏」本身就是要问的；这边问的是上游有哪些模型，换一把凭证不会换来另一份答案。
func (h *Handler) fetchChannelModels(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	// 复用探测目标：要的东西（打哪儿、哪几个协议、凭证）一模一样，为拉列表另起一个
	// 查询只会让两处对「渠道当下长什么样」的理解慢慢分叉。
	target, err := store.ChannelProbeTarget(c.Request.Context(), h.db, id)
	if err != nil {
		h.writeError(c, err)
		return
	}
	var cred string
	for _, x := range target.Credentials {
		if !x.Disabled {
			cred = x.Value
			break
		}
	}
	results := upstream.ListModelsFor(c.Request.Context(), target.BaseURL, target.Protocols, cred)
	// 只报渠道名与拉到几组，不报 base_url，更不报凭证值。
	h.log.Info("拉上游模型列表", "channel", target.Name, "groups", len(results))
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// ── 凭证池 ──────────────────────────────────────────────────────────────
//
// 凭证值可回读（口径层 v0.47 推翻 v0.28），但**只在这一组接口里**：列表回名字、值、
// 状态、时间与停用原因。掩码在前端做，后端发全串——掩码后的串复制出去没用，而 PO 要的
// 正是复制。其余任何接口都不带值，收口在这里。

func (h *Handler) listCredentials(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	list, err := store.ListChannelCredentials(c.Request.Context(), h.db, id)
	if err != nil {
		h.log.Error("列渠道凭证失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, list)
}

// addCredentials 往池子里**追加**若干份（口径层 v0.38：追加，不是整把替换）。
//
// 支持一次贴多行：换号、扩容时人手里往往是一整块文本。名字留空由 store 给 `凭证 N`。
func (h *Handler) addCredentials(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		// 单条与批量共用一个入口：单条填 credential，批量填 credentials（一行一份）。
		Name        string `json:"name"`
		Credential  string `json:"credential"`
		Credentials string `json:"credentials"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	var items []store.NewCredential
	if cred := strings.TrimSpace(in.Credential); cred != "" {
		items = append(items, store.NewCredential{Name: in.Name, Value: cred})
	}
	for _, line := range strings.Split(in.Credentials, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			items = append(items, store.NewCredential{Value: line})
		}
	}
	if len(items) == 0 {
		fail(c, http.StatusBadRequest, "凭证不能为空")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.AddChannelCredentials(ctx, tx, id, items)
	})
}

// updateCredential 改名 / 换值 / 停用 / 启用。credential 留空即不动值——页面上读不到
// 原值，改个名字还要重贴一遍 key 等于每次都换一把。
func (h *Handler) updateCredential(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Name       string `json:"name"`
		Credential string `json:"credential"`
		Disabled   bool   `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.UpdateCredential(ctx, tx, id, store.CredentialUpdate{
			Name: in.Name, Value: in.Credential, Disabled: in.Disabled,
		})
	})
}

func (h *Handler) deleteCredential(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.DeleteCredential(ctx, tx, id)
	})
}

func (h *Handler) addChannelModel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		UpstreamModel string `json:"upstream_model"`
		// Protocols 是协议子集（口径层 v0.40），不传或传空数组 = 继承渠道全集。
		Protocols []string `json:"protocols"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	model := strings.TrimSpace(in.UpstreamModel)
	if model == "" {
		fail(c, http.StatusBadRequest, "纳管模型名不能为空")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.AddChannelModel(ctx, tx, id, model, toProtocolSet(in.Protocols))
	})
}

// toProtocolSet 把前端传的字符串数组收成协议集，只去空格——取值合法性交给 store 侧的
// ParseSet，一处校验一处报错，别在两层各写一遍还写出两种口径。
func toProtocolSet(in []string) protocol.Set {
	set := make(protocol.Set, 0, len(in))
	for _, p := range in {
		if p = strings.TrimSpace(p); p != "" {
			set = append(set, protocol.Protocol(p))
		}
	}
	return set
}

func (h *Handler) updateChannelModel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Disabled bool `json:"disabled"`
		// 指针是为了分清「没提这个字段」和「提了、要清空」（口径层 v0.40）：
		// nil 不动那一列，空数组则是显式改回「继承渠道全集」。同 key_mode 那条
		// 理由——PUT 整体覆盖时，老前端不传的字段不该被静默改掉。
		Protocols *[]string `json:"protocols"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		if err := store.SetChannelModelDisabled(ctx, tx, id, in.Disabled); err != nil {
			return err
		}
		if in.Protocols == nil {
			return nil
		}
		return store.SetChannelModelProtocols(ctx, tx, id, toProtocolSet(*in.Protocols))
	})
}

func (h *Handler) deleteChannelModel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.DeleteChannelModel(ctx, tx, id)
	})
}

// ── 接入点 ──────────────────────────────────────────────────────────────

func (h *Handler) listAccessPoints(c *gin.Context) {
	list, err := store.ListAccessPointsDetail(c.Request.Context(), h.db)
	if err != nil {
		h.log.Error("列接入点失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, list)
}

type accessPointInput struct {
	Model          string `json:"model"`
	Disabled       bool   `json:"disabled"`
	ChannelModelID int64  `json:"channel_model_id"`
	Weight         int    `json:"weight"`
}

// normalize 兜住 weight 的零值。前端不填 weight 时它是 0，而 weight=0 的候选会被
// Resolve 与 Validate 一起当成「不存在」——接入点保存下去了却怎么都路由不到，
// 是个很难自己想明白的坑。临时闸下只有一个候选，权重多少都一样，直接兜到 100。
func (in *accessPointInput) normalize() {
	in.Model = strings.TrimSpace(in.Model)
	if in.Weight <= 0 {
		in.Weight = 100
	}
}

func (h *Handler) createAccessPoint(c *gin.Context) {
	var in accessPointInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	in.normalize()
	if in.Model == "" {
		fail(c, http.StatusBadRequest, "接入点名不能为空")
		return
	}
	if in.ChannelModelID == 0 {
		fail(c, http.StatusBadRequest, "要选一个纳管模型作为候选")
		return
	}
	h.writeResult(c, func(ctx context.Context, tx *sql.Tx) (any, error) {
		id, err := store.CreateAccessPoint(ctx, tx, in.Model, in.ChannelModelID, in.Weight)
		if err != nil {
			return nil, err
		}
		return gin.H{"id": id}, nil
	})
}

func (h *Handler) updateAccessPoint(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in accessPointInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	in.normalize()
	if in.ChannelModelID == 0 {
		fail(c, http.StatusBadRequest, "要选一个纳管模型作为候选")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.UpdateAccessPoint(ctx, tx, id, in.Model, in.Disabled, in.ChannelModelID, in.Weight)
	})
}

func (h *Handler) deleteAccessPoint(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.DeleteAccessPoint(ctx, tx, id)
	})
}

// ── 网关 key ────────────────────────────────────────────────────────────

func (h *Handler) listKeys(c *gin.Context) {
	list, err := store.ListAPIKeys(c.Request.Context(), h.db)
	if err != nil {
		h.log.Error("列网关 key 失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, list)
}

// createKey 生成明文，哈希与明文各存一列（口径层 v0.47）。
//
// 明文照旧在这个响应里回一次，但它不再是唯一的一次：列表接口也带明文，页面上随时
// 能看能复制。加列之前建的 key 拿不回来——哈希不可逆，那些只能删了重建。
func (h *Handler) createKey(c *gin.Context) {
	var in struct {
		Name          string `json:"name"`
		AllowedModels string `json:"allowed_models"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "key 名不能为空")
		return
	}
	plain, err := generateKey()
	if err != nil {
		h.log.Error("生成 key 失败", "err", err)
		fail(c, http.StatusInternalServerError, "生成失败")
		return
	}
	h.writeResult(c, func(ctx context.Context, tx *sql.Tx) (any, error) {
		id, err := store.CreateAPIKey(ctx, tx, name, auth.Hash(plain), plain, normalizeAllowed(in.AllowedModels))
		if err != nil {
			return nil, err
		}
		return gin.H{"id": id, "key": plain}, nil
	})
}

func (h *Handler) updateKey(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Name          string `json:"name"`
		AllowedModels string `json:"allowed_models"`
		Disabled      bool   `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		fail(c, http.StatusBadRequest, "key 名不能为空")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.UpdateAPIKey(ctx, tx, id, name, normalizeAllowed(in.AllowedModels), in.Disabled)
	})
}

func (h *Handler) deleteKey(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.DeleteAPIKey(ctx, tx, id)
	})
}

// generateKey 造一把新的网关 key：sk-ptg- 前缀 + 32 位十六进制（128 bit 熵）。
//
// 前缀是给人看的——从一堆环境变量里一眼认出「这是网关的 key，不是上游的」。
func generateKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "sk-ptg-" + hex.EncodeToString(buf), nil
}

// normalizeAllowed 把白名单收拾干净：去空白、去空项，全空即 `*`（不限）。
func normalizeAllowed(raw string) string {
	var items []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return "*"
	}
	return strings.Join(items, ",")
}

// ── 用量 ────────────────────────────────────────────────────────────────

// maxLogLimit 兜住 limit：用量页一次拉几万行只会把浏览器和这一条 SQLite 连接
// 一起拖住——连接池是 1，那期间所有转发请求都在排队。
const maxLogLimit = 500

// listLogs 列流水。筛选与翻页都在后端做（口径层 v0.53）——此前「只看失败」是前端
// 在已经拉回来的那 100 条里过滤，翻页一上来就会露馅：筛的是当前这一页，不是流水。
//
// 回的是 `{rows, total}` 而不是裸数组（口径层 v0.61）：页码要跳，得先知道有几页。
// total 按**同一组条件**数，`before` 也算在内——管理端把 before 钉在进页那一刻的最大
// id 上，于是行与总数说的是同一批数据，翻页途中新写进来的流水不会让总页数在手底下变。
// 计数与取行是两条独立的语句、没有包在一个事务里：中间挤进新行只可能影响不带 before
// 的那一次（进页第一发），而管理端拿到第一发之后就把 before 钉住了，下一次点击自动纠正。
// 为这点误差开一个事务，代价是把连接池只有 1 的那条 SQLite 连接多占一会儿。
//
// 认不得的参数一律忽略、不报错：同 usage 的立论，展示参数写错不该让页面打不开。
func (h *Handler) listLogs(c *gin.Context) {
	f := store.CallLogFilter{
		Limit:  clampQuery(c, "limit", 100, 1, maxLogLimit),
		Offset: clampQuery(c, "offset", 0, 0, 1<<20),
		// 上限取 int32 上界而不是更大的数：clampQuery 收的是 int，写死一个 64 位常量
		// 会让 32 位目标（树莓派那类）编译不过，而流水 id 到不了二十亿。
		Before:     int64(clampQuery(c, "before", 0, 0, 1<<31-1)),
		Model:      c.Query("model"),
		APIKeyName: c.Query("key"),
		// endpoint 精确匹配那四条转发路径之一（#17）。不校验取值：与 model/key 同款
		// 原样下推，认不得的值筛出空列表，而校验后忽略等于把「筛错了」显示成「全部
		// 流水」——那比空列表更误导。
		Endpoint:   c.Query("endpoint"),
		FailedOnly: c.Query("only") == "bad",
	}
	rows, err := store.ListCallLogs(c.Request.Context(), h.db, f)
	if err != nil {
		h.log.Error("列调用流水失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	total, err := store.CountCallLogs(c.Request.Context(), h.db, f)
	if err != nil {
		h.log.Error("数调用流水失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"rows": rows, "total": total})
}

// usage 汇总用量。by=model（默认，按请求的模型）/ by=key（按网关 API Key，v0.53）/
// by=credential（按上游凭证，v0.38）。
//
// 认不得的 by 当默认处理而不是报错：这是个只影响展示的查询参数，写错了不该让整个
// 页面打不开（同 clampQuery 的立论）。
func (h *Handler) usage(c *gin.Context) {
	days := clampQuery(c, "days", 7, 1, 365)
	dim := store.UsageByModel
	switch c.Query("by") {
	case store.UsageByKey:
		dim = store.UsageByKey
	case store.UsageByCredential:
		dim = store.UsageByCredential
	}
	rows, err := store.UsageBy(c.Request.Context(), h.db, days, dim)
	if err != nil {
		h.log.Error("汇总用量失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "by": dim, "rows": rows})
}

// usageDaily 把最近 days 天按本地自然日分桶，用量图用它。
//
// 与 usage 分成两个端点而不是塞进同一份回包：分桶只跟 days 有关、与聚合维度无关，
// 合在一起的话每切一次「按模型 / 按 API Key / 按上游凭证」都得把它重算一遍。
func (h *Handler) usageDaily(c *gin.Context) {
	days := clampQuery(c, "days", 7, 1, 365)
	rows, err := store.UsageDaily(c.Request.Context(), h.db, days)
	if err != nil {
		h.log.Error("按天汇总用量失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "rows": rows})
}

// ── 小工具 ──────────────────────────────────────────────────────────────

// pathID 解析 :id。解析不出就自己回 400 并返回 false——调用方直接 return。
func pathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "id 不合法")
		return 0, false
	}
	return id, true
}

// clampQuery 读一个整数查询参数并夹到 [min, max]。缺失或解析失败都用 def，
// 不报错：翻页参数写错不该让整个页面打不开。
func clampQuery(c *gin.Context, name string, def, minV, maxV int) int {
	v, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return def
	}
	return max(minV, min(maxV, v))
}
