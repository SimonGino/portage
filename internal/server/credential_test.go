package server_test

// credential_test.go 钉住渠道凭证池与 key 层内环（口径层 v0.38）。
//
// 全部走主接缝——真的发一次转发请求，让 upstream 从**它收到的凭证**来判断内环有没有
// 换过。断言换没换绝不去看内部状态：内环是为了让客户端拿到一个成功的响应而存在的，
// 只有上游那一侧的观测才说明这件事真的发生了。

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/config"
	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/store"

	_ "modernc.org/sqlite"
)

// byCredential 让假上游按收到的凭证决定回什么：内环换没换凭证，只有上游看得见。
func byCredential(up *gatewaytest.Upstream, status map[string]int) {
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		code, ok := status[r.Header.Get("x-api-key")]
		if !ok {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`))
	}
}

// seedPool 配一条只有凭证池不同的完整链路：一个渠道、若干份带名字的凭证、
// 一个纳管模型、一个接入点。返回凭证名 → id。
func seedPool(t *testing.T, db *sql.DB, baseURL string, creds ...[2]string) map[string]int64 {
	t.Helper()
	ch := gatewaytest.SeedChannel(t, db, "anthropic-official", "anthropic", baseURL, "")
	ids := map[string]int64{}
	for _, c := range creds {
		ids[c[0]] = gatewaytest.SeedNamedCredential(t, db, ch, c[0], c[1])
	}
	model := gatewaytest.SeedChannelModel(t, db, ch, "claude-sonnet-4-5")
	ap := gatewaytest.SeedAccessPoint(t, db, "gw-sonnet")
	gatewaytest.SeedCandidate(t, db, ap, model, 100)
	return ids
}

func credentialState(t *testing.T, db *sql.DB, id int64) (disabled bool, reason, at string) {
	t.Helper()
	var r, a sql.NullString
	if err := db.QueryRow(
		`SELECT disabled, disabled_reason, disabled_at FROM channel_keys WHERE id = ?`, id).
		Scan(&disabled, &r, &a); err != nil {
		t.Fatalf("读凭证状态失败: %v", err)
	}
	return disabled, r.String, a.String
}

// 401 换下一把，但**不摘**（口径层 v0.95 推翻 v0.38 的「只有 401 摘」）：网关不改
// 凭证状态，停用只由人在管理端做。换到好凭证照样成功，坏的那把状态原样躺着。
func TestKeyRingSwitchesOn401ButDisablesNothing(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	byCredential(up, map[string]int{"sk-bad": http.StatusUnauthorized})
	db := gatewaytest.NewDB(t)
	ids := seedPool(t, db, up.URL, [2]string{"坏号", "sk-bad"}, [2]string{"好号", "sk-good"})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{})

	resp := g.Post(t, "/v1/messages", `{"model":"gw-sonnet","messages":[]}`, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("换到好凭证之后应当成功，得到 %d：%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if got := up.Last(t).Header.Get("x-api-key"); got != "sk-good" {
		t.Errorf("最后一次上游请求用的凭证是 %q，期望换到 sk-good", got)
	}

	// 等一小会儿再看：v0.95 之前摘除是异步落库的，要证明「没有摘」，得给一个还在摘
	// 的实现留出摘的时间。
	time.Sleep(100 * time.Millisecond)
	for name, id := range ids {
		if disabled, _, _ := credentialState(t, db, id); disabled {
			t.Errorf("401 摘掉了凭证 %s——v0.95 之后网关不许动凭证状态", name)
		}
	}

	// 归因记的是**真正发出请求**的那一份，不是第一次试的那一份。
	row := g.LastCallRow(t)
	if row.ChannelKeyName != "好号" {
		t.Errorf("流水记的凭证是 %q，期望 好号", row.ChannelKeyName)
	}
	if row.RetryCount != 1 {
		t.Errorf("换了一次凭证，retry_count 应为 1，得到 %d", row.RetryCount)
	}
}

// 人工停用的凭证不进候选集：请求直接用好的那把，上游一次都不会看到停用的。
func TestDisabledCredentialIsNotSelected(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ids := seedPool(t, db, up.URL, [2]string{"停用号", "sk-off"}, [2]string{"好号", "sk-good"})
	if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1,
		disabled_reason = '人工停用', disabled_at = CURRENT_TIMESTAMP
		WHERE id = ?`, ids["停用号"]); err != nil {
		t.Fatal(err)
	}
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{})

	resp := g.Post(t, "/v1/messages", `{"model":"gw-sonnet","messages":[]}`, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("请求失败：%d", resp.StatusCode)
	}
	if got := up.Count(); got != 1 {
		t.Errorf("请求打了 %d 次上游，停用的凭证不该被试", got)
	}
	if got := up.Last(t).Header.Get("x-api-key"); got != "sk-good" {
		t.Errorf("请求用的凭证是 %q", got)
	}
}

