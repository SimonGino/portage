package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件测的是 CC→R 转换路径（口径层 §2.1 优先级③下半）：OpenAI 兼容客户端
// 挂 Responses 渠道。
//
// 去程的入站字节用 testdata/golden/in-cc-* 真实发包；回程直接喂
// testdata/golden/responses-stream-*/response.raw 的**真实上游转录**，而不是手搭 SSE
// ——手搭的只会长成我以为的样子，这条链要扛的是上游实际发出来的形态。

const cc2rUpstreamModel = "gpt-5.6-luna"

func newCC2RGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL,
		cc2rUpstreamModel, openaiCredential)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{}), up
}

// goldenResponseRaw 读一份真实上游 SSE 转录，原样当假上游的回话。
func goldenResponseRaw(t *testing.T, sample string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", sample, "response.raw"))
	if err != nil {
		t.Skipf("样本尚未采集：%s", sample)
	}
	return string(raw)
}

// respondWithGolden 让假上游把真实转录整份吐回去。
func respondWithGolden(t *testing.T, up *gatewaytest.Upstream, sample string) {
	t.Helper()
	up.RespondWith(200, map[string]string{"Content-Type": "text/event-stream"},
		goldenResponseRaw(t, sample))
}

// responsesOKBody 是非流式回话。**没有真实样本**（九份 Responses 转录全是
// stream:true），所以这一份是手搭的，形状照终帧里的 response 对象（展开层 §9.4②）。
const responsesOKBody = `{"id":"resp_buffered","object":"response","model":"` + cc2rUpstreamModel +
	`","status":"completed","output":[` +
	`{"type":"reasoning","id":"rs_1","encrypted_content":"gAAAAABsecret","summary":[]},` +
	`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"读完了"}]},` +
	`{"type":"function_call","call_id":"call_9","name":"read","arguments":"{\"filePath\":\"notes.md\"}"}],` +
	`"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4},"output_tokens":5,` +
	`"output_tokens_details":{"reasoning_tokens":2}}}`

