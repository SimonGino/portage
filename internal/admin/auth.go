package admin

// auth.go 是注册与找回那半边（#72，口径层 §2.10 #62）：邀请码注册、邮箱验证链接、
// 找回/重置密码。全部躲在两道闸后——形态闸（无 admin 不挂载）与 #66 互斥闸
// （声明形态不注册，见 Mount）。
//
// 一条贯穿的口径：**SMTP 未配则注册入口关闭**（#62 决议 3）。判在服务端不判在
// 页面——auth-config 只是给前端画界面的，真正的闸必须长在 register 里。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/mail"
	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// resendCooldown 是验证信重发冷却（#62 决议 2）。冷却看「上次发是什么时候」，
// 与那封信里的 token 是否还有效无关。
const resendCooldown = 60 * time.Second

// siteURL 取站点外部 URL，去掉尾斜杠。邮件链接与 OAuth 回调地址都从它拼——
// 它是 settings 里的运行期配置（#62 决议 7），没配就是空串。
func (h *Handler) siteURL(ctx context.Context) (string, error) {
	v, err := store.GetSetting(ctx, h.db, store.SettingSiteURL)
	return strings.TrimSuffix(strings.TrimSpace(v), "/"), err
}

// mailReady 判「邮件这条路走不走得通」：SMTP 配好了、站点外部 URL 也有——链接
// 类邮件缺了后者拼不出可点的地址，只配 SMTP 等于寄一封指路到空气的信。
func (h *Handler) mailReady(ctx context.Context) (bool, error) {
	cfg, err := mail.LoadConfig(ctx, h.db)
	if err != nil {
		return false, err
	}
	if !cfg.Configured() {
		return false, nil
	}
	site, err := h.siteURL(ctx)
	return site != "", err
}

