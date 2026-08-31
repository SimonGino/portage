package admin

// oauth.go 是 GitHub/Google 登录（#72，#62 决议 4/5）。选型与坑全部按 #69 调研：
// golang.org/x/oauth2 + 授权码流 + PKCE（S256），state 落一次性 cookie；GitHub 身份
// 键用不可变数字 id（login 可改名）、邮箱走 /user/emails 取 primary+verified；
// Google 走 OIDC，身份键用 id_token 的 sub，直连 token 端点取回的 id_token 免验签。
// 不存上游 access/refresh token——登录即用即弃，顺带让 Google Testing 状态的 7 天
// 过期无感。
//
// **设计允许只配 GitHub 或完全不配 OAuth**（Google 硬限 HTTPS/禁裸 IP/公共后缀，
// 内网部署注册不了）：每家 provider 各自看 settings 里配没配，没配就不出现在
// auth-config 里、start 直接拒。回调 URL 固定 /panel/oauth/<provider>/callback，
// 管理端配置页把完整地址摆出来供复制去上游注册——共享应用没有官方旁路（#69）。

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// 支持的两家 provider。顺序即登录页按钮顺序。
var oauthProviders = []string{"github", "google"}

// oauthCookie 是 start → callback 之间的接力 cookie：state（防 CSRF）、PKCE
// verifier、这一趟是登录还是绑定。HttpOnly + 10 分钟 + 限定在 /panel/oauth 路径下。
const oauthCookie = "portage_oauth"

// oauthFlow 是那只 cookie 里装的东西。不签名：它本来就只对「同一个浏览器」有意义
// ——state 要跟回调 query 里的比对，verifier 只有配上上游签发的 code 才有用，
// 伪造自己浏览器里的 cookie 骗不到任何别人的东西。**唯一不能信它的地方是身份**：
// 绑定模式下「绑到谁名下」必须重验会话 cookie，不能从这里读（见 oauthCallback）。
type oauthFlow struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Link     bool   `json:"link,omitempty"`
}

// oauthConfig 从 settings 现读一家 provider 的 client 配置；没配齐（id 或 secret
// 缺）返回 nil——「没配」是合法形态不是错误。改完即生效靠的就是每次现读。
func (h *Handler) oauthConfig(ctx context.Context, provider string) (*oauth2.Config, error) {
	var idKey, secretKey string
	var endpoint oauth2.Endpoint
	var scopes []string
	switch provider {
	case "github":
		idKey, secretKey = store.SettingGitHubClientID, store.SettingGitHubClientSecret
		endpoint = endpoints.GitHub
		// email 要 user:email：/user 的 email 字段在用户未公开时是 null（#69）。
		scopes = []string{"read:user", "user:email"}
	case "google":
		idKey, secretKey = store.SettingGoogleClientID, store.SettingGoogleClientSecret
		endpoint = endpoints.Google
		scopes = []string{"openid", "email", "profile"}
	default:
		return nil, nil
	}
	id, err := store.GetSetting(ctx, h.db, idKey)
	if err != nil {
		return nil, err
	}
	secret, err := store.GetSetting(ctx, h.db, secretKey)
	if err != nil {
		return nil, err
	}
	if id == "" || secret == "" {
		return nil, nil
	}
	site, err := h.siteURL(ctx)
	if err != nil {
		return nil, err
	}
	return &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		Endpoint:     endpoint,
		Scopes:       scopes,
		RedirectURL:  site + "/panel/oauth/" + provider + "/callback",
	}, nil
}