// 出站请求：打 /v1/responses、模型翻译、system 变 developer 项、工具扁平、
// store:false，以及 CC 独有形态一个都不许漏。
func TestCC2RRequestReachesUpstreamAsResponses(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-tool-turn1", false), nil)

	req := up.Last(t)
	if req.Path != "/v1/responses" {
		t.Errorf("上游端点 = %q, 期望 /v1/responses", req.Path)
	}

	var sent struct {
		Model  string `json:"model"`
		Store  *bool  `json:"store"`
		Stream *bool  `json:"stream"`
		Input  []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"input"`
		Tools []struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
			Function   json.RawMessage `json:"function"`
		} `json:"tools"`
		ToolChoice any `json:"tool_choice"`
	}
	if err := json.Unmarshal(req.Body, &sent); err != nil {
		t.Fatalf("上游收到的不是 Responses 请求: %v\n%s", err, req.Body)
	}

	if sent.Model != cc2rUpstreamModel {
		t.Errorf("model = %q, 期望翻译成纳管名 %q", sent.Model, cc2rUpstreamModel)
	}
	if sent.Store == nil || *sent.Store {
		t.Errorf("store = %v，期望恒发 false", sent.Store)
	}
	if sent.Stream != nil {
		t.Errorf("非流式请求带了 stream: %v", *sent.Stream)
	}
	if len(sent.Input) == 0 {
		t.Fatalf("input 为空: %s", req.Body)
	}
	// CC 的 system 消息编成 developer 项，用 input_text 部件。
	first := sent.Input[0]
	if first.Type != "message" || first.Role != "developer" {
		t.Errorf("首项 = %+v，期望 developer 消息项", first)
	}
	if len(first.Content) == 0 || first.Content[0].Type != "input_text" {
		t.Errorf("developer 部件 = %+v，期望 input_text", first.Content)
	}
	// 工具声明扁平：有 type/name/parameters，没有 CC 那层 function 嵌套。
	if len(sent.Tools) == 0 {
		t.Fatalf("tools 丢了: %s", req.Body)
	}
	for _, tool := range sent.Tools {
		if tool.Type != "function" || tool.Name == "" || len(tool.Parameters) == 0 {
			t.Errorf("工具声明不完整: %+v", tool)
		}
		if len(tool.Function) != 0 {
			t.Errorf("工具声明套了一层 function，那是 CC 的形状: %s", tool.Function)
		}
	}

	// 顶层键按**键名**查，不按子串查：样本里的系统提示正文本身就含 "instructions"
	// 一类的词，子串匹配会把提示词的内容误判成我们发出去的字段。
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		// 采样参数：gpt-5.x 推理模型收到就 400，Responses 也没有 stop 参数。
		"temperature", "top_p", "stop",
		// CC 独有的顶层形态。
		"stream_options", "max_tokens", "messages",
		// 一律不合成的 Responses 独有字段（客户端说的是 CC，没说过这些）。
		"instructions", "previous_response_id", "prompt_cache_key",
		"parallel_tool_calls", "reasoning", "include", "client_metadata",
	} {
		if _, ok := keys[forbidden]; ok {
			t.Errorf("顶层字段 %q 漏进了 Responses 请求体（本不该带）", forbidden)
		}
	}
	// input 项里也不许出现 CC 的角色。
	for _, it := range sent.Input {
		if it.Role == "system" || it.Role == "tool" {
			t.Errorf("input 项出现了 CC 角色 %q，Responses 只有 user/assistant/developer", it.Role)
		}
	}
}

// CC 的工具调用与结果要摊成顶层 function_call / function_call_output 项，靠 call_id 配对。
func TestCC2RToolRoundTripBecomesTopLevelItems(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-parallel-turn2", false), nil)

	var sent struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
			Args   string `json:"arguments"`
			Output any    `json:"output"`
		} `json:"input"`
	}
	body := up.Last(t).Body
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("上游收到的不是 Responses 请求: %v\n%s", err, body)
	}

	calls := map[string]bool{}
	var results int
	for _, it := range sent.Input {
		switch it.Type {
		case "function_call":
			if it.Name == "" || it.Args == "" {
				t.Errorf("function_call 不完整: %+v", it)
			}
			calls[it.CallID] = true
		case "function_call_output":
			results++
			if !calls[it.CallID] {
				t.Errorf("结果 %q 对不上任何调用（孤儿结果会被上游拒）", it.CallID)
			}
			if _, ok := it.Output.(string); !ok {
				t.Errorf("output = %T，Responses 收的是纯字符串", it.Output)
			}
		}
	}
	if len(calls) != 2 || results != 2 {
		t.Errorf("并行两调用两结果没对上: calls=%d results=%d\n%s", len(calls), results, body)
	}
}

// 下行流：真实 Responses 转录要变成 Chat Completions 线格式。
func TestCC2RStreamIsChatCompletionsWireFormat(t *testing.T) {
	gw, up := newCC2RGateway(t)
	respondWithGolden(t, up, "responses-stream-text")

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", true), nil)
	body := gatewaytest.ReadBody(t, resp)

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	// Responses 的事件名一个都不许漏到客户端——客户端等的是 Chat Completions。
	for _, leaked := range []string{
		"event:", "response.created", "response.completed", "output_text.delta",
		"output_index", "sequence_number", "obfuscation",
	} {
		if strings.Contains(body, leaked) {
			t.Errorf("Responses 线格式 %q 漏给了 CC 客户端:\n%s", leaked, body)
		}
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Errorf("CC 流必须以 [DONE] 收尾:\n%s", body)
	}
	if !strings.Contains(body, `"chat.completion.chunk"`) {
		t.Errorf("帧不是 chat.completion.chunk:\n%s", body)
	}
	if !strings.Contains(body, "pong") {
		t.Errorf("正文丢了:\n%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("finish_reason 没给出来:\n%s", body)
	}
	// 样本 expect 里核过的数：input 14521 / output 5 / 缓存读 3840。CC 的
	// prompt_tokens 与 Responses 的 input_tokens 同为毛值，直映。
	if !strings.Contains(body, `"prompt_tokens":14521`) ||
		!strings.Contains(body, `"completion_tokens":5`) {
		t.Errorf("usage 没对上样本 expect:\n%s", body)
	}
	// 上游 id 原样透传（口径层 v0.31）。
	if !strings.Contains(body, "resp_0e31d87cc858e4e6016a7ec1584bc48198aeee9c350dd75384") {
		t.Errorf("上游 id 没透传:\n%s", body)
	}
}

// 真实工具轮：custom_tool_call 的自由文本入参到了 CC 侧必须是**合法 JSON**，
// 而推理一个字都不许漏。
func TestCC2RToolStreamGivesValidJSONArgs(t *testing.T) {
	gw, up := newCC2RGateway(t)
	raw := goldenResponseRaw(t, "responses-stream-tool-turn1")
	up.RespondWith(200, map[string]string{"Content-Type": "text/event-stream"}, raw)

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-tool-turn1", true), nil)
	body := gatewaytest.ReadBody(t, resp)

	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("finish_reason 没映成 tool_calls，客户端不会去跑工具:\n%s", body)
	}
	// 推理不许泄漏：真实转录里 reasoning item 带一串上千字符的密文。
	for _, leaked := range []string{"encrypted_content", "gAAAAAB", "reasoning_summary", `"reasoning"`} {
		if strings.Contains(body, leaked) {
			t.Errorf("推理相关的 %q 漏给了 CC 客户端:\n%s", leaked, body)
		}
	}

	// 把分片拼回去，arguments 必须解得开——上游发的是 JS 源码，出口侧包成了
	// {"input":"…"}，解不开就是给客户端灌了一段它解不动的东西。
	var args strings.Builder
	var callID, name string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(line[6:]), &chunk) != nil {
			continue
		}
		for _, ch := range chunk.Choices {
			for _, tc := range ch.Delta.ToolCalls {
				if tc.ID != "" {
					callID = tc.ID
				}
				if tc.Function.Name != "" {
					name = tc.Function.Name
				}
				args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if name != "exec" || !strings.HasPrefix(callID, "call_") {
		t.Fatalf("工具标识没转过来: name=%q id=%q\n%s", name, callID, body)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args.String()), &parsed); err != nil {
		t.Fatalf("arguments 不是合法 JSON，CC 客户端解不动: %v\n%s", err, args.String())
	}
	if s, _ := parsed["input"].(string); !strings.Contains(s, "tools.exec_command") {
		t.Errorf("包装后的入参丢了原文: %v", parsed)
	}
}

// 非流式：Responses 的完整响应体要聚合成 chat.completion。
func TestCC2RBufferedResponseIsChatCompletion(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-tool-turn1", false), nil)
	raw := gatewaytest.ReadBody(t, resp)

	var out struct {
		Object  string `json:"object"`
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("回给客户端的不是 CC 响应: %v\n%s", err, raw)
	}
	if out.Object != "chat.completion" || out.ID != "resp_buffered" {
		t.Errorf("object/id = %q/%q", out.Object, out.ID)
	}
	msg := out.Choices[0].Message
	if msg.Content == nil || *msg.Content != "读完了" {
		t.Errorf("正文没对上: %v", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_9" {
		t.Fatalf("工具调用没转过去: %+v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].Function.Arguments != `{"filePath":"notes.md"}` {
		t.Errorf("arguments = %q", msg.ToolCalls[0].Function.Arguments)
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q", out.Choices[0].FinishReason)
	}
	if out.Usage.PromptTokens != 10 || out.Usage.PromptTokensDetails.CachedTokens != 4 ||
		out.Usage.CompletionTokensDetails.ReasoningTokens != 2 {
		t.Errorf("usage 没对上: %+v", out.Usage)
	}
	// Responses 的形态与那段密文一个都不许漏给客户端。
	for _, leaked := range []string{"encrypted_content", "gAAAAAB", "output_text", "input_tokens", `"status"`} {
		if strings.Contains(raw, leaked) {
			t.Errorf("Responses 字段 %q 漏给了 CC 客户端:\n%s", leaked, raw)
		}
	}
}

// 上游的推理摘要要到得了 CC 客户端（口径层 v0.62 出向合成、v0.73 ② 补上的 CC→R 这一格）。
//
// 用真实转录喂：responses-stream-reasoning-turn1 里 reasoning_summary_text.delta 与
// encrypted_content 同时在，正好一并钉住「摘要下来、密文不下来」。
func TestCC2RReasoningSummaryReachesCCClient(t *testing.T) {
	gw, up := newCC2RGateway(t)
	respondWithGolden(t, up, "responses-stream-reasoning-turn1")

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", true), nil)
	body := gatewaytest.ReadBody(t, resp)

	if !strings.Contains(body, `"reasoning_content"`) {
		t.Fatalf("CC 客户端没收到 reasoning_content（出向合成没生效）:\n%s", body)
	}
	// 正文得是上游那段摘要本身，不是空壳。
	if !strings.Contains(body, "Planning file reading command") {
		t.Errorf("reasoning_content 里没有上游那段摘要:\n%s", body)
	}
	// 密文与 Responses 线格式一个都不许漏。
	for _, leaked := range []string{"encrypted_content", "gAAAAAB", "reasoning_summary", "event:"} {
		if strings.Contains(body, leaked) {
			t.Errorf("%q 漏给了 CC 客户端:\n%s", leaked, body)
		}
	}
}

// 闸门确实开着：这一格若还关着，回的是 501「没有对应的转换路径」。
func TestCC2RGateIsOpen(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	resp := gw.Post(t, "/v1/chat/completions",
		`{"model":"`+accessPointModel+`","messages":[{"role":"user","content":"hi"}]}`, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != 200 {
		t.Fatalf("状态码 = %d（501 说明闸没开）\n%s", resp.StatusCode, body)
	}
	if up.Count() == 0 {
		t.Error("上游一次都没被打到")
	}
}

// attrsText 把一行日志的属性摊平成一个字符串，好整体断言「点名了什么、没带上什么」
// ——登记项是 []string，slog 的 JSON handler 出来是 []any，逐个断言反而看不清。
func attrsText(l gatewaytest.LogLine) string { return fmt.Sprint(l.Attrs) }

// dropStream 是手搭的上游 SSE：九份真实转录里**没有**服务端工具的样本（全是纯文本
// 与函数调用），而这条链要验的恰恰是「上游发了我们放不出去的 item」。形状照
// openai-python 2.24.0 的 ResponseWebSearchCallInProgressEvent / output_item.added
// 与 litellm types/llms/openai.py 的事件名表搭。
//
// 里头有三样东西：认得的 web_search_call 结构事件（不该登记）、认不得的 item
// （web_search_call 本身，该登记一次而不是 added/done 各一次）、认不得的事件名
// （response.gizmo.progress，该登记一次而不是逐帧）。
const dropStream = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_drop","model":"` + cc2rUpstreamModel + `","status":"in_progress"}}` + "\n\n" +
	"event: response.output_item.added\n" +
	`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_abc","status":"in_progress","action":{"type":"search","query":"绝密关键词"}}}` + "\n\n" +
	"event: response.web_search_call.in_progress\n" +
	`data: {"type":"response.web_search_call.in_progress","output_index":0,"item_id":"ws_abc"}` + "\n\n" +
	"event: response.web_search_call.completed\n" +
	`data: {"type":"response.web_search_call.completed","output_index":0,"item_id":"ws_abc"}` + "\n\n" +
	"event: response.output_item.done\n" +
	`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_abc","status":"completed"}}` + "\n\n" +
	"event: response.gizmo.progress\n" +
	`data: {"type":"response.gizmo.progress","output_index":0,"pct":10}` + "\n\n" +
	"event: response.gizmo.progress\n" +
	`data: {"type":"response.gizmo.progress","output_index":0,"pct":90}` + "\n\n" +
	"event: response.output_item.added\n" +
	`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","output_index":1,"item_id":"msg_1","delta":"查完了"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_drop","status":"completed","usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"

// 上游发来的服务端工具项我们放不出去，只登记 + 一行 Warn：不合成 server_tool_use、
// 不计费（PO 2026-09-02）。日志必须点名是什么项，否则「回答里凭空提到搜索结果」
// 这类现象查不到因；同时**不许带 item 内容**（搜索词是用户数据）。
func TestCC2RStreamUnknownUpstreamItemsAreLogged(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "text/event-stream"}, dropStream)

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", true), nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 = %d\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "查完了") {
		t.Errorf("认得的正文也没出来:\n%s", body)
	}

	lines := gw.Lines("上游响应里有转换路径放不出去的项，已丢弃；若是服务端工具，上游成本已经发生")
	if len(lines) != 1 {
		t.Fatalf("丢弃日志 %d 行, 期望 1 行；已落日志：%s", len(lines), gw.RawLog())
	}
	one := attrsText(lines[0])
	for _, want := range []string{"web_search_call(ws_abc)", "event:response.gizmo.progress"} {
		if !strings.Contains(one, want) {
			t.Errorf("日志没点名 %q: %s", want, one)
		}
	}
	// 认得的结构事件不该混进来，item 内容更不该。
	for _, leak := range []string{"web_search_call.in_progress", "绝密关键词"} {
		if strings.Contains(one, leak) {
			t.Errorf("日志里混进了 %q: %s", leak, one)
		}
	}
}

// 非流式同一条链：output 数组里认不得的项照样登记，照样一行 Warn。
func TestCC2RBufferedUnknownUpstreamItemsAreLogged(t *testing.T) {
	gw, up := newCC2RGateway(t)
	body := `{"id":"resp_drop","object":"response","model":"` + cc2rUpstreamModel +
		`","status":"completed","output":[` +
		`{"type":"web_search_call","id":"ws_abc","status":"completed","action":{"type":"search","query":"绝密关键词"}},` +
		`{"type":"image_generation_call","id":"ig_1","result":"iVBORw0KGgo"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"查完了"}]}],` +
		`"usage":{"input_tokens":3,"output_tokens":2}}`
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, body)

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", false), nil)
	got := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 = %d\n%s", resp.StatusCode, got)
	}
	if !strings.Contains(got, "查完了") {
		t.Errorf("认得的正文也没出来:\n%s", got)
	}

	lines := gw.Lines("上游响应里有转换路径放不出去的项，已丢弃；若是服务端工具，上游成本已经发生")
	if len(lines) != 1 {
		t.Fatalf("丢弃日志 %d 行, 期望 1 行；已落日志：%s", len(lines), gw.RawLog())
	}
	one := attrsText(lines[0])
	for _, want := range []string{"web_search_call(ws_abc)", "image_generation_call(ig_1)"} {
		if !strings.Contains(one, want) {
			t.Errorf("日志没点名 %q: %s", want, one)
		}
	}
	if strings.Contains(one, "绝密关键词") || strings.Contains(one, "iVBORw0KGgo") {
		t.Errorf("日志里带上了 item 内容: %s", one)
	}
}

