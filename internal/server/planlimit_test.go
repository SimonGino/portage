package server_test

// #215 的记账词集成：订阅渠道撞限映射。上游 429 带 `subscription_sharing_usage_limit_exceeded`
// 的请求落第 14 词 plan_limit_exceeded；同族其余拒绝码（403 user_not_eligible、
// 503 usage_unavailable）仍落 upstream_error；429 只换不摘不冷却，全池撞限把最后那份
// 429 原样交回客户端。构造样本（真实 429 形态未采到，#205 只采到 400 的 {"detail":…}）
// 的三形态读法与 magpie siwcErrorCode 同源：error.code / detail.code / detail 句子带码。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
)

// planLimitUpstreamModel 与 planLimitAP 是这批用例自己的模型名，避免与别家共用一个
// 接入点时互相踩流水。
const (
	planLimitUpstreamModel = "gpt-6.1-sol"
	planLimitAP            = "gw-plan"
)

// ccPlanLimitRequest 是 CC 入站的最小请求：chatgpt_account 渠道声明的是 openai_responses，
// CC 入口必然走转换路径（③下半）——这正是撞限词的落点；同协议 R 入口今天还在透传
// 窗口里（#213 落地后关闭），透传 4xx 的 error 列按 v0.28 纪律留空。
const ccPlanLimitRequest = `{"model":"` + planLimitAP + `","messages":[{"role":"user","content":"hi"}]}`

// farFuture 是「这把凭证不用刷」的过期时刻：懒刷新只认剩 3 分钟内的，够远就一次
// token 端点都不打，用例里不用再起假签发方。
func farFuture() int64 { return time.Now().Add(time.Hour).Unix() }

// siwcCred 拼一把 chatgpt_account 凭证 JSON（§7.13 字段表的最小合法形状）。
func siwcCred(access, refresh string, expiresAt int64) string {
	return `{"client_id":"oaiapp-1","host_id":"urn:uuid:h","sub":"u1","email":"a@b",` +
		`"id_token":"idt","access_token":"` + access + `","refresh_token":"` + refresh + `",` +
		`"expires_at":` + strconv.FormatInt(expiresAt, 10) + `,"scopes":["chatgpt.tokens.use.direct"]}`
}

// seedPlanLimitGateway 种一个 chatgpt_account 渠道（openai_responses）+ 逐把命名的凭证，
// 纳管条目经 store.AddChannelModel 建成（真写门，顺带吃 0 价默认），CC 入站走转换。
// 退避重试关到 0：同一把凭证只打一发，换了几把凭证 upstream 就该被打了几个来回。
func seedPlanLimitGateway(t *testing.T, opts gatewaytest.Options, creds ...string) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "siwc-plan", "openai_responses", up.URL, "")
	if _, err := db.Exec(`UPDATE channels SET credential_type = 'chatgpt_account' WHERE id = ?`, ch); err != nil {
		t.Fatalf("改凭证类型失败: %v", err)
	}
	for i, c := range creds {
		gatewaytest.SeedNamedCredential(t, db, ch, "号"+strconv.Itoa(i+1), c)
	}
	if err := store.AddChannelModel(t.Context(), db, ch, planLimitUpstreamModel, protocol.Set{}); err != nil {
		t.Fatalf("加纳管模型失败: %v", err)
	}
	// 条目走真写门建成后，candidate 挂的 id 从库里回读——AddChannelModel 幂等，
	// 不能再靠裸 INSERT 拿 id。
	var m int64
	if err := db.QueryRow(
		`SELECT id FROM channel_models WHERE channel_id = ? AND upstream_model = ?`,
		ch, planLimitUpstreamModel).Scan(&m); err != nil {
		t.Fatalf("回读纳管条目 id: %v", err)
	}
	ap := gatewaytest.SeedAccessPoint(t, db, planLimitAP)
	gatewaytest.SeedCandidate(t, db, ap, m, 100)
	if opts.Retry == (config.Retry{}) {
		opts.Retry = config.Retry{MaxRetries: 0}
	}
	gw := gatewaytest.StartWith(t, db, opts)
	return gw, up
}