// oauthStart 把浏览器送去上游授权页。mode=link 是账号页的「绑定」，要求已登录。
func (h *Handler) oauthStart(c *gin.Context) {
	provider := c.Param("provider")
	ctx := c.Request.Context()
	if !slices.Contains(oauthProviders, provider) {
		redirectAdmin(c, "", url.Values{"oauth_error": {"不认识的登录方式"}})
		return
	}
	cfg, err := h.oauthConfig(ctx, provider)
	if err != nil {
		h.log.Error("读 OAuth 配置失败", "provider", provider, "err", err)
		redirectAdmin(c, "", url.Values{"oauth_error": {"读取配置失败"}})
		return
	}
	if cfg == nil {
		redirectAdmin(c, "", url.Values{"oauth_error": {"管理员未配置 " + provider + " 登录"}})
		return
	}
	if site, err := h.siteURL(ctx); err != nil || site == "" {
		// 回调地址从站点外部 URL 拼；没配它，上游会拿到一个跟注册值对不上的
		// redirect_uri，与其让人在上游的报错页里猜，不如在门口说清。
		redirectAdmin(c, "", url.Values{"oauth_error": {"管理员未配置站点外部 URL，OAuth 登录不可用"}})
		return
	}
	link := c.Query("mode") == "link"
	if link {
		if _, ok, err := h.validSession(c); err != nil || !ok {
			redirectAdmin(c, "", url.Values{"oauth_error": {"绑定前先登录"}})
			return
		}
	}
	state, err := randomHex(16)
	if err != nil {
		h.log.Error("生成 state 失败", "err", err)
		redirectAdmin(c, "", url.Values{"oauth_error": {"内部错误"}})
		return
	}
	flow := oauthFlow{State: state, Verifier: oauth2.GenerateVerifier(), Link: link}
	raw, err := json.Marshal(flow)
	if err != nil {
		h.log.Error("编码 OAuth cookie 失败", "err", err)
		redirectAdmin(c, "", url.Values{"oauth_error": {"内部错误"}})
		return
	}
	// SameSite=Lax 而不是全站那套 Strict：回调是从 github.com/google.com 跳回来的
	// 顶级导航，Strict 的 cookie 在那一跳上不发，state 校验会永远失败。Lax 在顶级
	// GET 导航上发、跨站 POST/子请求仍不发，对这只只装随机数的 cookie 足够。
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthCookie,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/panel/oauth",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	// PKCE 两家都带上（#69）：GitHub 已正式支持且仅 S256，Google web client 上无害。
	c.Redirect(http.StatusFound, cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(flow.Verifier)))
}

