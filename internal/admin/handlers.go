package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/pricing"
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
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if in.Email == "" {
		fail(c, http.StatusBadRequest, "缺少邮箱")
		return
	}

	// #72 起邮箱密码登录验的是 users.password_hash——#71「搬 hash 复制不删源」在
	// 本票转正，settings 那份只剩初始化与回滚兜底的职责，登录不再读它。
	ctx := c.Request.Context()
	u, err := store.GetUserAuthByEmail(ctx, h.db, in.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.log.Error("读用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "登录失败")
		return
	}
	// 「没有这个邮箱」与「密码错」折成同一句话、走同一个延时：分开说等于给爆破
	// 脚本一个免费的邮箱存在性预言机。OAuth-only 账号（无密码）也落在这一句里——
	// 页面上「忘记密码」那条路兼作它的设密码入口（#62 决议 6）。
	if errors.Is(err, store.ErrNotFound) || u.PasswordHash == nil ||
		bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(in.Password)) != nil {
		time.Sleep(loginFailDelay)
		h.log.Warn("管理端登录失败", "remote", c.ClientIP())
		fail(c, http.StatusUnauthorized, "邮箱或密码不对")
		return
	}
	// 停用即对系统一切访问冻结（#61）。放在密码验过之后：验过说明是本人，这句
	// 状态告知不算泄露；验之前就说「已停用」反而白送邮箱存在性。
	if u.Disabled {
		fail(c, http.StatusForbidden, "账号已停用")
		return
	}
	token, ttl, err := store.CreateSession(ctx, h.db, u.ID)
	if err != nil {
		h.log.Error("生成会话失败", "err", err)
		fail(c, http.StatusInternalServerError, "登录失败")
		return
	}
	h.setSessionCookie(c, token, int(ttl.Seconds()))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) logout(c *gin.Context) {
	if token, _ := c.Cookie(cookieName); token != "" {
		if err := store.DeleteSession(c.Request.Context(), h.db, token); err != nil {
			// 只记不拦：登出的可见效果（清 cookie）照常发生，库里那行最晚在
			// 过期 sweep 时也会走。
			h.log.Error("删除会话失败", "err", err)
		}
	}
	h.setSessionCookie(c, "", -1)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// session 让前端在加载时问一句「我还登着吗」，免得每个页面各自靠 401 去发现。
// #72 起登着时还带出「我是谁」：前端要按角色分壳、按验证态锁功能，逐页各查一遍
// 不如这里一次说全。
func (h *Handler) session(c *gin.Context) {
	su, authed, err := h.validSession(c)
	if err != nil {
		h.log.Error("校验会话失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取状态失败")
		return
	}
	hash, err := store.GetSetting(c.Request.Context(), h.db, store.SettingAdminPasswordHash)
	if err != nil {
		h.log.Error("读管理端密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取状态失败")
		return
	}
	out := gin.H{
		"authenticated": authed,
		"password_set":  hash != "",
	}
	if authed {
		u, err := store.GetUser(c.Request.Context(), h.db, su.ID)
		if err != nil {
			h.log.Error("读用户失败", "err", err)
			fail(c, http.StatusInternalServerError, "读取状态失败")
			return
		}
		out["user"] = u
	}
	c.JSON(http.StatusOK, out)
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
	// 未验证功能全锁（#62 决议 2）：去验证页之外的自助动作一概不给，改密码也在
	// 锁单里。admin 与逃生门建的号造出来就是已验证，这道闸实际只拦注册流的新号。
	if !h.requireVerified(c) {
		return
	}
	ctx := c.Request.Context()
	// #72 起改的是**自己**的密码：验的是本人 users 里那份哈希，不再读 settings——
	// 多用户之后「管理密码」不是一个全局物件，它就是第一个 admin 的密码。
	u, err := store.GetUserAuthByID(ctx, h.db, sessionUser(c).ID)
	if err != nil {
		h.log.Error("读用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "修改失败")
		return
	}
	if u.PasswordHash == nil {
		// OAuth-only 账号没有旧密码可验，设密码走「忘记密码」那条邮件链路
		// （#62 决议 6）——那条路验证的是邮箱所有权，比「登着就能设」硬。
		fail(c, http.StatusBadRequest, "该账号未设置过密码：走「忘记密码」用邮件链接设置")
		return
	}
	// 已经登录了还要验旧密码：cookie 可能是别人在这台机器上留下的。
	if bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(in.Old)) != nil {
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
	if err := h.setPassword(ctx, u.ID, string(newHash)); err != nil {
		h.log.Error("写密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "修改失败")
		return
	}
	// 本人的会话全部作废，包括发起这次修改的这一个——改完要重登。只吊销自己的：
	// 多用户之后 DeleteAllSessions 会把无辜的人一起踢下线。
	if err := store.DeleteUserSessions(ctx, h.db, u.ID); err != nil {
		h.log.Error("吊销会话失败", "err", err)
		fail(c, http.StatusInternalServerError, "修改失败")
		return
	}
	h.setSessionCookie(c, "", -1)
	h.log.Info("用户密码已修改，本人会话已吊销", "user", u.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true, "relogin": true})
}

// setPassword 写一个用户的密码哈希，并在他是第一个 admin 时同步 settings 里那份
// 复制——两份不一起动的话，回滚到 #71 之前的二进制会拿着旧密码放行（见
// EnsureFirstAdmin 的注释）。改密码与重置密码两条路共用这一处。
func (h *Handler) setPassword(ctx context.Context, userID int64, hash string) error {
	if err := store.SetUserPasswordHash(ctx, h.db, userID, hash); err != nil {
		return err
	}
	if adminID, err := store.FirstAdminID(ctx, h.db); err == nil && adminID == userID {
		if err := store.SetSetting(ctx, h.db, store.SettingAdminPasswordHash, hash); err != nil {
			return err
		}
	}
	return nil
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

// channelInput 是**新建**渠道的表单（#48 批2 起修改走四笔按意图的字段写，不再用它）。
// credential 可选带一把（省去「建完再去加凭证」这一步）——之后凭证走
// /channels/:id/credentials 那套逐条 CRUD。
type channelInput struct {
	Name string `json:"name"`
	// BaseURL 是每协议出站根地址（口径层 v0.96 ②）：键是协议名，**填了哪个协议就是
	// 声明了哪个**，至少一个；从映射里去掉一个协议 = 取消声明，服务端拒绝删空。
	// 收 map 而不是直接收 store.BaseURLs：结构体对认不得的键是静默丢弃，而这里拼错
	// 协议名的下场该是一条点名的 400，不是一个悄悄少了协议的渠道。
	BaseURL map[string]string `json:"base_url"`
	// KeyMode 是凭证选取模式 polling/random（口径层 v0.44 起露在凭证池弹窗里，不在渠道表单）。
	KeyMode string `json:"key_mode"`
	// MaxConcurrency 是渠道级并发上限（口径层 v0.49）：0 = 不限。指针留 nil 表示
	// 「请求体没提」，缺省不动库里那一列（同 KeyMode 的整体覆盖陷阱）。
	MaxConcurrency *int `json:"max_concurrency"`
	// SupportsCompaction 是 compaction 能力位（口径层 v0.54）：上游认不认 Codex 的
	// compaction_trigger。指针的 nil 同 MaxConcurrency——缺省不动那一列。
	SupportsCompaction *bool `json:"supports_compaction"`
	// SupportsStatefulResponses 是有状态续链能力位（口径层 v0.88）：上游认不认
	// previous_response_id。指针的 nil 同上——缺省不动那一列；这一位默认是 true，
	// 借零值当哨兵会让每次保存都把渠道悄悄关掉。
	SupportsStatefulResponses *bool `json:"supports_stateful_responses"`
	// Provider 是 models.dev 标注（口径层 §2.10，#74）：只服务建议价与图标分组，
	// 不参与路由。指针的 nil 同上——缺省时建成「未标注」。
	Provider   *string `json:"provider"`
	Disabled   bool    `json:"disabled"`
	Credential string  `json:"credential"`
}

// parseBaseURLs 把请求体里的「协议名 → 地址」映射落到 store.BaseURLs。收 map 的
// 理由见 channelInput.BaseURL；拼错协议名回点名的 400。
func parseBaseURLs(m map[string]string) (store.BaseURLs, error) {
	var urls store.BaseURLs
	for key, url := range m {
		p := protocol.Normalize(protocol.Protocol(strings.TrimSpace(key)))
		if !p.Valid() {
			return store.BaseURLs{}, store.InvalidInput{
				Reason: "base_url 的键 " + strconv.Quote(key) + " 不是 anthropic/openai/openai_responses 之一"}
		}
		urls.Set(p, strings.TrimSpace(url))
	}
	return urls, nil
}

func (in channelInput) toStore() (store.ChannelInput, error) {
	urls, err := parseBaseURLs(in.BaseURL)
	if err != nil {
		return store.ChannelInput{}, err
	}
	return store.ChannelInput{
		Name:                      strings.TrimSpace(in.Name),
		BaseURLs:                  urls,
		KeyMode:                   strings.TrimSpace(in.KeyMode),
		MaxConcurrency:            in.MaxConcurrency,
		SupportsCompaction:        in.SupportsCompaction,
		SupportsStatefulResponses: in.SupportsStatefulResponses,
		Provider:                  in.Provider,
		Disabled:                  in.Disabled,
	}, nil
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
		input, err := in.toStore()
		if err != nil {
			return nil, err
		}
		id, err := store.CreateChannel(ctx, tx, input)
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

// 渠道的修改是四笔按意图的字段写（#48 批2），不再有整体覆盖的 PUT：页面上哪个控件
// 动了就打哪一笔，前端不回传自己没编辑的字段，「哪些列动」的判断全在 store 的意图
// writer 里。

func (h *Handler) putChannelBaseURL(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		BaseURL map[string]string `json:"base_url"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		urls, err := parseBaseURLs(in.BaseURL)
		if err != nil {
			return err
		}
		return store.UpdateChannelBaseURLs(ctx, tx, id, urls)
	})
}

func (h *Handler) putChannelKeyMode(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		KeyMode string `json:"key_mode"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.UpdateChannelKeyMode(ctx, tx, id, in.KeyMode)
	})
}

func (h *Handler) putChannelDisabled(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Disabled bool `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.SetChannelDisabled(ctx, tx, id, in.Disabled)
	})
}

func (h *Handler) putChannelSettings(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in store.ChannelSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		fail(c, http.StatusBadRequest, "渠道名不能为空")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.UpdateChannelSettings(ctx, tx, id, in)
	})
}

// probeInput 是一次检测的入参（口径层 v0.96 ③）：点哪把凭证用哪把（含已停用）、
// 全部纳管模型或单选一个、勾了哪几个协议。勾选不落库，结果也不落库。
type probeInput struct {
	CredentialID int64 `json:"credential_id"`
	// Model 空串 = 全部启用中的纳管模型；非空必须是其中之一。
	Model     string   `json:"model"`
	Protocols []string `json:"protocols"`
}

// probeChannel 跑一次模型级检测：bind → upstream.ProbeChannel → JSON。检测的全部
// 策略（选择校验、fan-out、矩阵组装、保密规则）在 upstream.ProbeChannel 里（#51），
// 这里只是它的 HTTP adapter。
func (h *Handler) probeChannel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in probeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	target, err := store.ChannelProbeTarget(c.Request.Context(), h.db, id)
	if err != nil {
		h.writeError(c, err)
		return
	}
	m, err := upstream.ProbeChannel(c.Request.Context(), target, upstream.ProbeSelection{
		CredentialID: in.CredentialID,
		Model:        in.Model,
		Protocols:    in.Protocols,
	})
	if err != nil {
		// 选择类错误在 upstream 只是「参数对不上渠道现状」，翻成 400 是这层 adapter 的事。
		if sel, ok := errors.AsType[upstream.SelectionError](err); ok {
			fail(c, http.StatusBadRequest, sel.Reason)
			return
		}
		h.writeError(c, err)
		return
	}
	// 只报渠道名与规模，不报 base_url，更不报凭证值。响应带凭证名——403 的格子要
	// 靠它说清「用的是哪把」（那正是 403 的凭证相关含义）。
	h.log.Info("模型检测", "channel", target.Name, "models", len(m.Rows), "protocols", m.Protocols.String())
	c.JSON(http.StatusOK, gin.H{
		"credential": m.Credential,
		"models":     m.Rows,
	})
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

// fetchChannelModels 朝渠道声明的每个协议侧拉一次模型列表，回给表单做预勾选
// （口径层 v0.40）。
//
// **拉回来的东西不落库、不参与路由**：它只是替人把「这个模型在哪个协议侧存在」看一眼，
// 落库的仍是人在表单上确认过的配置。中转站的 `/v1/models` 返回一份写死的大列表是常态，
// 直接采信等于把一份会撒谎的缓存放进请求路径——那正是 §2.2 拒绝把探测做成闸的理由。
//
// 只用第一把**启用**凭证，不像 Probe 那样逐把跑：那边逐把是因为「这把停用的凭证还坏
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
	// 各协议打各的出站根地址（口径层 v0.96 ②，#49）：哪个协议用哪个地址的知识在
	// upstream 里，这儿只把整份映射递过去。
	results := upstream.ListModelsFor(c.Request.Context(), target.BaseURLs, cred)
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
	for line := range strings.SplitSeq(in.Credentials, "\n") {
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
		// MaxInputTokens 是输入上限（估算）（口径层 v0.99）。指针理由同上：
		// nil 不动，0 是显式清成「不限」，两种意图在 JSON 里不能长一样。
		MaxInputTokens *int `json:"max_input_tokens"`
		// Prices 是四价（口径层 §2.10，#74）。外层指针 nil = 没提不动；提了就是
		// 整组覆盖，组内的 null = 清回未定价、0 = 真免费（见 store.ChannelModelPrices）。
		Prices *store.ChannelModelPrices `json:"prices"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		if err := store.SetChannelModelDisabled(ctx, tx, id, in.Disabled); err != nil {
			return err
		}
		if in.MaxInputTokens != nil {
			if err := store.SetChannelModelMaxInputTokens(ctx, tx, id, *in.MaxInputTokens); err != nil {
				return err
			}
		}
		if in.Prices != nil {
			if err := store.SetChannelModelPrices(ctx, tx, id, *in.Prices); err != nil {
				return err
			}
		}
		if in.Protocols == nil {
			return nil
		}
		return store.SetChannelModelProtocols(ctx, tx, id, toProtocolSet(*in.Protocols))
	})
}

