package server_test

// import_test.go 钉导入接口（#59）：POST /admin/api/import 走启动 apply 同一条链路，
// 覆盖式（文件里没有的实体删掉）、失败整份回滚一次报全、成功后不切事实源（管理端
// 照常可写）。声明文件形态下的 409 在 admin_test.go 的写闸清单里一并钉着。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

const importFile = `
channels:
  - name: qwen
    base_url:
      openai: https://example.internal/v1
    credentials:
      - name: 主号
        credential: sk-upstream-1
    models:
      - upstream_model: Qwen3-27B
access_points:
  - model: claude-sonnet-4
    candidates:
      - target: qwen/Qwen3-27B
api_keys:
  - name: laptop
    key: sk-ptg-import-test
`

// TestImportOverwritesAndKeepsAdminWritable 钉三件事：覆盖语义（库里已有、文件里
// 没有的实体被删）、变更清单回给前端、导入之后管理端照常可写（不切事实源）。
func TestImportOverwritesAndKeepsAdminWritable(t *testing.T) {
	db := gatewaytest.NewDB(t)
	// 库里先躺一套配置：导入的文件里没有它，导完必须消失——这是「覆盖不是合并」。
	gatewaytest.SeedPassthrough(t, db, "old-ap", "anthropic", "https://old.internal", "old-model", "sk-old")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var out struct {
		Changes []string `json:"changes"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/import", importFile, &out)
	if len(out.Changes) == 0 {
		t.Fatal("导入动了库却没回变更清单")
	}
	joined := strings.Join(out.Changes, "；")
	for _, want := range []string{"新增渠道 qwen", "删除接入点 old-ap", "新增 API Key laptop"} {
		if !strings.Contains(joined, want) {
			t.Errorf("变更清单里缺 %q：%s", want, joined)
		}
	}

	var oldChannels int
	if err := db.QueryRow(`SELECT COUNT(*) FROM channels WHERE name != 'qwen'`).Scan(&oldChannels); err != nil {
		t.Fatal(err)
	}
	if oldChannels != 0 {
		t.Errorf("覆盖式导入之后文件外的渠道还剩 %d 个", oldChannels)
	}

	// 不切事实源：导入之后管理端写接口照常成功，而不是像声明文件形态那样 409。
	status, body := a.Do(t, http.MethodPost, "/admin/api/channels",
		`{"name":"after-import","base_url":{"openai":"https://example.internal/v1"},"credential":"sk-x"}`)
	if status != http.StatusOK {
		t.Errorf("导入之后管理端应照常可写，得到 %d：%s", status, body)
	}
}

// TestImportSameFileTwiceReportsNoChanges 钉「导入是 reconcile 不是重建」：同一份
// 文件导两遍，第二遍的变更清单是空数组（不是 null——前端只判长度）。
func TestImportSameFileTwiceReportsNoChanges(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	a.JSONInto(t, http.MethodPost, "/admin/api/import", importFile, nil)
	_, raw := a.Do(t, http.MethodPost, "/admin/api/import", importFile)
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("第二遍导入的响应不是 JSON：%s", raw)
	}
	if string(out["changes"]) != "[]" {
		t.Errorf("无变化时 changes 应是空数组，得到 %s", out["changes"])
	}
}

// TestImportInvalidFileRollsBackAndReportsAll 钉「失败整份回滚、一次报全」：
// 闸一（selfCheck）与闸二（store.Validate）的问题都在 400 报文里，库一行不变。
func TestImportInvalidFileRollsBackAndReportsAll(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "old-ap", "anthropic", "https://old.internal", "old-model", "sk-old")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	t.Run("解析错", func(t *testing.T) {
		status, body := a.Do(t, http.MethodPost, "/admin/api/import", "channels: [")
		if status != http.StatusBadRequest || !strings.Contains(body, "解析声明文件") {
			t.Errorf("坏 YAML 应 400 且点名解析失败，得到 %d：%s", status, body)
		}
	})
	t.Run("未知字段被严格档拒", func(t *testing.T) {
		status, body := a.Do(t, http.MethodPost, "/admin/api/import",
			strings.Replace(importFile, "    base_url:", "    base_urls:", 1))
		if status != http.StatusBadRequest {
			t.Errorf("未知字段应 400，得到 %d：%s", status, body)
		}
	})
	t.Run("自校验错一次报全", func(t *testing.T) {
		bad := strings.Replace(importFile, "  - name: laptop\n    key: sk-ptg-import-test\n", "", 1)
		bad = strings.Replace(bad, "target: qwen/Qwen3-27B", "target: qwen/不存在", 1)
		status, body := a.Do(t, http.MethodPost, "/admin/api/import", bad)
		if status != http.StatusBadRequest {
			t.Fatalf("自校验错应 400，得到 %d：%s", status, body)
		}
		for _, want := range []string{"找不到对应的渠道纳管模型", "一把 API Key 都没有"} {
			if !strings.Contains(body, want) {
				t.Errorf("一次报全里缺 %q：%s", want, body)
			}
		}
	})
	t.Run("库校验错整份回滚", func(t *testing.T) {
		// base_url 缺 scheme：selfCheck 放行（那是 Validate 的判据），闸二在事务里拦下。
		status, body := a.Do(t, http.MethodPost, "/admin/api/import",
			strings.Replace(importFile, "https://example.internal/v1", "example.internal", 1))
		if status != http.StatusBadRequest {
			t.Errorf("库校验错应 400，得到 %d：%s", status, body)
		}
	})

	// 四次失败之后库必须原封不动：种子渠道还在，文件里的渠道一个没进来。
	var seeded, imported int
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_points WHERE model='old-ap'`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM channels WHERE name='qwen'`).Scan(&imported); err != nil {
		t.Fatal(err)
	}
	if seeded != 1 || imported != 0 {
		t.Errorf("失败的导入动了库：种子接入点剩 %d 个，文件渠道进来 %d 个", seeded, imported)
	}
}

// TestImportRequiresSession 钉导入在 auth 组里：它的响应体能改整份业务配置，
// 未登录连门都不该进。
func TestImportRequiresSession(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	if status, body := g.Admin(t).Do(t, http.MethodPost, "/admin/api/import", importFile); status != http.StatusUnauthorized {
		t.Errorf("未登录导入应 401，得到 %d：%s", status, body)
	}
}

// TestExportImportRoundtripIsNoop 钉导出↔导入同一套形状：把当前库导出来再导回去，
// 变更清单为空——两条路对「业务配置长什么样」的理解一旦分叉，这条会先红。
func TestExportImportRoundtripIsNoop(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	a.JSONInto(t, http.MethodPost, "/admin/api/import", importFile, nil)
	status, exported := a.Do(t, http.MethodGet, "/admin/api/export", "")
	if status != http.StatusOK {
		t.Fatalf("导出失败：%d %s", status, exported)
	}
	var out struct {
		Changes []string `json:"changes"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/import", exported, &out)
	if len(out.Changes) != 0 {
		t.Errorf("导出再导入应无变化，实际：%v", out.Changes)
	}
}
