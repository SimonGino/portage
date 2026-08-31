package server_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/store"
)

// logRow 只取断言用得上的几列。
type logRow struct {
	ID             int64  `json:"id"`
	Endpoint       string `json:"endpoint"`
	ModelRequested string `json:"model_requested"`
	APIKeyName     string `json:"api_key_name"`
	Status         int    `json:"status"`
	// QueueWaitMs 用指针：断言的是「这一列出现在回包里」（#7 之前只写不读），
	// 值类型的 0 分不清「回了 0」和「压根没回这个键」。
	QueueWaitMs *int64 `json:"queue_wait_ms"`
}

// logPage 是 /panel/api/logs 的回包形状（口径层 v0.61 起带 total——页码要跳，
// 得先知道有几页）。Total 按同一组筛选、同一个 before 数出来。
type logPage struct {
	Rows  []logRow `json:"rows"`
	Total int64    `json:"total"`
}

// seedTwoModelGateway 起一个能打两个模型名的网关，其中一个模型的调用会失败。
func seedTwoModelGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "test-anthropic", "anthropic", up.URL, "sk-up")
	for _, m := range []string{"model-a", "model-b"} {
		modelID := gatewaytest.SeedChannelModel(t, db, channelID, m)
		apID := gatewaytest.SeedAccessPoint(t, db, m)
		gatewaytest.SeedCandidate(t, db, apID, modelID, 100)
	}
	return gatewaytest.Start(t, db), up
}

func postModel(t *testing.T, gw *gatewaytest.Gateway, model string) {
	t.Helper()
	resp := gw.Post(t, "/v1/messages", `{"model":"`+model+`","messages":[]}`, nil)
	gatewaytest.ReadBody(t, resp)
}

// 口径层 v0.53：筛选下推后端。此前「只看失败」是前端在已拉回的那一页里过滤——
// 筛出来的是「这一页里的失败」，而人问的是「这段时间的失败」，一分页就露馅。
func TestLogsFilterByModelAndFailure(t *testing.T) {
	gw, up := seedTwoModelGateway(t)

	postModel(t, gw, "model-a")
	up.RespondWith(http.StatusInternalServerError, nil, `{"error":{"message":"炸了"}}`)
	postModel(t, gw, "model-b")
	gw.WaitCallRows(t, 2)

	a := gw.LoggedIn(t)

	t.Run("按模型", func(t *testing.T) {
		var page logPage
		a.JSONInto(t, http.MethodGet, "/panel/api/logs?model=model-a", "", &page)
		if len(page.Rows) != 1 || page.Rows[0].ModelRequested != "model-a" {
			t.Fatalf("按模型筛出来的是 %+v", page.Rows)
		}
	})

	t.Run("只看失败", func(t *testing.T) {
		var page logPage
		a.JSONInto(t, http.MethodGet, "/panel/api/logs?only=bad", "", &page)
		if len(page.Rows) != 1 || page.Rows[0].ModelRequested != "model-b" {
			t.Fatalf("只看失败筛出来的是 %+v", page.Rows)
		}
		// 判据是状态码而不是 error 列非空：上游透传 4xx 的 error 列本就是空的，
		// 漏掉它「只看失败」就名不副实。
		if page.Rows[0].Status < 400 {
			t.Errorf("status = %d, 期望 >= 400", page.Rows[0].Status)
		}
	})

	t.Run("两个条件叠加", func(t *testing.T) {
		var page logPage
		a.JSONInto(t, http.MethodGet, "/panel/api/logs?only=bad&model=model-a", "", &page)
		if len(page.Rows) != 0 {
			t.Fatalf("model-a 没失败过，却筛出了 %+v", page.Rows)
		}
	})

	// 总数跟着筛选走，不是「库里一共多少行」。它是页码的分母，两者不同源的话，
	// 「只看失败」下会按全部流水算出一堆翻过去是空的页。
	t.Run("总数按同一组筛选算", func(t *testing.T) {
		var all, bad logPage
		a.JSONInto(t, http.MethodGet, "/panel/api/logs", "", &all)
		a.JSONInto(t, http.MethodGet, "/panel/api/logs?only=bad", "", &bad)
		if all.Total != 2 || bad.Total != 1 {
			t.Fatalf("total: 全部 = %d（期望 2），只看失败 = %d（期望 1）", all.Total, bad.Total)
		}
	})
}

