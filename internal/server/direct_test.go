package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/store"
)

// 纳管模型直连（口径层 v0.32）：客户端把 `渠道名/纳管模型名` 填进 model 字段就能
// 打到那个渠道，不必先建接入点。转发出去的 model 是**裸的**纳管模型名——限定名是
// 网关的对外寻址语法，上游不认识它。

const directBody = `{"model":"bailian/qwen3-max","messages":[{"role":"user","content":"hi"}]}`

func seedDirect(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "bailian", "openai", up.URL, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "qwen3-max")
	return gatewaytest.Start(t, db), up
}

func TestDirectQualifiedNameRoutesWithoutAnAccessPoint(t *testing.T) {
	gw, up := seedDirect(t)

	resp := gw.Post(t, "/v1/chat/completions", directBody, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, body)
	}

	got := up.Last(t)
	if !strings.Contains(string(got.Body), `"model":"qwen3-max"`) {
		t.Errorf("上游收到的 model 不是裸纳管模型名: %s", got.Body)
	}
	if strings.Contains(string(got.Body), "bailian/") {
		t.Errorf("限定名漏给了上游，它不认识这个前缀: %s", got.Body)
	}
}

// 日志里记的是客户端填的那个名字。限定名和接入点名在这一列里是平权的——「我调的是
// 什么」和「它去了哪个渠道」本来就是两列。
func TestDirectCallLogsTheQualifiedName(t *testing.T) {
	gw, _ := seedDirect(t)
	gw.Post(t, "/v1/chat/completions", directBody, nil)

	row := gw.LastCallRow(t)
	if row.ModelRequested != "bailian/qwen3-max" {
		t.Errorf("model_requested = %q, 期望限定名", row.ModelRequested)
	}
	if row.ModelUpstream != "qwen3-max" {
		t.Errorf("model_upstream = %q, 期望裸纳管模型名", row.ModelUpstream)
	}
	if row.ChannelName != "bailian" {
		t.Errorf("channel_name = %q", row.ChannelName)
	}
}

// 接入点优先：接入点名恰好长得和某个限定名一样时，走接入点。它是显式配出来的对外
// 契约，限定名是自动派生的。
func TestAccessPointWinsOverAQualifiedNameItShadows(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	other := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)

	ch := gatewaytest.SeedChannel(t, db, "bailian", "openai", up.URL, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "qwen3-max")

	// 另一个渠道上建一个接入点，名字故意撞成上面那条的限定名。
	elsewhere := gatewaytest.SeedChannel(t, db, "elsewhere", "openai", other.URL, "sk-other")
	cm := gatewaytest.SeedChannelModel(t, db, elsewhere, "real-model")
	ap := gatewaytest.SeedAccessPoint(t, db, "bailian/qwen3-max")
	gatewaytest.SeedCandidate(t, db, ap, cm, 100)

	gw := gatewaytest.Start(t, db)
	gw.Post(t, "/v1/chat/completions", directBody, nil)

	if other.Count() != 1 {
		t.Fatalf("接入点指向的上游收到 %d 次请求，期望 1 次（接入点该优先）", other.Count())
	}
	if up.Count() != 0 {
		t.Errorf("直连那条上游收到 %d 次请求，期望 0 次", up.Count())
	}
	if !strings.Contains(string(other.Last(t).Body), `"model":"real-model"`) {
		t.Errorf("改写后的 model 不对: %s", other.Last(t).Body)
	}
}

func TestDirectUnknownQualifiedNameIs404(t *testing.T) {
	gw, _ := seedDirect(t)

	resp := gw.Post(t, "/v1/chat/completions",
		`{"model":"bailian/nope","messages":[]}`, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("状态码 = %d, 期望 404；body=%s", resp.StatusCode, body)
	}
	// 文案不能说「接入点不存在」：客户端填的是限定名，那样会把人引去接入点页面
	// 找一个本来就不该存在的条目。
	if strings.Contains(body, "接入点") {
		t.Errorf("404 文案把限定名说成接入点: %s", body)
	}
}

