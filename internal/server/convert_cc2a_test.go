package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件测的是 CC→A 转换路径（口径层 §2.1 优先级③上半）：opencode 一类的
// OpenAI 客户端挂 Claude 渠道。
//
// 入站字节用 testdata/golden/in-cc-* 真实发包，只把 model 换成接入点名——手搭的
// 请求体只会长成我以为的样子，而这条链路真正要扛的是 opencode 实际发出来的形态。

const cc2aUpstreamModel = "claude-sonnet-5"

func newCC2AGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "anthropic", up.URL,
		cc2aUpstreamModel, openaiCredential)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{}), up
}

// ccSampleBody 读一份真实入站样本，把 model 换成接入点名，并按需改 stream。
func ccSampleBody(t *testing.T, name string, stream bool) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", name, "request.json"))
	if err != nil {
		t.Skipf("样本尚未采集：%s", name)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	body["model"] = accessPointModel
	body["stream"] = stream
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

const anthropicOKBody = `{"id":"msg_1","model":"` + cc2aUpstreamModel + `","type":"message",` +
	`"role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":1,"output_tokens":1}}`

// 出站请求：打 Anthropic 端点、模型翻译、system 消息上提、CC 独有字段一个都不许漏。
func TestCC2ARequestReachesUpstreamAsMessages(t *testing.T) {
	gw, up := newCC2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, anthropicOKBody)

	gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", false), nil)

	req := up.Last(t)
	if req.Path != "/v1/messages" {
		t.Errorf("上游端点 = %q, 期望 /v1/messages", req.Path)
	}

	var sent struct {
		Model     string            `json:"model"`
		MaxTokens int               `json:"max_tokens"`
		System    []json.RawMessage `json:"system"`
		Messages  []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		ToolChoice map[string]any `json:"tool_choice"`
	}
	if err := json.Unmarshal(req.Body, &sent); err != nil {
		t.Fatalf("上游收到的不是 Anthropic 请求: %v\n%s", err, req.Body)
	}

	if sent.Model != cc2aUpstreamModel {
		t.Errorf("model = %q, 期望翻译成纳管名 %q", sent.Model, cc2aUpstreamModel)
	}
	if sent.MaxTokens != 32000 {
		t.Errorf("max_tokens = %d，样本里客户端给的是 32000", sent.MaxTokens)
	}
	// CC 的 system 消息必须上提到顶层：留在 messages 里 Anthropic 直接拒。
	if len(sent.System) == 0 {
		t.Errorf("system 消息没上提到顶层 system: %s", req.Body)
	}
	for _, m := range sent.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			t.Errorf("messages 里出现了 %q，Anthropic 只收 user/assistant", m.Role)
		}
	}
	// 工具声明：两层嵌套要拍平，input_schema 是必填字段。
	if len(sent.Tools) == 0 {
		t.Fatalf("tools 丢了: %s", req.Body)
	}
	for _, tool := range sent.Tools {
		if tool.Name == "" || len(tool.InputSchema) == 0 {
			t.Errorf("工具声明不完整（input_schema 必填）: %+v", tool)
		}
	}
	if sent.ToolChoice["type"] != "auto" {
		t.Errorf("tool_choice = %v，样本里是 \"auto\"", sent.ToolChoice)
	}

	// CC 独有的形态一个都不许漏进 Anthropic 请求体。
	for _, forbidden := range []string{
		"stream_options", "tool_call_id", "tool_calls", "max_completion_tokens",
		`"function"`, `"role":"system"`, `"role":"tool"`,
	} {
		if strings.Contains(string(req.Body), forbidden) {
			t.Errorf("CC 侧字段 %q 漏进了 Anthropic 请求体", forbidden)
		}
	}
}

// 并行工具轮：CC 的两条独立 tool 消息要并成 Anthropic 的一条 user 消息两个块。
// 这是两个协议之间最硬的形态分歧，不并就是 400。
func TestCC2AMergesToolMessagesIntoOneUserTurn(t *testing.T) {
	gw, up := newCC2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, anthropicOKBody)

	gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-parallel-turn2", false), nil)

	var sent struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
				ID        string `json:"id"`
			} `json:"content"`
		} `json:"messages"`
	}
	body := up.Last(t).Body
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("上游收到的不是 Anthropic 请求: %v\n%s", err, body)
	}

	// user → assistant → user，角色严格交替（Anthropic 的硬约束）。
	var roles []string
	for _, m := range sent.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Fatalf("角色序列 = %v，期望严格交替的 user/assistant/user\n%s", roles, body)
	}

	last := sent.Messages[2]
	if len(last.Content) != 2 {
		t.Fatalf("末条消息块数 = %d，两条 tool 消息应并进同一条 user 消息\n%s", len(last.Content), body)
	}
	ids := []string{last.Content[0].ToolUseID, last.Content[1].ToolUseID}
	if ids[0] != "call_goldenrec_read_a" || ids[1] != "call_goldenrec_read_b" {
		t.Errorf("tool_use_id = %v，id 应原样携带且保持次序", ids)
	}
	// assistant 那条的 tool_use 必须真的在，否则结果就成了孤儿，会被 Anthropic 拒。
	var calls int
	for _, b := range sent.Messages[1].Content {
		if b.Type == "tool_use" {
			calls++
		}
	}
	if calls != 2 {
		t.Errorf("assistant 轮的 tool_use 数 = %d，期望 2", calls)
	}
}