// #17：按端点筛。model 与 only 筛不动这一格——`/v1/messages` 与
// `/v1/messages/count_tokens` 的入站协议同为 anthropic，而 count_tokens 那些行连模型
// 都常常是空的，不给这个筛子就只能靠时间戳在流水里认它们。
func TestLogsFilterByEndpoint(t *testing.T) {
	gw, _ := newAnthropicGateway(t)

	gw.Post(t, "/v1/messages", anthropicRequest, nil)
	gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil)
	gw.Post(t, "/v1/messages/count_tokens", countTokensRequest, nil)
	gw.WaitCallRows(t, 3)

	a := gw.LoggedIn(t)

	t.Run("只留这个端点的行", func(t *testing.T) {
		var page logPage
		a.JSONInto(t, http.MethodGet,
			"/panel/api/logs?endpoint="+url.QueryEscape("/v1/messages/count_tokens"), "", &page)
		if len(page.Rows) != 2 {
			t.Fatalf("筛出 %d 行，期望 2；rows=%+v", len(page.Rows), page.Rows)
		}
		for _, r := range page.Rows {
			if r.Endpoint != "/v1/messages/count_tokens" {
				t.Errorf("混进了 endpoint = %q 的行", r.Endpoint)
			}
		}
		// total 与行共用 callLogWhere，不同源的话页码的分母就是另一组条件算出来的。
		if page.Total != 2 {
			t.Errorf("total = %d, 期望 2", page.Total)
		}
	})

	// 同为 anthropic 入口的两个端点必须真的分得开——这正是本票的现象：只看协议，
	// 它们是同一档。
	t.Run("同协议的另一个端点不混进来", func(t *testing.T) {
		var page logPage
		a.JSONInto(t, http.MethodGet,
			"/panel/api/logs?endpoint="+url.QueryEscape("/v1/messages"), "", &page)
		if len(page.Rows) != 1 || page.Rows[0].Endpoint != "/v1/messages" {
			t.Fatalf("筛出来的是 %+v，期望只剩那一条 /v1/messages", page.Rows)
		}
	})
}

