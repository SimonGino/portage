package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件测的是 R→A 转换路径（#25，口径层 §2.1 优先级②）：Codex CLI 挂 Claude。
//
// 与 convert_responses_test.go（R→CC）共用入口那半边，所以入站解码的断言不在这里
// 重复；这边验的是**另一半**——Anthropic 出口的请求编码与响应解码。

const anthropicUpstreamModel = "claude-sonnet-5"

func newR2AGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL,
		anthropicUpstreamModel, openaiCredential)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{}), up
}

// anthropicExecStreamFrames 是假 Anthropic 上游的回话：一段正文 + 一个 exec 调用。
//
// input 是**包装过的**对象——Anthropic 的 tool_use.input 只收 JSON 对象，而 exec 的
// 入参是 JS 源码，所以出口侧包成了 {"input":"…"}。一个诚实的上游就会照我们发过去的
// input_schema 这样回。
func anthropicExecStreamFrames(t *testing.T) []string {
	t.Helper()
	js := `const r = await tools.exec_command({cmd:"cat b.txt"}); text(r.output)`
	wrapped, err := protocol.WrapCustomToolArgs(js)
	if err != nil {
		t.Fatal(err)
	}
	// 分两片下发，钉住分片重组。
	cut := len(wrapped) / 2
	frag1, err := json.Marshal(wrapped[:cut])
	if err != nil {
		t.Fatal(err)
	}
	frag2, err := json.Marshal(wrapped[cut:])
	if err != nil {
		t.Fatal(err)
	}
	return []string{
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_up1","model":"` + anthropicUpstreamModel +
			`","usage":{"input_tokens":88,"cache_read_input_tokens":40,"output_tokens":1}}}

`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

`,
		`event: ping
data: {"type": "ping"}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我看看"}}

`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}

`,
		`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":` + string(frag1) + `}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":` + string(frag2) + `}}

`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":1}

`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":88,"cache_read_input_tokens":40,"output_tokens":12}}

`,
		`event: message_stop
data: {"type":"message_stop"}

`,
	}
}

// 出站请求：打 Anthropic 端点、模型翻译、max_tokens 补上、system 上提、Responses
// 独有字段一个都不许漏过去。
func TestR2ARequestReachesUpstreamAsMessages(t *testing.T) {
	gw, up := newR2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_1","model":"`+anthropicUpstreamModel+`","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":1,"output_tokens":1}}`)

	gw.Post(t, "/v1/responses",
		strings.Replace(convertResponsesRequest, `"stream":true`, `"stream":false`, 1), nil)

	req := up.Last(t)
	if req.Path != "/v1/messages" {
		t.Errorf("上游端点 = %q, 期望 /v1/messages", req.Path)
	}

	var sent struct {
		Model     string            `json:"model"`
		MaxTokens int               `json:"max_tokens"`
		System    []json.RawMessage `json:"system"`
		Messages  []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
				Content   string          `json:"content"`
			} `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(req.Body, &sent); err != nil {
		t.Fatalf("上游收到的不是 Anthropic 请求: %v\n%s", err, req.Body)
	}

	if sent.Model != anthropicUpstreamModel {
		t.Errorf("model = %q, 期望翻译成纳管名 %q", sent.Model, anthropicUpstreamModel)
	}
	// max_tokens 是 Anthropic 的必填字段，而 Codex 那条请求根本没给 max_output_tokens。
	if sent.MaxTokens <= 0 {
		t.Errorf("max_tokens = %d，Anthropic 必填，缺了就是 400", sent.MaxTokens)
	}
	// developer 消息上提到 system：留在 messages 里 Anthropic 会拒。
	if len(sent.System) == 0 {
		t.Errorf("developer 消息没上提到 system: %s", req.Body)
	}
	for _, m := range sent.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			t.Errorf("messages 里出现了 %q，Anthropic 只收 user/assistant", m.Role)
		}
	}

	// custom 工具的 input_schema 必须合成出来——它既是必填字段，也是唯一告诉模型
	// 「该回 {"input": …}」的东西。
	var sawExec bool
	for _, tool := range sent.Tools {
		if tool.Name != "exec" {
			continue
		}
		sawExec = true
		if _, ok := tool.InputSchema.Properties[protocol.CustomToolArgsKey]; !ok {
			t.Errorf("exec 的 input_schema 没声明 %q: %s", protocol.CustomToolArgsKey, req.Body)
		}
	}
	if !sawExec {
		t.Errorf("exec 没编进 tools: %s", req.Body)
	}

	// 历史里的 custom_tool_call：input 必须是**对象**（包装过的），不是裸 JS 字符串。
	var sawCall, sawResult bool
	for _, m := range sent.Messages {
		for _, b := range m.Content {
			switch b.Type {
			case "tool_use":
				sawCall = true
				var input map[string]any
				if err := json.Unmarshal(b.Input, &input); err != nil {
					t.Errorf("tool_use.input 不是对象，Anthropic 会拒: %s", b.Input)
				} else if s, _ := input[protocol.CustomToolArgsKey].(string); !strings.Contains(s, "exec_command") {
					t.Errorf("包装里没有原始 JS: %v", input)
				}
			case "tool_result":
				sawResult = true
				if b.ToolUseID != "call_1" {
					t.Errorf("tool_result 的 tool_use_id = %q", b.ToolUseID)
				}
				if !strings.Contains(b.Content, "alpha-one") {
					t.Errorf("工具结果内容丢了: %q", b.Content)
				}
			}
		}
	}
	if !sawCall || !sawResult {
		t.Errorf("历史里的工具调用/结果没转过去: call=%v result=%v\n%s", sawCall, sawResult, req.Body)
	}

	// Responses 独有字段一个都不许漏进 Anthropic 请求。
	for _, forbidden := range []string{
		"input_text", "custom_tool_call", "additional_tools",
		"encrypted_content", "prompt_cache_key", "client_metadata", "reasoning",
		"lark", "format",
	} {
		if strings.Contains(string(req.Body), forbidden) {
			t.Errorf("Responses 侧字段 %q 漏进了 Anthropic 请求体", forbidden)
		}
	}
}

// 下行流：Anthropic 的 SSE 要变成 Responses 线格式，且 custom 工具的入参被**对称
// 拆包**回裸 JS。拆不回来，Codex 的 exec 拿到一段包着 JS 的 JSON，直接语法错。
func TestR2AStreamIsResponsesWireFormat(t *testing.T) {
	gw, up := newR2AGateway(t)
	streamUpstream(t, up, anthropicExecStreamFrames(t)...)()

	resp := gw.Post(t, "/v1/responses", convertResponsesRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	// Anthropic 的事件名一个都不许漏到客户端——客户端等的是 Responses。
	for _, leaked := range []string{
		"content_block_delta", "message_start", "input_json_delta", "thinking_delta",
	} {
		if strings.Contains(body, leaked) {
			t.Errorf("Anthropic 事件名 %q 漏给了 Codex:\n%s", leaked, body)
		}
	}
	if !strings.Contains(body, "response.completed") {
		t.Errorf("没有 Responses 的终帧:\n%s", body)
	}
	// Responses 不发 [DONE]，那是 Chat Completions 的收尾。
	if strings.Contains(body, "[DONE]") {
		t.Errorf("Responses 流不该有 [DONE]:\n%s", body)
	}

	// 拆包：客户端应当拿到裸 JS，而不是 {"input":"const …"}。
	if strings.Contains(body, `{\"input\":\"const`) {
		t.Errorf("包装没拆，Codex 会把它当 JS 语法错:\n%s", body)
	}
	if !strings.Contains(body, "exec_command") {
		t.Errorf("工具入参丢了:\n%s", body)
	}
	if !strings.Contains(body, "custom_tool_call") {
		t.Errorf("声明成 custom 的工具没回成 custom_tool_call:\n%s", body)
	}
}

