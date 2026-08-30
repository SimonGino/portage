// Package admin is the management plane: password login, an in-memory session,
// and CRUD over the配置表 that the relay reads.
//
// 与转发端**彻底分离**（口径层 §2.7）：这里认 cookie 会话，转发端认 api_keys；
// 两套凭证互不可用，两边的中间件也不共用。分离不是洁癖——管理端能改渠道凭证，
// 网关 key 泄露一把就等于交出全部上游账号，那是完全不同量级的后果。
//
// 按 PO 于 M3 的裁定，本包只收管理面：`GET /v1/models` 与 `/healthz` 是业务面，
// 留在 internal/server（这也了结了展开层 §3 那条「实现偏离待裁 v0.12」）。
package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SimonGino/portage/internal/mail"
	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// cookieName 是会话 cookie 的名字。带 portage_ 前缀，免得和同一台机器上别的服务撞名
// ——cookie 是按域名共享的，端口不隔离。
const cookieName = "portage_admin"

// Handler 是管理端的全部状态：一个库连接。会话自 #71 起也在库里（sessions 表），
// 这里不再持有会话表。
type Handler struct {
	db  *sql.DB
	log *slog.Logger
	// declarative 是声明文件形态旗（#48）：业务配置以文件为准，写接口回 409。
	// 只读的是**业务配置**——检测、fetch-models、导出、流水/用量、改密码照常：
	// 前两样不写库，导出与查询本就是读，密码是运行期状态、不在文件里（§2.9 #28）。
	//
	// #66 起它还多管半件事：声明形态下**用户体系整体不挂载**（注册/邀请码/OAuth
	// 等路由不注册、404 而非 409），见 Mount。
	declarative bool
	// mail 是发信出口（#72）。持函数不直连 mail.Send，注册/验证/重置的流程测试
	// 才能把真 SMTP 换成记录桩。
	mail mail.Sender
}

func New(db *sql.DB, log *slog.Logger, declarative bool) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{db: db, log: log, declarative: declarative, mail: mail.DefaultSender}
}

