package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/server"
)

// 形态闸：**管理面存在 ⟺ 有管理密码**（口径层 §2.9 #27）。
//
// 闸只看 cfg（配置文件或 PORTAGE_ADMIN_PASSWORD），不看库——形态是进程属性，而库是
// 共享资源；看库的话，一台带 UI 的观测实例往 settings 里写的密码会把生产那台纯转发机
// 的管理面一起打开。
//
// 要钉的是 **404 是路由级不是鉴权级**：没密码时整个 admin.Mount 都不调，于是
// /admin/api/login 这种本来就不需要会话的端点也一并消失。两者的区别在攻击面上是实的
// ——鉴权级 404 意味着那段代码还在跑，只是拒绝你。
func startGateway(t *testing.T, adminPassword string) *httptest.Server {
	t.Helper()
	db := gatewaytest.NewDB(t)
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

	t.Run("没密码则整个不注册", func(t *testing.T) {
		base := startGateway(t, "").URL
		for _, p := range paths {
			if got := status(t, base, p.method, p.path, p.body); got != http.StatusNotFound {
				t.Errorf("%s %s 回了 %d，纯转发形态下该是路由级 404", p.method, p.path, got)
			}
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