// temperature：CC 收 0~2，Anthropic 超过 1 直接 400，转换时必须 clamp（展开层 §2）。
func TestCC2AClampsTemperature(t *testing.T) {
	gw, up := newCC2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, anthropicOKBody)

	gw.Post(t, "/v1/chat/completions",
		`{"model":"`+accessPointModel+`","temperature":1.8,`+
			`"messages":[{"role":"user","content":"hi"}]}`, nil)

	var sent struct {
		Temperature float64 `json:"temperature"`
	}
	body := up.Last(t).Body
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Temperature != 1 {
		t.Errorf("temperature = %v，期望收进 Anthropic 的 0~1；1.8 发过去就是 400", sent.Temperature)
	}
}

// 下行流：Anthropic 的 SSE 要变成 Chat Completions 线格式。
func TestCC2AStreamIsChatCompletionsWireFormat(t *testing.T) {
	gw, up := newCC2AGateway(t)
	streamUpstream(t, up,
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_up1","model":"`+cc2aUpstreamModel+
			`","usage":{"input_tokens":88,"cache_read_input_tokens":40,"output_tokens":1}}}

`,
		`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"我看看"}}

`,
		`event: content_block_stop
data: {"type":"content_block_stop","index":0}

`,
		`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"filePath\":\"notes.md\"}"}}

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
	)()

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-tool-turn1", true), nil)
	body := gatewaytest.ReadBody(t, resp)

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	// Anthropic 的事件名一个都不许漏到客户端——客户端等的是 Chat Completions。
	for _, leaked := range []string{
		"event:", "content_block_delta", "message_start", "input_json_delta", "tool_use",
	} {
		if strings.Contains(body, leaked) {
			t.Errorf("Anthropic 线格式 %q 漏给了 CC 客户端:\n%s", leaked, body)
		}
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Errorf("CC 流必须以 [DONE] 收尾:\n%s", body)
	}
	if !strings.Contains(body, `"chat.completion.chunk"`) {
		t.Errorf("帧不是 chat.completion.chunk:\n%s", body)
	}
	if !strings.Contains(body, "我看看") {
		t.Errorf("正文丢了:\n%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("finish_reason 没映成 tool_calls，客户端不会去跑工具:\n%s", body)
	}
	if !strings.Contains(body, "notes.md") {
		t.Errorf("工具入参丢了:\n%s", body)
	}
	// 样本要过 stream_options.include_usage，那 usage 帧就得补上。CC 的 prompt_tokens
	// 是毛值：上游净值 88 + 缓存读 40 = 128（protocol.Usage 的约定，portage-legacy#72）。
	if !strings.Contains(body, `"prompt_tokens":128`) {
		t.Errorf("要过 include_usage 却没有 usage 帧，或 prompt_tokens 不是毛值:\n%s", body)
	}
	// 上游 id 原样透传（口径层 v0.31），不该被换成网关补的 chatcmpl-。
	if !strings.Contains(body, "msg_up1") {
		t.Errorf("上游 id 没透传:\n%s", body)
	}
}

