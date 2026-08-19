package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件测的是 A→CC 转换路径（portage-legacy#11）：Claude Code 挂第三方便宜模型，整个项目的
// 主诉求。同协议透传的用例在 relay_test.go / openai_test.go，两条路不共用断言。

const ccUpstreamModel = "qwen3-max"

// convertRequest 是一轮工具调用的**第二个**请求：带 tool_use 与 tool_result。
// 这才是 A→CC 真正要转的形态，纯文本轮转对了不说明工具轮也对。
const convertRequest = `{"model":"gw-sonnet","max_tokens":1024,"stream":true,` +
	`"metadata":{"user_id":"user_abc123"},` +
	`"thinking":{"type":"adaptive"},` +
	`"system":[{"type":"text","text":"be brief","cache_control":{"type":"ephemeral"}}],` +
	`"tools":[{"name":"Read","description":"读文件","input_schema":{"type":"object","properties":{"file":{"type":"string"}}}},` +
	`{"type":"advisor_20260301","name":"advisor","model":"claude-fable-5","input_schema":{"type":"object"}}],` +
	`"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"读一下 a.go"}]},` +
	`{"role":"assistant","content":[{"type":"thinking","thinking":"先读文件","signature":"sig-abc"},` +
	`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file":"a.go"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"package main",` +
	`"cache_control":{"type":"ephemeral"}}]}]}`

func newConvertGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, ccUpstreamModel, openaiCredential)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{}), up
}