// 翻页是「before 钉窗口上沿 + offset 在窗口内定位」（口径层 v0.61）。
//
// 单用 offset 会错位：流水是时间序、新行不断插到头部，翻到第二页时 offset 已经被新写入
// 的行推着往后错，同一条出现两次。钉住 before 之后，翻页途中来的新行全落在线上边，第二页
// 与总数都不受影响——而 offset 让「直接跳到第 N 页」成立，那是纯游标做不到的。
func TestLogsPageJumpWithinAnchoredWindow(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	for range 5 {
		postModel(t, gw, "model-a")
	}
	gw.WaitCallRows(t, 5)

	a := gw.LoggedIn(t)
	var first logPage
	a.JSONInto(t, http.MethodGet, "/panel/api/logs?limit=2", "", &first)
	if len(first.Rows) != 2 || first.Total != 5 {
		t.Fatalf("第一页 = %d 行 / total %d, 期望 2 行 / 5", len(first.Rows), first.Total)
	}
	if first.Rows[0].ID <= first.Rows[1].ID {
		t.Errorf("流水没有最新在前：%d, %d", first.Rows[0].ID, first.Rows[1].ID)
	}
	// 管理端进页时把窗口上沿钉在当时的最大 id 上（+1 是因为 before 取的是严格小于）。
	anchor := strconv.FormatInt(first.Rows[0].ID+1, 10)

	// 翻页途中又来了两次调用——不钉 before 的话，它们会把第一页的两行挤到第二页去。
	postModel(t, gw, "model-a")
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 7)

	// 直接跳到第 3 页（offset = 2 页 × 2 行），不必先把前两页翻一遍。
	var third logPage
	a.JSONInto(t, http.MethodGet, "/panel/api/logs?limit=2&offset=4&before="+anchor, "", &third)
	if len(third.Rows) != 1 {
		t.Fatalf("第 3 页 = %d 行, 期望剩下的 1 行", len(third.Rows))
	}
	if third.Total != 5 {
		t.Errorf("total = %d, 期望仍是钉住窗口时的 5——新写进来的两行在线上边", third.Total)
	}

	// 第二页装的仍是原来那五行里的第 3、4 条，新行一个都没挤进来。
	var second logPage
	a.JSONInto(t, http.MethodGet, "/panel/api/logs?limit=2&offset=2&before="+anchor, "", &second)
	if len(second.Rows) != 2 {
		t.Fatalf("第 2 页 = %d 行, 期望 2", len(second.Rows))
	}
	if second.Rows[0].ID >= first.Rows[1].ID {
		t.Errorf("第 2 页首行 id=%d, 不比第 1 页末行 %d 更早——窗口没钉住", second.Rows[0].ID, first.Rows[1].ID)
	}
	if second.Rows[1].ID <= third.Rows[0].ID {
		t.Errorf("第 2 页末行 id=%d 不比第 3 页首行 %d 晚", second.Rows[1].ID, third.Rows[0].ID)
	}
}

// 「没解析到模型名」那一档在模型维度里得有个名字，并且点得进去。
//
// 401 鉴权失败、请求体不是合法 JSON、缺 model 字段这几条路径都在 relay 给
// requestedModel 赋值之前就返回了，流水里那一列是空串。空 label 不能原样进模型维度：
// 用量页的模型下拉用「空串 = 全部模型」表示不筛，撞上之后两行会同时显示成选中。
func TestUsageUnknownModelLabelled(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	postModel(t, gw, "model-a")
	// 合法 JSON 但没有 model 字段——最短的一条走到那个分支的路。
	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", `{"messages":[]}`, nil))
	gw.WaitCallRows(t, 2)

	a := gw.LoggedIn(t)

	var usage struct {
		Rows []struct {
			Label string `json:"label"`
			Calls int64  `json:"calls"`
		} `json:"rows"`
	}
	a.JSONInto(t, http.MethodGet, "/panel/api/usage?by=model", "", &usage)
	var unknown int64
	for _, r := range usage.Rows {
		if r.Label == "" {
			t.Fatalf("模型维度里出现了空 label：%+v", usage.Rows)
		}
		if r.Label == store.UnknownModelLabel {
			unknown = r.Calls
		}
	}
	if unknown != 1 {
		t.Fatalf("%q 这一档 = %d 次, 期望 1（%+v）", store.UnknownModelLabel, unknown, usage.Rows)
	}

	// 下拉里的每一项都是拿这些 label 填的，筛不动就是个点了没反应的死选项。
	var page logPage
	a.JSONInto(t, http.MethodGet,
		"/panel/api/logs?model="+url.QueryEscape(store.UnknownModelLabel), "", &page)
	if len(page.Rows) != 1 || page.Rows[0].ModelRequested != "" {
		t.Fatalf("按 %q 筛出来的是 %+v", store.UnknownModelLabel, page.Rows)
	}
}

