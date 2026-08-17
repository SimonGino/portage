package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

const openaiCredential = "sk-upstream-secret"

const ccRequest = `{"model":"gw-cc","messages":[{"role":"user","content":"hi"}],` +
	`"tools":[{"type":"function","function":{"name":"get_weather"}}],"vendor_extra":{"model":"nested"}}`

const responsesRequest = `{"model":"gw-resp","input":[{"role":"user","content":"hi"}],` +
	`"reasoning":{"effort":"low"},"metadata":{"model":"must-not-change"},"store":false}`

// newOpenAIGateway 起一个入口协议为 proto 的网关，接入点对外名 apModel、纳管模型名
// upstreamName。
func newOpenAIGateway(t *testing.T, apModel, proto, upstreamName string) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, apModel, proto, up.URL, upstreamName, openaiCredential)
	return gatewaytest.Start(t, db), up
}

func TestChatCompletionsPassthrough(t *testing.T) {
	gw, up := newOpenAIGateway(t, "gw-cc", "openai", "qwen3-max-2025-09-23")
	const upstreamBody = `{"id":"chatcmpl-1","object":"chat.completion",` +
		`"choices":[{"message":{"role":"assistant","content":"你好"}}],"usage":{"total_tokens":9}}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/chat/completions", ccRequest, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if body := gatewaytest.ReadBody(t, resp); body != upstreamBody {
		t.Errorf("响应体不是逐字节回传\n上游: %s\n客户端: %s", upstreamBody, body)
	}

	got := up.Last(t)
	if got.Path != "/v1/chat/completions" {
		t.Errorf("上游 path = %q", got.Path)
	}
	want := strings.Replace(ccRequest, `"model":"gw-cc"`, `"model":"qwen3-max-2025-09-23"`, 1)
	if string(got.Body) != want {
		t.Errorf("请求体除顶层 model 外应逐字节保真\n期望: %s\n收到: %s", want, got.Body)
	}
}

func TestResponsesPassthrough(t *testing.T) {
	gw, up := newOpenAIGateway(t, "gw-resp", "openai_responses", "gpt-5")
	const upstreamBody = `{"id":"resp_1","object":"response","output":[{"type":"message"}]}`
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"}, upstreamBody)

	resp := gw.Post(t, "/v1/responses", responsesRequest, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if body := gatewaytest.ReadBody(t, resp); body != upstreamBody {
		t.Errorf("响应体不是逐字节回传\n上游: %s\n客户端: %s", upstreamBody, body)
	}
	got := up.Last(t)
	if got.Path != "/v1/responses" {
		t.Errorf("上游 path = %q", got.Path)
	}
	want := strings.Replace(responsesRequest, `"model":"gw-resp"`, `"model":"gpt-5"`, 1)
	if string(got.Body) != want {
		t.Errorf("请求体除顶层 model 外应逐字节保真\n期望: %s\n收到: %s", want, got.Body)
	}
}

func TestOpenAIStreamPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		proto   string
		apModel string
		body    string
		// frames 用各自协议真实的行格式：Responses 每帧带 event: 行，CC 不带。
		// 两边都塞了 \r\n\r\n 帧和多行 data，任何按行重组的实现都会改字节。
		frames []string
	}{
		{"chat completions", "/v1/chat/completions", "openai", "gw-cc",
			`{"model":"gw-cc","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			[]string{
				"data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n",
				"data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\r\n\r\n",
				"data: [DONE]\n\n",
			}},
		{"responses", "/v1/responses", "openai_responses", "gw-resp",
			`{"model":"gw-resp","stream":true,"input":[{"role":"user","content":"hi"}]}`,
			[]string{
				"event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0," +
					"\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\"}}\n\n",
				"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\"," +
					"\"sequence_number\":1,\"item_id\":\"msg_1\",\"output_index\":0,\"delta\":\"你\"}\r\n\r\n",
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2," +
					"\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"total_tokens\":9}}}\n\n",
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gw, up := newOpenAIGateway(t, tc.apModel, tc.proto, "upstream-model")
			streamUpstream(t, up, tc.frames...)()

			resp := gw.Post(t, tc.path, tc.body, nil)
			body := gatewaytest.ReadBody(t, resp)

			if want := strings.Join(tc.frames, ""); body != want {
				t.Errorf("流式字节与上游写出的不一致\n上游: %q\n客户端: %q", want, body)
			}
		})
	}
}

