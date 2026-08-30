package admin

// 会话自 #71 起落库（sessions 表，口径层 §2.10 #61），本文件只剩管理端这一侧的
// 薄封装：SQL 与 TTL 语义都在 store（session.go），这里管的是 HTTP 那半——cookie
// 怎么设、无效时回什么。
//
// 旧内存版「重启即全部失效是特性」的立论（口径层 §2.7 时代）被 #61 正式推翻：
// 多用户下重启踢掉所有人不再是零代价，密码泄露的补救改走「改密码吊销全部会话」
// （store.DeleteAllSessions），扳机换了、语义没丢。

import (
	"net/http"

	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
)

// validSession 判 cookie 里的 token 是否有效，顺带滑动续期并带出背后的人。
//
// 库错误与「无效」分开报：无效是正常业务（401，去登录），库错误是这次没判成
// （500）——把后者说成前者会让一次磁盘抖动表现成「莫名其妙被登出」。
func (h *Handler) validSession(c *gin.Context) (store.SessionUser, bool, error) {
	token, _ := c.Cookie(cookieName)
	return store.TouchSession(c.Request.Context(), h.db, token)
}

// ctxUserKey 是 requireSession 把会话用户塞进 gin.Context 的键。
const ctxUserKey = "portage.session_user"

// sessionUser 取本次请求背后的人。只在 requireSession 之后的 handler 里可用——
// 中间件已经把 401 挡在门外，这里取不到只能是把 handler 挂错了组，宁可 panic
// 也别把一个零值 ID 当成真用户往下传。
func sessionUser(c *gin.Context) store.SessionUser {
	return c.MustGet(ctxUserKey).(store.SessionUser)
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