// 上游的 thinking / signature 在这条路上必然丢弃：Responses 侧没有真实的 reasoning
// delta 转录可依（§9.1 缺口）。要钉的是**丢了不炸**，且那串 signature 不许漏进正文
// ——漏了，Codex 会把一串 base64 当回答渲染出来。
func TestR2AThinkingIsDroppedNotLeaked(t *testing.T) {
	gw, up := newR2AGateway(t)
	streamUpstream(t, up,
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_up2","model":"`+anthropicUpstreamModel+`"}}

`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先读文件"}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EuwDCokBCBAYAipA"}}

`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}

`,
		`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"读好了"}}

`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":1}

`,
		`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":6}}

`,
		`event: message_stop
data: {"type":"message_stop"}

`,
	)()

	resp := gw.Post(t, "/v1/responses", convertResponsesRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != 200 {
		t.Fatalf("状态码 = %d\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "读好了") || !strings.Contains(body, "response.completed") {
		t.Errorf("正文没转过来:\n%s", body)
	}
	if strings.Contains(body, "EuwDCokBCBAYAipA") {
		t.Errorf("signature 漏进了下行流，Codex 会把 base64 当回答渲染:\n%s", body)
	}
	if strings.Contains(body, "先读文件") {
		t.Errorf("推理正文漏进了下行流（本路径应当丢弃）:\n%s", body)
	}
}

// 非流式：整包 Anthropic 响应聚合成一个 Responses 响应体。
func TestR2ANonStreamAggregates(t *testing.T) {
	gw, up := newR2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_1","model":"`+anthropicUpstreamModel+`","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"读好了"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":88,"output_tokens":12,"cache_read_input_tokens":40}}`)

	resp := gw.Post(t, "/v1/responses",
		strings.Replace(convertResponsesRequest, `"stream":true`, `"stream":false`, 1), nil)
	body := gatewaytest.ReadBody(t, resp)

	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("回给客户端的不是 Responses JSON: %v\n%s", err, body)
	}
	if out.Status != "completed" {
		t.Errorf("status = %q", out.Status)
	}
	// 响应 id 原样透传（口径层 v0.31）。
	if out.ID != "msg_1" {
		t.Errorf("上游响应 id 没透传: %q", out.ID)
	}
	if len(out.Output) == 0 || len(out.Output[0].Content) == 0 ||
		out.Output[0].Content[0].Text != "读好了" {
		t.Errorf("正文没聚合出来: %s", body)
	}
	// Responses 的 input_tokens 是毛值：上游净值 88 + 缓存读 40 = 128。Codex 按
	// total_tokens 判压缩触发点，低估会让它撞上游 400（protocol.Usage 的约定，#72）。
	if out.Usage.InputTokens != 128 || out.Usage.OutputTokens != 12 {
		t.Errorf("usage 没带过来: %+v，期望毛值 input 128", out.Usage)
	}
	if out.Usage.InputTokensDetails.CachedTokens != 40 {
		t.Errorf("缓存命中数没带过来: %+v", out.Usage)
	}
}

// 闸门：R→A 这一格开了。
func TestR2AGateIsOpen(t *testing.T) {
	gw, up := newR2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_1","model":"`+anthropicUpstreamModel+`","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)

	resp := gw.Post(t, "/v1/responses",
		strings.Replace(convertResponsesRequest, `"stream":true`, `"stream":false`, 1), nil)
	if resp.StatusCode != 200 {
		t.Errorf("状态码 = %d, 期望 200；body=%s", resp.StatusCode, gatewaytest.ReadBody(t, resp))
	}
	if up.Count() == 0 {
		t.Error("请求没到上游")
	}
}