// ── models.dev 快照（口径层 §2.10 计价，#68/#74）─────────────────────────
//
// 两个只读端点，读的都是 go:embed 的裁剪快照，不碰库也不打上游。**只做建议**：
// provider 列表灌「上游设置」的标注下拉，四价给填价 UI 摆参考——采不采纳都是人在
// 条目上点过才落库。快照坏了（生成/提交环节的错）回 500 点名，别静默回空列表——
// 那会把「发版资产坏了」显示成「models.dev 没这个 provider」。

func (h *Handler) pricingProviders(c *gin.Context) {
	providers, err := pricing.Providers()
	if err != nil {
		h.log.Error("读 models.dev 快照失败", "err", err)
		fail(c, http.StatusInternalServerError, "内置 models.dev 快照读不出来，这是发版资产的问题")
		return
	}
	c.JSON(http.StatusOK, providers)
}

func (h *Handler) pricingModels(c *gin.Context) {
	provider := strings.TrimSpace(c.Query("provider"))
	if provider == "" {
		fail(c, http.StatusBadRequest, "缺 provider 参数：models.dev 的 provider id")
		return
	}
	prices, err := pricing.ModelPrices(provider)
	if err != nil {
		h.log.Error("读 models.dev 快照失败", "err", err)
		fail(c, http.StatusInternalServerError, "内置 models.dev 快照读不出来，这是发版资产的问题")
		return
	}
	// 查无此家回空对象不报错：标注是自由文本、快照随发版才更新，「没有建议」是
	// 正常答案。map 为 nil 时显式给个空的，别让前端拿到 null 还得判一层。
	if prices == nil {
		prices = map[string]pricing.ModelPrice{}
	}
	c.JSON(http.StatusOK, gin.H{"provider": provider, "models": prices})
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

// keyMine 判「这把 key 的明文与编辑对这个登录者开不开放」（#63：明文仅 key 主人
// 可见，admin 对他人的 key 只做元数据治理）。无主 key 对 admin 按「我的」对待：
// 它没有主人可言，而认领规则（#66）本来就把它归给第一个 admin，只差一次重启——
// 声明形态首启的那批 key 也因此在管理端照旧可见可复制，不倒退 v0.47。
func keyMine(k store.APIKey, su store.SessionUser) bool {
	if k.UserID != nil {
		return *k.UserID == su.ID
	}
	return su.Role == store.RoleAdmin
}

// maskKeys 回填 Mine 并抹掉他人 key 的明文。抹在出口这一处而不是 store：store
// 不知道谁在看，而「谁在看」正是这条规则的全部内容。
func maskKeys(list []store.APIKey, su store.SessionUser) []store.APIKey {
	for i := range list {
		list[i].Mine = keyMine(list[i], su)
		if !list[i].Mine {
			list[i].Key = ""
		}
	}
	return list
}

func (h *Handler) listKeys(c *gin.Context) {
	list, err := store.ListAPIKeys(c.Request.Context(), h.db)
	if err != nil {
		h.log.Error("列网关 key 失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, maskKeys(list, sessionUserFrom(c)))
}

// createKey 生成明文，哈希与明文各存一列（口径层 v0.47）。
//
// 明文照旧在这个响应里回一次，但它不再是唯一的一次：列表接口也带明文，页面上随时
// 能看能复制。加列之前建的 key 拿不回来——哈希不可逆，那些只能删了重建。
//
// 归属落在**登录者本人**名下（#63）：admin 不代建（手动建号的逃生门已够，建号后
// 用户自己建 key），于是这个 handler 管理端与「我的 Key」两条路由共用——两边建的
// 都只能是自己的 key，差别只在挂不挂 admin 闸与写闸。
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
	owner := sessionUserFrom(c).ID
	h.writeResult(c, func(ctx context.Context, tx *sql.Tx) (any, error) {
		id, err := store.CreateAPIKey(ctx, tx, name, auth.Hash(plain), plain,
			normalizeAllowed(in.AllowedModels), &owner)
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
	allowed := normalizeAllowed(in.AllowedModels)
	// 他人的 key 只许元数据治理（#63）：可停用/启用，不许改名与白名单——那两样是
	// key 主人的自我管理面。判在事务外用 h.db 读即可：与写之间隔着的那一瞬不构成
	// 可利用的竞态（归属只会被启动认领改动，请求期间不变）。
	cur, err := store.GetAPIKey(c.Request.Context(), h.db, id)
	if err != nil {
		h.writeError(c, err)
		return
	}
	if !keyMine(cur, sessionUserFrom(c)) &&
		(name != cur.Name || allowed != normalizeAllowed(cur.AllowedModels)) {
		fail(c, http.StatusForbidden, "他人的 API Key 只能停用或删除，名字与可访问模型归 key 主人自己管")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.UpdateAPIKey(ctx, tx, id, name, allowed, in.Disabled)
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

// ── 我的 Key（用户侧，#73；页面形态归 #76）─────────────────────────────
//
// 与治理面共用 createKey（建的本来就只能是自己的），列表与改删则把归属焊死在
// store 的 WHERE 里——别人的 id 与不存在的 id 同一个 404，不区分。

func (h *Handler) listMyKeys(c *gin.Context) {
	su := sessionUserFrom(c)
	list, err := store.ListUserAPIKeys(c.Request.Context(), h.db, su.ID)
	if err != nil {
		h.log.Error("列我的 key 失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, maskKeys(list, su))
}

func (h *Handler) updateMyKey(c *gin.Context) {
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
	su := sessionUserFrom(c)
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.UpdateUserAPIKey(ctx, tx, su.ID, id, name, normalizeAllowed(in.AllowedModels), in.Disabled)
	})
}

func (h *Handler) deleteMyKey(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	su := sessionUserFrom(c)
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.DeleteUserAPIKey(ctx, tx, su.ID, id)
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
	for item := range strings.SplitSeq(raw, ",") {
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
// by=credential（按上游凭证，v0.38）。时间范围见 usageRange。
//
// 认不得的 by 当默认处理而不是报错：这是个只影响展示的查询参数，写错了不该让整个
// 页面打不开（同 clampQuery 的立论）。
func (h *Handler) usage(c *gin.Context) {
	dim := store.UsageByModel
	switch c.Query("by") {
	case store.UsageByKey:
		dim = store.UsageByKey
	case store.UsageByCredential:
		dim = store.UsageByCredential
	}
	rng, ok := usageRange(c)
	if !ok {
		return
	}
	rows, err := store.UsageBy(c.Request.Context(), h.db, rng, dim)
	if err != nil {
		h.log.Error("汇总用量失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	// 走 from/to 那条路时不回 days：这一发算的是那一段，回一个没参与的窗口档位是撒谎。
	if !rng.Spanned() {
		c.JSON(http.StatusOK, gin.H{"days": rng.Days, "by": dim, "rows": rows})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"by":   dim,
		"from": rng.From.Unix(),
		"to":   rng.To.Unix(),
		"rows": rows,
	})
}

// usageRange 解析用量查询的记账范围：默认「近 days 个自然日」，`from`/`to` 两个都给
// 时顶掉 days（unix 秒，半开区间 [from, to)），只给一个当没给。
//
// **解析失败回 400，不走「认不得就忽略」那条**：clampQuery 与 by 的立论是「写错了不该
// 让整个页面打不开」，那条管的是只影响**展示**的参数；from/to 定的是**记账范围**，
// 静默忽略会拿整窗的合计去回答「这一小时」——那不是显示得不对，是数字本身错了且看不
// 出来。例外只开给这两个参数，days / by / unit 照旧忽略。
//
// 解析不出就自己回 400 并返回 false——调用方直接 return。
func usageRange(c *gin.Context) (store.UsageRange, bool) {
	r := store.UsageRange{Days: clampQuery(c, "days", 7, 1, 365)}
	from, okFrom := unixQuery(c, "from")
	to, okTo := unixQuery(c, "to")
	switch {
	case !okFrom || !okTo: // 写错了
	case from.IsZero() || to.IsZero(): // 只给一端，或一端都没给：照 days 走
		return r, true
	case to.After(from):
		r.From, r.To = from, to
		return r, true
	}
	// 措辞里不带数字：这条 400 的要害是「一个数都别给」——给了就有人当成结果读。
	fail(c, http.StatusBadRequest, "from/to 必须是 unix 秒，且 to 晚于 from")
	return r, false
}

// unixQuery 读一个 unix 秒的查询参数。缺失（含空串）回零值 + true，解析失败回 false。
func unixQuery(c *gin.Context, name string) (time.Time, bool) {
	raw := c.Query(name)
	if raw == "" {
		return time.Time{}, true
	}
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// usageBuckets 把最近 days 天按 unit 分桶，排行页第一层「节律带」用它。
//
// 与 usage 分成两个端点而不是塞进同一份回包：分桶只跟 days / unit 有关、与聚合维度
// 无关，合在一起的话每切一次「按模型 / 按 API Key / 按上游凭证」都得把它重算一遍。
//
// 认不得的 unit 当 day 处理而不是报错：同 by，这是个只影响展示的查询参数。
func (h *Handler) usageBuckets(c *gin.Context) {
	days := clampQuery(c, "days", 7, 1, 365)
	unit := store.BucketDay
	if c.Query("unit") == store.BucketHour {
		unit = store.BucketHour
	}
	rows, err := store.UsageBuckets(c.Request.Context(), h.db, days, unit, time.Now())
	if err != nil {
		h.log.Error("按区间分桶汇总用量失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days, "unit": unit, "rows": rows})
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