// randomHex 是 n 字节 crypto/rand 的 hex。state 的猜不出来全靠它。
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// oauthIdentity 是从上游拿回来的身份三件套。
type oauthIdentity struct {
	Provider string `json:"provider"`
	// UserID 是上游的不可变主键：GitHub 数字 id、Google sub。邮箱不做主键——
	// 它在上游可换绑（#69）。
	UserID string `json:"provider_user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

// oauthCallback 处理上游跳回来的授权码：换 token、取身份、按三态分流——
// 已绑定→登录；同邮箱已有账号→自动关联再登录（#62 决议 4）；无匹配→发接力 token
// 进「完成注册」页（#62 决议 5，先登后补码）。
func (h *Handler) oauthCallback(c *gin.Context) {
	provider := c.Param("provider")
	ctx := c.Request.Context()
	oops := func(msg string) { redirectAdmin(c, "", url.Values{"oauth_error": {msg}}) }

	cfg, err := h.oauthConfig(ctx, provider)
	if err != nil || cfg == nil {
		oops("管理员未配置 " + provider + " 登录")
		return
	}
	raw, err := c.Cookie(oauthCookie)
	// 一次性：不管成败，先把接力 cookie 清掉。
	http.SetCookie(c.Writer, &http.Cookie{Name: oauthCookie, Path: "/panel/oauth", MaxAge: -1, HttpOnly: true})
	if err != nil {
		oops("登录流程已过期，请重试")
		return
	}
	var flow oauthFlow
	if data, err := base64.RawURLEncoding.DecodeString(raw); err != nil || json.Unmarshal(data, &flow) != nil {
		oops("登录流程已损坏，请重试")
		return
	}
	if flow.State == "" || c.Query("state") != flow.State {
		oops("state 校验失败，请重试")
		return
	}
	if errCode := c.Query("error"); errCode != "" {
		// 用户在上游点了拒绝之类。error 参数是上游的机器码，原样给人看没意义。
		oops("上游取消了授权（" + errCode + "）")
		return
	}
	code := c.Query("code")
	if code == "" {
		oops("上游没有返回授权码")
		return
	}
	// 换 token 限时：上游 token 端点挂起不该占着回调请求不放。
	exCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tok, err := cfg.Exchange(exCtx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		h.log.Warn("OAuth 换 token 失败", "provider", provider, "err", err)
		oops("与上游交换凭据失败，请重试")
		return
	}
	var ident oauthIdentity
	var verified bool
	switch provider {
	case "github":
		ident, verified, err = fetchGitHubIdentity(exCtx, cfg, tok)
	case "google":
		ident, verified, err = parseGoogleIdentity(tok)
	}
	if err != nil {
		h.log.Warn("取上游身份失败", "provider", provider, "err", err)
		oops("读取上游账号信息失败，请重试")
		return
	}
	// 仅认上游 verified 邮箱（#62 决议 4）：没验证过的邮箱既不能自动关联、也不能
	// 免验证信——认了它，任何人在上游填一个别人的邮箱就能顶号。
	if !verified || ident.Email == "" {
		oops("你的 " + provider + " 账号没有已验证的邮箱，无法用它登录")
		return
	}
	ident.Email = store.NormalizeEmail(ident.Email)

	if flow.Link {
		h.finishOAuthLink(c, ident)
		return
	}
	h.finishOAuthLogin(c, ident)
}

// finishOAuthLink 是账号页「绑定」的收尾：身份必须重新从会话 cookie 里验出来，
// 绝不信接力 cookie——那只 cookie 客户端改得动，信它等于让人把身份绑进别人账号。
func (h *Handler) finishOAuthLink(c *gin.Context, ident oauthIdentity) {
	ctx := c.Request.Context()
	su, ok, err := h.validSession(c)
	if err != nil || !ok {
		redirectAdmin(c, "", url.Values{"oauth_error": {"会话已过期，重新登录后再绑定"}})
		return
	}
	if err := store.LinkOAuthIdentity(ctx, h.db, su.ID, ident.Provider, ident.UserID); err != nil {
		if isConstraint(err) {
			redirectAdmin(c, "", url.Values{"oauth_error": {"绑定失败：这个上游账号已被绑定，或你已绑定过这家"}})
			return
		}
		h.log.Error("绑定 OAuth 身份失败", "err", err)
		redirectAdmin(c, "", url.Values{"oauth_error": {"绑定失败"}})
		return
	}
	redirectAdmin(c, "", url.Values{"oauth_linked": {ident.Provider}})
}

// finishOAuthLogin 按身份三态分流登录。
func (h *Handler) finishOAuthLogin(c *gin.Context, ident oauthIdentity) {
	ctx := c.Request.Context()
	oops := func(msg string) { redirectAdmin(c, "", url.Values{"oauth_error": {msg}}) }

	// 态一：这个上游身份已经绑过人——直接登录。
	uid, found, err := store.FindOAuthUser(ctx, h.db, ident.Provider, ident.UserID)
	if err != nil {
		h.log.Error("查 OAuth 身份失败", "err", err)
		oops("登录失败")
		return
	}
	if !found {
		// 态二：同邮箱已有账号——自动关联（#62 决议 4）。上游已验证的邮箱与库里
		// 同一串，就是同一个人；顺手把验证态补真（上游验过的不该还欠一封验证信）。
		u, err := store.GetUserAuthByEmail(ctx, h.db, ident.Email)
		switch {
		case err == nil:
			if err := store.LinkOAuthIdentity(ctx, h.db, u.ID, ident.Provider, ident.UserID); err != nil {
				h.log.Error("自动关联 OAuth 身份失败", "err", err)
				oops("登录失败")
				return
			}
			if !u.EmailVerified {
				if err := store.SetEmailVerified(ctx, h.db, u.ID); err != nil {
					h.log.Error("补验证态失败", "err", err)
				}
			}
			uid = u.ID
		case errors.Is(err, store.ErrNotFound):
			// 态三：无匹配账号——先登后补码（#62 决议 5）。身份装进一次性接力
			// token，进「完成注册」页填邀请码。
			payload, err := json.Marshal(ident)
			if err != nil {
				h.log.Error("编码 OAuth 身份失败", "err", err)
				oops("登录失败")
				return
			}
			token, err := store.CreateAuthToken(ctx, h.db, store.TokenOAuthSignup, nil,
				string(payload), store.TokenTTLOAuthSignup)
			if err != nil {
				h.log.Error("生成完成注册 token 失败", "err", err)
				oops("登录失败")
				return
			}
			redirectAdmin(c, "oauth-complete", url.Values{"token": {token}})
			return
		default:
			h.log.Error("查用户失败", "err", err)
			oops("登录失败")
			return
		}
	}
	u, err := store.GetUser(ctx, h.db, uid)
	if err != nil {
		h.log.Error("读用户失败", "err", err)
		oops("登录失败")
		return
	}
	if u.Disabled {
		oops("账号已停用")
		return
	}
	token, ttl, err := store.CreateSession(ctx, h.db, uid)
	if err != nil {
		h.log.Error("生成会话失败", "err", err)
		oops("登录失败")
		return
	}
	h.setSessionCookie(c, token, int(ttl.Seconds()))
	redirectAdmin(c, "", nil)
}

// oauthPending 给完成注册页读「拿到的邮箱是什么」。只读不消费——填错一次邀请码
// 不该把整趟 OAuth 作废（见 store.PeekAuthToken）。
func (h *Handler) oauthPending(c *gin.Context) {
	_, payload, err := store.PeekAuthToken(c.Request.Context(), h.db, c.Query("token"), store.TokenOAuthSignup)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "链接无效或已过期：重新用上游账号登录一次")
		return
	}
	if err != nil {
		h.log.Error("读完成注册 token 失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	var ident oauthIdentity
	if err := json.Unmarshal([]byte(payload), &ident); err != nil {
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"provider": ident.Provider, "email": ident.Email})
}

// oauthComplete 是「完成注册」页的提交（#62 决议 5）：接力 token + 邀请码 → 建号。
// OAuth verified 邮箱视为已验证，免验证信；账号无密码（OAuth-only，#61）。
func (h *Handler) oauthComplete(c *gin.Context) {
	var in struct {
		Token      string `json:"token"`
		InviteCode string `json:"invite_code"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	ctx := c.Request.Context()
	// 先 Peek 后 Consume 不可取——中间隔着邀请码校验，两个请求会都过 Peek。
	// 直接消费，失败的路径（邀请码无效）把身份再存回一个新 token 让人重试。
	_, payload, err := store.ConsumeAuthToken(ctx, h.db, in.Token, store.TokenOAuthSignup)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusBadRequest, "登录流程已过期：重新用上游账号登录一次")
		return
	}
	if err != nil {
		h.log.Error("消费完成注册 token 失败", "err", err)
		fail(c, http.StatusInternalServerError, "注册失败")
		return
	}
	var ident oauthIdentity
	if err := json.Unmarshal([]byte(payload), &ident); err != nil {
		fail(c, http.StatusInternalServerError, "注册失败")
		return
	}
	var uid int64
	err = h.inTx(ctx, func(tx store.Conn) error {
		// 回调到提交之间世界可能变了：身份被绑上了（另一个标签页），或同邮箱
		// 被注册了。两种都折成「关联进已有账号」，与回调时的分流同一套规则。
		if id, found, err := store.FindOAuthUser(ctx, tx, ident.Provider, ident.UserID); err != nil {
			return err
		} else if found {
			uid = id
			return nil
		}
		if u, err := store.GetUserAuthByEmail(ctx, tx, ident.Email); err == nil {
			uid = u.ID
			return store.LinkOAuthIdentity(ctx, tx, uid, ident.Provider, ident.UserID)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		var err error
		if uid, err = store.CreateUser(ctx, tx, ident.Email, nil, ident.Name, store.RoleUser, true); err != nil {
			return err
		}
		if err := store.ConsumeInviteCode(ctx, tx, strings.TrimSpace(in.InviteCode), uid); err != nil {
			return err
		}
		return store.LinkOAuthIdentity(ctx, tx, uid, ident.Provider, ident.UserID)
	})
	if errors.Is(err, store.ErrInvalidInput) {
		// 邀请码不对。身份 token 已被消费，重发一个新的让人在原页面重试——
		// 不然填错一个码就得整趟 OAuth 重走。
		fresh, tokenErr := store.CreateAuthToken(ctx, h.db, store.TokenOAuthSignup, nil,
			payload, store.TokenTTLOAuthSignup)
		if tokenErr != nil {
			h.log.Error("重发完成注册 token 失败", "err", tokenErr)
			fail(c, http.StatusBadRequest, err.Error()+"；请重新用上游账号登录一次")
			return
		}
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error(), "token": fresh})
		return
	}
	if err != nil {
		h.log.Error("OAuth 完成注册失败", "err", err)
		fail(c, http.StatusInternalServerError, "注册失败")
		return
	}
	u, err := store.GetUser(ctx, h.db, uid)
	if err == nil && u.Disabled {
		fail(c, http.StatusForbidden, "账号已停用")
		return
	}
	token, ttl, err := store.CreateSession(ctx, h.db, uid)
	if err != nil {
		h.log.Error("生成会话失败", "err", err)
		fail(c, http.StatusInternalServerError, "注册成功但登录失败，请手动登录")
		return
	}
	h.setSessionCookie(c, token, int(ttl.Seconds()))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// listIdentities 列本人绑定的上游身份（账号设置那半，页面壳在 #76）。
