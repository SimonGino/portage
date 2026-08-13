package server_test

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// logRow 只取断言用得上的几列。
type logRow struct {
	ID             int64  `json:"id"`
	ModelRequested string `json:"model_requested"`
	APIKeyName     string `json:"api_key_name"`
	Status         int    `json:"status"`
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
		var rows []logRow
		a.JSONInto(t, http.MethodGet, "/admin/api/logs?model=model-a", "", &rows)
		if len(rows) != 1 || rows[0].ModelRequested != "model-a" {
			t.Fatalf("按模型筛出来的是 %+v", rows)
		}
	})

	t.Run("只看失败", func(t *testing.T) {
		var rows []logRow
		a.JSONInto(t, http.MethodGet, "/admin/api/logs?only=bad", "", &rows)
		if len(rows) != 1 || rows[0].ModelRequested != "model-b" {
			t.Fatalf("只看失败筛出来的是 %+v", rows)
		}
		// 判据是状态码而不是 error 列非空：上游透传 4xx 的 error 列本就是空的，
		// 漏掉它「只看失败」就名不副实。
		if rows[0].Status < 400 {
			t.Errorf("status = %d, 期望 >= 400", rows[0].Status)
		}
	})

	t.Run("两个条件叠加", func(t *testing.T) {
		var rows []logRow
		a.JSONInto(t, http.MethodGet, "/admin/api/logs?only=bad&model=model-a", "", &rows)
		if len(rows) != 0 {
			t.Fatalf("model-a 没失败过，却筛出了 %+v", rows)
		}
	})
}

// 翻页走 before 游标而不是 offset：流水是时间序、新行不断插到头部，offset 翻到第二页
// 时已经被新写入的行推着错位，同一条会出现两次。
func TestLogsPaginateWithBeforeCursor(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	for i := 0; i < 3; i++ {
		postModel(t, gw, "model-a")
	}
	gw.WaitCallRows(t, 3)

	a := gw.LoggedIn(t)
	var first []logRow
	a.JSONInto(t, http.MethodGet, "/admin/api/logs?limit=2", "", &first)
	if len(first) != 2 {
		t.Fatalf("第一页 = %d 行, 期望 2", len(first))
	}
	if first[0].ID <= first[1].ID {
		t.Errorf("流水没有最新在前：%d, %d", first[0].ID, first[1].ID)
	}

	// 翻页途中又来了一次调用——offset 分页会在这里把第一页的末行再吐一遍。
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 4)

	var next []logRow
	a.JSONInto(t, http.MethodGet,
		"/admin/api/logs?limit=2&before="+strconv.FormatInt(first[1].ID, 10), "", &next)
	if len(next) != 1 {
		t.Fatalf("第二页 = %d 行, 期望剩下的 1 行", len(next))
	}
	if next[0].ID >= first[1].ID {
		t.Errorf("第二页回了 id=%d, 不比游标 %d 更早", next[0].ID, first[1].ID)
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
	gw.LoggedIn(t).JSONInto(t, http.MethodGet, "/admin/api/usage?by=key", "", &usage)
	if usage.By != "key" {
		t.Fatalf("by = %q, 期望 key", usage.By)
	}
	if len(usage.Rows) != 1 || usage.Rows[0].Label != "test-default" || usage.Rows[0].Calls != 1 {
		t.Fatalf("按 key 聚合的结果是 %+v, 期望 test-default 一行一次", usage.Rows)
	}
}

// 按天分桶恒返回 days 行、最后一行是**本地时区的今天**（口径层 v0.55）。
//
// 两件事一起钉：①没有调用的那天也占一格——只吐有行的日子的话，空着的几天会从横轴上
// 消失、剩下的柱子挤在一起，看起来像是一直在用。②桶按本地日历切，而 created_at 存的
// 是 UTC；UTC+8 下这两者差 8 小时，照 UTC 分桶的话「今天」要到本地早上八点才开始。
func TestUsageDailyBuckets(t *testing.T) {
	gw, _ := seedTwoModelGateway(t)
	postModel(t, gw, "model-a")
	gw.WaitCallRows(t, 1)

	var daily struct {
		Days int `json:"days"`
		Rows []struct {
			Day          string `json:"day"`
			Calls        int64  `json:"calls"`
			OutputTokens int64  `json:"output_tokens"`
		} `json:"rows"`
	}
	gw.LoggedIn(t).JSONInto(t, http.MethodGet, "/admin/api/usage/daily?days=7", "", &daily)
	if len(daily.Rows) != 7 {
		t.Fatalf("回了 %d 行, 「7 天」恒是 7 行", len(daily.Rows))
	}
	last := daily.Rows[6]
	if today := time.Now().Format("2006-01-02"); last.Day != today {
		t.Errorf("最后一行是 %q, 期望本地时区的今天 %q", last.Day, today)
	}
	if last.Calls != 1 {
		t.Errorf("今天这一桶 = %d 次调用, 期望 1", last.Calls)
	}
	for _, r := range daily.Rows[:6] {
		if r.Calls != 0 {
			t.Errorf("%s 这一桶有 %d 次调用, 这几天本该是空的", r.Day, r.Calls)
		}
	}
}
