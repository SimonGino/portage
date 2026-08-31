package admin

// users.go 是用户治理那半边（#72）：建用户逃生门（#62 决议 3）、邀请码生成/撤销
// （决议 1）、SMTP / OAuth client / 站点外部 URL 配置（决议 7）。全部 admin only，
// 且在 #66 互斥闸后（声明形态不注册，见 Mount）。

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/mail"
	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// ── 用户 ────────────────────────────────────────────────────────────────

func (h *Handler) listUsers(c *gin.Context) {
	list, err := store.ListUsers(c.Request.Context(), h.db)
	if err != nil {
		h.log.Error("列用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, list)
}

// createUser 是 SMTP 未配时的逃生门（#62 决议 3）：admin 直接建号、**标记已验证**
// ——验证信的职责是证明邮箱归属，admin 面对面发号时这份证明由人担保。
func (h *Handler) createUser(c *gin.Context) {
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(in.Password) < 8 {
		fail(c, http.StatusBadRequest, "密码至少 8 位")
		return
	}
	if in.Role == "" {
		in.Role = store.RoleUser
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		h.log.Error("哈希密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	hs := string(hash)
	uid, err := store.CreateUser(c.Request.Context(), h.db, in.Email, &hs, in.DisplayName, in.Role, true)
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		fail(c, http.StatusBadRequest, err.Error())
		return
	case isConstraint(err):
		fail(c, http.StatusConflict, "该邮箱已注册过")
		return
	case err != nil:
		h.log.Error("建用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "创建失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": uid})
}

// setUserRole 任免角色（#61：admin 可任免，多 admin 允许）。自己降自己也走同一条
// 路——守门条件只认「最后一个启用的 admin」，不认「是不是本人」：还有别的 admin 在，
// 自降是合法的交接；只剩自己时，降级被拦下的理由与降别人完全一样。角色是每次请求
// 联查出来的（TouchSession），降级即时生效，不用吊销会话。
func (h *Handler) setUserRole(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "id 不是数字")
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.userWrite(c, store.SetUserRole(c.Request.Context(), h.db, id, in.Role))
}

// setUserDisabled 停用/启用。停用即冻结（#61）：已发出的会话在下一次请求就失效
// （TouchSession 联查 disabled），会话行留着——启用后老会话照旧能用，沿 #71 的裁定。
func (h *Handler) setUserDisabled(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "id 不是数字")
		return
	}
	var in struct {
		Disabled bool `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.userWrite(c, store.SetUserDisabled(c.Request.Context(), h.db, id, in.Disabled))
}

// setUserQuota 设月度配额（#75，口径层 §2.10）：null = 不限额（默认）、0 = 封停、
// 正数 = 每月美元上限。指针直传——JSON 的 null 与数值 0 在这里语义完全不同，
// 不能用零值兜。改额即时生效：闸每次请求现算 SUM，没有计数器要同步。
func (h *Handler) setUserQuota(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "id 不是数字")
		return
	}
	var in struct {
		MonthlyQuotaUSD *float64 `json:"monthly_quota_usd"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	h.userWrite(c, store.SetUserQuota(c.Request.Context(), h.db, id, in.MonthlyQuotaUSD))
}

// userWrite 是两个用户治理写接口共用的收尾：ErrNotFound→404、守门条件→400。
func (h *Handler) userWrite(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, "用户不存在")
	case errors.Is(err, store.ErrInvalidInput):
		fail(c, http.StatusBadRequest, err.Error())
	case err != nil:
		h.log.Error("写用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "保存失败")
	default:
		c.Status(http.StatusNoContent)
	}
}

// ── 邀请码 ──────────────────────────────────────────────────────────────

func (h *Handler) listInviteCodes(c *gin.Context) {
	list, err := store.ListInviteCodes(c.Request.Context(), h.db)
	if err != nil {
		h.log.Error("列邀请码失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *Handler) createInviteCodes(c *gin.Context) {
	var in struct {
		Count int `json:"count"`
		// 有效期按小时收（0 = 不过期）：邀请码的常见档位是「几天内用掉」，
		// 小时粒度够了，再细是假精度。
		ExpiresInHours int `json:"expires_in_hours"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if in.Count == 0 {
		in.Count = 1
	}
	if in.ExpiresInHours < 0 {
		fail(c, http.StatusBadRequest, "有效期不能是负数")
		return
	}
	codes, err := store.CreateInviteCodes(c.Request.Context(), h.db,
		in.Count, time.Duration(in.ExpiresInHours)*time.Hour)
	if errors.Is(err, store.ErrInvalidInput) {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		h.log.Error("生成邀请码失败", "err", err)
		fail(c, http.StatusInternalServerError, "生成失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"codes": codes})
}

func (h *Handler) revokeInviteCode(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "id 不是数字")
		return
	}
	err = store.RevokeInviteCode(c.Request.Context(), h.db, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, "记录不存在")
	case errors.Is(err, store.ErrInvalidInput):
		fail(c, http.StatusBadRequest, err.Error())
	case err != nil:
		h.log.Error("撤销邀请码失败", "err", err)
		fail(c, http.StatusInternalServerError, "撤销失败")
	default:
		c.Status(http.StatusNoContent)
	}
}

// ── SMTP / OAuth / 站点地址配置 ─────────────────────────────────────────

// getAuthSettings 回配置现状。**secret 三样（SMTP 密码、两家 client_secret）只回
// 「设没设」，永不回值**——同「上游 key 只存服务端」口径（#62 决议 7）。
// 顺带把两家的回调 URL 拼好：注册 OAuth 应用要填的就是它，让人复制不让人拼。
func (h *Handler) getAuthSettings(c *gin.Context) {
	ctx := c.Request.Context()
	get := func(key string) string {
		v, err := store.GetSetting(ctx, h.db, key)
		if err != nil {
			h.log.Error("读设置失败", "key", key, "err", err)
		}
		return v
	}
	site := strings.TrimSuffix(strings.TrimSpace(get(store.SettingSiteURL)), "/")
	callback := func(provider string) string {
		if site == "" {
			return ""
		}
		return site + "/panel/oauth/" + provider + "/callback"
	}
	c.JSON(http.StatusOK, gin.H{
		"site_url": site,
		"smtp": gin.H{
			"host":         get(store.SettingSMTPHost),
			"port":         get(store.SettingSMTPPort),
			"encryption":   get(store.SettingSMTPEncryption),
			"username":     get(store.SettingSMTPUsername),
			"from":         get(store.SettingSMTPFrom),
			"password_set": get(store.SettingSMTPPassword) != "",
		},
		"github": gin.H{
			"client_id":  get(store.SettingGitHubClientID),
			"secret_set": get(store.SettingGitHubClientSecret) != "",
		},
		"google": gin.H{
			"client_id":  get(store.SettingGoogleClientID),
			"secret_set": get(store.SettingGoogleClientSecret) != "",
		},
		"callback_urls": gin.H{
			"github": callback("github"),
			"google": callback("google"),
		},
	})
}

// authSettingsInput 全字段指针：nil = 不动那一项。secret 类同一套语义——nil 保留
// 现值（页面上 secret 从不回显，表单里那格空着不等于要清掉），空串 = 明确清空。
type authSettingsInput struct {
	SiteURL *string `json:"site_url"`
	SMTP    *struct {
		Host       *string `json:"host"`
		Port       *string `json:"port"`
		Encryption *string `json:"encryption"`
		Username   *string `json:"username"`
		Password   *string `json:"password"`
		From       *string `json:"from"`
	} `json:"smtp"`
	GitHub *oauthClientInput `json:"github"`
	Google *oauthClientInput `json:"google"`
}

type oauthClientInput struct {
	ClientID *string `json:"client_id"`
	Secret   *string `json:"secret"`
}

// putAuthSettings 写配置，改完即生效（发信与 OAuth 每次都现读 settings）。
// 校验只拦「必然用不了」的形态：加密档拼错、端口不是数、站点地址不是 http(s)。
func (h *Handler) putAuthSettings(c *gin.Context) {
	var in authSettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	type kv struct{ key, value string }
	var writes []kv
	put := func(key string, v *string) {
		if v != nil {
			writes = append(writes, kv{key, strings.TrimSpace(*v)})
		}
	}
	if in.SiteURL != nil {
		site := strings.TrimSuffix(strings.TrimSpace(*in.SiteURL), "/")
		if site != "" {
			u, err := url.Parse(site)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				fail(c, http.StatusBadRequest, "站点外部 URL 要是完整的 http(s) 地址，例如 https://portage.example.com")
				return
			}
		}
		writes = append(writes, kv{store.SettingSiteURL, site})
	}
	if s := in.SMTP; s != nil {
		if s.Encryption != nil {
			switch strings.TrimSpace(*s.Encryption) {
			case "", mail.EncryptionSTARTTLS, mail.EncryptionSSL, mail.EncryptionNone:
			default:
				fail(c, http.StatusBadRequest, "加密方式只有 starttls / ssl / none 三档")
				return
			}
		}
		if s.Port != nil && strings.TrimSpace(*s.Port) != "" {
			if p, err := strconv.Atoi(strings.TrimSpace(*s.Port)); err != nil || p < 1 || p > 65535 {
				fail(c, http.StatusBadRequest, "端口要是 1~65535 的数字")
				return
			}
		}
		put(store.SettingSMTPHost, s.Host)
		put(store.SettingSMTPPort, s.Port)
		put(store.SettingSMTPEncryption, s.Encryption)
		put(store.SettingSMTPUsername, s.Username)
		put(store.SettingSMTPPassword, s.Password)
		put(store.SettingSMTPFrom, s.From)
	}
	if g := in.GitHub; g != nil {
		put(store.SettingGitHubClientID, g.ClientID)
		put(store.SettingGitHubClientSecret, g.Secret)
	}
	if g := in.Google; g != nil {
		put(store.SettingGoogleClientID, g.ClientID)
		put(store.SettingGoogleClientSecret, g.Secret)
	}
	err := h.inTx(c.Request.Context(), func(tx store.Conn) error {
		for _, w := range writes {
			// 清空即删行：GetSetting 不区分「没设」与空串，留一行空串只会让
			// 「这项配了没有」在表里多一种写法。
			if w.value == "" {
				if err := store.DeleteSetting(c.Request.Context(), tx, w.key); err != nil {
					return err
				}
				continue
			}
			if err := store.SetSetting(c.Request.Context(), tx, w.key, w.value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		h.log.Error("写登录配置失败", "err", err)
		fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// testEmail 朝指定地址发一封测试信。SMTP 配置改完即生效的另一半是「改完立刻能
// 验证」——不然只能拿一次真实注册当试纸。
func (h *Handler) testEmail(c *gin.Context) {
	var in struct {
		To string `json:"to"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(in.To) == "" {
		fail(c, http.StatusBadRequest, "缺少收件地址")
		return
	}
	ctx := c.Request.Context()
	cfg, err := mail.LoadConfig(ctx, h.db)
	if err != nil {
		h.log.Error("读邮件配置失败", "err", err)
		fail(c, http.StatusInternalServerError, "发送失败")
		return
	}
	if !cfg.Configured() {
		fail(c, http.StatusServiceUnavailable, "SMTP 还没配好：至少要有服务器地址与发件地址")
		return
	}
	// 测试发信限时：SMTP 连不上时默认超时是分钟级，管理端的按钮不该转那么久。
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := h.mail(sendCtx, h.db, strings.TrimSpace(in.To), "Portage 邮件测试",
		"这是一封来自 Portage 的测试邮件。收到它说明 SMTP 配置可用。"); err != nil {
		// 错误原文帮人定位（拒连、鉴权失败、发件域未验证），go-mail 不会把密码
		// 写进错误——secret 不回显的口径在这里靠的是上游库的行为加这句声明。
		fail(c, http.StatusBadGateway, "发信失败："+err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