// 非流式：Anthropic 的完整响应体要聚合成 chat.completion。
func TestCC2ABufferedResponseIsChatCompletion(t *testing.T) {
	gw, up := newCC2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"},
		`{"id":"msg_2","model":"`+cc2aUpstreamModel+`","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"读完了"},`+
			`{"type":"tool_use","id":"toolu_9","name":"read","input":{"filePath":"notes.md"}}],`+
			`"stop_reason":"tool_use",`+
			`"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":4}}`)

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-tool-turn1", false), nil)
	raw := gatewaytest.ReadBody(t, resp)

	var out struct {
		Object  string `json:"object"`
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Role      string  `json:"role"`
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
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
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("回给客户端的不是 CC 响应: %v\n%s", err, raw)
	}
	if out.Object != "chat.completion" {
		t.Errorf("object = %q", out.Object)
	}
	if out.ID != "msg_2" {
		t.Errorf("id = %q，上游给了就原样透传", out.ID)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("choices 数 = %d", len(out.Choices))
	}
	msg := out.Choices[0].Message
	if msg.Content == nil || *msg.Content != "读完了" {
		t.Errorf("正文没对上: %v", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "toolu_9" {
		t.Fatalf("工具调用没转过去: %+v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].Function.Arguments != `{"filePath":"notes.md"}` {
		t.Errorf("arguments = %q，应是能解开的 JSON 字符串", msg.ToolCalls[0].Function.Arguments)
	}
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q，Anthropic 的 tool_use 应映成 tool_calls", out.Choices[0].FinishReason)
	}
	// 非流式恒带 usage，与 include_usage 无关。prompt_tokens 是毛值：10 + 缓存读 4。
	if out.Usage.PromptTokens != 14 || out.Usage.TotalTokens != 19 {
		t.Errorf("usage 没对上: %+v，期望毛值 prompt 14 / total 19", out.Usage)
	}
	if out.Usage.PromptTokensDetails.CachedTokens != 4 {
		t.Errorf("cache_read 没映成 cached_tokens: %+v", out.Usage)
	}
	// Anthropic 的形态一个都不许漏给客户端。
	for _, leaked := range []string{"tool_use", "stop_reason", "input_tokens", `"type":"message"`} {
		if strings.Contains(raw, leaked) {
			t.Errorf("Anthropic 字段 %q 漏给了 CC 客户端:\n%s", leaked, raw)
		}
	}
}

// developer 消息要上提到 Anthropic 的顶层 system，不能落进 messages。
//
// 这一条走的是全链路而不是单测：归一在 CC 解码侧、上提在 Anthropic 编码侧，分开看
// 两边都「对」，错的是它们之间那一环——归一漏了的话 developer 会当 user 处理，还会
// 跟紧随其后的 user 消息合并，两个单测都照样绿。
func TestCC2ADeveloperBecomesSystem(t *testing.T) {
	gw, up := newCC2AGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, anthropicOKBody)

	gw.Post(t, "/v1/chat/completions", `{"model":"`+accessPointModel+`","stream":false,"messages":[
		{"role":"developer","content":"你是个严谨的助手"},
		{"role":"user","content":"你好"}]}`, nil)

	req := up.Last(t)
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["system"] == nil {
		t.Fatalf("developer 没上提到顶层 system: %s", req.Body)
	}
	if !strings.Contains(string(req.Body), "你是个严谨的助手") {
		t.Errorf("系统提示的正文丢了: %s", req.Body)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages 数 = %d，期望只剩那条 user——developer 已经上提走了", len(msgs))
	}
	only, _ := msgs[0].(map[string]any)
	if only["role"] != "user" {
		t.Errorf("剩下那条的角色 = %v", only["role"])
	}
	// 合并进 user 的话正文会同时出现在 messages 里，那正是漏归一的症状。
	if raw, _ := json.Marshal(msgs); strings.Contains(string(raw), "你是个严谨的助手") {
		t.Errorf("系统提示被并进了 user 消息: %s", raw)
	}
}

// 上游的 thinking 要合成成 CC 的 delta.reasoning_content（口径层 v0.62），signature
// 一个字节都不许漏——它是一串 base64，漏了客户端会当回答渲染。
//
// 手搭上游帧而不是用 golden：本库唯一两份带 thinking 的真实 Anthropic 转录
// （anthropic-{stream-thinking,thinking-high}）都是 effort-only 采的，thinking 正文
// **整段为空**、只有 signature，钉不住「正文要到得了客户端」这一半。空正文那一半由
// 那两份样本在 protocol 层钉（golden_test 的 thinkingSamples）。
func TestCC2AThinkingBecomesReasoningContent(t *testing.T) {
	gw, up := newCC2AGateway(t)
	streamUpstream(t, up,
		`event: message_start
data: {"type":"message_start","message":{"id":"msg_up1","model":"`+cc2aUpstreamModel+`"}}

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

	resp := gw.Post(t, "/v1/chat/completions", ccSampleBody(t, "in-cc-text", true), nil)
	body := gatewaytest.ReadBody(t, resp)

	if !strings.Contains(body, `"reasoning_content":"先读文件"`) {
		t.Errorf("推理没合成成 reasoning_content:\n%s", body)
	}
	if strings.Contains(body, "EuwDCokBCBAYAipA") || strings.Contains(body, "signature") {
		t.Errorf("signature 漏进了下行流:\n%s", body)
	}
	// 推理不许混进 content：客户端把两格分开渲染。
	if strings.Contains(body, `"content":"先读文件"`) {
		t.Errorf("推理混进了正文:\n%s", body)
	}
	if !strings.Contains(body, `"content":"读好了"`) {
		t.Errorf("正文丢了:\n%s", body)
	}
}

// 转换失败口径在 CC 入口的那一面（口径层 v1.14 ⑧，#96）：tool_choice 点名的工具
// 没声明，Anthropic 出口回 400，错误按 OpenAI 外壳带 code / param，一个字节不打上游。
func TestCC2ARejectsToolChoiceNamingUndeclaredTool(t *testing.T) {
	gw, up := newCC2AGateway(t)

	body := `{"model":"` + accessPointModel + `",` +
		`"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}],` +
		`"tool_choice":{"type":"function","function":{"name":"ghost"}},` +
		`"messages":[{"role":"user","content":"hi"}]}`
	resp := gw.Post(t, "/v1/chat/completions", body, nil)
	got := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400；body=%s", resp.StatusCode, got)
	}
	assertOpenAIError(t, got)
	for _, want := range []string{`"param":"tool_choice"`, `"code":"invalid_value"`, "ghost", "read"} {
		if !strings.Contains(got, want) {
			t.Errorf("400 响应里没有 %s: %s", want, got)
		}
	}
	if up.Count() != 0 {
		t.Errorf("请求不该到达上游，却收到 %d 次", up.Count())
	}
}
