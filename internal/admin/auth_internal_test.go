package admin

// #72 用户体系的流程测试：注册→验证、重发冷却、角色闸、重置密码、OAuth 完成注册、
// 声明形态 404、配置 secret 不回显。直接在包内建 Handler——发信桩要换到 h.mail 上，
// 这只有包内做得到；跨包的路由挂载与纯转发 404 由 internal/server 那边的测试把守。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const testAdminPassword = "admin-test-password"

// mailRec 是发信桩：记下每封信，不碰网络。fail 非空时装一台坏掉的 SMTP。
type mailRec struct {
	mu   sync.Mutex
	sent []struct{ To, Subject, Body string }
	fail error
}

func (m *mailRec) send(_ context.Context, _ *sql.DB, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, struct{ To, Subject, Body string }{to, subject, body})
	return nil
}

func (m *mailRec) setFail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}

func (m *mailRec) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *mailRec) last(t *testing.T) struct{ To, Subject, Body string } {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("桩里一封信都没有")
	}
	return m.sent[len(m.sent)-1]
}

// newAuthServer 起一个只挂管理端的进程内服务。返回的 db 供用例直接摆数据。
func newAuthServer(t *testing.T, declarative bool) (*httptest.Server, *sql.DB, *mailRec) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := Bootstrap(t.Context(), db, testAdminPassword); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	h := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), declarative)
	rec := &mailRec{}
	h.mail = rec.send
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db, rec
}

// client 是带 cookie 罐的请求器——会话在 cookie 里，普通 http.Client 会把登录丢掉。
type client struct {
	base string
	hc   *http.Client
}

func newClient(t *testing.T, srv *httptest.Server) *client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("建 cookie 罐: %v", err)
	}
	return &client{base: srv.URL, hc: &http.Client{Jar: jar}}
}

func (cl *client) do(t *testing.T, method, path, body string) (int, string, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, cl.base+path, rd)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应: %v", err)
	}
	return resp.StatusCode, string(raw), resp.Header
}

func (cl *client) login(t *testing.T, email, password string) {
	t.Helper()
	status, body, _ := cl.do(t, http.MethodPost, "/panel/api/login",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
	if status != http.StatusOK {
		t.Fatalf("登录 %s 失败，status=%d：%s", email, status, body)
	}
}

// configureMail 把「邮件这条路」配通：SMTP host+from、站点外部 URL。
func configureMail(t *testing.T, db *sql.DB) {
	t.Helper()
	for k, v := range map[string]string{
		store.SettingSMTPHost: "smtp.example.com",
		store.SettingSMTPFrom: "noreply@example.com",
		store.SettingSiteURL:  "https://gw.example.com",
	} {
		if err := store.SetSetting(t.Context(), db, k, v); err != nil {
			t.Fatalf("写设置 %s: %v", k, err)
		}
	}
}

// inviteCode 生成一个可用邀请码。
func inviteCode(t *testing.T, db *sql.DB) string {
	t.Helper()
	codes, err := store.CreateInviteCodes(t.Context(), db, 1, 0)
	if err != nil {
		t.Fatalf("生成邀请码: %v", err)
	}
	return codes[0]
}

// seedVerifiedUser 直接在库里造一个已验证、有密码的普通用户。
func seedVerifiedUser(t *testing.T, db *sql.DB, email, password string) int64 {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("哈希: %v", err)
	}
	hs := string(hash)
	uid, err := store.CreateUser(t.Context(), db, email, &hs, "", store.RoleUser, true)
	if err != nil {
		t.Fatalf("建用户: %v", err)
	}
	return uid
}

var tokenRe = regexp.MustCompile(`token=([0-9a-f]{64})`)

func mailToken(t *testing.T, body string) string {
	t.Helper()
	m := tokenRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("信里没有 token 链接：%s", body)
	}
	return m[1]
}

// ── 注册与验证 ──────────────────────────────────────────────────────────

