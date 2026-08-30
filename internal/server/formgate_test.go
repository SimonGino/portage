package server_test

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/admin"
	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/server"
)

// 形态闸：**库里存在 admin 用户 或 配了管理密码 → 管理面在**（口径层 §2.9 #27，
// §2.10 #61 演化）。「闸只看 cfg 不看库」的旧口径被 #61 正式推翻——用户是库里的
// 实体，「有没有用户体系」只有库答得上来；不变的那半是横向约束：无 admin 用户且
// 未配密码时，行为与纯转发现状**逐字一致**。
//
// 要钉的是 **404 是路由级不是鉴权级**：两个判据都不中时整个 admin.Mount 都不调，
// 于是 /admin/api/login 这种本来就不需要会话的端点也一并消失。两者的区别在攻击面上
// 是实的——鉴权级 404 意味着那段代码还在跑，只是拒绝你。
func startGateway(t *testing.T, adminPassword string) *httptest.Server {
	t.Helper()
	return startGatewayWith(t, gatewaytest.NewDB(t), adminPassword)
}

// startGatewayWith 收现成的库：闸在 Engine 构建时查一次 admin 用户，要测「库里有
// admin」那一态就得先把号种进去再起引擎。
func startGatewayWith(t *testing.T, db *sql.DB, adminPassword string) *httptest.Server {
	t.Helper()
	gatewaytest.SeedAPIKey(t, db, "test-default", gatewaytest.DefaultKey)
	cfg := config.Default()
	cfg.AdminPassword = adminPassword
	srv := httptest.NewServer(server.New(cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil))).Engine())
	t.Cleanup(srv.Close)
	return srv
}

func status(t *testing.T, base, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestAdminSurfaceExistsOnlyWithAdminPassword(t *testing.T) {
	// 这几条覆盖管理面的三类入口：SPA 页面、需要会话的 API、以及**不需要**会话的登录
	// 端点。最后那条是重点——鉴权级的闸拦不住它，只有路由级的能。
	paths := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/admin", ""},
		{http.MethodGet, "/admin/api/channels", ""},
		{http.MethodPost, "/admin/api/login", `{"password":"admin-test-password"}`},
	}

	t.Run("有密码则管理面在", func(t *testing.T) {
		base := startGateway(t, gatewaytest.AdminPassword).URL
		for _, p := range paths {
			if got := status(t, base, p.method, p.path, p.body); got == http.StatusNotFound {
				t.Errorf("%s %s 回了 404，有密码时管理面应当存在", p.method, p.path)
			}
		}
	})

	t.Run("没密码且库里无 admin 则整个不注册", func(t *testing.T) {
		base := startGateway(t, "").URL
		for _, p := range paths {
			if got := status(t, base, p.method, p.path, p.body); got != http.StatusNotFound {
				t.Errorf("%s %s 回了 %d，纯转发形态下该是路由级 404", p.method, p.path, got)
			}
		}
	})

	// #61 演化出的新一态：密码没配，但库里已有 admin 用户（往常由上一次配着密码的
	// 启动造出来）——管理面照挂，且老密码还能登录。这正是旧闸下会哑掉的形态：
	// 撤掉配置里的密码行，管理面连同「用库里的密码登录」一起消失。
	t.Run("没密码但库里有 admin 也挂载", func(t *testing.T) {
		db := gatewaytest.NewDB(t)
		// 走真实的初始化链路造号：Bootstrap 写 settings 哈希并确保第一个 admin。
		if ok, err := admin.Bootstrap(t.Context(), db, gatewaytest.AdminPassword); err != nil || !ok {
			t.Fatalf("Bootstrap = (%v, %v)", ok, err)
		}
		base := startGatewayWith(t, db, "").URL
		for _, p := range paths {
			if got := status(t, base, p.method, p.path, p.body); got == http.StatusNotFound {
				t.Errorf("%s %s 回了 404，库里有 admin 用户时管理面应当挂载", p.method, p.path)
			}
		}
		if got := status(t, base, http.MethodPost, "/admin/api/login",
			`{"password":"`+gatewaytest.AdminPassword+`"}`); got != http.StatusOK {
			t.Errorf("库里的密码登录回了 %d，期望 200", got)
		}
	})

	// 会话落库的对外可见面（#61 推翻「重启即全吊销是特性」）：同一个库起第二个引擎
	// 等于一次重启，老 cookie 必须还能用。两个实例都不配密码、全凭库里的 admin 撑起
	// 管理面——顺手把新闸也压了一遍。
	t.Run("会话落库后重启仍有效", func(t *testing.T) {
		db := gatewaytest.NewDB(t)
		if ok, err := admin.Bootstrap(t.Context(), db, gatewaytest.AdminPassword); err != nil || !ok {
			t.Fatalf("Bootstrap = (%v, %v)", ok, err)
		}
		first := startGatewayWith(t, db, "")
		resp, err := http.Post(first.URL+"/admin/api/login", "application/json",
			strings.NewReader(`{"password":"`+gatewaytest.AdminPassword+`"}`))
		if err != nil {
			t.Fatalf("登录: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(resp.Cookies()) == 0 {
			t.Fatalf("登录 status=%d cookies=%d", resp.StatusCode, len(resp.Cookies()))
		}

		second := startGatewayWith(t, db, "")
		req, err := http.NewRequest(http.MethodGet, second.URL+"/admin/api/channels", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, ck := range resp.Cookies() {
			req.AddCookie(ck)
		}
		got, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, got.Body)
		got.Body.Close()
		if got.StatusCode != http.StatusOK {
			t.Errorf("重启后老会话回了 %d，落库的会话应当活过重启", got.StatusCode)
		}
	})

	t.Run("转发面与 healthz 不受形态影响", func(t *testing.T) {
		base := startGateway(t, "").URL
		if got := status(t, base, http.MethodGet, "/healthz", ""); got != http.StatusOK {
			t.Errorf("/healthz 是业务面，纯转发形态下必须还在，实得 %d", got)
		}
		// 不带 key 打转发面应当是 401（鉴权级），不是 404——转发面是这个形态存在的
		// 全部理由，闸把它一起关掉就本末倒置了。
		if got := status(t, base, http.MethodPost, "/v1/messages", `{"model":"x"}`); got != http.StatusUnauthorized {
			t.Errorf("/v1/messages 应当回 401（鉴权级），实得 %d", got)
		}
	})
}
