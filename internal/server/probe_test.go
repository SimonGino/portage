package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 检测只剩一层：模型级真实请求（口径层 v0.96 ①③，拆除子路径可达性探测层）。
// 契约：入参 = 指定凭证（允许已停用）+ 模型选择（空 = 全部启用中的纳管模型）+
// 协议集合（必须 ⊆ 已声明协议）；响应 = 模型 × 协议矩阵 + 用的哪把凭证。
// 只提示、不落库、不进路由；三态沿 v0.43：2xx 通 / 404、405 不通 / 其余说不清。

type probeResponse struct {
	Credential string `json:"credential"`
	Models     []struct {
		Model   string `json:"model"`
		Results []struct {
			Protocol string `json:"protocol"`
			State    string `json:"state"`
			Status   int    `json:"status"`
			Detail   string `json:"detail"`
		} `json:"results"`
	} `json:"models"`
}

func probeBody(credID int64, model string, protocols ...string) string {
	raw, _ := json.Marshal(map[string]any{
		"credential_id": credID, "model": model, "protocols": protocols,
	})
	return string(raw)
}

// probedRequest 是假上游收到的一次请求：好测试只看外部行为，而检测的外部行为一半
// 在响应里、另一半就是这些发出去的字节。
type probedRequest struct {
	Path string
	Auth string
	Body map[string]any
}

// matrixUpstream 按请求体里的模型名演不同的上游：good 回 200、gone 回 404、
// limited 回 429。顺带把收到的请求存下来，给「发的到底是什么、用的哪把凭证」用。
func matrixUpstream(t *testing.T, seen *[]probedRequest) string {
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
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// 三态判定（口径层 v0.43 沿用）：2xx = 通，404/405 = 不通，其余 = 说不清。
// 顺带断言发的是带模型名的最小真实请求：max_tokens 压到 1，别真花钱。
func TestProbeReportsThreeStates(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "matrix", "openai", matrixUpstream(t, &seen), "")
	cred := gatewaytest.SeedNamedCredential(t, db, ch, "主力", "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	gatewaytest.SeedChannelModel(t, db, ch, "gone")
	gatewaytest.SeedChannelModel(t, db, ch, "limited")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe",
		probeBody(cred, "", "openai"), &got)

	if got.Credential != "主力" {
		t.Errorf("响应该带用的哪把凭证（403 的格子靠它说清），得到 %q", got.Credential)
	}
	states := map[string]string{}
	for _, row := range got.Models {
		if len(row.Results) != 1 {
			t.Fatalf("只勾了一个协议，每行该只有一格：%+v", row)
		}
		states[row.Model] = row.Results[0].State
	}
	want := map[string]string{"good": "ok", "gone": "missing", "limited": "unclear"}
	for model, state := range want {
		if states[model] != state {
			t.Errorf("模型 %s 的三态 = %q，期望 %q", model, states[model], state)
		}
	}
	for _, req := range seen {
		if req.Body["max_tokens"] != float64(1) {
			t.Errorf("模型 %v 的检测请求没把 max_tokens 压到 1：%+v", req.Body["model"], req.Body)
		}
	}
}

// 指定凭证生效（口径层 v0.96 ③：用户点哪把用哪把，不再固定第一把启用）。
func TestProbeUsesTheChosenCredential(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "pick", "openai", matrixUpstream(t, &seen), "")
	gatewaytest.SeedNamedCredential(t, db, ch, "第一把", "sk-first")
	second := gatewaytest.SeedNamedCredential(t, db, ch, "第二把", "sk-second")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe",
		probeBody(second, "", "openai"), &got)

	if got.Credential != "第二把" {
		t.Errorf("credential = %q，期望点的那把「第二把」", got.Credential)
	}
	for _, req := range seen {
		if req.Auth != "Bearer sk-second" {
			t.Errorf("检测用错凭证：%q，期望点的那把 sk-second", req.Auth)
		}
	}
}