// SMTP 未配则注册入口关闭（#62 决议 3）：auth-config 只是画界面的，真正的闸在
// register 里，两处都要验。
func TestRegisterClosedWithoutMail(t *testing.T) {
	srv, db, _ := newAuthServer(t, false)
	cl := newClient(t, srv)

	status, body, _ := cl.do(t, http.MethodGet, "/panel/api/auth-config", "")
	if status != http.StatusOK || !strings.Contains(body, `"registration_open":false`) {
		t.Fatalf("auth-config = %d %s，期望 registration_open:false", status, body)
	}
	if !strings.Contains(body, "管理员未配置邮件发信") {
		t.Fatalf("注册关闭时要带提示语：%s", body)
	}
	code := inviteCode(t, db)
	status, _, _ = cl.do(t, http.MethodPost, "/panel/api/register",
		fmt.Sprintf(`{"invite_code":%q,"email":"a@example.com","password":"password1"}`, code))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("未配邮件注册 = %d，期望 503", status)
	}

	// 只配 SMTP 不配站点 URL 也不算通——链接类邮件拼不出可点的地址。
	if err := store.SetSetting(t.Context(), db, store.SettingSMTPHost, "smtp.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(t.Context(), db, store.SettingSMTPFrom, "noreply@example.com"); err != nil {
		t.Fatal(err)
	}
	if status, body, _ = cl.do(t, http.MethodGet, "/panel/api/auth-config", ""); !strings.Contains(body, `"registration_open":false`) {
		t.Fatalf("缺站点 URL 时 auth-config = %d %s，期望仍然关闭", status, body)
	}
	if err := store.SetSetting(t.Context(), db, store.SettingSiteURL, "https://gw.example.com"); err != nil {
		t.Fatal(err)
	}
	if status, body, _ = cl.do(t, http.MethodGet, "/panel/api/auth-config", ""); !strings.Contains(body, `"registration_open":true`) {
		t.Fatalf("配齐后 auth-config = %d %s，期望开放", status, body)
	}
}