// 撞限码的三种上游说法（refusalCode 认的三形态），加同族两个兄弟码钉「不误报」。
// 真实 429 形态未采到（#205 只采到 400 的 {"detail":…}——那一族恰恰是 detail 两形态），
// 构造样本照 magpie siwcErrorCode 的测试用例写：兄弟两份逐字、撞限那份同形状（message 是
// 我们的话）。三种形态都得**阳性**钉死——refusalCode 任何一支退化成读不出码，下面
// 的用例当场红，而不是只靠兄弟码的阴性断言兜着。
const (
	limitBodyErrorShape = `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"plan limit reached"}}`
	limitBodyDetailCode = `{"detail":{"code":"subscription_sharing_usage_limit_exceeded"}}`
	limitBodyDetailText = `{"detail":"subscription_sharing_usage_limit_exceeded: try later"}`
	eligibleBody        = `{"detail":{"code":"subscription_sharing_user_not_eligible"}}`
	unavailableBody     = `{"detail":"subscription_sharing_usage_unavailable: try later"}`
)

// TestPlanLimitExceededWordOnUpstream429：上游 429 带撞限码——三种形态都落 plan_limit_exceeded。
// 客户端拿到 429（状态码原样保留）、错误原文照记、出站端点非空（打到过上游）。error
// 信封那格才回显上游 message，detail 两格没 message 可读，客户端收到按状态码合成的一句。
func TestPlanLimitExceededWordOnUpstream429(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"error 信封", limitBodyErrorShape, "plan limit reached"},
		{"detail 对象", limitBodyDetailCode, ""},
		{"detail 句子", limitBodyDetailText, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := seedPlanLimitGateway(t, gatewaytest.Options{}, siwcCred("at-1", "rt-1", farFuture()))
			up.RespondWith(http.StatusTooManyRequests, nil, tc.body)

			resp := gw.Post(t, "/v1/chat/completions", ccPlanLimitRequest, nil)
			if resp.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("状态码 = %d, 期望 429 原样交回；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
			}
			if tc.wantMsg != "" {
				if body := gatewaytest.ReadBody(t, resp); !strings.Contains(body, tc.wantMsg) {
					t.Errorf("客户端该看到上游那句 message: %s", body)
				}
			}
			row := gw.LastCallRow(t)
			if !row.Error.Valid || row.Error.String != "plan_limit_exceeded" {
				t.Errorf("error 列 = %v, 期望 plan_limit_exceeded", row.Error)
			}
			if !strings.Contains(row.ErrorDetail.String, "subscription_sharing_usage_limit_exceeded") {
				t.Errorf("错误原文该带撞限码: %q", row.ErrorDetail.String)
			}
			if row.UpstreamEndpoint != "/v1/responses" {
				t.Errorf("出站端点 = %q, 期望 /v1/responses——撞限的请求打到过上游", row.UpstreamEndpoint)
			}
			if row.Status != http.StatusTooManyRequests {
				t.Errorf("流水状态码 = %d, 期望 429", row.Status)
			}
		})
	}
}

// TestSiblingRefusalsStayUpstreamError：同族其余码不换词——403 user_not_eligible 与
// 503 usage_unavailable 仍落 upstream_error（口径层 §2.2 v1.52 逐字）。
func TestSiblingRefusalsStayUpstreamError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"user_not_eligible", http.StatusForbidden, eligibleBody},
		{"usage_unavailable", http.StatusServiceUnavailable, unavailableBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := seedPlanLimitGateway(t, gatewaytest.Options{}, siwcCred("at-1", "rt-1", farFuture()))
			up.RespondWith(tc.status, nil, tc.body)

			resp := gw.Post(t, "/v1/chat/completions", ccPlanLimitRequest, nil)
			if resp.StatusCode != tc.status {
				t.Fatalf("状态码 = %d, 期望 %d 原样交回", resp.StatusCode, tc.status)
			}
			row := gw.LastCallRow(t)
			if !row.Error.Valid || row.Error.String != "upstream_error" {
				t.Errorf("error 列 = %v, 期望 upstream_error——同族其余码不换词", row.Error)
			}
		})
	}
}

