package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件测的是 R→CC 转换路径（#12，口径层 §2.1 优先级①下半）：Codex CLI 挂第三方
// 便宜模型。A→CC 的用例在 convert_test.go，两条路不共用断言——它们唯一共享的是 CC
// 出口那半边，而这一侧要验的恰恰是另外半边。

// convertResponsesRequest 是一轮工具调用的**第二个**请求，形态照 Codex CLI 0.144 实采
// （testdata/golden/in-responses-tool-turn2）裁剪：additional_tools 声明一个 custom
// 的 exec 和一个 function 的 wait，历史里带 reasoning 的密文、custom_tool_call 与
// 它的结果。纯文本轮转对了不说明工具轮也对。
const convertResponsesRequest = `{"model":"gw-sonnet","stream":true,` +
	`"store":false,"parallel_tool_calls":false,` +
	`"reasoning":{"effort":"high","context":"all_turns"},` +
	`"include":["reasoning.encrypted_content"],` +
	`"prompt_cache_key":"cache-key-1",` +
	`"client_metadata":{"session_id":"sess-1"},` +
	`"input":[` +
	`{"type":"additional_tools","role":"developer","tools":[` +
	`{"type":"custom","name":"exec","description":"跑一段 JS","format":{"type":"grammar","syntax":"lark","definition":"start: SOURCE"}},` +
	`{"type":"function","name":"wait","description":"等一会","parameters":{"type":"object"}}]},` +
	`{"type":"message","role":"developer","content":[{"type":"input_text","text":"be brief"}]},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"读一下 a.txt"}]},` +
	`{"type":"reasoning","summary":[],"encrypted_content":"gAAAAAB-opaque-cipher"},` +
	`{"type":"custom_tool_call","status":"completed","call_id":"call_1","name":"exec",` +
	`"input":"const r = await tools.exec_command({cmd:\"cat a.txt\"}); text(r.output)"},` +
	`{"type":"custom_tool_call_output","call_id":"call_1","output":[` +
	`{"type":"input_text","text":"Script completed\n"},{"type":"input_text","text":"alpha-one\n"}]}]}`

func newResponsesConvertGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, ccUpstreamModel, openaiCredential)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{}), up
}