// ccStreamFrames 是假上游要发的 CC SSE：一段正文 + 两个并行工具调用 + usage。
func ccStreamFrames() []string {
	return []string{
		`data: {"id":"chatcmpl-1","model":"` + ccUpstreamModel + `","choices":[{"index":0,"delta":{"role":"assistant","content":"我读一下"}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"Read","arguments":""}}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"Read","arguments":""}}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"file\":\"a.go\"}"}}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"name":"","arguments":"{\"file\":\"b.go\"}"}}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		`data: {"choices":[],"usage":{"prompt_tokens":69,"completion_tokens":74,"prompt_tokens_details":{"cached_tokens":11}}}` + "\n\n",
		"data: [DONE]\n\n",
	}
}

// ccStreamAll 让假上游一口气把 CC 的流写完（复用 stream_test.go 的门控助手，立刻
// 放行）。要卡在首帧的用例自己拿 release 不放。
func ccStreamAll(t *testing.T, up *gatewaytest.Upstream) {
	t.Helper()
	streamUpstream(t, up, ccStreamFrames()...)()
}

// 出站请求：打 CC 端点、model 翻译成纳管名、按 CC 形态编码、强制注入
// include_usage，且入口协议独有的字段一个都不许漏过去。
func TestConvertedRequestReachesUpstreamAsChatCompletions(t *testing.T) {
	gw, up := newConvertGateway(t)
	ccStreamAll(t, up)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages?beta=true", convertRequest, nil))

	got := up.Last(t)
	if got.Path != "/v1/chat/completions" {
		t.Errorf("上游 path = %q, 期望 /v1/chat/completions", got.Path)
	}
	// 查询串不带过去：?beta=true 是 Anthropic 的方言，挂到 CC 端点上不是保真是串味。
	// （portage-legacy#20 定的「整串照抄」管的是同协议透传那条路。）
	if got.RawQuery != "" {
		t.Errorf("上游 RawQuery = %q, 转换路径不该把入口协议的查询串带过去", got.RawQuery)
	}
	if got.Header.Get("Authorization") != "Bearer "+openaiCredential {
		t.Errorf("凭证注入不对: %q", got.Header.Get("Authorization"))
	}

	var sent struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(got.Body, &sent); err != nil {
		t.Fatalf("发给上游的不是合法 JSON: %v\n%s", err, got.Body)
	}

	if sent.Model != ccUpstreamModel {
		t.Errorf("上游 model = %q, 期望翻译成纳管模型名 %q", sent.Model, ccUpstreamModel)
	}
	if !sent.Stream || !sent.StreamOptions.IncludeUsage {
		t.Errorf("流式请求缺 stream/stream_options.include_usage: %+v", sent)
	}
	if sent.MaxTokens != 1024 {
		t.Errorf("max_tokens = %d, 期望 1024", sent.MaxTokens)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Function.Name != "Read" {
		t.Errorf("工具声明 = %+v，服务端工具 advisor 应被丢掉", sent.Tools)
	}

	// 工具轮的三条关键消息：assistant 带 tool_calls、随后一条 role=tool 带同一个 id。
	var sawToolCall, sawToolResult bool
	for _, m := range sent.Messages {
		if len(m.ToolCalls) > 0 {
			sawToolCall = true
			if m.ToolCalls[0].ID != "toolu_1" {
				t.Errorf("tool_call id = %q, 期望原样携带 toolu_1", m.ToolCalls[0].ID)
			}
			if m.ToolCalls[0].Function.Arguments != `{"file":"a.go"}` {
				t.Errorf("tool_call arguments = %q", m.ToolCalls[0].Function.Arguments)
			}
		}
		if m.Role == "tool" {
			sawToolResult = true
			if m.ToolCallID != "toolu_1" {
				t.Errorf("tool_call_id = %q, 与 tool_use_id 对不上", m.ToolCallID)
			}
		}
		if len(m.Content) > 0 {
			var s string
			if json.Unmarshal(m.Content, &s) != nil {
				t.Errorf("content 不是字符串（严格中转会拒）: %s", m.Content)
			}
		}
	}
	if !sawToolCall || !sawToolResult {
		t.Errorf("工具轮没编全: tool_calls=%v role=tool=%v", sawToolCall, sawToolResult)
	}

	// 入口协议独有的字段一个都不许漏过去。
	body := string(got.Body)
	for _, forbidden := range []string{"metadata", "user_abc123", "cache_control", "signature", "sig-abc", "advisor_20260301", "thinking"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("入口协议独有内容 %q 漏进了 CC 请求: %s", forbidden, body)
		}
	}
}

// 丢弃必须有日志警告（口径层 §2.6）：不做伪映射，也不静默。
func TestConvertedRequestWarnsAboutDroppedFields(t *testing.T) {
	gw, up := newConvertGateway(t)
	ccStreamAll(t, up)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", convertRequest, nil))

	lines := gw.Lines("跨协议转换丢弃字段")
	if len(lines) == 0 {
		t.Fatalf("没有丢弃警告日志；已落日志: %s", gw.RawLog())
	}
	raw := gw.RawLog()
	for _, want := range []string{"metadata", "cache_control", "thinking", "server_tool"} {
		if !strings.Contains(raw, want) {
			t.Errorf("丢弃警告里没提 %q: %s", want, raw)
		}
	}
}

// 下行响应：CC 的 SSE 要变成 Anthropic 的线格式，并行工具调用按序重组成互不嵌套的
// 内容块。
func TestConvertedStreamIsAnthropicWireFormat(t *testing.T) {
	gw, up := newConvertGateway(t)
	ccStreamAll(t, up)

	resp := gw.Post(t, "/v1/messages", convertRequest, nil)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := gatewaytest.ReadBody(t, resp)

	var events []string
	for _, chunk := range strings.Split(strings.TrimRight(body, "\n"), "\n\n") {
		if chunk == "" {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(chunk, "event: "), "\n")
		events = append(events, name)
	}
	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_stop", // 正文
		"content_block_start", "content_block_delta", "content_block_stop", // 工具 1
		"content_block_start", "content_block_delta", "content_block_stop", // 工具 2
		"message_delta", "message_stop",
	}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("帧序列 =\n  %v\n期望 =\n  %v\nbody=%s", events, want, body)
	}

	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Errorf("stop_reason 没映成 tool_use: %s", body)
	}
	if !strings.Contains(body, `"input_json_delta"`) {
		t.Errorf("工具参数没走 input_json_delta: %s", body)
	}
	// usage 在 message_delta 里补齐——CC 的 usage 要到流末才来。A 出口的 input_tokens
	// 是**净值**：上游毛值 69 减掉缓存读 11 = 58，缓存那 11 另占一个字段，客户端自己
	// 相加（protocol.Usage 的约定，portage-legacy#72）。
	if !strings.Contains(body, `"input_tokens":58`) || !strings.Contains(body, `"output_tokens":74`) {
		t.Errorf("message_delta 没带上游报的 usage，或没减回净值: %s", body)
	}
	if !strings.Contains(body, `"cache_read_input_tokens":11`) {
		t.Errorf("缓存读没带下来: %s", body)
	}
	// 响应 id 原样透上游的（chatcmpl-1），不重新编一个 msg_xxx：网关日志、上游账单
	// 与客户端看到的 id 因此是同一个，排障时能对上。Anthropic 客户端不校验 id 形态，
	// 也不需要把它回带给下一轮。
	if !strings.Contains(body, `"id":"chatcmpl-1"`) {
		t.Errorf("上游响应 id 没带下来: %s", body)
	}
	// 除 id 外，CC 侧的字段名一个都不该出现在下行流里。
	for _, forbidden := range []string{"finish_reason", "tool_calls\":[", "chat.completion"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("CC 侧字段 %q 漏进了 Anthropic 下行流", forbidden)
		}
	}
}

// 逐字出现：上游第一帧一到，客户端就该收到字节，而不是等整条流结束。
func TestConvertedStreamDoesNotBuffer(t *testing.T) {
	gw, up := newConvertGateway(t)
	// 上游写完首帧就卡住，直到 release。网关若攒着不 flush，客户端在这段时间里
	// 一个字节也拿不到。
	release := streamUpstream(t, up, ccStreamFrames()...)

	resp := gw.Post(t, "/v1/messages", convertRequest, nil)
	first := gatewaytest.ReadSome(t, resp.Body, 2*time.Second)
	release()

	if !strings.Contains(first, "message_start") {
		t.Errorf("首批字节里没有 message_start: %q", first)
	}
}

// 非流式：上游完整 JSON → canonical → 合法 Anthropic 响应体，stop_reason 非空。
func TestConvertedNonStreamAggregates(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(http.StatusOK, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-2","object":"chat.completion","model":"`+ccUpstreamModel+`",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"读完了",`+
			`"tool_calls":[{"id":"call_a","type":"function","function":{"name":"Read","arguments":"{\"file\":\"a.go\"}"}}]},`+
			`"finish_reason":"tool_calls"}],`+
			`"usage":{"prompt_tokens":12,"completion_tokens":34}}`)

	nonStream := strings.Replace(convertRequest, `"stream":true`, `"stream":false`, 1)
	resp := gw.Post(t, "/v1/messages", nonStream, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d；body=%s", resp.StatusCode, body)
	}
	var out struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		StopReason *string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n%s", err, body)
	}
	if out.Type != "message" || out.Role != "assistant" {
		t.Errorf("响应头部不是 Anthropic 形态: %+v", out)
	}
	// Anthropic 非流式的 stop_reason 不接受 null/空串——客户端按它决定要不要继续。
	if out.StopReason == nil || *out.StopReason == "" {
		t.Fatalf("stop_reason 为空: %s", body)
	}
	if *out.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, 期望 tool_use", *out.StopReason)
	}
	if len(out.Content) != 2 {
		t.Fatalf("content 有 %d 块，期望 2 块: %s", len(out.Content), body)
	}
	if out.Content[1].Input["file"] != "a.go" {
		t.Errorf("tool_use.input 不是解开的对象: %+v", out.Content[1])
	}
	if out.Usage.InputTokens != 12 || out.Usage.OutputTokens != 34 {
		t.Errorf("usage = %+v", out.Usage)
	}
	// 上游非流式请求不该带 stream 相关参数。
	if strings.Contains(string(up.Last(t).Body), "stream") {
		t.Errorf("非流式请求带了 stream 参数: %s", up.Last(t).Body)
	}
}