// 403 换而**不摘**——v0.38 起如此（当年只有 401 摘），v0.95 之后「不摘」成了全部
// 状态码的通则，这条继续钉着 403 的换凭证路径。
func TestForbiddenSwitchesCredentialButDisablesNothing(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	byCredential(up, map[string]int{"sk-first": http.StatusForbidden})
	db := gatewaytest.NewDB(t)
	ids := seedPool(t, db, up.URL, [2]string{"一号", "sk-first"}, [2]string{"二号", "sk-second"})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{})

	resp := g.Post(t, "/v1/messages", `{"model":"gw-sonnet","messages":[]}`, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("403 之后应换凭证并成功，得到 %d", resp.StatusCode)
	}
	if got := up.Last(t).Header.Get("x-api-key"); got != "sk-second" {
		t.Errorf("403 没有触发换凭证，最后用的是 %q", got)
	}
	// 等一小会儿：要证明的是「没有摘」，得给一个错误的实现留出摘的时间。
	time.Sleep(100 * time.Millisecond)
	for name, id := range ids {
		if disabled, _, _ := credentialState(t, db, id); disabled {
			t.Errorf("403 摘掉了凭证 %s", name)
		}
	}
}

// 凭证耗尽就是耗尽：最后一次上游响应原样透出去，网关不改写不吞，也不动任何一把的
// 状态（v0.95）。
func TestAllCredentialsExhaustedReturnsUpstreamErrorVerbatim(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	byCredential(up, map[string]int{"sk-a": http.StatusUnauthorized, "sk-b": http.StatusUnauthorized})
	db := gatewaytest.NewDB(t)
	ids := seedPool(t, db, up.URL, [2]string{"一号", "sk-a"}, [2]string{"二号", "sk-b"})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{})

	resp := g.Post(t, "/v1/messages", `{"model":"gw-sonnet","messages":[]}`, nil)

	// 最后一次上游响应原样透出去，网关不改写不吞。
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("凭证耗尽应原样回上游的 401，得到 %d", resp.StatusCode)
	}
	time.Sleep(100 * time.Millisecond)
	for name, id := range ids {
		if disabled, _, _ := credentialState(t, db, id); disabled {
			t.Errorf("凭证 %s 被摘了——v0.95 之后 401 也不摘", name)
		}
	}
	// 失败的这一行也要记下最后用的是哪把（口径层 v0.38）：一次全军覆没的调用恰恰
	// 是最需要知道「换到底了没有」的时候，只记成功的等于把归因留给猜。
	if got := g.LastCallRow(t).ChannelKeyName; got != "二号" {
		t.Errorf("失败流水的 channel_key_name = %q，期望最后用的那份「二号」", got)
	}
}

// 全局尝试上限封的是「一次请求最多打几次上游」，跨凭证累计（口径层 v0.38）。
// 没有它，最坏耗时会随凭证数线性增长——而凭证是运营数据，随时会加。
func TestGlobalAttemptBudgetCapsUpstreamSends(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	up.RespondWith(http.StatusTooManyRequests, nil, `{"error":"slow down"}`)
	db := gatewaytest.NewDB(t)
	ids := seedPool(t, db, up.URL,
		[2]string{"一号", "sk-1"}, [2]string{"二号", "sk-2"},
		[2]string{"三号", "sk-3"}, [2]string{"四号", "sk-4"})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{Retry: config.Retry{
		MaxRetries: 1, MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
	}})

	resp := g.Post(t, "/v1/messages", `{"model":"gw-sonnet","messages":[]}`, nil)

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("预算耗尽应回最后一次上游错误，得到 %d", resp.StatusCode)
	}
	// 4 份凭证 × (1 次 + 1 次重试) = 8 次，被 max_attempts 封在 3 次。
	if got := up.Count(); got != 3 {
		t.Errorf("上游收到 %d 次请求，max_attempts=3 应当封在 3 次", got)
	}
	// 429 换而**不摘**，也不做冷却（v0.38 起如此，v0.95 扩成通则）。等一小会儿再看，
	// 给一个会摘的实现留出摘的时间。
	time.Sleep(100 * time.Millisecond)
	for name, id := range ids {
		if disabled, _, _ := credentialState(t, db, id); disabled {
			t.Errorf("429 摘掉了凭证 %s", name)
		}
	}
}

// polling 是轮转而不是「永远从第一把开始」：每次都从头开始等于第一把跑满、其余当
// 备胎，多凭证摊量的意义就没了。
func TestPollingRotatesAcrossRequests(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	seedPool(t, db, up.URL, [2]string{"一号", "sk-1"}, [2]string{"二号", "sk-2"})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{})

	var used []string
	for range 4 {
		g.Post(t, "/v1/messages", `{"model":"gw-sonnet","messages":[]}`, nil)
		used = append(used, up.Last(t).Header.Get("x-api-key"))
	}

	want := []string{"sk-1", "sk-2", "sk-1", "sk-2"}
	for i := range want {
		if used[i] != want[i] {
			t.Fatalf("轮询顺序是 %v，期望 %v", used, want)
		}
	}
}