// 凭证注入按渠道协议分叉：OpenAI 系走 Authorization: Bearer，绝不能同时冒出
// Anthropic 的 x-api-key。
func TestOpenAIChannelUsesBearerCredential(t *testing.T) {
	gw, up := newOpenAIGateway(t, "gw-cc", "openai", "qwen3-max")

	// 客户端这三个头都得真发出来，否则「上游收不到」的断言恒真、抓不住任何回归。
	// 凭证头填真实有效的网关 key：无效的话请求停在 401，压根到不了上游。
	gw.Post(t, "/v1/chat/completions", ccRequest, map[string]string{
		"Authorization":     "Bearer " + gatewaytest.DefaultKey,
		"x-api-key":         gatewaytest.DefaultKey,
		"anthropic-version": "2023-06-01",
	})

	got := up.Last(t)
	if v := got.Header.Get("Authorization"); v != "Bearer "+openaiCredential {
		t.Errorf("Authorization = %q, 期望注入渠道凭证而非客户端自带的", v)
	}
	if v := got.Header.Get("x-api-key"); v != "" {
		t.Errorf("客户端自带的 x-api-key 漏到了 openai 上游: %q", v)
	}
	if v := got.Header.Get("anthropic-version"); v != "" {
		t.Errorf("openai 渠道不该收到 anthropic-version: %q", v)
	}
}

// 走不通的路要按**入口**协议的原生格式回错——客户端只认得它自己那套。
//
// 载体是 count_tokens×openai_responses，而且是个**永久**反例：#80 之后九宫格全开，
// 「这一格还没做」的 501 已经不存在了，唯一还会 501 的是 count_tokens——它是
// Anthropic 独有端点，CC 与 Responses 两边根本没有可以转过去的上游端点（见
// conversionOpen 的注释）。所以这里不会像此前那两个子测试一样，随着某一格放开而
// 变成在测一条不再存在的行为。
//
// 此前的 CC 子测试（CC 入口打 openai_responses 渠道）已随 CC→R 放开而删除。
// 「错误用入站格式回、不泄 key/base_url」这两条断言在 CC 方向的载体改为
// TestCCInboundErrorKeepsOpenAIShape。
func TestCrossProtocolGateAnswersInInboundFormat(t *testing.T) {
	gw, up := newOpenAIGateway(t, "gw-sonnet", "openai_responses", "gpt-5.6")

	resp := gw.Post(t, "/v1/messages/count_tokens", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("状态码 = %d, 期望 501；body=%s", resp.StatusCode, body)
	}
	assertAnthropicError(t, body, "api_error")
	if !strings.Contains(body, "没有对应的转换路径") {
		t.Errorf("文案应点明该端点没有对应的转换路径: %s", body)
	}
	assertNoSecrets(t, body, openaiCredential, up.URL)
	if up.Count() != 0 {
		t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
	}
}