func (h *Handler) listIdentities(c *gin.Context) {
	if !h.requireVerified(c) {
		return
	}
	list, err := store.ListOAuthIdentities(c.Request.Context(), h.db, sessionUser(c).ID)
	if err != nil {
		h.log.Error("列 OAuth 身份失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	c.JSON(http.StatusOK, list)
}

// unlinkIdentity 解绑一家 provider。最后一条登录通道不许拆：OAuth-only 账号只剩
// 这一个身份时解绑等于把自己锁在门外，而设密码有现成的路（忘记密码那条邮件链）。
func (h *Handler) unlinkIdentity(c *gin.Context) {
	if !h.requireVerified(c) {
		return
	}
	ctx := c.Request.Context()
	me := sessionUser(c)
	u, err := store.GetUserAuthByID(ctx, h.db, me.ID)
	if err != nil {
		h.log.Error("读用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "解绑失败")
		return
	}
	if !u.HasPassword {
		list, err := store.ListOAuthIdentities(ctx, h.db, me.ID)
		if err != nil {
			h.log.Error("列 OAuth 身份失败", "err", err)
			fail(c, http.StatusInternalServerError, "解绑失败")
			return
		}
		if len(list) <= 1 {
			fail(c, http.StatusBadRequest, "这是该账号唯一的登录方式：先用「忘记密码」设置密码，再来解绑")
			return
		}
	}
	err = store.UnlinkOAuthIdentity(ctx, h.db, me.ID, c.Param("provider"))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "没有绑定过这家")
		return
	}
	if err != nil {
		h.log.Error("解绑失败", "err", err)
		fail(c, http.StatusInternalServerError, "解绑失败")
		return
	}
	c.Status(http.StatusNoContent)
}

// fetchGitHubIdentity 拿 GitHub 的身份三件套（#69）：/user 取不可变数字 id，
// /user/emails 取 primary+verified 的那条——/user 的 email 字段在用户未公开邮箱时
// 是 null，不能用。
func fetchGitHubIdentity(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauthIdentity, bool, error) {
	client := cfg.Client(ctx, tok)
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/user", &user); err != nil {
		return oauthIdentity{}, false, err
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/user/emails", &emails); err != nil {
		return oauthIdentity{}, false, err
	}
	ident := oauthIdentity{
		Provider: "github",
		UserID:   strconv.FormatInt(user.ID, 10),
		Name:     cmp.Or(user.Name, user.Login),
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			return identWithEmail(ident, e.Email), true, nil
		}
	}
	// 没有已验证的 primary 就退而取任意一条已验证的：口径认的是 verified，
	// primary 只是首选。一条 verified 都没有 → 按未验证处理。
	for _, e := range emails {
		if e.Verified {
			return identWithEmail(ident, e.Email), true, nil
		}
	}
	return ident, false, nil
}

