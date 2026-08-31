package gatewaytest

// admin.go 是管理端的测试入口。与转发端共用同一个 *Gateway（同一个引擎、同一个库），
// 因为「两套鉴权互不可用」这条口径只有在同一个进程里才验得出来。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/store"
)

// AdminPassword 是 StartWith 自动设的管理端密码。
const AdminPassword = "admin-test-password"

// AdminEmail 是第一个 admin 的占位邮箱（store.FirstAdminEmail）。#72 起登录按
// 邮箱 + 密码验 users 表，测试登录也得带上它。
const AdminEmail = "admin@localhost"

// AdminClient 是一个带 cookie 罐的客户端。管理端认的是 cookie，用普通 http.Client
// 会每次请求都丢掉会话，表现成「登录成功但下一句就 401」。
type AdminClient struct {
	g  *Gateway
	hc *http.Client
}

// Admin 返回一个**未登录**的管理端客户端。
func (g *Gateway) Admin(t *testing.T) *AdminClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("建 cookie 罐失败: %v", err)
	}
	return &AdminClient{g: g, hc: &http.Client{Jar: jar, Transport: client.Transport}}
}

// LoggedIn 返回一个已经登录好的管理端客户端。
func (g *Gateway) LoggedIn(t *testing.T) *AdminClient {
	t.Helper()
	a := g.Admin(t)
	if status, _ := a.Do(t, http.MethodPost, "/panel/api/login",
		`{"email":"`+AdminEmail+`","password":"`+AdminPassword+`"}`); status != http.StatusOK {
		t.Fatalf("管理端登录失败，status=%d", status)
	}
	return a
}

// Do 发一个管理端请求，返回状态码与响应体。body 为空串即不带请求体。
func (a *AdminClient) Do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, a.g.URL+path, reader)
	if err != nil {
		t.Fatalf("构造管理端请求失败: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		t.Fatalf("请求管理端失败: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读管理端响应失败: %v", err)
	}
	return resp.StatusCode, string(raw)
}

// JSONInto 发一个管理端请求并把响应体解进 out。非 2xx 直接让用例失败——调用方要断言
// 失败状态时用 Do。
func (a *AdminClient) JSONInto(t *testing.T, method, path, body string, out any) {
	t.Helper()
	status, raw := a.Do(t, method, path, body)
	if status < 200 || status >= 300 {
		t.Fatalf("%s %s 期望 2xx，得到 %d：%s", method, path, status, raw)
	}
	if out == nil {
		return
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		t.Fatalf("%s %s 的响应不是合法 JSON（%v）：%s", method, path, err, raw)
	}
}

// UserSession 造一个 user 角色的用户和它的库会话，返回带这枚 cookie 的客户端与
// 用户 id。#72（注册/邮箱登录）之前 user 角色没有任何 HTTP 登录路，会话只能直接
// 落库造——这不是绕过：TouchSession 联查 users 的那半照走，绕开的只有发 cookie。
//
// cookie 名在这里写死成 portage_admin：单 cookie 一套体系（#61）是线上契约，
// 测试硬编码它正是把契约钉住——改名会让这批用例一起红，提醒你那是破坏性变更。
func (g *Gateway) UserSession(t *testing.T, email string) (*AdminClient, int64) {
	t.Helper()
	res, err := g.DB.Exec(
		`INSERT INTO users (email, role, email_verified) VALUES (?, 'user', 1)`, email)
	if err != nil {
		t.Fatalf("种用户失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("读用户 id 失败: %v", err)
	}
	token, _, err := store.CreateSession(t.Context(), g.DB, id)
	if err != nil {
		t.Fatalf("造用户会话失败: %v", err)
	}
	a := g.Admin(t)
	u, err := url.Parse(g.URL)
	if err != nil {
		t.Fatalf("解析网关地址失败: %v", err)
	}
	a.hc.Jar.SetCookies(u, []*http.Cookie{{Name: "portage_admin", Value: token}})
	return a, id
}

// Cookies 返回当前罐子里属于网关的 cookie，供「登出之后 cookie 真的没了」这类断言。
func (a *AdminClient) Cookies(t *testing.T) []*http.Cookie {
	t.Helper()
	u, err := url.Parse(a.g.URL)
	if err != nil {
		t.Fatalf("解析网关地址失败: %v", err)
	}
	return a.hc.Jar.Cookies(u)
}