// 干净的回话不许无中生有一行 Warn——不然这行日志会被当噪音无视。
func TestCC2RNoDropLogOnCleanResponse(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", false), nil)

	if lines := gw.Lines("上游响应里有转换路径放不出去的项"); len(lines) != 0 {
		t.Errorf("干净回话也打了 %d 行丢弃日志：%s", len(lines), gw.RawLog())
	}
}

// C：allowed_tools 的 required 必须活着走到出口。此前 CC 入口解不出这个形态，
// ToolChoice 是零值 → 出口既不发 tool_choice 也不触发那道 400，客户端「必须调工具」
// 的要求整条蒸发。名单本身 canonical 收不下，折算 + 登记 + 一行 Warn。
func TestCC2RAllowedToolsRequiredSurvivesToUpstream(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	body := `{"model":"` + accessPointModel + `","stream":false,` +
		`"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}},` +
		`{"type":"function","function":{"name":"write","parameters":{"type":"object"}}}],` +
		`"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"required",` +
		`"tools":[{"type":"function","function":{"name":"read"}}]}},` +
		`"messages":[{"role":"user","content":"读一下"}]}`

	resp := gw.Post(t, "/v1/chat/completions", body, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 = %d\n%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	var sent struct {
		ToolChoice any `json:"tool_choice"`
	}
	if err := json.Unmarshal(up.Last(t).Body, &sent); err != nil {
		t.Fatalf("上游收到的不是 JSON: %v", err)
	}
	if sent.ToolChoice != "required" {
		t.Errorf("上游 tool_choice = %v, 期望 required（白名单折算成最近语义）", sent.ToolChoice)
	}
	lines := gw.Lines("入站请求里有转换路径收不下的字段，已按最近语义折算")
	if len(lines) != 1 {
		t.Fatalf("折算日志 %d 行, 期望 1 行；已落日志：%s", len(lines), gw.RawLog())
	}
	if one := attrsText(lines[0]); !strings.Contains(one, "tool_choice.allowed_tools(1 tools)") {
		t.Errorf("日志没点名折算了什么: %s", one)
	}
}

// 折算出来的 required 落空，照样撞既有那道 400 闸——这正是「解出来」的意义。
func TestCC2RAllowedToolsRequiredWithoutToolsIs400(t *testing.T) {
	gw, up := newCC2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	body := `{"model":"` + accessPointModel + `","stream":false,` +
		`"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[]}},` +
		`"messages":[{"role":"user","content":"读一下"}]}`

	resp := gw.Post(t, "/v1/chat/completions", body, nil)
	got := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400；body=%s", resp.StatusCode, got)
	}
	if up.Count() != 0 {
		t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
	}
}