func identWithEmail(ident oauthIdentity, email string) oauthIdentity {
	ident.Email = email
	return ident
}

// parseGoogleIdentity 从 token 响应里的 id_token 解身份（#69）：直接经 HTTPS 从
// Google token 端点取回的 id_token 免本地验签——它没经过任何第三方之手。
// email_verified 两种编码都见过（bool 与 "true"），都认。
func parseGoogleIdentity(tok *oauth2.Token) (oauthIdentity, bool, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return oauthIdentity{}, false, errors.New("token 响应缺少 id_token")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return oauthIdentity{}, false, errors.New("id_token 不是 JWT 形状")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return oauthIdentity{}, false, fmt.Errorf("解码 id_token: %w", err)
	}
	var claims struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return oauthIdentity{}, false, fmt.Errorf("解析 id_token: %w", err)
	}
	if claims.Sub == "" {
		return oauthIdentity{}, false, errors.New("id_token 缺少 sub")
	}
	verified := false
	switch v := claims.EmailVerified.(type) {
	case bool:
		verified = v
	case string:
		verified = v == "true"
	}
	return oauthIdentity{
		Provider: "google",
		UserID:   claims.Sub,
		Email:    claims.Email,
		Name:     claims.Name,
	}, verified, nil
}

// getJSON 打一个带上游 token 的 GET 并解 JSON。GitHub 要求带版本化的 Accept 头。
func getJSON(ctx context.Context, client *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s 回 %d", url, res.StatusCode)
	}
	return json.Unmarshal(body, dst)
}