// ccExecStreamFrames 是假 CC 上游的回话：一段正文 + 一个 exec 调用。
//
// arguments 是**包装过的** JSON——CC 契约要求它必须是 JSON 字符串，而 exec 的入参
// 是 JS 源码，所以出口侧包成了 {"input":"…"}（openaicc 的 argsWrapKey）。一个诚实
// 的上游就会这样把它回过来。
func ccExecStreamFrames(t *testing.T) []string {
	t.Helper()
	wrapped, err := json.Marshal(map[string]string{
		"input": `const r = await tools.exec_command({cmd:"cat b.txt"}); text(r.output)`,
	})
	if err != nil {
		t.Fatal(err)
	}
	argsFrag, err := json.Marshal(string(wrapped))
	if err != nil {
		t.Fatal(err)
	}
	return []string{
		`data: {"id":"chatcmpl-9","model":"` + ccUpstreamModel + `","choices":[{"index":0,"delta":{"role":"assistant","content":"我看看"}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"exec","arguments":""}}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":` + string(argsFrag) + `}}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n",
		`data: {"choices":[],"usage":{"prompt_tokens":88,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":40}}}` + "\n\n",
		"data: [DONE]\n\n",
	}
}

// 出站请求：打 CC 端点、模型翻译、Responses 独有字段一个都不许漏过去，且
// custom 工具的 JS 入参必须被包成合法 JSON——不包，严格上游会拒。
func TestResponsesRequestReachesUpstreamAsChatCompletions(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)

	gw.Post(t, "/v1/responses", strings.Replace(convertResponsesRequest, `"stream":true`, `"stream":false`, 1), nil)

	req := up.Last(t)
	if req.Path != "/v1/chat/completions" {
		t.Errorf("上游端点 = %q, 期望 /v1/chat/completions", req.Path)
	}

	var sent struct {
		Model    string `json:"model"`
		Messages []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string `json:"name"`
				Parameters struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(req.Body, &sent); err != nil {
		t.Fatalf("上游收到的不是 CC 请求: %v\n%s", err, req.Body)
	}
	if sent.Model != ccUpstreamModel {
		t.Errorf("model = %q, 期望翻译成纳管名 %q", sent.Model, ccUpstreamModel)
	}

	// additional_tools 里的两个工具都要落到 CC 的 tools 上；exec 没有 JSON Schema
	// （它只有 lark 文法），但工具本身不能因此消失。
	var names []string
	for _, tool := range sent.Tools {
		names = append(names, tool.Function.Name)
	}
	if strings.Join(names, ",") != "exec,wait" {
		t.Errorf("上游收到的工具 = %v, 期望 exec,wait", names)
	}

	// 而且 exec 必须**带一份合成的 parameters**，声明它收一个叫 input 的字符串。
	// 原先这里是空的：工具是发过去了，但没有任何东西告诉模型该回 {"input": …}，
	// 模型自由发挥回个 {"cmd": …}，回程拆包拆不动只好原样给出去，Codex 拿到一段
	// JSON 当 JS 跑。发出去的声明和回来的拆包必须是同一套约定。
	for _, tool := range sent.Tools {
		if tool.Function.Name != "exec" {
			continue
		}
		if _, ok := tool.Function.Parameters.Properties[protocol.CustomToolArgsKey]; !ok {
			t.Errorf("custom 工具 exec 的 parameters 没声明 %q: %s",
				protocol.CustomToolArgsKey, req.Body)
		}
	}

	// 历史里的 custom_tool_call：arguments 必须是**合法 JSON**（包装过的），
	// 而不是裸 JS。这是 §5 坑清单点名的那条「合成规则须与解包侧对称」的一半。
	var call, result bool
	for _, m := range sent.Messages {
		for _, tc := range m.ToolCalls {
			call = true
			if tc.ID != "call_1" {
				t.Errorf("tool_call id = %q, 期望原样携带 call_1", tc.ID)
			}
			var wrapper map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &wrapper); err != nil {
				t.Errorf("arguments 不是合法 JSON，严格上游会拒: %q", tc.Function.Arguments)
			} else if !strings.Contains(wrapper["input"].(string), "exec_command") {
				t.Errorf("包装里没有原始 JS: %v", wrapper)
			}
		}
		if m.Role == "tool" {
			result = true
			if m.ToolCallID != "call_1" {
				t.Errorf("tool 消息的 tool_call_id = %q", m.ToolCallID)
			}
			// Responses 的 output 是两段 input_text，拼成 CC 的纯文本 content。
			if !strings.Contains(m.Content, "alpha-one") {
				t.Errorf("工具结果内容丢了: %q", m.Content)
			}
		}
	}
	if !call || !result {
		t.Errorf("历史里的工具调用/结果没转过去: call=%v result=%v\n%s", call, result, req.Body)
	}

	// Responses 独有字段一个都不许漏进 CC 请求：那边不认，严格上游直接 400。
	//
	// `reasoning` 查的是**整个键**（带引号和冒号）而不是子串：CC 侧合法的
	// reasoning_effort 就以它开头，按子串查会把档位直传误判成泄漏（口径层 v0.65）。
	for _, forbidden := range []string{
		"input_text", "custom_tool_call", "additional_tools",
		"encrypted_content", "prompt_cache_key", "client_metadata", `"reasoning":`,
	} {
		if strings.Contains(string(req.Body), forbidden) {
			t.Errorf("Responses 侧字段 %q 漏进了 CC 请求体", forbidden)
		}
	}
	// 档位反过来必须同域直传（样本带 reasoning.effort）。
	var sentKeys map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &sentKeys); err != nil {
		t.Fatal(err)
	}
	if got := string(sentKeys["reasoning_effort"]); got != `"high"` {
		t.Errorf("reasoning_effort = %s，期望 \"high\"（档位直传）", got)
	}
}

