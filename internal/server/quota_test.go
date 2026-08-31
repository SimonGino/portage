package server_test

// 每用户月度配额闸（口径层 §2.10，#65/#75）：SUM(cost) ≥ 限额即 429、NULL 不限、
// 0 封停、UTC 自然月分界、count_tokens 豁免、quota_exceeded 落流水。走主接缝：
// 闸的位置（令牌桶后、Resolve 前）与豁免判据（端点不是协议）正是要测的东西。

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

const bobKey = "sk-ptg-bob-quota"

// newQuotaGateway 起一个带用户 bob 与其归属 key 的网关。
func newQuotaGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream, int64) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL, upstreamModel, anthropicCredential)
	g := gatewaytest.Start(t, db)
	_, bobID := g.UserSession(t, "bob@x")
	seedOwnedKey(t, g, bobID, "bob 的", bobKey)
	return g, up, bobID
}

func setQuota(t *testing.T, g *gatewaytest.Gateway, userID int64, quota any) {
	t.Helper()
	if _, err := g.DB.Exec(`UPDATE users SET monthly_quota_usd = ? WHERE id = ?`, quota, userID); err != nil {
		t.Fatalf("设限额失败: %v", err)
	}
}

func seedSpend(t *testing.T, g *gatewaytest.Gateway, userID int64, at time.Time, cost float64) {
	t.Helper()
	_, err := g.DB.Exec(`INSERT INTO call_logs
		(created_at, api_key_name, user_id, client_protocol, upstream_protocol,
		 model_requested, model_upstream, channel_name, status, total_ms, cost)
		VALUES (?, 'bob 的', ?, 'anthropic', 'anthropic', 'm', 'm', 'ch', 200, 1, ?)`,
		at.UTC().Format("2006-01-02 15:04:05"), userID, cost)
	if err != nil {
		t.Fatalf("种流水失败: %v", err)
	}
}

var bobHeader = map[string]string{"x-api-key": bobKey}

// 判据是 ≥ 不是 >：已用恰好到限额就拒（不预扣的另一半——超支只允许发生在「判过闸
// 的那一笔跑出来的账」上，不允许明知到线还放行）。429 不打上游，quota_exceeded
// 落流水且行归属本人。
func TestQuotaGateBlocksAtExactLimit(t *testing.T) {
	g, up, bobID := newQuotaGateway(t)
	setQuota(t, g, bobID, 5.0)
	seedSpend(t, g, bobID, time.Now(), 5.0)
	rows := g.CountCallRows(t)

	resp := g.Post(t, "/v1/messages", anthropicRequest, bobHeader)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, 期望 429；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if body := gatewaytest.ReadBody(t, resp); !strings.Contains(body, "本月配额已用尽") {
		t.Errorf("429 文案没说清楚原因: %s", body)
	}
	if up.Count() != 0 {
		t.Error("超限的请求打到了上游")
	}
	g.WaitCallRows(t, rows+1)
	var word string
	var uid sql.NullInt64
	if err := g.DB.QueryRow(
		`SELECT error, user_id FROM call_logs ORDER BY id DESC LIMIT 1`).Scan(&word, &uid); err != nil {
		t.Fatalf("读流水失败: %v", err)
	}
	if word != "quota_exceeded" {
		t.Errorf("错误词 = %q, 期望 quota_exceeded", word)
	}
	if !uid.Valid || uid.Int64 != bobID {
		t.Errorf("被拒的行也该归属本人：user_id=%+v 期望 %d", uid, bobID)
	}
}

// 差一分钱就放行；上月的账不算进本月（UTC 自然月分界）。
func TestQuotaGateAllowsBelowLimitAndIgnoresLastMonth(t *testing.T) {
	g, _, bobID := newQuotaGateway(t)
	setQuota(t, g, bobID, 5.0)
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	seedSpend(t, g, bobID, monthStart.Add(-time.Hour), 100.0) // 上月巨额，不算
	seedSpend(t, g, bobID, now, 4.99)

	resp := g.Post(t, "/v1/messages", anthropicRequest, bobHeader)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
}

// 0 = 封停：一笔账没有也拒，文案不同（这不是「用尽」，是「封了」）。
func TestQuotaZeroBlocksEverything(t *testing.T) {
	g, up, bobID := newQuotaGateway(t)
	setQuota(t, g, bobID, 0.0)

	resp := g.Post(t, "/v1/messages", anthropicRequest, bobHeader)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, 期望 429", resp.StatusCode)
	}
	if body := gatewaytest.ReadBody(t, resp); !strings.Contains(body, "封停") {
		t.Errorf("封停文案不对: %s", body)
	}
	if up.Count() != 0 {
		t.Error("封停的请求打到了上游")
	}
}