// TestPlanLimitSwapsWholePoolWithoutDisabling：全池撞限 → 换遍凭证（两把各打一发）、
// 一把不摘（disabled 仍 0）、回客户端 429、词只落一个。只换不摘不冷却是 v0.95 既有
// 纪律，这里钉它在撞限码上不破。
func TestPlanLimitSwapsWholePoolWithoutDisabling(t *testing.T) {
	gw, up := seedPlanLimitGateway(t, gatewaytest.Options{},
		siwcCred("at-1", "rt-1", farFuture()),
		siwcCred("at-2", "rt-2", farFuture()))
	up.RespondWith(http.StatusTooManyRequests, nil, limitBodyErrorShape)

	resp := gw.Post(t, "/v1/chat/completions", ccPlanLimitRequest, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, 期望 429", resp.StatusCode)
	}
	if n := up.Count(); n != 2 {
		t.Errorf("上游被打 %d 次, 期望 2——429 该换下一把凭证再试", n)
	}
	row := gw.LastCallRow(t)
	if !row.Error.Valid || row.Error.String != "plan_limit_exceeded" {
		t.Errorf("error 列 = %v, 期望 plan_limit_exceeded", row.Error)
	}
	if row.RetryCount != 1 {
		t.Errorf("retry_count = %d, 期望 1——换过一把凭证", row.RetryCount)
	}
	var disabled int
	if err := gw.DB.QueryRow(
		`SELECT COUNT(*) FROM channel_keys WHERE channel_id = (SELECT id FROM channels WHERE name = 'siwc-plan') AND disabled = 1`).
		Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled != 0 {
		t.Errorf("撞限不该摘凭证, %d 把被停用了——停用只由人在管理端做", disabled)
	}
}

