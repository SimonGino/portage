package server_test

// import_test.go 钉导入接口（#59）：POST /panel/api/import 走启动 apply 同一条链路，
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
	a.JSONInto(t, http.MethodPost, "/panel/api/import", importFile, &out)
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
	status, body := a.Do(t, http.MethodPost, "/panel/api/channels",
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

	a.JSONInto(t, http.MethodPost, "/panel/api/import", importFile, nil)
	_, raw := a.Do(t, http.MethodPost, "/panel/api/import", importFile)
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
		status, body := a.Do(t, http.MethodPost, "/panel/api/import", "channels: [")
		if status != http.StatusBadRequest || !strings.Contains(body, "解析声明文件") {
			t.Errorf("坏 YAML 应 400 且点名解析失败，得到 %d：%s", status, body)
		}
	})
	t.Run("未知字段被严格档拒", func(t *testing.T) {
		status, body := a.Do(t, http.MethodPost, "/panel/api/import",
			strings.Replace(importFile, "    base_url:", "    base_urls:", 1))
		if status != http.StatusBadRequest {
			t.Errorf("未知字段应 400，得到 %d：%s", status, body)
		}
	})
	t.Run("自校验错一次报全", func(t *testing.T) {
		bad := strings.Replace(importFile, "  - name: laptop\n    key: sk-ptg-import-test\n", "", 1)
		bad = strings.Replace(bad, "target: qwen/Qwen3-27B", "target: qwen/不存在", 1)
		status, body := a.Do(t, http.MethodPost, "/panel/api/import", bad)
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
		status, body := a.Do(t, http.MethodPost, "/panel/api/import",
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

// TestImportRequiresSession 钉导入与试算都在 auth 组里：它们的响应体能改（或预演
// 改）整份业务配置，未登录连门都不该进。
func TestImportRequiresSession(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	if status, body := g.Admin(t).Do(t, http.MethodPost, "/panel/api/import", importFile); status != http.StatusUnauthorized {
		t.Errorf("未登录导入应 401，得到 %d：%s", status, body)
	}
	if status, body := g.Admin(t).Do(t, http.MethodPost, "/panel/api/import/preview", importFile); status != http.StatusUnauthorized {
		t.Errorf("未登录试算应 401，得到 %d：%s", status, body)
	}
}

// TestExportImportRoundtripIsNoop 钉导出↔导入同一套形状：把当前库导出来再导回去，
// 变更清单为空——两条路对「业务配置长什么样」的理解一旦分叉，这条会先红。
func TestExportImportRoundtripIsNoop(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	a.JSONInto(t, http.MethodPost, "/panel/api/import", importFile, nil)
	status, exported := a.Do(t, http.MethodGet, "/panel/api/export", "")
	if status != http.StatusOK {
		t.Fatalf("导出失败：%d %s", status, exported)
	}
	var out struct {
		Changes []string `json:"changes"`
	}
	a.JSONInto(t, http.MethodPost, "/panel/api/import", exported, &out)
	if len(out.Changes) != 0 {
		t.Errorf("导出再导入应无变化，实际：%v", out.Changes)
	}
}

// TestImportPreviewMatchesRealImportWithoutWriting 钉试算（口径层 v1.03）的三个面：
// ①清单与同一份文件真导入将产生的**逐字一致**——预览的价值全在这个一致上；
// ②库一行不动（事务回滚收场，种子还在、文件里的东西一个没进来）；③试算之后真导入
// 照常能走（试算不留下任何会挡住导入的东西），导完再试算同一份文件回空数组。
func TestImportPreviewMatchesRealImportWithoutWriting(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "old-ap", "anthropic", "https://old.internal", "old-model", "sk-old")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var preview struct {
		Changes []string `json:"changes"`
	}
	a.JSONInto(t, http.MethodPost, "/panel/api/import/preview", importFile, &preview)
	if len(preview.Changes) == 0 {
		t.Fatal("试算没报任何变更，清单不该是空的")
	}

	var seeded, imported int
	if err := db.QueryRow(`SELECT COUNT(*) FROM channels WHERE name='test-anthropic'`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM channels WHERE name='qwen'`).Scan(&imported); err != nil {
		t.Fatal(err)
	}
	if seeded != 1 || imported != 0 {
		t.Errorf("试算动了库：种子渠道剩 %d 个，文件渠道进来 %d 个", seeded, imported)
	}

	// 同一份文件真导入，清单与试算逐字对得上：覆盖语义不受试算影响。
	var applied struct {
		Changes []string `json:"changes"`
	}
	a.JSONInto(t, http.MethodPost, "/panel/api/import", importFile, &applied)
	if strings.Join(applied.Changes, "；") != strings.Join(preview.Changes, "；") {
		t.Errorf("真导入清单与试算不一致：\n试算 %v\n导入 %v", preview.Changes, applied.Changes)
	}

	// 导完之后再试算同一份文件：无变化（空数组不回 null，前端只判长度）。
	var again struct {
		Changes []string `json:"changes"`
	}
	a.JSONInto(t, http.MethodPost, "/panel/api/import/preview", importFile, &again)
	if len(again.Changes) != 0 {
		t.Errorf("导完再试算同一份文件应无变化，实际：%v", again.Changes)
	}
}

// TestImportPreviewRejectsInvalidFile 钉试算的失败面：400 装的是校验原文（与真导入
// 同文，前端原样回显），库一行不动——试算不过 = 真导入也过不了。
func TestImportPreviewRejectsInvalidFile(t *testing.T) {
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "old-ap", "anthropic", "https://old.internal", "old-model", "sk-old")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	bad := strings.Replace(importFile, "target: qwen/Qwen3-27B", "target: qwen/不存在", 1)
	status, body := a.Do(t, http.MethodPost, "/panel/api/import/preview", bad)
	if status != http.StatusBadRequest {
		t.Fatalf("试算自校验错应 400，得到 %d：%s", status, body)
	}
	if !strings.Contains(body, "找不到对应的渠道纳管模型") {
		t.Errorf("试算 400 应带校验原文：%s", body)
	}

	var seeded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM channels WHERE name='test-anthropic'`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if seeded != 1 {
		t.Errorf("失败的试算动了库：种子渠道剩 %d 个", seeded)
	}
}