// 用量第三个维度（v0.53）：按**网关** API Key 聚合。此前唯一沾边的是「按上游凭证」
// ——名字像、含义完全是另一件事，PO 看着那一档答不上「我这把 key 跑了多少」。
func TestUsageByGatewayKey(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 1)

	var usage struct {
		By   string `json:"by"`
		Rows []struct {
			Label string `json:"label"`
			Calls int64  `json:"calls"`
		} `json:"rows"`
	}
	gw.LoggedIn(t).JSONInto(t, http.MethodGet, "/panel/api/usage?by=key", "", &usage)
	if usage.By != "key" {
		t.Fatalf("by = %q, 期望 key", usage.By)
	}
	if len(usage.Rows) != 1 || usage.Rows[0].Label != "test-default" || usage.Rows[0].Calls != 1 {
		t.Fatalf("按 key 聚合的结果是 %+v, 期望 test-default 一行一次", usage.Rows)
	}
}

// 按天分桶恒返回 days 行、最后一行是**本地时区的今天**（口径层 v0.55；v0.86 起端点
// 是 /usage/buckets，unit=day 完全覆盖已作废的 /usage/daily）。
//
// 两件事一起钉：①没有调用的那天也占一格——只吐有行的日子的话，空着的几天会从横轴上
// 消失、剩下的柱子挤在一起，看起来像是一直在用。②桶按本地日历切，而 created_at 存的
// 是 UTC；UTC+8 下这两者差 8 小时，照 UTC 分桶的话「今天」要到本地早上八点才开始。
func TestUsageBucketsByDay(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 1)

	var daily struct {
		Days int    `json:"days"`
		Unit string `json:"unit"`
		Rows []struct {
			Bucket       string `json:"bucket"`
			Calls        int64  `json:"calls"`
			Errors       int64  `json:"errors"`
			OutputTokens int64  `json:"output_tokens"`
		} `json:"rows"`
	}
	gw.LoggedIn(t).JSONInto(t, http.MethodGet, "/panel/api/usage/buckets?days=7&unit=day", "", &daily)
	if daily.Unit != "day" {
		t.Fatalf("unit = %q, 期望 day", daily.Unit)
	}
	if len(daily.Rows) != 7 {
		t.Fatalf("回了 %d 行, 「7 天」恒是 7 行", len(daily.Rows))
	}
	last := daily.Rows[6]
	if today := time.Now().Format("2006-01-02"); last.Bucket != today {
		t.Errorf("最后一行是 %q, 期望本地时区的今天 %q", last.Bucket, today)
	}
	if last.Calls != 1 {
		t.Errorf("今天这一桶 = %d 次调用, 期望 1", last.Calls)
	}
	for _, r := range daily.Rows[:6] {
		if r.Calls != 0 {
			t.Errorf("%s 这一桶有 %d 次调用, 这几天本该是空的", r.Bucket, r.Calls)
		}
	}
}

// 小时桶接得上，且认不得的 unit 当 day（同 by：只影响展示的查询参数写错了不该让整个
// 页面打不开）。
func TestUsageBucketsUnit(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 1)

	var got struct {
		Unit string `json:"unit"`
		Rows []struct {
			Bucket string `json:"bucket"`
			Calls  int64  `json:"calls"`
		} `json:"rows"`
	}
	a := gw.LoggedIn(t)
	// 前后各取一次时刻：整点恰好在这一发请求中间翻过去时两者差一，那不是缺陷。
	before := time.Now()
	a.JSONInto(t, http.MethodGet, "/panel/api/usage/buckets?days=1&unit=hour", "", &got)
	after := time.Now()
	if got.Unit != "hour" {
		t.Fatalf("unit = %q, 期望 hour", got.Unit)
	}
	// 桶只补到「现在」，不补到窗口右端（口径层 v0.86 ③）：从今天 00:00 到此刻的整点数。
	if n := len(got.Rows); n < before.Hour()+1 || n > after.Hour()+1 {
		t.Fatalf("回了 %d 格, 期望从 00:00 补到此刻共 %d 格", n, before.Hour()+1)
	}
	last := got.Rows[len(got.Rows)-1].Bucket
	if last != before.Format("2006-01-02 15:00") && last != after.Format("2006-01-02 15:00") {
		t.Errorf("最后一格是 %q, 期望此刻所在的整点", last)
	}

	a.JSONInto(t, http.MethodGet, "/panel/api/usage/buckets?days=7&unit=fortnight", "", &got)
	if got.Unit != "day" || len(got.Rows) != 7 {
		t.Errorf("认不得的 unit 回了 unit=%q / %d 行, 期望当成 day 的 7 行", got.Unit, len(got.Rows))
	}
}