// 「有这个名字但现在用不了」要和「没这个名字」分开。直连不进启动闸（它没有
// candidates 行），停用只能在请求时发现，一律报 404 会让人以为名字打错了。
func TestDirectDisabledModelIs503NotNotFound(t *testing.T) {
	gw, _ := seedDirect(t)
	if _, err := gw.DB.Exec(`UPDATE channel_models SET disabled = 1`); err != nil {
		t.Fatal(err)
	}

	resp := gw.Post(t, "/v1/chat/completions", directBody, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, 期望 503；body=%s", resp.StatusCode, body)
	}
}

// 白名单逐项精确匹配，两种名平权（口径层 v0.32）。只写接入点名的 key 不能借直连
// 绕过去打到同一个上游——那正是白名单要拦的事。
func TestAllowedModelsGatesTheDirectPathToo(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "bailian", "openai", up.URL, "sk-upstream")
	cm := gatewaytest.SeedChannelModel(t, db, ch, "qwen3-max")
	ap := gatewaytest.SeedAccessPoint(t, db, "gw-cc")
	gatewaytest.SeedCandidate(t, db, ap, cm, 100)

	gw := gatewaytest.Start(t, db)
	const limited = "sk-ptg-only-access-point"
	if _, err := gw.DB.Exec(
		`INSERT INTO api_keys (name, key_hash, allowed_models) VALUES (?, ?, ?)`,
		"限接入点", auth.Hash(limited), "gw-cc"); err != nil {
		t.Fatal(err)
	}
	header := map[string]string{"x-api-key": limited}

	resp := gw.Post(t, "/v1/chat/completions", directBody, header)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("直连状态码 = %d, 期望 403；body=%s", resp.StatusCode, body)
	}
	if up.Count() != 0 {
		t.Errorf("被拦下的请求仍打到了上游 %d 次", up.Count())
	}

	// 同一把 key 走它被授权的接入点仍然通——403 是白名单在起作用，不是把 key 打死了。
	ok := gw.Post(t, "/v1/chat/completions",
		`{"model":"gw-cc","messages":[{"role":"user","content":"hi"}]}`, header)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("接入点状态码 = %d, 期望 200；body=%s", ok.StatusCode, gatewaytest.ReadBody(t, ok))
	}
}

// 白名单里写限定名就该放行限定名。
func TestAllowedModelsAcceptsAQualifiedName(t *testing.T) {
	gw, up := seedDirect(t)
	const limited = "sk-ptg-only-qualified"
	if _, err := gw.DB.Exec(
		`INSERT INTO api_keys (name, key_hash, allowed_models) VALUES (?, ?, ?)`,
		"限限定名", auth.Hash(limited), "bailian/qwen3-max"); err != nil {
		t.Fatal(err)
	}

	resp := gw.Post(t, "/v1/chat/completions", directBody, map[string]string{"x-api-key": limited})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if up.Count() != 1 {
		t.Errorf("上游收到 %d 次请求，期望 1 次", up.Count())
	}
}

// 渠道名不能含 `/`：限定名是拼起来比对的，两边都允许 `/` 的话 `a/b/c` 有两种拆法，
// resolveDirect 的 LIMIT 1 会静默挑一个——同一个对外模型名今天打到这个渠道、明天
// 加了新渠道就换一个，且没有任何提示。歧义在源头掐掉，不在请求时猜。
func TestChannelNameWithSlashIsRejectedOnSave(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	status, body := a.Do(t, http.MethodPost, "/admin/api/channels", `{
		"name":"vendor/relay","protocols":["openai"],"base_url":"`+up.URL+`",
		"credential":"sk-upstream"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("含 `/` 的渠道名应该在保存时就被挡，得到 %d %s", status, body)
	}
	if !strings.Contains(body, "歧义") && !strings.Contains(body, "不能含") {
		t.Errorf("没说清为什么不行：%s", body)
	}
}

// 启动闸复查一遍：手工改过库（或者是这条校验加进来之前建的渠道）也要在启动时拦住，
// 而不是等到某次请求被静默路由到错的渠道。
func TestStartupRejectsChannelNameWithSlash(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "vendor/relay", "openai", up.URL, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "qwen3-max")

	err := store.Validate(t.Context(), db)
	if err == nil {
		t.Fatal("渠道名含 `/` 的库应该起不来")
	}
	if !strings.Contains(err.Error(), "歧义") {
		t.Errorf("启动闸的话没点出歧义这件事：%v", err)
	}
}
