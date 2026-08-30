package server_test

// API Key 多用户化的管理面用例（#73）：归属列与明文掩码、他人 key 只做元数据治理、
// 「我的 Key」限定本人、user 角色进不了治理面、声明形态下用户体系路由整体 404、
// 多用户库导入 409。走主接缝：真引擎、真库、真 cookie——「谁在看」正是这批规则的
// 全部内容，绕过会话就等于没测。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/gatewaytest"
)

// keyRow 是 GET /admin/api/keys 一行里本票关心的字段。
type keyRow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Key   string `json:"key"`
	Owner string `json:"owner"`
	Mine  bool   `json:"mine"`
}

func seedOwnedKey(t *testing.T, g *gatewaytest.Gateway, userID int64, name, plain string) int64 {
	t.Helper()
	res, err := g.DB.Exec(
		`INSERT INTO api_keys (name, key_hash, key_plain, user_id) VALUES (?, ?, ?, ?)`,
		name, auth.Hash(plain), plain, userID)
	if err != nil {
		t.Fatalf("种归属 key 失败: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("读 key id 失败: %v", err)
	}
	return id
}

func findKey(t *testing.T, rows []keyRow, name string) keyRow {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("列表里没有 %q：%+v", name, rows)
	return keyRow{}
}

// admin 看全体 key 的归属与状态，但**明文仅 key 主人可见**（#63）：他人的 key 明文
// 不下发；无主 key 对 admin 按「我的」对待（认领规则本来就把它归给第一个 admin）。
func TestKeysListShowsOwnerAndMasksOthersPlaintext(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	_, bobID := g.UserSession(t, "bob@x")
	seedOwnedKey(t, g, bobID, "bob 的", "sk-ptg-bob-secret")

	a := g.LoggedIn(t)
	var rows []keyRow
	a.JSONInto(t, http.MethodGet, "/admin/api/keys", "", &rows)

	orphan := findKey(t, rows, "test-default") // Start 种的 DefaultKey，无主
	if !orphan.Mine || orphan.Key != gatewaytest.DefaultKey {
		t.Errorf("无主 key 对 admin 该照旧可见可复制：mine=%v key=%q", orphan.Mine, orphan.Key)
	}
	bobs := findKey(t, rows, "bob 的")
	if bobs.Owner != "bob@x" {
		t.Errorf("归属列 = %q，期望 bob@x", bobs.Owner)
	}
	if bobs.Mine {
		t.Error("他人的 key 不该标成 mine")
	}
	if bobs.Key != "" {
		t.Errorf("他人的 key 明文漏出来了：%q", bobs.Key)
	}
	if strings.Contains(func() string { _, body := a.Do(t, http.MethodGet, "/admin/api/keys", ""); return body }(),
		"sk-ptg-bob-secret") {
		t.Error("响应原文里带着 bob 的明文——掩码掩了字段没掩住值")
	}
}

// 他人的 key 只做元数据治理（#63）：停用/启用与删除可以，改名与白名单 403。
func TestOthersKeyAllowsGovernanceOnly(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	_, bobID := g.UserSession(t, "bob@x")
	keyID := seedOwnedKey(t, g, bobID, "bob 的", "sk-ptg-bob-secret")
	a := g.LoggedIn(t)
	path := fmt.Sprintf("/admin/api/keys/%d", keyID)

	// 停用：名字与白名单原样回传（前端的开关就是这么发的）。
	if status, body := a.Do(t, http.MethodPut, path,
		`{"name":"bob 的","allowed_models":"*","disabled":true}`); status != http.StatusNoContent {
		t.Fatalf("停用他人 key 期望 204，得到 %d：%s", status, body)
	}
	var disabled bool
	if err := g.DB.QueryRow(`SELECT disabled FROM api_keys WHERE id = ?`, keyID).Scan(&disabled); err != nil || !disabled {
		t.Fatalf("停用没落库：disabled=%v err=%v", disabled, err)
	}
	// 改名：403，且库里一个字不动。
	if status, body := a.Do(t, http.MethodPut, path,
		`{"name":"抢过来","allowed_models":"*","disabled":true}`); status != http.StatusForbidden {
		t.Fatalf("改他人 key 的名字期望 403，得到 %d：%s", status, body)
	}
	var name string
	if err := g.DB.QueryRow(`SELECT name FROM api_keys WHERE id = ?`, keyID).Scan(&name); err != nil || name != "bob 的" {
		t.Fatalf("403 之后名字还是变了：%q err=%v", name, err)
	}
	// 改白名单：同一道 403。
	if status, _ := a.Do(t, http.MethodPut, path,
		`{"name":"bob 的","allowed_models":"claude-x","disabled":true}`); status != http.StatusForbidden {
		t.Errorf("改他人 key 的白名单期望 403，得到 %d", status)
	}
	// 删除是治理动作，照常。
	if status, body := a.Do(t, http.MethodDelete, path, ""); status != http.StatusNoContent {
		t.Errorf("删他人 key 期望 204，得到 %d：%s", status, body)
	}
}

// 「我的 Key」限定本人（#73）：列表只有自己的，建的归自己名下且明文回读；他人的
// id 与不存在的 id 同一个 404；user 角色进不了治理面（403）。
func TestMyKeysScopedToSelf(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	u, bobID := g.UserSession(t, "bob@x")

	// 建：归自己名下，明文只此一份地回来。
	var created struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	u.JSONInto(t, http.MethodPost, "/admin/api/my/keys",
		`{"name":"我的第一把","allowed_models":"claude-x"}`, &created)
	if !strings.HasPrefix(created.Key, "sk-ptg-") {
		t.Fatalf("建出来的 key 长相不对：%q", created.Key)
	}
	var owner int64
	if err := g.DB.QueryRow(`SELECT user_id FROM api_keys WHERE id = ?`, created.ID).Scan(&owner); err != nil || owner != bobID {
		t.Fatalf("建的 key 没归到自己名下：owner=%d err=%v", owner, err)
	}

	// 列：只有自己的——无主的 DefaultKey 不在里面。
	var rows []keyRow
	u.JSONInto(t, http.MethodGet, "/admin/api/my/keys", "", &rows)
	if len(rows) != 1 || rows[0].Name != "我的第一把" {
		t.Fatalf("我的列表该只有自己那把：%+v", rows)
	}
	if !rows[0].Mine || rows[0].Key != created.Key {
		t.Errorf("自己的 key 该明文回读：mine=%v key=%q", rows[0].Mine, rows[0].Key)
	}

	// 改与删自己的：白名单可自设（#63：自我约束工具不是权限边界）。
	mine := fmt.Sprintf("/admin/api/my/keys/%d", created.ID)
	if status, body := u.Do(t, http.MethodPut, mine,
		`{"name":"改名了","allowed_models":"*","disabled":true}`); status != http.StatusNoContent {
		t.Fatalf("改自己的 key 期望 204，得到 %d：%s", status, body)
	}

	// 别人的（无主 DefaultKey）：与不存在的 id 同一个 404，不区分。
	var orphanID int64
	if err := g.DB.QueryRow(`SELECT id FROM api_keys WHERE name = 'test-default'`).Scan(&orphanID); err != nil {
		t.Fatalf("找 DefaultKey: %v", err)
	}
	for _, id := range []int64{orphanID, 99999} {
		p := fmt.Sprintf("/admin/api/my/keys/%d", id)
		if status, _ := u.Do(t, http.MethodPut, p, `{"name":"抢","allowed_models":"*"}`); status != http.StatusNotFound {
			t.Errorf("PUT %s 期望 404，得到 %d", p, status)
		}
		if status, _ := u.Do(t, http.MethodDelete, p, ""); status != http.StatusNotFound {
			t.Errorf("DELETE %s 期望 404，得到 %d", p, status)
		}
	}
	if status, body := u.Do(t, http.MethodDelete, mine, ""); status != http.StatusNoContent {
		t.Errorf("删自己的 key 期望 204，得到 %d：%s", status, body)
	}

	// user 角色摸治理面：403——「我的 Key」开门不等于整个管理面开门。
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/api/keys", ""},
		{http.MethodGet, "/admin/api/channels", ""},
		{http.MethodPost, "/admin/api/keys", `{"name":"越权"}`},
	} {
		if status, _ := u.Do(t, probe.method, probe.path, probe.body); status != http.StatusForbidden {
			t.Errorf("%s %s 对 user 角色期望 403，得到 %d", probe.method, probe.path, status)
		}
	}
}