// Bootstrap 用配置里的明文密码初始化管理端密码，**且只在库里还没有密码时**。
//
// 这就是口径层 §2.7「登录后可改，改后配置项失效」的实现：改过的密码落在 settings
// 表，之后每次启动都会走到「库里已经有了」这一支，配置文件里那行从此没有作用。
// 若不这样，重启会把管理员改过的密码悄悄改回配置里的旧值——而配置文件常年躺在
// 仓库或 compose 里，等于密码根本改不掉。
//
// #71 起多补一手：有了密码哈希就确保第一个 admin **用户**存在（口径层 §2.10 #61，
// admin_password 的完整语义从此是「仅在无 admin 用户时初始化造号」）。migrate 里
// 那一步只够老库——新库跑 migrate 时 settings 还是空的，hash 是这里刚写进去的，
// 造号只能跟在后面。EnsureFirstAdmin 幂等，两处调用不冲突。
//
// 返回 false 表示这次启动之后仍然没有密码：管理端会拒绝一切登录。
func Bootstrap(ctx context.Context, db *sql.DB, plaintext string) (bool, error) {
	existing, err := store.GetSetting(ctx, db, store.SettingAdminPasswordHash)
	if err != nil {
		return false, err
	}
	if existing == "" {
		if plaintext == "" {
			return false, nil
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
		if err != nil {
			return false, err
		}
		if err := store.SetSetting(ctx, db, store.SettingAdminPasswordHash, string(hash)); err != nil {
			return false, err
		}
	}
	if _, err := store.EnsureFirstAdmin(ctx, db); err != nil {
		return false, err
	}
	return true, nil
}

// Mount 挂管理端的全部路由。
//
// 收 *gin.Engine 而不是 *gin.RouterGroup：前端是个 SPA，深链接（/admin/keys）
// 得回同一份 index.html，而 gin 的路由树不允许 `/admin/*filepath` 与
// `/admin/api/...` 并存（catch-all 与静态段冲突，注册时直接 panic）。所以静态资源
// 走 NoRoute 兜底，那需要引擎本身。
func (h *Handler) Mount(r *gin.Engine) {
	api := r.Group("/admin/api")

	// 登录三件套不鉴权：没登录的时候正是要调它们。
	api.POST("/login", h.login)
	api.POST("/logout", h.logout)
	api.GET("/session", h.session)

	// 用户体系的无会话半区（#72）：注册、邮箱验证、找回密码、OAuth。**声明形态下
	// 整个不注册**（#66 互斥闸）：挂声明文件 ⇒ 用户体系不挂载，404 是路由级的——
	// 与业务配置写闸的 409 刻意不同，那边是「资源在别处」，这边是「这套东西不存在」。
	if !h.declarative {
		api.GET("/auth-config", h.authConfig)
		api.POST("/register", h.register)
		api.POST("/verify-email", h.verifyEmail)
		api.POST("/password-reset", h.requestPasswordReset)
		api.POST("/password-reset/confirm", h.confirmPasswordReset)
		api.GET("/oauth/pending", h.oauthPending)
		api.POST("/oauth/complete", h.oauthComplete)
		// OAuth 跳转两条是浏览器导航不是 fetch，不挂 /admin/api 前缀——错误也回
		// 302 到页面，回 JSON 的话人盯着的是一屏裸字符串。
		r.GET("/admin/oauth/:provider/start", h.oauthStart)
		r.GET("/admin/oauth/:provider/callback", h.oauthCallback)
	}

	auth := api.Group("", h.requireSession())

	// 「我的 Key」（#73）：任意角色可用，但**挂声明文件时整组不注册**（#66 ①）。
	// 404 而不是 409——写闸的 409 说的是「管理面在、事实源在文件」，而用户体系在
	// 声明形态下整个不存在，装作在只是锁着，会引人去找解锁的路。
	// 组上加 requireVerified：未验证邮箱功能全锁（#62 决议 2），自用面不例外。
	// allowed_models 用户可自设（#63：自我约束工具不是权限边界）。
	if !h.declarative {
		my := auth.Group("/my", func(c *gin.Context) { h.requireVerified(c) })
		my.GET("/keys", h.listMyKeys)
		// 建 key 与治理面共用一个 handler：两条路建的都只能是登录者自己的 key
		// （#63 不代建），差别只在这条不挂 admin 闸与写闸（不注册即 404，见上）。
		my.POST("/keys", h.createKey)
		my.PUT("/keys/:id", h.updateMyKey)
		my.DELETE("/keys/:id", h.deleteMyKey)
	}
	auth.POST("/password", h.changePassword)
	if !h.declarative {
		// 重发验证信只要会话不要验证态——未验证的人正是它的用户。
		auth.POST("/verify-email/resend", h.resendVerifyEmail)
		// 账号侧的 OAuth 绑定/解绑（#62 决议 4 的「手动」半边；页面壳在 #76）。
		auth.GET("/account/identities", h.listIdentities)
		auth.DELETE("/account/identities/:provider", h.unlinkIdentity)
	}

	// 治理面：现有的全部业务配置与观测接口都是 admin 的地盘。#72 之前登录即 admin，
	// 这层闸是空气；普通用户能登录之后它就是实的——user 角色的会话打这些接口一律
	// 403，不是 401（会话是有效的，差的是角色）。
	adm := auth.Group("", h.requireAdmin())

	// 业务配置的写接口全走这个组：声明文件形态下统一 409（#48）。「业务配置」即
	// 声明文件里那四张表——渠道（含凭证与纳管模型）、接入点、API Key；组外留下的
	// POST（改密码、检测、fetch-models）都不写业务配置。
	cw := adm.Group("", h.rejectWritesWhenDeclarative())

	if !h.declarative {
		// 用户治理（#72）：建用户逃生门、邀请码、SMTP/OAuth/站点地址配置。
		// 同在 #66 互斥闸后——声明形态没有用户体系，这些配置面跟着消失。
		adm.GET("/users", h.listUsers)
		adm.POST("/users", h.createUser)
		adm.PUT("/users/:id/role", h.setUserRole)
		adm.PUT("/users/:id/disabled", h.setUserDisabled)
		adm.GET("/invite-codes", h.listInviteCodes)
		adm.POST("/invite-codes", h.createInviteCodes)
		adm.DELETE("/invite-codes/:id", h.revokeInviteCode)
		adm.GET("/auth-settings", h.getAuthSettings)
		adm.PUT("/auth-settings", h.putAuthSettings)
		adm.POST("/auth-settings/test-email", h.testEmail)
	}

	adm.GET("/channels", h.listChannels)
	cw.POST("/channels", h.createChannel)
	// 修改是四笔按意图的字段写（#48 批2），没有整体覆盖的 PUT /channels/:id：
	// 那个接口逼每个调用点回传全量渠道，「回传旧 state 覆盖别处刚存的」在两个
	// 前端文件里各咬过一次。
	cw.PUT("/channels/:id/base-url", h.putChannelBaseURL)
	cw.PUT("/channels/:id/key-mode", h.putChannelKeyMode)
	cw.PUT("/channels/:id/disabled", h.putChannelDisabled)
	cw.PUT("/channels/:id/settings", h.putChannelSettings)
	cw.DELETE("/channels/:id", h.deleteChannel)
	// 凭证池：逐条 CRUD + 追加式批量粘贴（口径层 v0.38 改写 v0.28 的整把替换）。
	// 依旧**没有任何读凭证值的接口**——GET 回的是名字与状态。
	adm.GET("/channels/:id/credentials", h.listCredentials)
	cw.POST("/channels/:id/credentials", h.addCredentials)
	cw.PUT("/credentials/:id", h.updateCredential)
	cw.DELETE("/credentials/:id", h.deleteCredential)
	adm.POST("/channels/:id/probe", h.probeChannel)
	// 拉上游模型列表给表单做预勾选（口径层 v0.40）。POST 而不是 GET：它会朝上游
	// 发真请求、花上游的配额，不该被浏览器或中间层当成可缓存的读操作重放。
	adm.POST("/channels/:id/fetch-models", h.fetchChannelModels)
	cw.POST("/channels/:id/models", h.addChannelModel)
	cw.PUT("/channel-models/:id", h.updateChannelModel)
	cw.DELETE("/channel-models/:id", h.deleteChannelModel)
	// models.dev 快照的两个只读端点（口径层 §2.10 计价，#68/#74）：provider 标注
	// 取值域 + 按 provider 的建议价。读的是 go:embed 资产不碰库，声明形态下照常可用
	// ——那形态下页面只读，但看建议价不是写业务配置。
	auth.GET("/pricing/providers", h.pricingProviders)
	auth.GET("/pricing/models", h.pricingModels)

	adm.GET("/access-points", h.listAccessPoints)
	cw.POST("/access-points", h.createAccessPoint)
	cw.PUT("/access-points/:id", h.updateAccessPoint)
	cw.DELETE("/access-points/:id", h.deleteAccessPoint)

	adm.GET("/keys", h.listKeys)
	cw.POST("/keys", h.createKey)
	cw.PUT("/keys/:id", h.updateKey)
	cw.DELETE("/keys/:id", h.deleteKey)

	// 导出整份业务配置为 channels.yaml（口径层 §2.9 #32）。在 auth 组里，且**没有**
	// 第二条出口（不做 CLI 导出子命令）——见 export.go。
	adm.GET("/export", h.exportConfig)
	// 导入一份 channels.yaml，覆盖式（#59）。在写闸组里：声明文件形态下导进去的
	// 改动活不过下次重启 apply，与其余业务配置写接口同一句 409——见 import.go。
	cw.POST("/import", h.importConfig)

	adm.GET("/logs", h.listLogs)
	adm.GET("/usage", h.usage)
	adm.GET("/usage/buckets", h.usageBuckets)

	h.mountUI(r)
}

// requireSession 是管理面的鉴权。
//
// 只认 cookie。刻意**不**接受 `Authorization: Bearer <会话 token>`：那会让一个
// 转发端的 key 和一个管理端的 token 长在同一个头上，迟早有人把两者混起来用，
// 「彻底分离」就名存实亡了。
//
// 会话自 #71 起落库，校验联查 users.disabled——用户被停用时已发出的 cookie 当场
// 失效（口径层 §2.10：停用即对系统一切访问冻结），这正是内存版做不到的那半。
//
// 过闸的人放进上下文（sessionUserFrom 取）：#73 起 key 的明文可见性、归属治理与
// 「我的 Key」都要按「谁在看」判，后面的 handler 不该各自再查一趟会话。
func (h *Handler) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		su, ok, err := h.validSession(c)
		if err != nil {
			h.log.Error("校验会话失败", "err", err)
			fail(c, http.StatusInternalServerError, "会话校验失败")
			return
		}
		if !ok {
			fail(c, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		c.Set(ctxUserKey, su)
		c.Next()
	}
}

// sessionUserFrom 取出 requireSession 放进来的登录者。与 sessionUser 的差别在
// 取不到时的姿态：这里返回零值让归属判定全部落空——所有「是我的吗」都判否，
// 宁可少见到明文也别把别人的明文放出去（#73 的 key 可见性判定用这只）。
func sessionUserFrom(c *gin.Context) store.SessionUser {
	if v, ok := c.Get(ctxUserKey); ok {
		if su, ok := v.(store.SessionUser); ok {
			return su
		}
	}
	return store.SessionUser{}
}

// requireAdmin 是治理面的角色闸，挂在 requireSession 之后（#63：admin = 治理面，
// user = 自用面）。403 而不是 401：会话是有效的，差的是角色——回 401 会让前端把
// 一个登着的普通用户不停踢回登录页。
func (h *Handler) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if sessionUser(c).Role != store.RoleAdmin {
			fail(c, http.StatusForbidden, "需要管理员权限")
		}
	}
}

