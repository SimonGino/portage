package server_test

// 我的观测面（#75）：本人流水/用量/配额只给本人的账，出口裁运营细节（凭证名与
// 上游 request-id）；治理面的按用户筛选与 by=user 聚合。走主接缝：「谁在看」由
// 会话决定，归属焊死在 WHERE 里——这两条正是要测的边界。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// seedUserCall 种一行完整的流水：带归属、成本、凭证名与上游 request-id——
// 后两样正是用户侧出口要裁掉的。userID 0 = 无主（NULL）。
func seedUserCall(t *testing.T, g *gatewaytest.Gateway, userID int64, keyName, model string, cost float64) {
	t.Helper()
	var uid any
	if userID != 0 {
		uid = userID
	}
	_, err := g.DB.Exec(`INSERT INTO call_logs
		(created_at, api_key_name, user_id, client_protocol, upstream_protocol,
		 model_requested, model_upstream, channel_name, channel_key_name, status,
		 total_ms, cost, error_detail, upstream_request_id)
		VALUES (?, ?, ?, 'anthropic', 'anthropic', ?, ?, '某渠道', '主力凭证', 400,
		        1, ?, '{"error":"上游这么说的"}', 'req_upstream_secret')`,
		time.Now().UTC().Format("2006-01-02 15:04:05"), keyName, uid, model, model, cost)
	if err != nil {
		t.Fatalf("种流水失败: %v", err)
	}
}

type logsResp struct {
	Rows []struct {
		APIKeyName        string  `json:"api_key_name"`
		User              string  `json:"user"`
		ChannelName       string  `json:"channel_name"`
		ChannelKeyName    string  `json:"channel_key_name"`
		ErrorDetail       *string `json:"error_detail"`
		UpstreamRequestID string  `json:"upstream_request_id"`
	} `json:"rows"`
	Total int64 `json:"total"`
}

// 我的流水只有自己的行，且裁凭证名与上游 request-id、留渠道名与错误原文——
// 「我这次为什么失败」看得见，「网关拿哪份凭证打的」看不见。响应原文级断言：
// 掩了字段没掩住值就算漏。
func TestMyLogsScopedAndScrubbed(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	u, bobID := g.UserSession(t, "bob@x")
	_, carolID := g.UserSession(t, "carol@x")
	seedUserCall(t, g, bobID, "bob 的", "model-a", 1.0)
	seedUserCall(t, g, carolID, "carol 的", "model-b", 2.0)
	seedUserCall(t, g, 0, "", "model-c", 3.0)

	var got logsResp
	u.JSONInto(t, http.MethodGet, "/panel/api/my/logs", "", &got)
	if got.Total != 1 || len(got.Rows) != 1 || got.Rows[0].APIKeyName != "bob 的" {
		t.Fatalf("我的流水该只有自己那行：total=%d rows=%+v", got.Total, got.Rows)
	}
	r := got.Rows[0]
	if r.ChannelKeyName != "" || r.UpstreamRequestID != "" {
		t.Errorf("凭证名与上游 request-id 该裁掉：key=%q reqid=%q", r.ChannelKeyName, r.UpstreamRequestID)
	}
	if r.ChannelName != "某渠道" || r.ErrorDetail == nil || !strings.Contains(*r.ErrorDetail, "上游这么说的") {
		t.Errorf("渠道名与错误原文该保留：channel=%q detail=%v", r.ChannelName, r.ErrorDetail)
	}
	// 原文级：裁的是值不是字段名。
	_, body := u.Do(t, http.MethodGet, "/panel/api/my/logs", "")
	for _, leak := range []string{"主力凭证", "req_upstream_secret", "carol"} {
		if strings.Contains(body, leak) {
			t.Errorf("响应原文漏出 %q：%s", leak, body)
		}
	}
	// 查询参数抢不走别人的账：user 参数在这条路上不存在。
	var probe logsResp
	u.JSONInto(t, http.MethodGet, fmt.Sprintf("/panel/api/my/logs?user=%d", carolID), "", &probe)
	if probe.Total != 1 || probe.Rows[0].APIKeyName != "bob 的" {
		t.Errorf("带 user 参数也只能看自己的：%+v", probe)
	}
}