// 用量页的按凭证聚合（口径层 v0.38）：「这个号跑了多少」是个聚合问题，只给逐行的
// 日志表等于把 group by 留给人的肉眼做。
func TestUsageAggregatesByCredential(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	seedPool(t, db, up.URL, [2]string{"主号", "sk-1"})
	g := gatewaytest.StartWith(t, db, gatewaytest.Options{})
	g.Post(t, "/v1/messages", `{"model":"gw-sonnet","messages":[]}`, nil)
	g.WaitCallRows(t, 1)

	_, body := g.LoggedIn(t).Do(t, http.MethodGet, "/panel/api/usage?by=credential", "")

	if !strings.Contains(body, `"主号"`) {
		t.Errorf("按凭证聚合的用量里没有这份凭证：%s", body)
	}
	if !strings.Contains(body, `"by":"credential"`) {
		t.Errorf("响应没有回明聚合维度：%s", body)
	}
}

// 老库迁移：v0.38 之前的 channel_keys 没有 name、call_logs 没有 channel_key_name。
// Open 要把两列补上、给存量凭证补名，且第二遍是零行命中（幂等本身就是守卫）。
func TestOpenBackfillsCredentialNames(t *testing.T) {
	path := t.TempDir() + "/legacy.db"

	// 手写的是**旧形状**的两张表，schema.sql 的 IF NOT EXISTS 会跳过它们——这正是
	// 老库的处境。其余表照常由 Open 建。
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE channel_keys (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  channel_id INTEGER NOT NULL,
		  credential TEXT NOT NULL,
		  disabled INTEGER NOT NULL DEFAULT 0,
		  disabled_reason TEXT, disabled_at DATETIME,
		  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
		CREATE TABLE call_logs (
		  id INTEGER PRIMARY KEY AUTOINCREMENT,
		  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		  api_key_name TEXT NOT NULL, client_protocol TEXT NOT NULL,
		  upstream_protocol TEXT NOT NULL, model_requested TEXT NOT NULL,
		  model_upstream TEXT NOT NULL, channel_name TEXT NOT NULL,
		  status INTEGER NOT NULL, retry_count INTEGER NOT NULL DEFAULT 0,
		  ttft_ms INTEGER, total_ms INTEGER NOT NULL,
		  input_tokens INTEGER, output_tokens INTEGER,
		  cache_read_tokens INTEGER, cache_write_tokens INTEGER, error TEXT);
		INSERT INTO channel_keys (channel_id, credential) VALUES (1,'sk-a'), (1,'sk-b'), (2,'sk-c');
		INSERT INTO call_logs (api_key_name, client_protocol, upstream_protocol,
		  model_requested, model_upstream, channel_name, status, total_ms)
		VALUES ('k','anthropic','anthropic','m','mu','ch',200,1);`); err != nil {
		t.Fatal(err)
	}
	legacy.Close()

	for round := 1; round <= 2; round++ {
		db, err := store.Open(path)
		if err != nil {
			t.Fatalf("第 %d 遍打开失败: %v", round, err)
		}
		// 编号按**渠道内**的次序，不是全局 id：一个只有两把凭证的渠道里冒出
		// 「凭证 3」比没有名字更难读。
		for id, want := range map[int64]string{1: "凭证 1", 2: "凭证 2", 3: "凭证 1"} {
			var got string
			if err := db.QueryRow(`SELECT name FROM channel_keys WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("第 %d 遍 channel_keys[%d].name = %q，期望 %q", round, id, got, want)
			}
		}
		// 老流水该列是空串，用量页照样聚合得动（不是 NULL，否则 GROUP BY 会多一档）。
		var name string
		if err := db.QueryRow(`SELECT channel_key_name FROM call_logs WHERE id = 1`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "" {
			t.Errorf("老流水的 channel_key_name 应为空串，得到 %q", name)
		}
		// 真的在**迁移过的库**上跑一遍按凭证聚合——验收要的是「用量页不炸」，只查列值
		// 查不出这件事。老流水走到过上游（它有渠道名），所以不能被归进「(未走到上游)」。
		rows, err := store.UsageBy(context.Background(), db, store.UsageRange{Days: 7}, store.UsageByCredential, 0)
		if err != nil {
			t.Fatalf("第 %d 遍在迁移库上按凭证聚合失败: %v", round, err)
		}
		if len(rows) != 1 || rows[0].Label != "(未记录凭证)" || rows[0].Calls != 1 {
			t.Errorf("第 %d 遍聚合结果 = %+v，期望一行「(未记录凭证)」×1", round, rows)
		}
		db.Close()
	}
}