// 转换路径也要落 token 数：流式下这全靠强制注入的 include_usage。
func TestConvertedCallIsLoggedWithUsage(t *testing.T) {
	gw, up := newConvertGateway(t)
	ccStreamAll(t, up)

	gatewaytest.ReadBody(t, gw.Post(t, "/v1/messages", convertRequest, nil))

	line := gw.LastCall(t)
	if line.Str("outcome") != "ok" || line.Int64("status") != http.StatusOK {
		t.Errorf("outcome/status = %q/%d", line.Str("outcome"), line.Int64("status"))
	}
	if line.Str("inbound_protocol") != "anthropic" || line.Str("channel_protocol") != "openai" {
		t.Errorf("协议对没记全: %+v", line.Attrs)
	}
	if line.Int64("input_tokens") != 69 || line.Int64("output_tokens") != 74 {
		t.Errorf("token 数没记上（include_usage 没生效？）: %+v", line.Attrs)
	}
	if line.Int64("cache_read_tokens") != 11 {
		t.Errorf("cache_read_tokens = %d, 期望 11", line.Int64("cache_read_tokens"))
	}
	if _, ok := line.Attrs["ttfb_ms"]; !ok {
		t.Error("流式调用缺 ttfb_ms")
	}
}

// 转换路径上的断流同样要记成 stream_aborted。
//
// 这条最容易漏：断流是**带内**传下来的（解码侧放一条 EvError 就收摊），入口编码侧把
// 错误帧写给客户端后正常返回，收场词不在那之后补一刀就是一次干净的 200/ok——而透传
// 路径上同一件事记的是 stream_aborted，两条路的流水对不齐。客户端自己提前断开（Ctrl-C、
// 超时）走的也是这条，实测在 /observability 上就是一串「200 / ok / 0 tokens」。
func TestConvertedStreamAbortIsLoggedAsAborted(t *testing.T) {
	gw, up := newConvertGateway(t)
	first := ccStreamFrames()[0]
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, first)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}

	resp := gw.Post(t, "/v1/messages", convertRequest, nil)
	_, _ = io.ReadAll(resp.Body)

	line := gw.LastCall(t)
	if line.Str("outcome") != "stream_aborted" {
		t.Errorf("outcome = %q, 期望 stream_aborted", line.Str("outcome"))
	}
	if line.Int64("status") != http.StatusOK {
		t.Errorf("status = %d；断流发生在 200 已发出之后", line.Int64("status"))
	}
	// 断流前 Tap 已经看到的字段要留着，那是判断「断在哪」的线索。
	if line.Str("upstream_reported_model") != ccUpstreamModel {
		t.Errorf("upstream_reported_model = %q, 断流前解出来的字段不该丢", line.Str("upstream_reported_model"))
	}
	// CC 的 usage 只在最后一帧，断在它之前就是 0/0——流水里那些 0 token 的行是这么
	// 来的，不是 usage 没被解出来。
	if line.Int64("input_tokens") != 0 || line.Int64("output_tokens") != 0 {
		t.Errorf("断在 usage 帧之前，token 应为 0/0: %+v", line.Attrs)
	}
}

