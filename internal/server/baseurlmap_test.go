package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol"
)

// 每协议一份出站根地址（口径层 v0.96 ②）：同一渠道给 openai 与 anthropic 填**不同**
// 的根地址，命中哪个协议就打哪份地址。v0.96 之前单一 base_url 表达不出这种渠道——
// 同一家上游把两个协议挂在不同域名下时，只能拆成两个渠道名。

// seedSplitRoots 走**管理 API** 建渠道（票面测试口径点名这条路径）：配置得从人真正
// 用的那个口进来，路由才算被端到端验过——直插 SQL 会绕过 base_url 映射的解析与校验。
func seedSplitRoots(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream, *gatewaytest.Upstream) {
	t.Helper()
	upCC := gatewaytest.NewUpstream(t)
	upA := gatewaytest.NewUpstream(t)
	gw := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := gw.LoggedIn(t)

	var created struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/channels", `{
		"name":"split","base_url":{"openai":"`+upCC.URL+`","anthropic":"`+upA.URL+`"},
		"credential":"sk-upstream"}`, &created)
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(created.ID)+"/models",
		`{"upstream_model":"m-split"}`, nil)
	return gw, upCC, upA
}

// 透传：两条入口各打各的根。CC 入口只到 openai 那份地址，Anthropic 入口只到
// anthropic 那份地址——地址跟着**命中的协议**走，不是跟着渠道走。
func TestPassthroughUsesTheHitProtocolsRoot(t *testing.T) {
	gw, upCC, upA := seedSplitRoots(t)

	resp := gw.Post(t, "/v1/chat/completions",
		`{"model":"split/m-split","messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CC 入口状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if got := upCC.Last(t); got.Path != "/v1/chat/completions" {
		t.Errorf("openai 根收到 %s，期望 /v1/chat/completions", got.Path)
	}
	if upA.Count() != 0 {
		t.Errorf("CC 请求打到了 anthropic 的根上（%d 次）", upA.Count())
	}

	resp = gw.Post(t, "/v1/messages",
		`{"model":"split/m-split","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Anthropic 入口状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if got := upA.Last(t); got.Path != "/v1/messages" {
		t.Errorf("anthropic 根收到 %s，期望 /v1/messages", got.Path)
	}
	if upCC.Count() != 1 {
		t.Errorf("Anthropic 请求打到了 openai 的根上（openai 根共收到 %d 次）", upCC.Count())
	}
}

// 转换路径同样按命中协议取地址：Responses 入口不在集合里，按 cc > responses >
// anthropic 回退选中 openai，请求要落在 openai 那份根上，anthropic 的根一次都不挨。
func TestConversionUsesThePickedProtocolsRoot(t *testing.T) {
	gw, upCC, upA := seedSplitRoots(t)

	resp := gw.Post(t, "/v1/responses", `{"model":"split/m-split","input":"hi"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if got := upCC.Last(t); got.Path != "/v1/chat/completions" {
		t.Errorf("openai 根收到 %s，期望转换后的 /v1/chat/completions", got.Path)
	}
	if upA.Count() != 0 {
		t.Errorf("转换请求打到了 anthropic 的根上（%d 次）", upA.Count())
	}
}

// 支持协议集是从地址派生的：只给 anthropic 填地址的渠道，模型照常挂在 /v1/models
// 上，CC 入口走 CC→Anthropic 转换、打到唯一那份根——不需要任何独立的协议勾选。
func TestDerivedSetDrivesModelsAndRouting(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannelURLs(t, db, "a-only",
		map[protocol.Protocol]string{protocol.Anthropic: up.URL}, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "claude-x")
	gw := gatewaytest.Start(t, db)

	if body := gatewaytest.ReadBody(t, gw.Get(t, "/v1/models")); !strings.Contains(body, "a-only/claude-x") {
		t.Errorf("/v1/models 里没有派生集撑起来的限定名：%s", body)
	}

	resp := gw.Post(t, "/v1/chat/completions",
		`{"model":"a-only/claude-x","messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if got := up.Last(t); got.Path != "/v1/messages" {
		t.Errorf("上游收到 %s，期望转换到 /v1/messages", got.Path)
	}
}

// 管理端：删掉最后一行地址等于渠道一个协议都不声明，服务端要拒（口径层 v0.96 ②）。
// 前端把「删行」直接映射成「map 里少一个键」，这道闸不设在页面上而设在服务端。
func TestAdminRejectsDeletingTheLastBaseURL(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	var created struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/channels", `{
		"name":"one-root","base_url":{"anthropic":"https://api.example.com"},
		"credential":"sk-x"}`, &created)

	status, body := a.Do(t, http.MethodPut, "/admin/api/channels/"+itoa(created.ID),
		`{"name":"one-root","base_url":{}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("删到零地址应 400，得到 %d：%s", status, body)
	}
	if !strings.Contains(body, "至少") {
		t.Errorf("报错没说清「至少留一个」：%s", body)
	}

	// 拒掉之后原地址不动。
	var channels []adminChannel
	a.JSONInto(t, http.MethodGet, "/admin/api/channels", "", &channels)
	if len(channels) != 1 {
		t.Fatalf("渠道丢了：%+v", channels)
	}
}

// base_url 的键必须是三个协议名之一：拼错的键要点名拒掉，而不是被结构体解码静默丢掉
// ——静默丢掉的后果正好是上一条测试拦的「删到零地址」，但报错会指错方向。
func TestAdminRejectsUnknownProtocolKeyInBaseURL(t *testing.T) {
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	status, body := a.Do(t, http.MethodPost, "/admin/api/channels",
		`{"name":"typo","base_url":{"anthropic_messages":"https://api.example.com"},"credential":"sk-x"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("未知协议键应 400，得到 %d：%s", status, body)
	}
	if !strings.Contains(body, "anthropic_messages") {
		t.Errorf("报错要点名是哪个键拼错了：%s", body)
	}
}