func TestRegisterAndVerifyFlow(t *testing.T) {
	srv, db, rec := newAuthServer(t, false)
	configureMail(t, db)
	cl := newClient(t, srv)
	code := inviteCode(t, db)

	// 注册：建号 + 销码 + 发验证信 + 直接给会话（未验证可登录，功能全锁）。
	status, body, _ := cl.do(t, http.MethodPost, "/panel/api/register",
		fmt.Sprintf(`{"invite_code":%q,"email":"New@Example.com","password":"password1"}`, code))
	if status != http.StatusOK || !strings.Contains(body, `"mail_sent":true`) {
		t.Fatalf("注册 = %d %s", status, body)
	}
	msg := rec.last(t)
	if msg.To != "new@example.com" || !strings.Contains(msg.Body, "https://gw.example.com/panel/verify?token=") {
		t.Fatalf("验证信不对：to=%s body=%s", msg.To, msg.Body)
	}

	// 会话已在，但未验证：session 带出 email_verified:false，自助动作（改密码）403。
	status, body, _ = cl.do(t, http.MethodGet, "/panel/api/session", "")
	if status != http.StatusOK || !strings.Contains(body, `"email_verified":false`) {
		t.Fatalf("session = %d %s，期望 email_verified:false", status, body)
	}
	status, _, _ = cl.do(t, http.MethodPost, "/panel/api/password",
		`{"old_password":"password1","new_password":"password2"}`)
	if status != http.StatusForbidden {
		t.Fatalf("未验证改密码 = %d，期望 403", status)
	}
	// 普通用户就算验证了也进不了治理面——但未验证时同样是 403（角色闸在验证闸外）。
	status, _, _ = cl.do(t, http.MethodGet, "/panel/api/channels", "")
	if status != http.StatusForbidden {
		t.Fatalf("user 会话打治理面 = %d，期望 403", status)
	}

	// 点链接验证：token 一次性。
	token := mailToken(t, msg.Body)
	status, _, _ = cl.do(t, http.MethodPost, "/panel/api/verify-email", fmt.Sprintf(`{"token":%q}`, token))
	if status != http.StatusOK {
		t.Fatalf("验证 = %d", status)
	}
	status, body, _ = cl.do(t, http.MethodGet, "/panel/api/session", "")
	if !strings.Contains(body, `"email_verified":true`) {
		t.Fatalf("验证后 session = %d %s", status, body)
	}
	status, _, _ = cl.do(t, http.MethodPost, "/panel/api/verify-email", fmt.Sprintf(`{"token":%q}`, token))
	if status != http.StatusBadRequest {
		t.Fatalf("重放验证 token = %d，期望 400", status)
	}

	// 同邮箱再注册：409，且新邀请码不能被半截事务烧掉。
	code2 := inviteCode(t, db)
	status, _, _ = cl2Register(t, srv, code2, "new@example.com")
	if status != http.StatusConflict {
		t.Fatalf("重复邮箱注册 = %d，期望 409", status)
	}
	list, err := store.ListInviteCodes(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, ic := range list {
		if ic.Code == code2 && ic.UsedByEmail != "" {
			t.Fatalf("失败注册烧掉了邀请码：%+v", ic)
		}
	}

	// 邀请码不对：400，且不留半个用户。
	status, _, _ = cl2Register(t, srv, "nonsense", "other@example.com")
	if status != http.StatusBadRequest {
		t.Fatalf("坏邀请码注册 = %d，期望 400", status)
	}
	if _, err := store.GetUserAuthByEmail(t.Context(), db, "other@example.com"); err == nil {
		t.Fatal("坏邀请码注册不该留下用户")
	}
}

// cl2Register 用一个新客户端发注册——复用带着会话的客户端会把会话 cookie 搅进来。
func cl2Register(t *testing.T, srv *httptest.Server, code, email string) (int, string, http.Header) {
	t.Helper()
	return newClient(t, srv).do(t, http.MethodPost, "/panel/api/register",
		fmt.Sprintf(`{"invite_code":%q,"email":%q,"password":"password1"}`, code, email))
}

func TestResendVerifyCooldown(t *testing.T) {
	srv, db, rec := newAuthServer(t, false)
	configureMail(t, db)
	cl := newClient(t, srv)
	status, _, _ := cl.do(t, http.MethodPost, "/panel/api/register",
		fmt.Sprintf(`{"invite_code":%q,"email":"a@example.com","password":"password1"}`, inviteCode(t, db)))
	if status != http.StatusOK {
		t.Fatalf("注册 = %d", status)
	}

	// 注册刚发过一封：立刻重发要吃 60s 冷却，带 Retry-After。
	status, _, hdr := cl.do(t, http.MethodPost, "/panel/api/verify-email/resend", "")
	if status != http.StatusTooManyRequests || hdr.Get("Retry-After") == "" {
		t.Fatalf("冷却内重发 = %d Retry-After=%q，期望 429 + 头", status, hdr.Get("Retry-After"))
	}
	// 把上次发信拨回两分钟前——冷却看「上次发是什么时候」。
	if _, err := db.Exec(`UPDATE auth_tokens SET created_at = datetime('now', '-2 minutes')`); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = cl.do(t, http.MethodPost, "/panel/api/verify-email/resend", ""); status != http.StatusOK {
		t.Fatalf("冷却过后重发 = %d，期望 200", status)
	}
	if rec.count() != 2 {
		t.Fatalf("发信数 = %d，期望 2", rec.count())
	}

	// 验证完之后没有可重发的东西。
	token := mailToken(t, rec.last(t).Body)
	if status, _, _ = cl.do(t, http.MethodPost, "/panel/api/verify-email", fmt.Sprintf(`{"token":%q}`, token)); status != http.StatusOK {
		t.Fatalf("验证 = %d", status)
	}
	if status, _, _ = cl.do(t, http.MethodPost, "/panel/api/verify-email/resend", ""); status != http.StatusBadRequest {
		t.Fatalf("已验证重发 = %d，期望 400", status)
	}
}

// ── 登录与角色闸 ────────────────────────────────────────────────────────

func TestLoginAndRoleGate(t *testing.T) {
	srv, db, _ := newAuthServer(t, false)

	// 邮箱不存在与密码不对折成同一句 401——不给邮箱存在性预言机。
	cl := newClient(t, srv)
	status, body, _ := cl.do(t, http.MethodPost, "/panel/api/login",
		`{"email":"ghost@example.com","password":"whatever1"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("未知邮箱登录 = %d，期望 401", status)
	}
	wrongMsg := body
	status, body, _ = cl.do(t, http.MethodPost, "/panel/api/login",
		fmt.Sprintf(`{"email":%q,"password":"wrong-password"}`, store.FirstAdminEmail))
	if status != http.StatusUnauthorized || body != wrongMsg {
		t.Fatalf("密码错 = %d %s，期望与未知邮箱同一句", status, body)
	}

	// admin 正常进治理面。
	admin := newClient(t, srv)
	admin.login(t, store.FirstAdminEmail, testAdminPassword)
	if status, _, _ = admin.do(t, http.MethodGet, "/panel/api/channels", ""); status != http.StatusOK {
		t.Fatalf("admin 读渠道 = %d", status)
	}

	// user 角色的会话打治理面：403 不是 401——会话有效，差的是角色。
	seedVerifiedUser(t, db, "u@example.com", "user-pass-1")
	user := newClient(t, srv)
	user.login(t, "u@example.com", "user-pass-1")
	for _, path := range []string{"/panel/api/channels", "/panel/api/users", "/panel/api/keys", "/panel/api/auth-settings"} {
		if status, _, _ = user.do(t, http.MethodGet, path, ""); status != http.StatusForbidden {
			t.Errorf("user 打 %s = %d，期望 403", path, status)
		}
	}

	// 停用即冻结：登录 403，已发出的会话当场失效。
	if _, err := db.Exec(`UPDATE users SET disabled = 1 WHERE email = 'u@example.com'`); err != nil {
		t.Fatal(err)
	}
	if status, _, _ = user.do(t, http.MethodGet, "/panel/api/session", ""); status != http.StatusOK {
		t.Fatalf("session 探测 = %d", status)
	}
	status, body, _ = user.do(t, http.MethodGet, "/panel/api/session", "")
	if !strings.Contains(body, `"authenticated":false`) {
		t.Fatalf("停用后 session = %s，期望 authenticated:false", body)
	}
	status, _, _ = newClient(t, srv).do(t, http.MethodPost, "/panel/api/login",
		`{"email":"u@example.com","password":"user-pass-1"}`)
	if status != http.StatusForbidden {
		t.Fatalf("停用登录 = %d，期望 403", status)
	}
}

// 任免与停用（#61：admin 可任免角色，多 admin 允许；停用即冻结）。角色是每次请求
// 联查出来的，升降都即时生效；停用/启用走 #71 的会话冻结语义——会话行不删，
// 启用后老会话复活。
func TestUserGovernance(t *testing.T) {
	srv, db, _ := newAuthServer(t, false)
	admin := newClient(t, srv)
	admin.login(t, store.FirstAdminEmail, testAdminPassword)
	uid := seedVerifiedUser(t, db, "u@example.com", "user-pass-1")
	user := newClient(t, srv)
	user.login(t, "u@example.com", "user-pass-1")

	// 只剩一个启用的 admin：降级/停用都被护栏拦下（自己也算）。
	adminID, err := store.FirstAdminID(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct{ path, body string }{
		{fmt.Sprintf("/panel/api/users/%d/role", adminID), `{"role":"user"}`},
		{fmt.Sprintf("/panel/api/users/%d/disabled", adminID), `{"disabled":true}`},
	} {
		if status, body, _ := admin.do(t, http.MethodPut, probe.path, probe.body); status != http.StatusBadRequest {
			t.Fatalf("最后一个 admin %s = %d %s，期望 400", probe.path, status, body)
		}
	}

	// 升普通用户为 admin：老会话立刻拿到治理面（角色每请求联查，不用重登）。
	if status, body, _ := admin.do(t, http.MethodPut,
		fmt.Sprintf("/panel/api/users/%d/role", uid), `{"role":"admin"}`); status != http.StatusNoContent {
		t.Fatalf("升级 = %d %s", status, body)
	}
	if status, _, _ := user.do(t, http.MethodGet, "/panel/api/channels", ""); status != http.StatusOK {
		t.Fatalf("升级后老会话打治理面 = %d，期望 200", status)
	}

	// 有替补后原 admin 自降合法（交接），降完立刻 403。
	if status, body, _ := admin.do(t, http.MethodPut,
		fmt.Sprintf("/panel/api/users/%d/role", adminID), `{"role":"user"}`); status != http.StatusNoContent {
		t.Fatalf("自降 = %d %s", status, body)
	}
	if status, _, _ := admin.do(t, http.MethodGet, "/panel/api/channels", ""); status != http.StatusForbidden {
		t.Fatalf("自降后打治理面 = %d，期望 403", status)
	}
	// 把两个号都复位：原 admin 升回来，替补降回 user，剩下的用例按初始格局跑。
	if status, _, _ := user.do(t, http.MethodPut,
		fmt.Sprintf("/panel/api/users/%d/role", adminID), `{"role":"admin"}`); status != http.StatusNoContent {
		t.Fatalf("升回原 admin 失败：%d", status)
	}
	if status, _, _ := admin.do(t, http.MethodPut,
		fmt.Sprintf("/panel/api/users/%d/role", uid), `{"role":"user"}`); status != http.StatusNoContent {
		t.Fatalf("降回 user 失败：%d", status)
	}

	// 停用即冻结：老会话下一次请求就失效；启用后同一个 cookie 复活（#71 冻结不删行）。
	if status, _, _ := admin.do(t, http.MethodPut,
		fmt.Sprintf("/panel/api/users/%d/disabled", uid), `{"disabled":true}`); status != http.StatusNoContent {
		t.Fatalf("停用失败")
	}
	if _, body, _ := user.do(t, http.MethodGet, "/panel/api/session", ""); !strings.Contains(body, `"authenticated":false`) {
		t.Fatalf("停用后老会话 = %s，期望已冻结", body)
	}
	if status, _, _ := admin.do(t, http.MethodPut,
		fmt.Sprintf("/panel/api/users/%d/disabled", uid), `{"disabled":false}`); status != http.StatusNoContent {
		t.Fatalf("启用失败")
	}
	if _, body, _ := user.do(t, http.MethodGet, "/panel/api/session", ""); !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("启用后老会话 = %s，期望复活", body)
	}

	// 普通用户没有任免权：角色闸在前。
	if status, _, _ := user.do(t, http.MethodPut,
		fmt.Sprintf("/panel/api/users/%d/role", uid), `{"role":"admin"}`); status != http.StatusForbidden {
		t.Fatalf("user 调任免 = %d，期望 403", status)
	}
	if status, _, _ := admin.do(t, http.MethodPut, "/panel/api/users/9999/role", `{"role":"user"}`); status != http.StatusNotFound {
		t.Fatalf("不存在的用户 = %d，期望 404", status)
	}
}

// ── 找回密码 ────────────────────────────────────────────────────────────

func TestPasswordResetFlow(t *testing.T) {
	srv, db, rec := newAuthServer(t, false)
	configureMail(t, db)
	seedVerifiedUser(t, db, "u@example.com", "old-pass-1")

	// 未知邮箱静默 200，不发信。
	cl := newClient(t, srv)
	status, _, _ := cl.do(t, http.MethodPost, "/panel/api/password-reset", `{"email":"ghost@example.com"}`)
	if status != http.StatusOK || rec.count() != 0 {
		t.Fatalf("未知邮箱重置 = %d 发信 %d，期望 200 且不发信", status, rec.count())
	}

	// 真实邮箱：来一封 30 分钟链接。先登录留一个会话，等会儿验证它被吊销。
	sess := newClient(t, srv)
	sess.login(t, "u@example.com", "old-pass-1")
	status, _, _ = cl.do(t, http.MethodPost, "/panel/api/password-reset", `{"email":"u@example.com"}`)
	if status != http.StatusOK || rec.count() != 1 {
		t.Fatalf("重置请求 = %d 发信 %d", status, rec.count())
	}
	token := mailToken(t, rec.last(t).Body)
	if !strings.Contains(rec.last(t).Body, "/panel/reset?token=") {
		t.Fatalf("重置信链接不对：%s", rec.last(t).Body)
	}

	status, _, _ = cl.do(t, http.MethodPost, "/panel/api/password-reset/confirm",
		fmt.Sprintf(`{"token":%q,"password":"new-pass-99"}`, token))
	if status != http.StatusOK {
		t.Fatalf("确认重置 = %d", status)
	}
	// 全部会话吊销（#62 决议 6）：重置的常见动机正是「怕别人还登着」。
	_, body, _ := sess.do(t, http.MethodGet, "/panel/api/session", "")
	if !strings.Contains(body, `"authenticated":false`) {
		t.Fatalf("重置后旧会话 = %s，期望已失效", body)
	}
	// token 一次性。
	status, _, _ = cl.do(t, http.MethodPost, "/panel/api/password-reset/confirm",
		fmt.Sprintf(`{"token":%q,"password":"new-pass-98"}`, token))
	if status != http.StatusBadRequest {
		t.Fatalf("重放重置 token = %d，期望 400", status)
	}
	// 新密码生效，旧的不认。
	status, _, _ = newClient(t, srv).do(t, http.MethodPost, "/panel/api/login",
		`{"email":"u@example.com","password":"old-pass-1"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("旧密码登录 = %d，期望 401", status)
	}
	newClient(t, srv).login(t, "u@example.com", "new-pass-99")
}

// 发信失败的重置请求必须与成功**同形**：这个接口不鉴权，按邮箱分叉的任何状态码
// 都是免费的邮箱存在性预言机——「502 = 这个邮箱在库里」。失败只落服务端日志。
func TestPasswordResetMailFailureIsAnonymous(t *testing.T) {
	srv, db, rec := newAuthServer(t, false)
	configureMail(t, db)
	seedVerifiedUser(t, db, "u@example.com", "user-pass-1")
	cl := newClient(t, srv)

	ghostStatus, ghostBody, _ := cl.do(t, http.MethodPost, "/panel/api/password-reset", `{"email":"ghost@example.com"}`)
	rec.setFail(errors.New("smtp: 550 rejected"))
	realStatus, realBody, _ := cl.do(t, http.MethodPost, "/panel/api/password-reset", `{"email":"u@example.com"}`)
	if realStatus != http.StatusOK || realStatus != ghostStatus || realBody != ghostBody {
		t.Fatalf("发信失败 = %d %s，未知邮箱 = %d %s——两者必须同形", realStatus, realBody, ghostStatus, ghostBody)
	}
}

// 第一个 admin 走重置：settings 里那份复制要跟着动——不同步的话回滚到 #71 前的
// 二进制会拿旧密码放行。
func TestPasswordResetSyncsFirstAdminCopy(t *testing.T) {
	srv, db, rec := newAuthServer(t, false)
	configureMail(t, db)
	cl := newClient(t, srv)
	if status, _, _ := cl.do(t, http.MethodPost, "/panel/api/password-reset",
		fmt.Sprintf(`{"email":%q}`, store.FirstAdminEmail)); status != http.StatusOK {
		t.Fatalf("重置请求 = %d", status)
	}
	token := mailToken(t, rec.last(t).Body)
	if status, _, _ := cl.do(t, http.MethodPost, "/panel/api/password-reset/confirm",
		fmt.Sprintf(`{"token":%q,"password":"brand-new-pw1"}`, token)); status != http.StatusOK {
		t.Fatalf("确认重置 = %d", status)
	}
	hash, err := store.GetSetting(t.Context(), db, store.SettingAdminPasswordHash)
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("brand-new-pw1")) != nil {
		t.Fatal("settings 里的 admin 密码复制没有跟着重置走")
	}
}

// ── OAuth 完成注册 ──────────────────────────────────────────────────────

// 走不了真上游，从「回调已把身份装进接力 token」那一步开始验完成注册页的闭环。
// 上游交互（换 token、取身份）的正确性靠 #69 调研 + 联调，不在单测射程里。
func TestOAuthCompleteFlow(t *testing.T) {
	srv, db, _ := newAuthServer(t, false)
	configureMail(t, db)
	payload := `{"provider":"github","provider_user_id":"4242","email":"oauth@example.com","name":"Oct Ocat"}`
	token, err := store.CreateAuthToken(t.Context(), db, store.TokenOAuthSignup, nil, payload, store.TokenTTLOAuthSignup)
	if err != nil {
		t.Fatal(err)
	}
	cl := newClient(t, srv)

	// pending 只读不消费：页面加载看邮箱那一眼不烧 token。
	for range 2 {
		status, body, _ := cl.do(t, http.MethodGet, "/panel/api/oauth/pending?token="+token, "")
		if status != http.StatusOK || !strings.Contains(body, "oauth@example.com") {
			t.Fatalf("pending = %d %s", status, body)
		}
	}

	// 邀请码不对：token 已消费，响应里要带一个新 token 供原页面重试。
	status, body, _ := cl.do(t, http.MethodPost, "/panel/api/oauth/complete",
		fmt.Sprintf(`{"token":%q,"invite_code":"nope"}`, token))
	if status != http.StatusBadRequest {
		t.Fatalf("坏邀请码 = %d %s", status, body)
	}
	var retry struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &retry); err != nil || len(retry.Token) != 64 {
		t.Fatalf("坏邀请码的响应要带新 token：%s", body)
	}
	if status, _, _ = cl.do(t, http.MethodPost, "/panel/api/oauth/complete",
		fmt.Sprintf(`{"token":%q,"invite_code":"nope"}`, token)); status != http.StatusBadRequest {
		t.Fatalf("旧 token 重放 = %d，期望 400", status)
	}

	// 用新 token + 正确邀请码完成：建号（已验证、无密码）、销码、绑身份、给会话。
	code := inviteCode(t, db)
	status, _, _ = cl.do(t, http.MethodPost, "/panel/api/oauth/complete",
		fmt.Sprintf(`{"token":%q,"invite_code":%q}`, retry.Token, code))
	if status != http.StatusOK {
		t.Fatalf("完成注册 = %d", status)
	}
	status, body, _ = cl.do(t, http.MethodGet, "/panel/api/session", "")
	if !strings.Contains(body, `"email_verified":true`) || !strings.Contains(body, `"has_password":false`) {
		t.Fatalf("OAuth 新号 session = %d %s，期望已验证、无密码", status, body)
	}
	uid, found, err := store.FindOAuthUser(t.Context(), db, "github", "4242")
	if err != nil || !found {
		t.Fatalf("身份没绑上：%v %v", found, err)
	}

	// 唯一登录方式不许拆：无密码 + 只剩一个身份 → 解绑 400。
	status, body, _ = cl.do(t, http.MethodDelete, "/panel/api/account/identities/github", "")
	if status != http.StatusBadRequest || !strings.Contains(body, "唯一的登录方式") {
		t.Fatalf("解绑唯一通道 = %d %s，期望 400", status, body)
	}
	if _, found, _ := store.FindOAuthUser(t.Context(), db, "github", "4242"); !found {
		t.Fatal("被拒的解绑不该真的删了身份")
	}
	_ = uid
}