// 下行流是 Responses 线格式，且 custom 工具的入参被**对称拆包**回裸 JS。
// 拆不回来，Codex 的 exec 拿到的是一段包着 JS 的 JSON，直接语法错——这是整条
// R→CC 路径最容易错、错了又最不显眼的一处。
func TestResponsesStreamIsResponsesWireFormat(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	streamUpstream(t, up, ccExecStreamFrames(t)...)()

	resp := gw.Post(t, "/v1/responses", convertResponsesRequest, nil)
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
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.output_item.added",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done",
		"response.output_item.done",
		"response.completed",
	}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("帧序列 =\n  %v\n期望 =\n  %v\nbody=%s", events, want, body)
	}

	// 拆包：下行流里应当是裸 JS，不该出现包装键。
	if !strings.Contains(body, `tools.exec_command`) {
		t.Errorf("工具入参里的 JS 没下来: %s", body)
	}
	if strings.Contains(body, `{\"input\":\"const`) {
		t.Errorf("包装没拆，Codex 会把它当 JS 语法错: %s", body)
	}
	// 结果要能对回调用：call_id 原样携带。
	if !strings.Contains(body, `"call_id":"call_x"`) {
		t.Errorf("call_id 没带下来: %s", body)
	}
	// usage 在终帧，且是上游自己报的数。
	if !strings.Contains(body, `"input_tokens":88`) || !strings.Contains(body, `"cached_tokens":40`) {
		t.Errorf("终帧没带上游报的 usage: %s", body)
	}
	// 上游 id 原样透传（口径层 v0.31）。
	if !strings.Contains(body, `"id":"chatcmpl-9"`) {
		t.Errorf("上游响应 id 没带下来: %s", body)
	}
	// CC 侧字段一个都不该出现在下行流里。
	for _, forbidden := range []string{"finish_reason", "chat.completion", `"delta":{"role"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("CC 侧字段 %q 漏进了 Responses 下行流", forbidden)
		}
	}
}

// 上游的 reasoning_content 要合成成 Responses 的推理摘要（口径层 v0.62 出向合成、
// v0.73 ② 补上的 R→CC 这一格）。用真实 CC 转录喂，不手搭。
//
// 帧序不是装饰：Codex 靠 reasoning_summary_part.added 立起 summary[0] 槽位，缺了它后续
// delta 索引到不存在的 part（同正文侧 content_part.added 那个坑）。
func TestResponsesReasoningSummaryReachesCodexClient(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	respondWithGolden(t, up, "cc-stream-reasoning-text")

	resp := gw.Post(t, "/v1/responses", convertResponsesRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	for _, want := range []string{
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.reasoning_summary_part.done",
		"Planning Chinese explanation",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("下行流里缺 %q（出向合成没生效或帧序不全）:\n%s", want, body)
		}
	}
	// 合成的 reasoning 项不写密文——我们手里没有封装，空串等于声称「有一个空封装」。
	if strings.Contains(body, "encrypted_content") {
		t.Errorf("合成的 reasoning 项里冒出了 encrypted_content:\n%s", body)
	}
	// CC 侧的字段名不许漏进 Responses 下行流。
	if strings.Contains(body, "reasoning_content") {
		t.Errorf("CC 的 reasoning_content 原样漏给了 Codex:\n%s", body)
	}
}

// 非流式走 DecodeFullBody + EncodeFullBody 那条路，聚合结果与流式一致。
func TestResponsesNonStreamAggregates(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"alpha-one"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":88,"completion_tokens":12}}`)

	resp := gw.Post(t, "/v1/responses",
		strings.Replace(convertResponsesRequest, `"stream":true`, `"stream":false`, 1), nil)
	body := gatewaytest.ReadBody(t, resp)

	var out struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("下行体不是 Responses 响应: %v\n%s", err, body)
	}
	if out.Object != "response" || out.Status != "completed" || out.ID != "chatcmpl-9" {
		t.Errorf("骨架不对: object=%q status=%q id=%q", out.Object, out.Status, out.ID)
	}
	if len(out.Output) != 1 || out.Output[0].Content[0].Text != "alpha-one" {
		t.Errorf("正文没聚合出来: %+v", out.Output)
	}
	if out.Usage.InputTokens != 88 || out.Usage.OutputTokens != 12 {
		t.Errorf("usage = %+v", out.Usage)
	}
}

// 带 encrypted_content 的 input 不使转换报错——§5 坑清单点名要的用例。那串密文
// 只有原上游解得开，跨协议必然作废；作废是**丢弃**，不是报错，更不是伪造一个
// 转过去。这里同时钉住它没被顺手带给 CC 上游。
func TestResponsesEncryptedReasoningIsDroppedNotFatal(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)

	resp := gw.Post(t, "/v1/responses",
		strings.Replace(convertResponsesRequest, `"stream":true`, `"stream":false`, 1), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 = %d, 带密文的请求不该失败: %s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if strings.Contains(string(up.Last(t).Body), "gAAAAAB") {
		t.Error("上游侧密文被带给了 CC 上游——它解不开，只会白占 token")
	}
}

// 闸门这次放开的是 /v1/responses × openai 这一格。/v1/chat/completions 打到
// CC 渠道是**同协议透传**，本来就不该进转换分支。
func TestResponsesGateOpensOnlyResponsesToCC(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"chatcmpl-9","model":"`+ccUpstreamModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)

	gw.Post(t, "/v1/chat/completions",
		`{"model":"`+accessPointModel+`","messages":[{"role":"user","content":"hi"}]}`, nil)

	// 透传路径不重编码：上游收到的应当是客户端原样发的字节（只有 model 被 splice）。
	if body := string(up.Last(t).Body); !strings.Contains(body, `"messages"`) || strings.Contains(body, `"input"`) {
		t.Errorf("同协议请求走了转换分支: %s", body)
	}
}

// 此处原有 TestGateStaysClosedForUnimplementedPath，钉的是「只开了一格，不是开了
// 一整行」。#80 九宫格全开之后没有哪一格还关着，这条性质不再存在，随该格放开一并
// 删除（它先后指过 R×anthropic 与 messages×openai_responses，两格如今都开了）。
//
// 「走不通的路要 501 且不碰上游」这条断言仍在，载体换成了唯一永久成立的那个反例
// ——count_tokens，见 openai_test.go 的 TestCrossProtocolGateAnswersInInboundFormat
// 与 protocolset_test.go 的 TestCountTokensNeedsAnthropicInTheSet。