// 我的用量只聚合自己的行，带 cost_usd；配额接口回限额与本月已用。
func TestMyUsageAndQuota(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	u, bobID := g.UserSession(t, "bob@x")
	_, carolID := g.UserSession(t, "carol@x")
	seedUserCall(t, g, bobID, "bob 的", "model-a", 1.5)
	seedUserCall(t, g, bobID, "bob 的", "model-a", 2.0)
	seedUserCall(t, g, carolID, "carol 的", "model-a", 100.0)

	var usage struct {
		By   string `json:"by"`
		Rows []struct {
			Label   string  `json:"label"`
			Calls   int64   `json:"calls"`
			CostUSD float64 `json:"cost_usd"`
		} `json:"rows"`
	}
	u.JSONInto(t, http.MethodGet, "/panel/api/my/usage", "", &usage)
	if len(usage.Rows) != 1 || usage.Rows[0].Label != "model-a" ||
		usage.Rows[0].Calls != 2 || usage.Rows[0].CostUSD != 3.5 {
		t.Errorf("我的用量该只有自己那 2 笔共 $3.5：%+v", usage.Rows)
	}
	u.JSONInto(t, http.MethodGet, "/panel/api/my/usage?by=key", "", &usage)
	if usage.By != "key" || len(usage.Rows) != 1 || usage.Rows[0].Label != "bob 的" {
		t.Errorf("by=key 该按自己的 key 聚合：by=%s rows=%+v", usage.By, usage.Rows)
	}

	var quota struct {
		MonthlyQuotaUSD *float64 `json:"monthly_quota_usd"`
		SpentUSD        float64  `json:"spent_usd"`
	}
	u.JSONInto(t, http.MethodGet, "/panel/api/my/quota", "", &quota)
	if quota.MonthlyQuotaUSD != nil || quota.SpentUSD != 3.5 {
		t.Errorf("配额该是不限 + 已用 $3.5：%+v", quota)
	}
	if _, err := g.DB.Exec(`UPDATE users SET monthly_quota_usd = 10 WHERE id = ?`, bobID); err != nil {
		t.Fatal(err)
	}
	u.JSONInto(t, http.MethodGet, "/panel/api/my/quota", "", &quota)
	if quota.MonthlyQuotaUSD == nil || *quota.MonthlyQuotaUSD != 10 {
		t.Errorf("设了限额该原样回：%+v", quota)
	}
}

// 「模型」页的只读清单对 user 角色开放：与 /v1/models 同一份谓词。
func TestMyModelsListsRoutable(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	g := gatewaytest.Start(t, db)
	u, _ := g.UserSession(t, "bob@x")

	var got struct {
		Models []struct {
			ID     string `json:"id"`
			Direct bool   `json:"direct"`
		} `json:"models"`
	}
	u.JSONInto(t, http.MethodGet, "/panel/api/my/models", "", &got)
	found := false
	for _, m := range got.Models {
		if m.ID == accessPointModel {
			found = true
		}
	}
	if !found {
		t.Errorf("清单里没有接入点 %q：%+v", accessPointModel, got.Models)
	}
}

// 改本人展示名：204 落库、trim 空白；user 角色可用（不是治理面）。
func TestUpdateProfileDisplayName(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	u, bobID := g.UserSession(t, "bob@x")

	if status, body := u.Do(t, http.MethodPut, "/panel/api/account/profile",
		`{"display_name":"  小 bob  "}`); status != http.StatusNoContent {
		t.Fatalf("改展示名期望 204，得到 %d：%s", status, body)
	}
	var name string
	if err := g.DB.QueryRow(`SELECT display_name FROM users WHERE id = ?`, bobID).Scan(&name); err != nil || name != "小 bob" {
		t.Errorf("展示名没落库（或没 trim）：%q err=%v", name, err)
	}
}

// 治理面的用户维度（#75）：/logs?user= 只回那个人的行，by=user 三种归属各自成行
// ——邮箱 / (无主 key) / (未鉴权)。
func TestAdminLogsUserFilterAndUsageByUser(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	_, bobID := g.UserSession(t, "bob@x")
	seedUserCall(t, g, bobID, "bob 的", "model-a", 1.0)
	seedUserCall(t, g, 0, "无主的", "model-a", 2.0)
	seedUserCall(t, g, 0, "", "model-a", 3.0)
	a := g.LoggedIn(t)

	var logs logsResp
	a.JSONInto(t, http.MethodGet, fmt.Sprintf("/panel/api/logs?user=%d", bobID), "", &logs)
	if logs.Total != 1 || len(logs.Rows) != 1 || logs.Rows[0].User != "bob@x" {
		t.Errorf("按用户筛该只回 bob 的行：total=%d rows=%+v", logs.Total, logs.Rows)
	}
	// 不筛时全量，且 admin 看得到凭证名——裁剪只发生在用户侧出口。
	a.JSONInto(t, http.MethodGet, "/panel/api/logs", "", &logs)
	if logs.Total != 3 {
		t.Errorf("不筛该回 3 行，得到 %d", logs.Total)
	}
	if len(logs.Rows) > 0 && logs.Rows[0].ChannelKeyName != "主力凭证" {
		t.Errorf("admin 的流水该带凭证名：%+v", logs.Rows[0])
	}

	var usage struct {
		Rows []struct {
			Label   string  `json:"label"`
			CostUSD float64 `json:"cost_usd"`
		} `json:"rows"`
	}
	a.JSONInto(t, http.MethodGet, "/panel/api/usage?by=user", "", &usage)
	got := map[string]float64{}
	for _, r := range usage.Rows {
		got[r.Label] = r.CostUSD
	}
	want := map[string]float64{"bob@x": 1.0, "(无主 key)": 2.0, "(未鉴权)": 3.0}
	for label, cost := range want {
		if got[label] != cost {
			t.Errorf("by=user 的 %q = %v, 期望 %v（全部：%+v）", label, got[label], cost, got)
		}
	}
}