// 已停用的凭证也能测（口径层 v0.38 立论承接）：恢复是纯人工的，「这把还坏不坏」
// 除了发一次请求没有别的办法回答。
func TestProbeAllowsDisabledCredential(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "revive", "openai", matrixUpstream(t, &seen), "")
	gatewaytest.SeedNamedCredential(t, db, ch, "活着的", "sk-live")
	dead := gatewaytest.SeedNamedCredential(t, db, ch, "被停的", "sk-dead")
	if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1 WHERE id = ?`, dead); err != nil {
		t.Fatalf("停用凭证失败: %v", err)
	}
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe",
		probeBody(dead, "", "openai"), &got)

	if got.Credential != "被停的" {
		t.Errorf("credential = %q，期望已停用的那把「被停的」", got.Credential)
	}
	if len(got.Models) != 1 || got.Models[0].Results[0].State != "ok" {
		t.Errorf("停用凭证照样发真实请求、照样回三态：%+v", got.Models)
	}
	for _, req := range seen {
		if req.Auth != "Bearer sk-dead" {
			t.Errorf("检测没用指定的停用凭证：%q", req.Auth)
		}
	}
}

// 协议参数过滤矩阵列：渠道声明两个协议、只勾一个，每行一格，另一侧零请求。
func TestProbeProtocolsFilterMatrixColumns(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "cols", "openai,openai_responses", matrixUpstream(t, &seen), "")
	cred := gatewaytest.SeedNamedCredential(t, db, ch, "主力", "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	gatewaytest.SeedChannelModel(t, db, ch, "gone")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe",
		probeBody(cred, "", "openai"), &got)

	for _, row := range got.Models {
		if len(row.Results) != 1 || row.Results[0].Protocol != "openai" {
			t.Errorf("只勾了 openai，行里却有别的格：%+v", row)
		}
	}
	for _, req := range seen {
		if req.Path != "/v1/chat/completions" {
			t.Errorf("没勾的协议侧发出了请求：%s", req.Path)
		}
	}
}

// 单选一个模型就只测那一个（口径层 v0.96 ③：新加一个模型不用整个矩阵重跑）。
// Responses 侧的请求体形状不一样：input + max_output_tokens（OpenAI 定了 16 的下限）。
func TestProbeSingleModelAcrossProtocols(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "single", "openai,openai_responses", matrixUpstream(t, &seen), "")
	cred := gatewaytest.SeedNamedCredential(t, db, ch, "主力", "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	gatewaytest.SeedChannelModel(t, db, ch, "gone")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var got probeResponse
	a.JSONInto(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe",
		probeBody(cred, "good", "openai", "openai_responses"), &got)

	if len(got.Models) != 1 || got.Models[0].Model != "good" {
		t.Fatalf("单选了 good，矩阵却是 %+v", got.Models)
	}
	if len(got.Models[0].Results) != 2 {
		t.Errorf("勾了两个协议该有两格：%+v", got.Models[0].Results)
	}
	for _, req := range seen {
		if req.Body["model"] != "good" {
			t.Errorf("没选的模型发出了请求：%v", req.Body["model"])
		}
		if req.Path == "/v1/responses" && req.Body["max_output_tokens"] != float64(16) {
			t.Errorf("Responses 侧该用 max_output_tokens: 16（OpenAI 的下限）：%+v", req.Body)
		}
	}
}

// 协议参数越出声明集被拒：可探范围严格 = 已声明协议（那一侧连根地址都没有）。
// 拒了就得一个请求都没发——参数错误不该花钱。
func TestProbeRejectsUndeclaredProtocol(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "narrow", "openai", matrixUpstream(t, &seen), "")
	cred := gatewaytest.SeedNamedCredential(t, db, ch, "主力", "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	status, body := a.Do(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe",
		probeBody(cred, "", "anthropic"))
	if status != http.StatusBadRequest {
		t.Errorf("越出声明集期望 400，得到 %d：%s", status, body)
	}
	if len(seen) != 0 {
		t.Errorf("被拒的检测发出了 %d 个请求", len(seen))
	}
}

// 参数不对的另外三个口：没这把凭证、没这个模型、协议一个没勾。都得 400、都零请求。
func TestProbeRejectsBadInput(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	ch := gatewaytest.SeedChannel(t, db, "guard", "openai", matrixUpstream(t, &seen), "")
	cred := gatewaytest.SeedNamedCredential(t, db, ch, "主力", "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, ch, "good")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	cases := map[string]string{
		"凭证不存在":  probeBody(cred+999, "", "openai"),
		"模型不存在":  probeBody(cred, "no-such-model", "openai"),
		"协议一个没勾": probeBody(cred, ""),
	}
	for name, body := range cases {
		if status, resp := a.Do(t, http.MethodPost,
			"/admin/api/channels/"+itoa(ch)+"/probe", body); status != http.StatusBadRequest {
			t.Errorf("%s 期望 400，得到 %d：%s", name, status, resp)
		}
	}
	if len(seen) != 0 {
		t.Errorf("被拒的检测发出了 %d 个请求", len(seen))
	}
}

// 保存后什么都不跑（口径层 v0.96 ①）：建渠道、改渠道的管理 API 调用之后，
// 上游收到的请求数为零——自动探测确已拆除，真实请求的钱只在人手点检测时花。
func TestSavingChannelSendsNothingUpstream(t *testing.T) {
	var seen []probedRequest
	db := gatewaytest.NewDB(t)
	url := matrixUpstream(t, &seen)
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	var created struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/admin/api/channels",
		fmt.Sprintf(`{"name":"quiet","base_url":{"openai":%q},"credential":"sk-upstream"}`, url), &created)
	a.JSONInto(t, http.MethodPut, "/admin/api/channels/"+itoa(created.ID)+"/settings",
		`{"name":"quiet-2"}`, nil)
	a.JSONInto(t, http.MethodPut, "/admin/api/channels/"+itoa(created.ID)+"/base-url",
		fmt.Sprintf(`{"base_url":{"openai":%q}}`, url), nil)

	if len(seen) != 0 {
		t.Errorf("保存渠道后上游收到了 %d 个请求——保存后不该有任何探测", len(seen))
	}
}

// 上游 key 与 base_url 不能出现在检测响应里（CLAUDE.md 的硬约束）。连不上时最容易
// 漏——Go 的传输错误原文里带着完整 URL。失败措辞的纪律也在这儿把关：说不清的格子
// 永远不定性「不支持」（口径层 v0.96 ③：检测是一次采样，定性交给人）。
func TestProbeNeverEchoesTheUpstreamAddress(t *testing.T) {
	db := gatewaytest.NewDB(t)
	const baseURL = "http://127.0.0.1:1/tenant7-secret-path"
	ch := gatewaytest.SeedChannel(t, db, "dead", "openai", baseURL, "")
	cred := gatewaytest.SeedNamedCredential(t, db, ch, "主力", "sk-upstream-secret")
	gatewaytest.SeedChannelModel(t, db, ch, "some-model")
	g := gatewaytest.Start(t, db)
	a := g.LoggedIn(t)

	_, body := a.Do(t, http.MethodPost, "/admin/api/channels/"+itoa(ch)+"/probe",
		probeBody(cred, "", "openai"))
	if strings.Contains(body, "tenant7-secret-path") || strings.Contains(body, "sk-upstream-secret") {
		t.Errorf("检测响应泄露了上游地址或凭证：%s", body)
	}
	if strings.Contains(body, "不支持") {
		t.Errorf("检测失败被定性成了「不支持」——它只是一次采样：%s", body)
	}

	var got probeResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(got.Models) != 1 || len(got.Models[0].Results) != 1 {
		t.Fatalf("期望矩阵一行一格，得到 %+v", got.Models)
	}
	if cell := got.Models[0].Results[0]; cell.State != "unclear" || cell.Status != 0 {
		t.Errorf("连不上的格子应是 unclear 且状态码 0：%+v", cell)
	}
}