// CC 入口的错误一律回 OpenAI 形状，且不泄漏上游凭证与 base_url。
//
// 用 503（接入点在、没有可用候选）当载体：它不依赖任何「某条路还没做」的临时状态，
// 把凭证清零就能稳定复现——而错误回显泄漏 key/base_url 恰恰最容易在这类
// 「网关自己判死、还没碰上游」的分支上出现，因为那些分支的文案是我们自己拼的。
func TestCCInboundErrorKeepsOpenAIShape(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, "gw-cc", "openai", up.URL, "gpt-4o", openaiCredential)
	gw := gatewaytest.Start(t, db)
	// 起完再把凭证全停掉：渠道与模型都还在，选出来的候选却没法用，正是
	// ErrNoUsableCandidate。必须在 Start 之后改——启动校验会把「有候选却没有启用
	// 凭证」的配置当场判死（v0.21 通则），种进去就起不来了。这也正是运行期真实的
	// 发生顺序：网关跑着跑着，凭证被摘了。
	if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1`); err != nil {
		t.Fatalf("停用凭证失败: %v", err)
	}

	resp := gw.Post(t, "/v1/chat/completions", ccRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, 期望 503；body=%s", resp.StatusCode, body)
	}
	assertOpenAIError(t, body)
	assertNoSecrets(t, body, openaiCredential, up.URL)
	if up.Count() != 0 {
		t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
	}
}

func TestModelsListsEnabledAccessPoints(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	channelID := gatewaytest.SeedChannel(t, db, "bailian", "openai", up.URL, "sk-upstream")
	modelID := gatewaytest.SeedChannelModel(t, db, channelID, "qwen3-max")
	enabled := gatewaytest.SeedAccessPoint(t, db, "gw-visible")
	gatewaytest.SeedCandidate(t, db, enabled, modelID, 100)

	// 停用的接入点同样配齐候选：否则一个「按有无可用候选过滤」的实现也能通过，
	// 这条断言就证明不了它真的看了 disabled。
	retired := gatewaytest.SeedAccessPoint(t, db, "gw-retired")
	gatewaytest.SeedCandidate(t, db, retired, modelID, 100)
	if _, err := db.Exec(`UPDATE access_points SET disabled = 1 WHERE id = ?`, retired); err != nil {
		t.Fatal(err)
	}

	gw := gatewaytest.Start(t, db)
	resp := gw.Get(t, "/v1/models")
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, body)
	}

	var parsed struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("不是合法 JSON: %v；body=%s", err, body)
	}
	if parsed.Object != "list" {
		t.Errorf("object = %q, 期望 list", parsed.Object)
	}
	// 口径层 v0.32：接入点名与纳管模型限定名两者都列、都可路由。停用的接入点仍不列。
	ids := make([]string, 0, len(parsed.Data))
	for _, e := range parsed.Data {
		ids = append(ids, e.ID)
		if e.Object != "model" || e.OwnedBy == "" || e.Created == 0 {
			t.Errorf("条目字段不完整: %+v", e)
		}
	}
	want := []string{"gw-visible", "bailian/qwen3-max"}
	if !slices.Equal(ids, want) {
		t.Fatalf("列出 %v，期望 %v: %s", ids, want, body)
	}
	// 裸的纳管模型名不该单独出现——它没有路由入口，列出来就是给 harness 挖坑。
	for _, e := range parsed.Data {
		if e.ID == "qwen3-max" {
			t.Errorf("列表出现了裸纳管模型名，直连只认限定名: %s", body)
		}
	}
}

// 停用渠道 / 停用纳管模型 / 渠道没有启用凭证的，限定名一律不列——「列出来的都调得通」
// 是这张表唯一的契约。接入点那半边由启动闸保证，直连这半边只能在这儿过滤。
func TestModelsOmitsUnusableDirectModels(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)

	okCh := gatewaytest.SeedChannel(t, db, "good", "openai", up.URL, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, okCh, "keep-me")

	offModel := gatewaytest.SeedChannelModel(t, db, okCh, "model-off")
	if _, err := db.Exec(`UPDATE channel_models SET disabled = 1 WHERE id = ?`, offModel); err != nil {
		t.Fatal(err)
	}

	offCh := gatewaytest.SeedChannel(t, db, "channel-off", "openai", up.URL, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, offCh, "hidden-by-channel")
	if _, err := db.Exec(`UPDATE channels SET disabled = 1 WHERE id = ?`, offCh); err != nil {
		t.Fatal(err)
	}

	nokeyCh := gatewaytest.SeedChannel(t, db, "nokey", "openai", up.URL, "sk-upstream")
	gatewaytest.SeedChannelModel(t, db, nokeyCh, "hidden-by-credential")

	gw := gatewaytest.Start(t, db)
	// 凭证在**启动之后**才停：启动闸不允许「启用渠道零可用凭证」，这一格只有运行中
	// 才构造得出来（手写 SQL 改库、或凭证被上游 401 打停）。而列表是每次请求现查的，
	// 所以它必须自己过滤，不能指望启动闸兜底。
	if _, err := db.Exec(`UPDATE channel_keys SET disabled = 1 WHERE channel_id = ?`, nokeyCh); err != nil {
		t.Fatal(err)
	}
	body := gatewaytest.ReadBody(t, gw.Get(t, "/v1/models"))

	if !strings.Contains(body, "good/keep-me") {
		t.Errorf("可用的限定名没列出来: %s", body)
	}
	for _, gone := range []string{"model-off", "hidden-by-channel", "hidden-by-credential"} {
		if strings.Contains(body, gone) {
			t.Errorf("列出了调不通的 %q: %s", gone, body)
		}
	}
}

func assertOpenAIError(t *testing.T, body string) {
	t.Helper()
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v；body=%s", err, body)
	}
	if parsed.Error.Message == "" {
		t.Errorf("error.message 为空: %s", body)
	}
	if parsed.Error.Type == "" {
		t.Errorf("error.type 为空: %s", body)
	}
	if strings.Contains(body, `"type":"error"`) {
		t.Errorf("OpenAI 入口不该回 Anthropic 的错误外壳: %s", body)
	}
}