// 同邮箱已有账号：完成注册直接折成关联，不建新号、不烧邀请码，顺手补验证态。
func TestOAuthCompleteLinksExistingAccount(t *testing.T) {
	srv, db, _ := newAuthServer(t, false)
	uid := seedVerifiedUser(t, db, "u@example.com", "user-pass-1")
	payload := `{"provider":"google","provider_user_id":"g-77","email":"u@example.com","name":"U"}`
	token, err := store.CreateAuthToken(t.Context(), db, store.TokenOAuthSignup, nil, payload, store.TokenTTLOAuthSignup)
	if err != nil {
		t.Fatal(err)
	}
	cl := newClient(t, srv)
	// 邀请码随便填：进的是已有账号，不该被码拦住。
	status, body, _ := cl.do(t, http.MethodPost, "/panel/api/oauth/complete",
		fmt.Sprintf(`{"token":%q,"invite_code":""}`, token))
	if status != http.StatusOK {
		t.Fatalf("关联已有账号 = %d %s", status, body)
	}
	got, found, err := store.FindOAuthUser(t.Context(), db, "google", "g-77")
	if err != nil || !found || got != uid {
		t.Fatalf("身份归属 = (%d, %v, %v)，期望 %d", got, found, err, uid)
	}
}

// ── 声明形态：用户体系整体不挂载（#66） ─────────────────────────────────