// requireVerified 把「未验证可登录但功能全锁」（#62 决议 2）落在接口侧：未验证的
// 会话只剩去验证页那几件事（看会话、登出、重发验证信），其余自助动作一律 403。
// admin 恒已验证（造号即豁免），这道闸实际只拦普通用户。
func (h *Handler) requireVerified(c *gin.Context) bool {
	u, err := store.GetUser(c.Request.Context(), h.db, sessionUser(c).ID)
	if err != nil {
		h.log.Error("读用户失败", "err", err)
		fail(c, http.StatusInternalServerError, "读取失败")
		return false
	}
	if !u.EmailVerified {
		fail(c, http.StatusForbidden, "邮箱未验证：先完成邮箱验证")
		return false
	}
	return true
}

// fail 回一个统一形状的错误并中断。
//
// 形状是 {"error": "..."}，与转发端那三种协议原生错误刻意不同——管理端的调用方是
// 我们自己的前端，不需要装成任何一家的 API，而形状一致能让前端只写一处解析。
// rejectWritesWhenDeclarative 是声明文件形态的只读闸（#48）：库只是文件的物化副本，
// 从管理端写进去的业务配置活不过下次重启——先漂移、后被文件抹掉，页面上全程看着
// 正常。与其让人踩这一幕，不如在门口把话说清。409 而不是 403：不是权限问题，是
// 「资源的事实源在别处」这个状态冲突。
func (h *Handler) rejectWritesWhenDeclarative() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.declarative {
			fail(c, http.StatusConflict, "本实例挂了声明文件，业务配置以文件为准：改 channels.yaml 后重启生效")
		}
	}
}