// authConfig 告诉登录页「有哪些门」：注册开不开、OAuth 有哪几家。不鉴权——
// 看门的人正是还没进门的人。secret 一个字不出，只有布尔与 provider 名。
func (h *Handler) authConfig(c *gin.Context) {
	ctx := c.Request.Context()
	ready, err := h.mailReady(ctx)
	if err != nil {
		h.log.Error("读邮件配置失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	providers := []string{}
	for _, p := range oauthProviders {
		cfg, err := h.oauthConfig(ctx, p)
		if err != nil {
			h.log.Error("读 OAuth 配置失败", "provider", p, "err", err)
			fail(c, http.StatusInternalServerError, "读取失败")
			return
		}
		if cfg != nil {
			providers = append(providers, p)
		}
	}
	out := gin.H{"registration_open": ready, "oauth": providers}
	if !ready {
		// 给注册入口关闭时的那句提示（#62 决议 3）。说「管理员未配」而不是裸一个
		// 布尔：站在门外的人该知道找谁，而不是以为自己点坏了什么。
		out["registration_closed_reason"] = "管理员未配置邮件发信，注册暂不可用；请联系管理员开通账号"
	}
	c.JSON(http.StatusOK, out)
}

// register 是邀请码注册（#62 决议 1/2）：码 + 邮箱 + 密码。建号、销码在同一个
// 事务里——码用了号没建成、或号建了码还活着，都是要不得的半截。
func (h *Handler) register(c *gin.Context) {
	var in struct {
		InviteCode string `json:"invite_code"`
		Email      string `json:"email"`
		Password   string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(in.Password) < 8 {
		fail(c, http.StatusBadRequest, "密码至少 8 位")
		return
	}
	ctx := c.Request.Context()
	if ready, err := h.mailReady(ctx); err != nil {
		h.log.Error("读邮件配置失败", "err", err)
		fail(c, http.StatusInternalServerError, "注册失败")
		return
	} else if !ready {
		// 503 不是 400：不是表单填错了，是这台实例当下没开这扇门。
		fail(c, http.StatusServiceUnavailable, "注册暂不可用：管理员未配置邮件发信")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		h.log.Error("哈希密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "注册失败")
		return
	}
	var uid int64
	err = h.inTx(ctx, func(tx store.Conn) error {
		hs := string(hash)
		var err error
		// email_verified = 0：注册通道的邮箱必须验证（#62 决议 2）；豁免只给
		// admin 逃生门（createUser）与 OAuth 的上游 verified 邮箱。
		if uid, err = store.CreateUser(ctx, tx, in.Email, &hs, "", store.RoleUser, false); err != nil {
			return err
		}
		return store.ConsumeInviteCode(ctx, tx, strings.TrimSpace(in.InviteCode), uid)
	})
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		fail(c, http.StatusBadRequest, err.Error())
		return
	case isConstraint(err):
		fail(c, http.StatusConflict, "该邮箱已注册过：直接登录，忘了密码走「忘记密码」")
		return
	case err != nil:
		h.log.Error("注册失败", "err", err)
		fail(c, http.StatusInternalServerError, "注册失败")
		return
	}
	// 验证信在事务外发：发信是网络动作，抱进事务会让一台慢 SMTP 把唯一的库连接
	// 拖住。发失败不撤销注册——号已经在了，去验证页上有重发按钮，那才是补救之路。
	mailErr := h.sendVerifyMail(ctx, uid, store.NormalizeEmail(in.Email))
	if mailErr != nil {
		h.log.Error("发验证邮件失败", "user", uid, "err", mailErr)
	}
	// 未验证也发会话（#62 决议 2：可登录但功能全锁）——登进来看到的是去验证页。
	token, ttl, err := store.CreateSession(ctx, h.db, uid)
	if err != nil {
		h.log.Error("生成会话失败", "err", err)
		fail(c, http.StatusInternalServerError, "注册成功但登录失败，请手动登录")
		return
	}
	h.setSessionCookie(c, token, int(ttl.Seconds()))
	c.JSON(http.StatusOK, gin.H{"ok": true, "mail_sent": mailErr == nil})
}

// sendVerifyMail 发一封带验证链接的信。链接从站点外部 URL 拼（#62 决议 2），
// token 24h 有效。
func (h *Handler) sendVerifyMail(ctx context.Context, userID int64, email string) error {
	site, err := h.siteURL(ctx)
	if err != nil {
		return err
	}
	token, err := store.CreateAuthToken(ctx, h.db, store.TokenVerifyEmail, &userID, "", store.TokenTTLVerifyEmail)
	if err != nil {
		return err
	}
	body := fmt.Sprintf(`你好，

点击下面的链接完成 Portage 的邮箱验证（24 小时内有效）：

%s/panel/verify?token=%s

如果这不是你发起的注册，忽略这封邮件即可。`, site, token)
	return h.mail(ctx, h.db, email, "验证你的 Portage 邮箱", body)
}

// verifyEmail 消费验证链接里的 token。不要求会话——点链接的浏览器未必是注册时
// 那个，token 本身就是凭据。
func (h *Handler) verifyEmail(c *gin.Context) {
	var in struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	ctx := c.Request.Context()
	uid, _, err := store.ConsumeAuthToken(ctx, h.db, in.Token, store.TokenVerifyEmail)
	if errors.Is(err, store.ErrNotFound) || (err == nil && uid == nil) {
		fail(c, http.StatusBadRequest, "链接无效或已过期：重新登录后再发一封验证邮件")
		return
	}
	if err != nil {
		h.log.Error("消费验证 token 失败", "err", err)
		fail(c, http.StatusInternalServerError, "验证失败")
		return
	}
	if err := store.SetEmailVerified(ctx, h.db, *uid); err != nil {
		h.log.Error("写验证态失败", "err", err)
		fail(c, http.StatusInternalServerError, "验证失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// resendVerifyEmail 重发验证信，60s 冷却（#62 决议 2）。要会话不要验证态——
// 未验证的人正是它的用户，这是「功能全锁」清单外的那几个动作之一。
//
// 发信失败这里**照实回 502**，与 requestPasswordReset 的匿名 200 刻意不同：那边
// 不鉴权、按邮箱分叉的状态码是存在性预言机；这边有会话、收件人就是本人的邮箱，
// 「发没发出去」是他该知道的事——瞒着他只会让人对着收件箱空等 60 秒再点一次。
func (h *Handler) resendVerifyEmail(c *gin.Context) {
	ctx := c.Request.Context()
	me := sessionUser(c)
	u, err := store.GetUser(ctx, h.db, me.ID)
	if err != nil {
		h.log.Error("读用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "发送失败")
		return
	}
	if u.EmailVerified {
		fail(c, http.StatusBadRequest, "邮箱已经验证过了")
		return
	}
	if ready, err := h.mailReady(ctx); err != nil {
		h.log.Error("读邮件配置失败", "err", err)
		fail(c, http.StatusInternalServerError, "发送失败")
		return
	} else if !ready {
		fail(c, http.StatusServiceUnavailable, "管理员未配置邮件发信，暂时发不了验证邮件")
		return
	}
	last, err := store.LastAuthTokenIssue(ctx, h.db, store.TokenVerifyEmail, me.ID)
	if err != nil {
		h.log.Error("查发信记录失败", "err", err)
		fail(c, http.StatusInternalServerError, "发送失败")
		return
	}
	if wait := resendCooldown - time.Since(time.Unix(last, 0)); last > 0 && wait > 0 {
		c.Header("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		fail(c, http.StatusTooManyRequests, "发得太频繁：60 秒内只能发一封")
		return
	}
	if err := h.sendVerifyMail(ctx, me.ID, u.Email); err != nil {
		h.log.Error("发验证邮件失败", "user", me.ID, "err", err)
		fail(c, http.StatusBadGateway, "发信失败："+err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// requestPasswordReset 发重置密码邮件（#62 决议 6）。**除「JSON 坏了」与「SMTP
// 根本没配」（全局状态，与邮箱无关）外，一律回同一个匿名 200**——找不到、已停用、
// 连发信失败都是：这个接口不鉴权，任何按邮箱分叉的状态码都是免费的邮箱存在性
// 预言机（发信失败若回 502，「502 = 这个邮箱在库里」）。发信失败只落服务端日志，
// 排障看日志不看响应。
func (h *Handler) requestPasswordReset(c *gin.Context) {
	var in struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	ctx := c.Request.Context()
	if ready, err := h.mailReady(ctx); err != nil {
		h.log.Error("读邮件配置失败", "err", err)
		fail(c, http.StatusInternalServerError, "发送失败")
		return
	} else if !ready {
		fail(c, http.StatusServiceUnavailable, "管理员未配置邮件发信，暂时找回不了密码；请联系管理员")
		return
	}
	u, err := store.GetUserAuthByEmail(ctx, h.db, in.Email)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if err != nil {
		h.log.Error("读用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "发送失败")
		return
	}
	site, err := h.siteURL(ctx)
	if err != nil {
		h.log.Error("读站点地址失败", "err", err)
		fail(c, http.StatusInternalServerError, "发送失败")
		return
	}
	token, err := store.CreateAuthToken(ctx, h.db, store.TokenResetPassword, &u.ID, "", store.TokenTTLResetPassword)
	if err != nil {
		h.log.Error("生成重置 token 失败", "err", err)
		fail(c, http.StatusInternalServerError, "发送失败")
		return
	}
	// OAuth-only 账号走同一封信「设置密码」（#62 决议 6）：流程一模一样，只有
	// 措辞分两句——对没有密码的人说「重置」像在指责他忘了一个不存在的东西。
	action := "重置密码"
	if !u.HasPassword {
		action = "设置密码"
	}
	body := fmt.Sprintf(`你好，

点击下面的链接为 Portage 账号%s（30 分钟内有效，只能用一次）：

%s/panel/reset?token=%s

如果这不是你发起的，忽略这封邮件即可，密码不会被改动。`, action, site, token)
	if err := h.mail(ctx, h.db, u.Email, "Portage "+action, body); err != nil {
		// 只记日志，不改响应：见函数头——按邮箱分叉的状态码就是存在性预言机。
		h.log.Error("发重置邮件失败", "user", u.ID, "err", err)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// confirmPasswordReset 消费重置链接：设新密码、吊销该用户全部会话（#62 决议 6）。
func (h *Handler) confirmPasswordReset(c *gin.Context) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(in.Password) < 8 {
		fail(c, http.StatusBadRequest, "新密码至少 8 位")
		return
	}
	ctx := c.Request.Context()
	uid, _, err := store.ConsumeAuthToken(ctx, h.db, in.Token, store.TokenResetPassword)
	if errors.Is(err, store.ErrNotFound) || (err == nil && uid == nil) {
		fail(c, http.StatusBadRequest, "链接无效或已过期：重新发起一次「忘记密码」")
		return
	}
	if err != nil {
		h.log.Error("消费重置 token 失败", "err", err)
		fail(c, http.StatusInternalServerError, "重置失败")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		h.log.Error("哈希密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "重置失败")
		return
	}
	if err := h.setPassword(ctx, *uid, string(hash)); err != nil {
		h.log.Error("写密码失败", "err", err)
		fail(c, http.StatusInternalServerError, "重置失败")
		return
	}
	// 旧邮件里的兄弟链接一并作废：重置成功之后，任何一条还活着的重置链接都是
	// 一扇没关的门。
	if err := store.DeleteAuthTokens(ctx, h.db, store.TokenResetPassword, *uid); err != nil {
		h.log.Error("清理重置 token 失败", "err", err)
	}
	// 吊销全部会话（#62 决议 6）：重置密码的常见动机正是「怕别人还登着」。
	if err := store.DeleteUserSessions(ctx, h.db, *uid); err != nil {
		h.log.Error("吊销会话失败", "err", err)
		fail(c, http.StatusInternalServerError, "密码已重置，但吊销旧会话失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// inTx 在一个事务里跑 fn。与 writeResult 的区别：不跑 store.Validate——用户表
// 不是业务配置，那道「能保存的一定能启动」的闸管不着它，白跑一遍六项检查。
func (h *Handler) inTx(ctx context.Context, fn func(tx store.Conn) error) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后这里是 no-op
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// redirectAdmin 把浏览器导航送回 SPA，带上给登录页看的一句话（query 上的
// oauth_error）。空串即不带参数。
func redirectAdmin(c *gin.Context, path string, params url.Values) {
	target := "/panel/" + strings.TrimPrefix(path, "/")
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	c.Redirect(http.StatusFound, target)
}
