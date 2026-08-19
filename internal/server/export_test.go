package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/declcfg"
	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/server"

	"github.com/gin-gonic/gin"
)

const exportPath = "/admin/api/export"

// engineWith 只为拿到路由表：形态闸在注册期就决定了管理面在不在，所以判断得看
// 路由表本身，而不是打一发请求看回什么码。
func engineWith(t *testing.T, adminPassword string) *gin.Engine {
	t.Helper()
	cfg := config.Default()
	cfg.AdminPassword = adminPassword
	return server.New(cfg, gatewaytest.NewDB(t), slog.New(slog.NewTextHandler(io.Discard, nil))).Engine()
}

// TestExportNeedsSessionAndHasNoSecondDoor 钉住导出的**唯一入口是登进管理面**。
//
// 沿用会话不加 step-up 是刻意的（口径层 §2.9 #32）：登录者今天已经能批量读到全部
// 秘密，多一道口令只是仪式感。于是这条端点的安全性完全押在「没有第二扇门」上——
// 一个不鉴权的导出路径、或者一个 CLI 导出子命令，都会让那个判断当场失效。
func TestExportNeedsSessionAndHasNoSecondDoor(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))

	t.Run("没登录就 401", func(t *testing.T) {
		if status, _ := g.Admin(t).Do(t, http.MethodGet, exportPath, ""); status != http.StatusUnauthorized {
			t.Errorf("未登录访问导出应当 401，实得 %d", status)
		}
	})

	t.Run("登录了就拿到明文全文", func(t *testing.T) {
		status, body := g.LoggedIn(t).Do(t, http.MethodGet, exportPath, "")
		if status != http.StatusOK {
			t.Fatalf("导出应当 200，实得 %d：%s", status, body)
		}
		// **不打码**：打了码的文件部署不了，而部署正是它存在的全部理由。
		if !strings.Contains(body, gatewaytest.DefaultKey) {
			t.Error("导出物里该有 API Key 原值")
		}
		if _, err := declcfg.Parse([]byte(body), "export.yaml"); err != nil {
			t.Errorf("导出物该是一份能被解析的声明文件：%v", err)
		}
	})

	t.Run("整个引擎只有这一条导出路由", func(t *testing.T) {
		// 用路由表而不是猜路径：将来谁加了 `/export.yaml` 或者一条挂在
		// /admin/api 之外的导出端点，这条断言会先红。
		var found []string
		for _, r := range engineWith(t, gatewaytest.AdminPassword).Routes() {
			if strings.Contains(r.Path, "export") {
				found = append(found, r.Method+" "+r.Path)
			}
		}
		if len(found) != 1 || found[0] != "GET "+exportPath {
			t.Errorf("导出路由该有且只有 GET %s，实得 %v", exportPath, found)
		}
	})
}

// TestExportGoneInPureForwardingForm 钉住纯转发形态下导出跟着管理面一起消失。
//
// 那台机器上的配置来自文件，人手里本来就有；而它是最可能暴露在公网的一台。
func TestExportGoneInPureForwardingForm(t *testing.T) {
	for _, r := range engineWith(t, "").Routes() {
		if strings.Contains(r.Path, "export") {
			t.Errorf("纯转发形态下不该注册导出路由，实有 %s %s", r.Method, r.Path)
		}
	}
}
