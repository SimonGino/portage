package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 协议可达性探测（口径层 v0.33 §2.2）：只提示、不落库、不参与路由。它要回答的是
// 「勾上的这几个子路径上游到底提供不提供」——勾错了的后果是那一半客户端全 404，
// 而启动闸看不见（那要发包才知道）。

// 探测结果两层（口径层 v0.43）：子路径层按凭证分组（v0.38 逐把凭证探），
// 模型矩阵每模型一行、每侧一格。
type probeResponse struct {
	Credentials []struct {
		Credential string `json:"credential"`
		Disabled   bool   `json:"disabled"`
		Results    []struct {
			Protocol  string `json:"protocol"`
			Reachable bool   `json:"reachable"`
			Status    int    `json:"status"`
			Detail    string `json:"detail"`
		} `json:"results"`
	} `json:"credentials"`
	Models []struct {
		Model   string `json:"model"`
		Results []struct {
			Protocol string `json:"protocol"`
			State    string `json:"state"`
			Status   int    `json:"status"`
			Detail   string `json:"detail"`
		} `json:"results"`
	} `json:"models"`
	ModelCredential string `json:"model_credential"`
}

// only 取唯一那一组凭证的结果——单凭证渠道的用例都只关心那一组。
func (p probeResponse) only(t *testing.T) []struct {
	Protocol  string `json:"protocol"`
	Reachable bool   `json:"reachable"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
} {
	t.Helper()
	if len(p.Credentials) != 1 {
		t.Fatalf("期望一组凭证的结果，得到 %+v", p.Credentials)
	}
	return p.Credentials[0].Results
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

	results := got.only(t)
	if len(results) != 2 {
		t.Fatalf("期望两个协议各一条结果，得到 %+v", results)
	}
	byProto := map[string]bool{}
	for _, r := range results {
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
// 漏——Go 的传输错误原文里带着完整 URL。种一个纳管模型让模型矩阵那一段也跑起来：
// 它的「连不上」文案走的是同一条 Redact 路径，也得盖住。
func TestProbeNeverEchoesTheUpstreamAddress(t *testing.T) {
	db := gatewaytest.NewDB(t)
	const baseURL = "http://127.0.0.1:1/tenant7-secret-path"
	ch := gatewaytest.SeedChannel(t, db, "dead", "openai", baseURL, "sk-upstream-secret")
	gatewaytest.SeedChannelModel(t, db, ch, "some-model")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	_, body := a.Do(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe?models=1", "")
	if strings.Contains(body, "tenant7-secret-path") || strings.Contains(body, "sk-upstream-secret") {
		t.Errorf("探测响应泄露了上游地址或凭证：%s", body)
	}

	var got probeResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if results := got.only(t); len(results) != 1 || results[0].Reachable {
		t.Errorf("连不上的渠道应判为不可达：%+v", results)
	}
	if len(got.Models) != 1 || len(got.Models[0].Results) != 1 {
		t.Fatalf("期望模型矩阵一行一格，得到 %+v", got.Models)
	}
	if cell := got.Models[0].Results[0]; cell.State != "unclear" || cell.Status != 0 {
		t.Errorf("连不上的格子应是 unclear 且状态码 0：%+v", cell)
	}
}

// ── 模型矩阵（口径层 v0.43）─────────────────────────────────────────────
//
// 子路径层答「协议集勾对没有」，答不了 v0.40 那个坑「渠道级探测全通、请求照样
// 404」。矩阵对每个启用中的纳管模型、按它的有效协议集逐侧发一个带模型名的最小
// 真实请求，三态回报：通（2xx）/ 不通（404/405）/ 说不清（其余）。

// modelMatrixUpstream 按请求体里的模型名演不同的上游：good 回 200、gone 回 404、
// limited 回 429。顺带把收到的请求存下来，给「发的到底是什么」那些断言用。
type probedRequest struct {
	Path string
	Auth string
	Body map[string]any
}

func modelMatrixUpstream(t *testing.T, seen *[]probedRequest) string {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		*seen = append(*seen, probedRequest{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		mu.Unlock()
		switch body["model"] {
		case "good":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "gone":
			w.WriteHeader(http.StatusNotFound)
		case "limited":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			// 空 `{}`（子路径层）落在这儿：400 证明路由存在。
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestProbeModelMatrixReportsThreeStates(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "matrix", "openai", modelMatrixUpstream(t, &seen), "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	gatewaytest.SeedChannelModel(t, db, ch, "gone")
	gatewaytest.SeedChannelModel(t, db, ch, "limited")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe?models=1", "", &got)

	states := map[string]string{}
	for _, row := range got.Models {
		if len(row.Results) != 1 {
			t.Fatalf("单协议渠道每行该只有一格：%+v", row)
		}
		states[row.Model] = row.Results[0].State
	}
	want := map[string]string{"good": "ok", "gone": "missing", "limited": "unclear"}
	for model, state := range want {
		if states[model] != state {
			t.Errorf("模型 %s 的三态 = %q，期望 %q", model, states[model], state)
		}
	}

	// 发的得是带模型名的最小真实请求：max_tokens 压到 1，别真花钱。
	for _, req := range seen {
		if req.Body["model"] == nil {
			continue // 子路径层的空 {}
		}
		if req.Body["max_tokens"] != float64(1) {
			t.Errorf("模型 %v 的探测请求没把 max_tokens 压到 1：%+v", req.Body["model"], req.Body)
		}
	}
}

// 模型自己声明了协议子集（口径层 v0.40）就只探那几侧；没声明的继承渠道全集。
// Responses 侧的请求体形状不一样：input + max_output_tokens（OpenAI 定了 16 的下限）。
func TestProbeModelMatrixHonorsModelProtocolSubset(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "subset", "openai,openai_responses", modelMatrixUpstream(t, &seen), "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	sub := gatewaytest.SeedChannelModel(t, db, ch, "gone")
	if _, err := db.Exec(`UPDATE channel_models SET protocols = 'openai' WHERE id = ?`, sub); err != nil {
		t.Fatalf("设子集失败: %v", err)
	}
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe?models=1", "", &got)

	cells := map[string]int{}
	for _, row := range got.Models {
		cells[row.Model] = len(row.Results)
	}
	if cells["good"] != 2 {
		t.Errorf("没声明子集的模型该继承渠道全集探两侧，得到 %d 格", cells["good"])
	}
	if cells["gone"] != 1 {
		t.Errorf("声明了 openai 子集的模型该只探一侧，得到 %d 格", cells["gone"])
	}

	for _, req := range seen {
		if req.Path != "/v1/responses" || req.Body["model"] == nil {
			continue
		}
		if req.Body["max_output_tokens"] != float64(16) {
			t.Errorf("Responses 侧该用 max_output_tokens: 16（OpenAI 的下限）：%+v", req.Body)
		}
		if req.Body["model"] != "good" {
			t.Errorf("Responses 侧只该出现没声明子集的模型，出现了 %v", req.Body["model"])
		}
	}
}

// 矩阵只用第一把**启用**凭证（每格都花钱，不逐把轰），停用的模型不探（路由不到，
// 结论没有消费者）。model_credential 让页面能标注「探的是哪把」——403 有「这把
// 凭证没开通这个模型」的含义。
func TestProbeModelMatrixUsesFirstEnabledCredentialAndSkipsDisabledModels(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "creds", "openai", modelMatrixUpstream(t, &seen), "")
	gatewaytest.SeedNamedCredential(t, db, ch, "被摘的", "sk-dead")
	if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1 WHERE channel_id = ?`, ch); err != nil {
		t.Fatalf("停用凭证失败: %v", err)
	}
	gatewaytest.SeedNamedCredential(t, db, ch, "活着的", "sk-live")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	off := gatewaytest.SeedChannelModel(t, db, ch, "gone")
	if _, err := db.Exec(`UPDATE channel_models SET disabled = 1 WHERE id = ?`, off); err != nil {
		t.Fatalf("停用模型失败: %v", err)
	}
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe?models=1", "", &got)

	if got.ModelCredential != "活着的" {
		t.Errorf("model_credential = %q，期望第一把启用凭证「活着的」", got.ModelCredential)
	}
	if len(got.Models) != 1 || got.Models[0].Model != "good" {
		t.Errorf("停用的模型不该被探：%+v", got.Models)
	}
	for _, req := range seen {
		if req.Body["model"] == "gone" {
			t.Error("停用的模型发出了探测请求")
		}
		if req.Body["model"] == "good" && req.Auth != "Bearer sk-live" {
			t.Errorf("矩阵用错凭证：%q，期望第一把启用的 sk-live", req.Auth)
		}
	}
}