// 声明形态互斥（#66 ①）：挂声明文件 ⇒ 用户体系路由整体不注册——404 而不是 409，
// 写闸的 409 清单不收它们；治理面的 key 写接口照旧 409。
func TestUserRoutesUnregisteredInDeclarativeForm(t *testing.T) {
	g := gatewaytest.StartWith(t, gatewaytest.NewDB(t), gatewaytest.Options{Declarative: true})
	a := g.LoggedIn(t)

	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/admin/api/my/keys"},
		{http.MethodPost, "/admin/api/my/keys"},
		{http.MethodPut, "/admin/api/my/keys/1"},
		{http.MethodDelete, "/admin/api/my/keys/1"},
	} {
		status, _ := a.Do(t, probe.method, probe.path, `{"name":"x"}`)
		if status != http.StatusNotFound {
			t.Errorf("声明形态下 %s %s 期望 404（整组不注册），得到 %d", probe.method, probe.path, status)
		}
	}
	// 对照组：治理面的 key 写接口是「挂载了但被声明形态锁写」，仍走 409 写闸。
	if status, _ := a.Do(t, http.MethodPost, "/admin/api/keys", `{"name":"x"}`); status != http.StatusConflict {
		t.Errorf("声明形态下 POST /admin/api/keys 期望 409，得到 %d", status)
	}
}

// 导入闸（#66 ⑤）：多用户库上 POST /admin/api/import 拒绝——覆盖语义会静默清光
// 用户的 key。409 且点名，库里一行不动。
func TestImportRefusedOnMultiUserDB(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	_, bobID := g.UserSession(t, "bob@x")
	seedOwnedKey(t, g, bobID, "bob 的", "sk-ptg-bob-secret")
	a := g.LoggedIn(t)

	status, body := a.Do(t, http.MethodPost, "/admin/api/import", `
api_keys:
  - name: 导入的
    key: sk-ptg-imported
`)
	if status != http.StatusConflict {
		t.Fatalf("多用户库导入期望 409，得到 %d：%s", status, body)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("错误体不是 JSON：%s", body)
	}
	for _, want := range []string{"不支持多用户", "bob 的"} {
		if !strings.Contains(e.Error, want) {
			t.Errorf("报错没点到 %q：%s", want, e.Error)
		}
	}
	var n int
	if err := g.DB.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE name = 'bob 的'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("409 之后 bob 的 key 不该动：n=%d err=%v", n, err)
	}
}