func TestDeclarativeHidesUserSystem(t *testing.T) {
	srv, _, _ := newAuthServer(t, true)
	cl := newClient(t, srv)
	cl.login(t, store.FirstAdminEmail, testAdminPassword)

	// 路由级 404——与业务写闸的 409 刻意不同：那边是「资源在别处」，这边是
	// 「这套东西不存在」。
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/panel/api/auth-config"},
		{http.MethodPost, "/panel/api/register"},
		{http.MethodPost, "/panel/api/verify-email"},
		{http.MethodPost, "/panel/api/password-reset"},
		{http.MethodPost, "/panel/api/password-reset/confirm"},
		{http.MethodGet, "/panel/api/oauth/pending"},
		{http.MethodPost, "/panel/api/oauth/complete"},
		{http.MethodPost, "/panel/api/verify-email/resend"},
		{http.MethodGet, "/panel/api/account/identities"},
		{http.MethodGet, "/panel/api/users"},
		{http.MethodPost, "/panel/api/users"},
		{http.MethodPut, "/panel/api/users/1/role"},
		{http.MethodPut, "/panel/api/users/1/disabled"},
		{http.MethodGet, "/panel/api/invite-codes"},
		{http.MethodPost, "/panel/api/invite-codes"},
		{http.MethodGet, "/panel/api/auth-settings"},
		{http.MethodPut, "/panel/api/auth-settings"},
		{http.MethodPost, "/panel/api/auth-settings/test-email"},
	} {
		if status, _, _ := cl.do(t, probe.method, probe.path, "{}"); status != http.StatusNotFound {
			t.Errorf("声明形态 %s %s = %d，期望 404", probe.method, probe.path, status)
		}
	}
	// OAuth 两条浏览器导航路由不注册后落进 NoRoute 的 SPA 兜底（回页面，不是裸
	// 404）——断言的是「handler 不在」：注册着的 start 一定回 302 跳转。
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noRedirect.Get(srv.URL + "/panel/oauth/github/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusFound {
		t.Errorf("声明形态 oauth start 还在跳转，期望路由不存在")
	}
	// 治理面的读照常活着——消失的只是用户体系。
	if status, _, _ := cl.do(t, http.MethodGet, "/panel/api/channels", ""); status != http.StatusOK {
		t.Errorf("声明形态读渠道 = %d，期望 200", status)
	}
}