// 一把启用的凭证都没有时矩阵整个跳过：拿空凭证发请求只会攒一屏 401 说不清。
// 这个状态启动闸不放行，只能在运行期出现——启动后把最后一把停用，探测入口照样能按。
func TestProbeModelMatrixSkipsWhenNoEnabledCredential(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "no-live-key", "openai", modelMatrixUpstream(t, &seen), "sk-dead")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)
	if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1 WHERE channel_id = ?`, ch); err != nil {
		t.Fatalf("停用凭证失败: %v", err)
	}

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe?models=1", "", &got)

	if len(got.Models) != 0 || got.ModelCredential != "" {
		t.Errorf("没有启用凭证还探了矩阵：models=%+v credential=%q", got.Models, got.ModelCredential)
	}
	for _, req := range seen {
		if req.Body["model"] != nil {
			t.Errorf("没有启用凭证却发出了带模型名的请求：%+v", req.Body)
		}
	}
}

// 矩阵默认不跑，要显式 `?models=1`（口径层 v0.43 ①「只由人手点」）。免费的子路径层
// 是保存渠道后自动跑的（v0.33），矩阵跟着默认挂上去的话，改一次 base_url 就静默打出
// 「模型数 × 协议数」次真实推理。缺省站在不花钱那一侧。
func TestProbeSkipsModelMatrixUnlessAskedFor(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "default-free", "openai", modelMatrixUpstream(t, &seen), "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe", "", &got)

	if len(got.Credentials) != 1 {
		t.Errorf("子路径层照跑不误：%+v", got.Credentials)
	}
	if len(got.Models) != 0 || got.ModelCredential != "" {
		t.Errorf("没要矩阵却跑了：models=%+v credential=%q", got.Models, got.ModelCredential)
	}
	for _, req := range seen {
		if req.Body["model"] != nil {
			t.Errorf("没要矩阵却发出了带模型名的花钱请求：%+v", req.Body)
		}
	}
}