func fail(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// write 在一个事务里做完写操作，然后跑一遍**启动时那一模一样的**配置校验，
// 不过就整体回滚并把校验原文回给前端。
//
// 这是管理端最要紧的一条不变量：能保存下去的配置，一定是能启动的配置。否则管理端
// 就成了「把网关改到起不来」的唯一入口——下次重启才炸，而那时人早忘了改过什么。
// 复用 store.Validate 而不是在前端另写一套校验，是为了让「UI 认」和「启动认」不会
// 各自漂移。
//
// Validate 必须对着 tx 跑，不能对着 h.db：连接池是 1，事务开着的时候再问 db 要连接
// 会永远等下去（不是报错，是挂住）。
func (h *Handler) write(c *gin.Context, fn func(ctx context.Context, tx *sql.Tx) error) {
	h.writeResult(c, func(ctx context.Context, tx *sql.Tx) (any, error) {
		return nil, fn(ctx, tx)
	})
}

// writeResult 同 write，但把 fn 的返回值作为 200 的响应体。新建类接口用它——
// 前端需要拿到新记录的 id，以及新 key 的明文（那一份**只此一次**）。
func (h *Handler) writeResult(c *gin.Context, fn func(ctx context.Context, tx *sql.Tx) (any, error)) {
	ctx := c.Request.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.log.Error("管理端开事务失败", "err", err)
		fail(c, http.StatusInternalServerError, "数据库忙，请重试")
		return
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后这里是 no-op

	result, err := fn(ctx, tx)
	if err != nil {
		h.writeError(c, err)
		return
	}
	if err := store.Validate(ctx, tx); err != nil {
		// 回滚由 defer 完成。校验原文直接回前端：它已经写成了「哪个记录、
		// 为什么不合法、怎么补」的样子，重新包装只会丢信息。
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		h.log.Error("管理端提交事务失败", "err", err)
		fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	if result == nil {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(http.StatusOK, result)
}

// writeError 把 store 层的错误翻成 HTTP。
func (h *Handler) writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(c, http.StatusNotFound, "记录不存在")
	case errors.Is(err, store.ErrInvalidInput):
		// 表单本身填错了，原样告诉前端哪里错——这类错误的文案是 store 写给人看的，
		// 不含上游凭证与 base_url。
		fail(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrInUse):
		// 删不掉是因为还有接入点指着它。这句话点了名，比外键那条通用文案有用。
		fail(c, http.StatusConflict, err.Error())
	case isConstraint(err):
		// UNIQUE / FOREIGN KEY。名字重复是最常见的一种，前端要能提示「换个名字」，
		// 不能笼统报 500。
		fail(c, http.StatusConflict, "与已有记录冲突：名称重复，或引用了不存在的渠道/模型")
	default:
		h.log.Error("管理端写操作失败", "err", err)
		fail(c, http.StatusInternalServerError, "保存失败")
	}
}

// isConstraint 认 modernc.org/sqlite 的约束冲突。
//
// 靠错误文本匹配而不是错误码：驱动没有导出可比对的 sentinel，返回的是自己的
// *sqlite.Error。文本匹配不精确，但判错的后果只是把 409 报成 500，不影响数据。
func isConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "CONSTRAINT")
}
