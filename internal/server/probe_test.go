package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonGino/ai-gateway/internal/gatewaytest"
)

// 协议可达性探测（口径层 v0.33 §2.2）：只提示、不落库、不参与路由。它要回答的是
// 「勾上的这几个子路径上游到底提供不提供」——勾错了的后果是那一半客户端全 404，
// 而启动闸看不见（那要发包才知道）。

type probeResponse struct {
	Results []struct {
		Protocol  string `json:"protocol"`
		Reachable bool   `json:"reachable"`
		Status    int    `json:"status"`
		Detail    string `json:"detail"`
	} `json:"results"`
}

// 一个只提供 CC 的上游：/v1/chat/completions 回 400（缺 model），/v1/responses 回
// 404。判据正是这个区别——**不是 2xx**：探测发的是空 JSON 体，任何真实上游都会拿
// 400 或 401 回绝它，而那恰恰证明路由存在。
func ccOnlyUpstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"missing model"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestProbeSeparatesMissingSubPathFromExistingOne(t *testing.T) {
	url := ccOnlyUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "half-open", "openai,openai_responses", url, "sk-upstream")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe", "", &got)

	if len(got.Results) != 2 {
		t.Fatalf("期望两个协议各一条结果，得到 %+v", got.Results)
	}
	byProto := map[string]bool{}
	for _, r := range got.Results {
		byProto[r.Protocol] = r.Reachable
	}
	if !byProto["openai"] {
		t.Error("上游回 400 说明子路径存在，不该判成不可达")
	}
	if byProto["openai_responses"] {
		t.Error("上游回 404，应判为子路径不存在")
	}
}

// 探测不改任何东西：它是独立的一次 POST，不缝在保存事务里，跑完渠道还是原样。
func TestProbeDoesNotPersistAnything(t *testing.T) {
	url := ccOnlyUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "half-open", "openai,openai_responses", url, "sk-upstream")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe", "", nil)

	var protocols string
	if err := db.QueryRow(`SELECT protocols FROM channels WHERE id = ?`, ch).Scan(&protocols); err != nil {
		t.Fatalf("读 protocols 失败: %v", err)
	}
	if protocols != "openai,openai_responses" {
		t.Errorf("探测改了协议集：%q——它只该提示", protocols)
	}
}

// 上游 key 与 base_url 不能出现在探测响应里（CLAUDE.md 的硬约束）。连不上时最容易
// 漏——Go 的传输错误原文里带着完整 URL。
func TestProbeNeverEchoesTheUpstreamAddress(t *testing.T) {
	db := gatewaytest.NewDB(t)
	const baseURL = "http://127.0.0.1:1/tenant7-secret-path"
	ch := gatewaytest.SeedChannel(t, db, "dead", "openai", baseURL, "sk-upstream-secret")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	_, body := a.Do(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe", "")
	if strings.Contains(body, "tenant7-secret-path") || strings.Contains(body, "sk-upstream-secret") {
		t.Errorf("探测响应泄露了上游地址或凭证：%s", body)
	}

	var got probeResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(got.Results) != 1 || got.Results[0].Reachable {
		t.Errorf("连不上的渠道应判为不可达：%+v", got.Results)
	}
}