// TestPlanLimitDoesNotStackWithReauthRequired：两词不叠加、一档一个词——池里一把
// 凭证刷新时撞死亡码（当场标停用、从本次尝试里摘掉），另一把活着的把请求打到上游
// 撞了限：流水只落 plan_limit_exceeded，死亡那把的停用现场（reason=reauth_required）
// 记在凭证行上，不进这次调用的词。
func TestPlanLimitDoesNotStackWithReauthRequired(t *testing.T) {
	nearExpiry := time.Now().Add(time.Minute).Unix()
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token 端点收不到表单: %v", err)
		}
		switch r.Form.Get("refresh_token") {
		case "rt-dead":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"refresh_token_reused","error_description":"gone"}`))
		case "rt-live":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"at-live-new","token_type":"Bearer","expires_in":3600,` +
				`"scope":"chatgpt.tokens.use.direct","id_token":"idt-2","refresh_token":"rt-live-2"}`))
		default:
			t.Errorf("token 端点收到不认识的 refresh_token: %q", r.Form.Get("refresh_token"))
		}
	}))
	t.Cleanup(token.Close)

	gw, up := seedPlanLimitGateway(t, gatewaytest.Options{SubscriptionIssuer: token.URL},
		siwcCred("at-dead", "rt-dead", nearExpiry),
		siwcCred("at-live", "rt-live", nearExpiry))
	up.RespondWith(http.StatusTooManyRequests, nil, limitBodyErrorShape)

	resp := gw.Post(t, "/v1/chat/completions", ccPlanLimitRequest, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, 期望 429；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if n := up.Count(); n != 1 {
		t.Errorf("上游被打 %d 次, 期望 1——死亡那把在出站前就被摘掉", n)
	}
	row := gw.LastCallRow(t)
	if !row.Error.Valid || row.Error.String != "plan_limit_exceeded" {
		t.Errorf("error 列 = %v, 期望 plan_limit_exceeded——一档一个词，不叠 reauth_required", row.Error)
	}
	if got := up.Last(t).Header.Get("Authorization"); got != "Bearer at-live-new" {
		t.Errorf("上游收到的 Bearer = %q, 期望活凭证刷新后的 access", got)
	}
	var disabled int
	var reason string
	if err := gw.DB.QueryRow(
		`SELECT disabled, COALESCE(disabled_reason,'') FROM channel_keys WHERE credential LIKE '%rt-dead%'`).
		Scan(&disabled, &reason); err != nil {
		t.Fatal(err)
	}
	if disabled == 0 || reason != "reauth_required" {
		t.Errorf("死亡那把该带着 reauth_required 停用, got disabled=%d reason=%q", disabled, reason)
	}
	if err := gw.DB.QueryRow(
		`SELECT disabled FROM channel_keys WHERE credential LIKE '%rt-live%'`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled != 0 {
		t.Error("活着的凭证不该被摘")
	}
}

// TestSubscriptionCallCostsZeroAndSkipsQuota：0 价默认串起整条链——订阅条目建成时
// 四价 0（非 NULL），有 usage 的成功调用 cost 落 0、token 照落，而这笔账不占用户
// 月度 USD 配额（SUM(cost) 不被推着走，下一发照常过闸）。
func TestSubscriptionCallCostsZeroAndSkipsQuota(t *testing.T) {
	gw, up := seedPlanLimitGateway(t, gatewaytest.Options{}, siwcCred("at-1", "rt-1", farFuture()))
	// R 入口撞 openai_responses 渠道是同协议透传（#213 落地前订阅渠道也走这条，
	// 200 成功路径两边一样）；上游把 usage 报满，cost 的判据才有货。
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"id":"resp_1","status":"completed","usage":{"input_tokens":100,"output_tokens":50}}`)

	_, bobID := gw.UserSession(t, "plan-bob@x")
	const bobKey = "sk-ptg-plan-bob"
	seedOwnedKey(t, gw, bobID, "bob 的", bobKey)
	if _, err := gw.DB.Exec(`UPDATE users SET monthly_quota_usd = 5.0 WHERE id = ?`, bobID); err != nil {
		t.Fatalf("设限额失败: %v", err)
	}
	header := map[string]string{"x-api-key": bobKey}

	// 两发：第一发落 cost 0 的账，第二发证明配额没被占——判据是 SUM(cost) 原地没动。
	for i := 0; i < 2; i++ {
		resp := gw.Post(t, "/v1/responses",
			`{"model":"`+planLimitAP+`","stream":false,"input":[{"type":"message","role":"user",`+
				`"content":[{"type":"input_text","text":"ping"}]}]}`, header)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("第 %d 发状态码 = %d, 期望 200；body=%s", i+1, resp.StatusCode, gatewaytest.ReadBody(t, resp))
		}
	}

	var cost sql.NullFloat64
	var inTok, outTok, uid sql.NullInt64
	if err := gw.DB.QueryRow(`SELECT cost, input_tokens, output_tokens, user_id FROM call_logs ORDER BY id DESC LIMIT 1`).
		Scan(&cost, &inTok, &outTok, &uid); err != nil {
		t.Fatalf("读 cost: %v", err)
	}
	if !cost.Valid || cost.Float64 != 0 {
		t.Errorf("cost = %+v, 期望 0 且非 NULL——四价落 0 的「真免费」", cost)
	}
	if !inTok.Valid || inTok.Int64 != 100 || !outTok.Valid || outTok.Int64 != 50 {
		t.Errorf("token 照落: input=%+v output=%+v, 期望 100/50", inTok, outTok)
	}
	if !uid.Valid || uid.Int64 != bobID { // 行归属本人，配额闸的判据才成立
		t.Errorf("行归属 = %+v, 期望 bob(%d)", uid, bobID)
	}
	q, err := store.UserQuotaState(context.Background(), gw.DB, bobID, time.Now())
	if err != nil {
		t.Fatalf("查配额: %v", err)
	}
	if q.SpentUSD != 0 {
		t.Errorf("本月已用 = %v, 期望 0——cost 0 的流水不占用户月度配额", q.SpentUSD)
	}
}
