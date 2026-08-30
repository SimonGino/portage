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
	declarative bool
}

func New(db *sql.DB, log *slog.Logger, declarative bool) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{db: db, log: log, declarative: declarative}
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

	auth := api.Group("", h.requireSession())
	auth.POST("/password", h.changePassword)

	// 业务配置的写接口全走这个组：声明文件形态下统一 409（#48）。「业务配置」即
	// 声明文件里那四张表——渠道（含凭证与纳管模型）、接入点、API Key；组外留下的
	// POST（改密码、检测、fetch-models）都不写业务配置。
	cw := auth.Group("", h.rejectWritesWhenDeclarative())

	auth.GET("/channels", h.listChannels)
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
	auth.GET("/channels/:id/credentials", h.listCredentials)
	cw.POST("/channels/:id/credentials", h.addCredentials)
	cw.PUT("/credentials/:id", h.updateCredential)
	cw.DELETE("/credentials/:id", h.deleteCredential)
	auth.POST("/channels/:id/probe", h.probeChannel)
	// 拉上游模型列表给表单做预勾选（口径层 v0.40）。POST 而不是 GET：它会朝上游
	// 发真请求、花上游的配额，不该被浏览器或中间层当成可缓存的读操作重放。
	auth.POST("/channels/:id/fetch-models", h.fetchChannelModels)
	cw.POST("/channels/:id/models", h.addChannelModel)
	cw.PUT("/channel-models/:id", h.updateChannelModel)
	cw.DELETE("/channel-models/:id", h.deleteChannelModel)

	auth.GET("/access-points", h.listAccessPoints)
	cw.POST("/access-points", h.createAccessPoint)
	cw.PUT("/access-points/:id", h.updateAccessPoint)
	cw.DELETE("/access-points/:id", h.deleteAccessPoint)

	auth.GET("/keys", h.listKeys)
	cw.POST("/keys", h.createKey)
	cw.PUT("/keys/:id", h.updateKey)
	cw.DELETE("/keys/:id", h.deleteKey)

	// 导出整份业务配置为 channels.yaml（口径层 §2.9 #32）。在 auth 组里，且**没有**
	// 第二条出口（不做 CLI 导出子命令）——见 export.go。
	auth.GET("/export", h.exportConfig)
	// 导入一份 channels.yaml，覆盖式（#59）。在写闸组里：声明文件形态下导进去的
	// 改动活不过下次重启 apply，与其余业务配置写接口同一句 409——见 import.go。
	cw.POST("/import", h.importConfig)

	auth.GET("/logs", h.listLogs)
	auth.GET("/usage", h.usage)
	auth.GET("/usage/buckets", h.usageBuckets)

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
func (h *Handler) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, err := h.validSession(c)
		if err != nil {
			h.log.Error("校验会话失败", "err", err)
			fail(c, http.StatusInternalServerError, "会话校验失败")
			return
		}
		if !ok {
			fail(c, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		c.Next()
	}
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