// ── 配置面：secret 永不回显 ─────────────────────────────────────────────

func TestAuthSettingsSecretsNeverEcho(t *testing.T) {
	srv, _, _ := newAuthServer(t, false)
	cl := newClient(t, srv)
	cl.login(t, store.FirstAdminEmail, testAdminPassword)

	status, _, _ := cl.do(t, http.MethodPut, "/panel/api/auth-settings", `{
		"site_url": "https://gw.example.com/",
		"smtp": {"host":"smtp.example.com","port":"587","encryption":"starttls",
		         "username":"mailer","password":"smtp-secret-value","from":"noreply@example.com"},
		"github": {"client_id":"gh-id","secret":"gh-secret-value"}
	}`)
	if status != http.StatusNoContent {
		t.Fatalf("写配置 = %d", status)
	}
	status, body, _ := cl.do(t, http.MethodGet, "/panel/api/auth-settings", "")
	if status != http.StatusOK {
		t.Fatalf("读配置 = %d", status)
	}
	for _, secret := range []string{"smtp-secret-value", "gh-secret-value"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret %q 回显进了配置读取：%s", secret, body)
		}
	}
	if !strings.Contains(body, `"password_set":true`) || !strings.Contains(body, `"secret_set":true`) {
		t.Fatalf("secret 的「设没设」布尔不对：%s", body)
	}
	// 尾斜杠被吞掉，回调 URL 拼好给人复制。
	if !strings.Contains(body, `"site_url":"https://gw.example.com"`) ||
		!strings.Contains(body, `"github":"https://gw.example.com/panel/oauth/github/callback"`) {
		t.Fatalf("站点地址/回调 URL 不对：%s", body)
	}

	// nil = 不动：不带 password 字段的更新保留现值。
	if status, _, _ = cl.do(t, http.MethodPut, "/panel/api/auth-settings",
		`{"smtp":{"host":"smtp2.example.com"}}`); status != http.StatusNoContent {
		t.Fatalf("部分更新 = %d", status)
	}
	_, body, _ = cl.do(t, http.MethodGet, "/panel/api/auth-settings", "")
	if !strings.Contains(body, `"host":"smtp2.example.com"`) || !strings.Contains(body, `"password_set":true`) {
		t.Fatalf("部分更新后配置不对：%s", body)
	}
	// 空串 = 明确清空。
	if status, _, _ = cl.do(t, http.MethodPut, "/panel/api/auth-settings",
		`{"smtp":{"password":""}}`); status != http.StatusNoContent {
		t.Fatalf("清空 = %d", status)
	}
	_, body, _ = cl.do(t, http.MethodGet, "/panel/api/auth-settings", "")
	if !strings.Contains(body, `"password_set":false`) {
		t.Fatalf("清空后 password_set 应为 false：%s", body)
	}

	// 校验闸：加密档拼错、端口不是数、站点地址不是 http(s)。
	for _, bad := range []string{
		`{"smtp":{"encryption":"tls"}}`,
		`{"smtp":{"port":"99999"}}`,
		`{"site_url":"gw.example.com"}`,
	} {
		if status, _, _ = cl.do(t, http.MethodPut, "/panel/api/auth-settings", bad); status != http.StatusBadRequest {
			t.Errorf("坏配置 %s = %d，期望 400", bad, status)
		}
	}
}