// 上游在流里回错误对象，与「读断了」不是一回事：那是把话说完了，只是说的是坏消息，
// 透传路径记 ok。转换路径两者都成了 EvError，不许因此把前者一起降级——收场词表得跟
// 透传路径对齐。
func TestConvertedInStreamErrorObjectKeepsOkOutcome(t *testing.T) {
	gw, up := newConvertGateway(t)
	streamUpstream(t, up,
		ccStreamFrames()[0],
		`data: {"error":{"message":"upstream blew up","type":"server_error"}}`+"\n\n",
		"data: [DONE]\n\n",
	)()

	resp := gw.Post(t, "/v1/messages", convertRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	line := gw.LastCall(t)
	if line.Str("outcome") != "ok" {
		t.Errorf("outcome = %q, 期望 ok（上游回错误对象不是断流）", line.Str("outcome"))
	}
	if !strings.Contains(body, "upstream blew up") {
		t.Errorf("上游的说明没带出来: %s", body)
	}
	assertNoSecrets(t, body, openaiCredential, up.URL)
}

// 上游报错时，客户端拿到的必须是 **Anthropic** 形状的错误 + 原样的状态码，
// 且不带上游凭证与 base_url。
func TestConvertedUpstreamErrorAnswersInAnthropicFormat(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(http.StatusTooManyRequests, map[string]string{"Content-Type": "application/json"},
		`{"error":{"message":"rate limited by upstream","type":"rate_limit_error","code":"429"}}`)

	resp := gw.Post(t, "/v1/messages", convertRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("状态码 = %d, 期望原样保留 429；body=%s", resp.StatusCode, body)
	}
	assertAnthropicError(t, body, "rate_limit_error")
	assertNoSecrets(t, body, openaiCredential, up.URL)
	if !strings.Contains(body, "rate limited by upstream") {
		t.Errorf("上游的说明没带出来，排障时只能靠猜: %s", body)
	}
}

// 请求体解不动是客户端的问题：回 400，且不该打到上游去。
func TestConvertedRejectsUndecodableRequest(t *testing.T) {
	gw, up := newConvertGateway(t)

	resp := gw.Post(t, "/v1/messages", `{"model":"gw-sonnet","messages":{"不是":"数组"}}`, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("状态码 = %d, 期望 400；body=%s", resp.StatusCode, body)
	}
	assertAnthropicError(t, body, "invalid_request_error")
	if up.Count() != 0 {
		t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
	}
}

// count_tokens 的 ep.Proto 同样是 anthropic，若按协议分路就会被当 /v1/messages
// 转出去。它走的必须是本地估算那条路（#18）：回 200 带数，且一个字节不打上游——
// 转出去的话 CC 那边根本没有对应端点，上游会收到一个 chat/completions 假请求。
func TestCountTokensDoesNotEnterTheConvertPath(t *testing.T) {
	gw, up := newConvertGateway(t)

	resp := gw.Post(t, "/v1/messages/count_tokens",
		`{"model":"gw-sonnet","messages":[{"role":"user","content":"hi"}]}`, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("count_tokens → openai 状态码 = %d, 期望 200（本地估算）；body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "input_tokens") {
		t.Errorf("回包该是估算数 {\"input_tokens\":N}: %s", body)
	}
	if up.Count() != 0 {
		t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
	}
}