// NULL = 不限额（默认）：已用再多也放行。
func TestQuotaNullIsUnlimited(t *testing.T) {
	g, _, bobID := newQuotaGateway(t)
	seedSpend(t, g, bobID, time.Now(), 99999.0)

	resp := g.Post(t, "/v1/messages", anthropicRequest, bobHeader)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
}

// count_tokens 豁免（判端点不判协议）：封停的账号也能问「这段话多少 token」——
// 它不打生成面，正是客户端拿来判断还能不能发的工具。
func TestQuotaExemptsCountTokens(t *testing.T) {
	g, _, bobID := newQuotaGateway(t)
	setQuota(t, g, bobID, 0.0)

	resp := g.Post(t, "/v1/messages/count_tokens", countTokensRequest, bobHeader)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（count_tokens 豁免）；body=%s",
			resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
}

// 成功转发的行落 user_id（写侧接线）：归属从认出的 key 的 owner 快照。
// 无主 key（DefaultKey）的行留 NULL——不入任何人的账。
func TestCallRowCarriesUserID(t *testing.T) {
	g, _, bobID := newQuotaGateway(t)

	g.Post(t, "/v1/messages", anthropicRequest, bobHeader)
	rows := g.WaitCallRows(t, 1)
	var uid sql.NullInt64
	if err := g.DB.QueryRow(`SELECT user_id FROM call_logs ORDER BY id DESC LIMIT 1`).Scan(&uid); err != nil {
		t.Fatalf("读流水失败: %v", err)
	}
	if !uid.Valid || uid.Int64 != bobID {
		t.Fatalf("归属没落库：user_id=%+v 期望 %d", uid, bobID)
	}

	g.Post(t, "/v1/messages", anthropicRequest, nil) // DefaultKey，无主
	g.WaitCallRows(t, rows+1)
	if err := g.DB.QueryRow(`SELECT user_id FROM call_logs ORDER BY id DESC LIMIT 1`).Scan(&uid); err != nil {
		t.Fatalf("读流水失败: %v", err)
	}
	if uid.Valid {
		t.Errorf("无主 key 的行该留 NULL，得到 %d", uid.Int64)
	}
}

// 调额立即生效：本来放行，把限额压到已用之下，下一发就拒——没有计数器要等失效。
func TestQuotaChangeTakesEffectImmediately(t *testing.T) {
	g, _, bobID := newQuotaGateway(t)
	setQuota(t, g, bobID, 100.0)
	seedSpend(t, g, bobID, time.Now(), 10.0)

	if resp := g.Post(t, "/v1/messages", anthropicRequest, bobHeader); resp.StatusCode != http.StatusOK {
		t.Fatalf("压额前该放行，得到 %d", resp.StatusCode)
	}
	setQuota(t, g, bobID, 10.0)
	if resp := g.Post(t, "/v1/messages", anthropicRequest, bobHeader); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("压额后该立即拒，得到 %d", resp.StatusCode)
	}
}

// admin 的调额接口：204 落库、null 清额、负数 400、没这个人 404、user 角色 403。
func TestSetUserQuotaEndpoint(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	u, bobID := g.UserSession(t, "bob@x")
	a := g.LoggedIn(t)
	path := fmt.Sprintf("/panel/api/users/%d/quota", bobID)

	if status, body := a.Do(t, http.MethodPut, path, `{"monthly_quota_usd":5}`); status != http.StatusNoContent {
		t.Fatalf("设限额期望 204，得到 %d：%s", status, body)
	}
	var quota sql.NullFloat64
	if err := g.DB.QueryRow(`SELECT monthly_quota_usd FROM users WHERE id = ?`, bobID).Scan(&quota); err != nil ||
		!quota.Valid || quota.Float64 != 5 {
		t.Fatalf("限额没落库：%+v err=%v", quota, err)
	}
	if status, _ := a.Do(t, http.MethodPut, path, `{"monthly_quota_usd":null}`); status != http.StatusNoContent {
		t.Fatalf("清限额期望 204，得到 %d", status)
	}
	if err := g.DB.QueryRow(`SELECT monthly_quota_usd FROM users WHERE id = ?`, bobID).Scan(&quota); err != nil || quota.Valid {
		t.Errorf("清限额后该是 NULL：%+v err=%v", quota, err)
	}
	if status, _ := a.Do(t, http.MethodPut, path, `{"monthly_quota_usd":-1}`); status != http.StatusBadRequest {
		t.Errorf("负限额期望 400，得到 %d", status)
	}
	if status, _ := a.Do(t, http.MethodPut, "/panel/api/users/99999/quota", `{"monthly_quota_usd":5}`); status != http.StatusNotFound {
		t.Errorf("没这个人期望 404，得到 %d", status)
	}
	if status, _ := u.Do(t, http.MethodPut, path, `{"monthly_quota_usd":5}`); status != http.StatusForbidden {
		t.Errorf("user 角色调额期望 403，得到 %d", status)
	}
}