// from/to 顶掉 days，且与 days 那条路径口径一致——取同一段时间，合计必须相等。
func TestUsageFromTo(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 1)

	type usageResp struct {
		Days int   `json:"days"`
		From int64 `json:"from"`
		To   int64 `json:"to"`
		Rows []struct {
			Label string `json:"label"`
			Calls int64  `json:"calls"`
		} `json:"rows"`
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	from, to := today.Unix(), today.AddDate(0, 0, 1).Unix()

	a := gw.LoggedIn(t)
	var byDays, byRange usageResp
	a.JSONInto(t, http.MethodGet, "/panel/api/usage?days=1", "", &byDays)
	a.JSONInto(t, http.MethodGet,
		"/panel/api/usage?days=7&from="+strconv.FormatInt(from, 10)+"&to="+strconv.FormatInt(to, 10),
		"", &byRange)

	if len(byDays.Rows) != 1 || len(byRange.Rows) != 1 || byDays.Rows[0] != byRange.Rows[0] {
		t.Fatalf("同一段时间两条路径不等: days %+v / from-to %+v", byDays.Rows, byRange.Rows)
	}
	// 顶掉 days 之后回包里就不该再有 days：这一发算的是那一段，回一个没参与的窗口
	// 档位是撒谎（请求里给的还是 days=7）。
	if byRange.Days != 0 || byRange.From != from || byRange.To != to {
		t.Errorf("回包是 days=%d from=%d to=%d, 期望不带 days 且原样回那一段",
			byRange.Days, byRange.From, byRange.To)
	}
}

// from/to 解析失败回 400，不走「认不得就忽略」那条（口径层 v0.86）：它们定的是**记账
// 范围**，静默忽略会拿整窗的合计去回答「这一小时」——那不是显示得不对，是数字本身错了
// 且看不出来。所以回包里一个数字都不能有，更不能有 rows。
func TestUsageFromToRejectsGarbage(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 1)

	a := gw.LoggedIn(t)
	for _, q := range []string{
		"from=abc&to=123", // 非数字
		"from=123&to=xyz", // 非数字
		"from=abc",        // 只给一个，照样是写错了
		"from=200&to=200", // to == from，空区间
		"from=300&to=200", // to < from
	} {
		status, body := a.Do(t, http.MethodGet, "/panel/api/usage?"+q, "")
		if status != http.StatusBadRequest {
			t.Errorf("%s 回了 %d, 期望 400", q, status)
		}
		if strings.Contains(body, `"rows"`) || strings.ContainsAny(body, "0123456789") {
			t.Errorf("%s 的回包带了数字或 rows: %s", q, body)
		}
	}
}

// 只给一端当没给：一端定不出区间，于是照 days 走。
func TestUsageLoneBoundIgnored(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 1)

	var got struct {
		Days int `json:"days"`
		Rows []struct {
			Calls int64 `json:"calls"`
		} `json:"rows"`
	}
	// 一个远在将来的 from，单给不该把结果清空。
	future := strconv.FormatInt(time.Now().AddDate(1, 0, 0).Unix(), 10)
	gw.LoggedIn(t).JSONInto(t, http.MethodGet, "/panel/api/usage?days=1&from="+future, "", &got)
	if got.Days != 1 || len(got.Rows) != 1 || got.Rows[0].Calls != 1 {
		t.Errorf("单给 from 回了 days=%d / %+v, 期望照 days=1 走出一行一次", got.Days, got.Rows)
	}
}
