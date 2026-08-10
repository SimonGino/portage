package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SimonGino/ai-gateway/internal/auth"
	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/store"
	"github.com/SimonGino/ai-gateway/internal/upstream"

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

// channelInput 是渠道表单。credential 只在**创建**时可选带一把——之后要换凭证走
// PUT /channels/:id/credential，而修改渠道的接口根本不看这个字段，因此「改个名字」
// 不可能顺手把凭证清空。
type channelInput struct {
	Name string `json:"name"`
	// Protocols 是支持协议集（口径层 v0.33），至少一个。
	Protocols  []string `json:"protocols"`
	BaseURL    string   `json:"base_url"`
	Disabled   bool     `json:"disabled"`
	Credential string   `json:"credential"`
}

func (in channelInput) toStore() store.ChannelInput {
	set := make(protocol.Set, 0, len(in.Protocols))
	for _, p := range in.Protocols {
		set = append(set, protocol.Protocol(strings.TrimSpace(p)))
	}
	return store.ChannelInput{
		Name:      strings.TrimSpace(in.Name),
		Protocols: set,
		BaseURL:   strings.TrimSpace(in.BaseURL),
		Disabled:  in.Disabled,
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
			if err := store.SetChannelCredential(ctx, tx, id, cred); err != nil {
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
	results := make([]upstream.ProbeResult, 0, len(target.Protocols))
	for _, p := range target.Protocols {
		results = append(results, upstream.Probe(c.Request.Context(), target.BaseURL, p, target.Credential))
	}
	// 只报渠道名，不报 base_url。
	h.log.Info("渠道协议探测", "channel", target.Name, "results", len(results))
	c.JSON(http.StatusOK, gin.H{"results": results})
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

// setChannelCredential 是凭证唯一的入口，且**只写不读**（PO 于 M3 裁定）。
// 没有对应的 GET：上游 key 只存服务端，一旦能回读，它就会出现在浏览器内存、
// devtools 的响应面板、以及任何一张截图里。
func (h *Handler) setChannelCredential(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Credential string `json:"credential"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	cred := strings.TrimSpace(in.Credential)
	if cred == "" {
		fail(c, http.StatusBadRequest, "凭证不能为空")
		return
	}
	h.write(c, func(ctx context.Context, tx *sql.Tx) error {
		return store.SetChannelCredential(ctx, tx, id, cred)
	})
}

func (h *Handler) addChannelModel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		UpstreamModel string `json:"upstream_model"`
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
		return store.AddChannelModel(ctx, tx, id, model)
	})
}

func (h *Handler) updateChannelModel(c *gin.Context) {
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
		return store.SetChannelModelDisabled(ctx, tx, id, in.Disabled)
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

// createKey 生成明文、存哈希、把明文**只回显这一次**。
//
// 服务端不留明文，所以没有「再看一次」的接口——忘了就删了重发一把。这条约束是
// key_hash 用裸 SHA-256（展开层 §7.1）的前提：明文只在这一个响应里存在过。
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
		id, err := store.CreateAPIKey(ctx, tx, name, auth.Hash(plain), normalizeAllowed(in.AllowedModels))
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

// generateKey 造一把新的网关 key：sk-aig- 前缀 + 32 位十六进制（128 bit 熵）。
//
// 前缀是给人看的——从一堆环境变量里一眼认出「这是网关的 key，不是上游的」。
func generateKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "sk-aig-" + hex.EncodeToString(buf), nil
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

func (h *Handler) listLogs(c *gin.Context) {
	limit := clampQuery(c, "limit", 100, 1, maxLogLimit)
	offset := clampQuery(c, "offset", 0, 0, 1<<20)
	rows, err := store.ListCallLogs(c.Request.Context(), h.db, limit, offset)
	if err != nil {
		h.log.Error("列调用流水失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (h *Handler) usage(c *gin.Context) {
	days := clampQuery(c, "days", 7, 1, 365)
	rows, err := store.UsageByModel(c.Request.Context(), h.db, days)
	if err != nil {
		h.log.Error("汇总用量失败", "err", err)
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
