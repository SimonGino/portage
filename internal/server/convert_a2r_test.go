package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 本文件测的是 A→R 转换路径（#80，口径层 §2.1 优先级④）：Claude Code 挂 Responses 渠道。
//
// 与 convert_cc2r_test.go 共用出口那半边，所以「Responses 请求长什么样」的通用断言
// 不在这里重复；这边验的是**另一半**——Anthropic 入口特有的形态（顶层 system 数组、
// thinking 块、tool_result 在 user 消息里）怎么落到 Responses 上，以及回程怎么变回
// Anthropic 线格式。

func newA2RGateway(t *testing.T) (*gatewaytest.Gateway, *gatewaytest.Upstream) {
	t.Helper()
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai_responses", up.URL,
		cc2rUpstreamModel, anthropicCredential)
	return gatewaytest.StartWith(t, db, gatewaytest.Options{}), up
}

// 出站请求：Anthropic 的顶层 system 数组要变成 developer 项，工具要拍成扁平 function。
func TestA2RRequestReachesUpstreamAsResponses(t *testing.T) {
	gw, up := newA2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/messages", ccSampleBody(t, "in-anthropic-tool-turn1", false), nil)

	req := up.Last(t)
	if req.Path != "/v1/responses" {
		t.Errorf("上游端点 = %q, 期望 /v1/responses", req.Path)
	}

	var sent struct {
		Model string `json:"model"`
		Store *bool  `json:"store"`
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		MaxOutputTokens int `json:"max_output_tokens"`
		Tools           []struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(req.Body, &sent); err != nil {
		t.Fatalf("上游收到的不是 Responses 请求: %v\n%s", err, req.Body)
	}

	if sent.Model != cc2rUpstreamModel {
		t.Errorf("model = %q", sent.Model)
	}
	if sent.Store == nil || *sent.Store {
		t.Errorf("store = %v，期望恒发 false", sent.Store)
	}
	// Anthropic 的 max_tokens 是必填项，样本里一定有；到 Responses 侧改名叫
	// max_output_tokens，不改名上游认不得。
	if sent.MaxOutputTokens <= 0 {
		t.Errorf("max_output_tokens = %d，客户端给的上限丢了", sent.MaxOutputTokens)
	}
	if len(sent.Input) == 0 {
		t.Fatalf("input 为空: %s", req.Body)
	}
	// Anthropic 的顶层 system 数组编成 developer 项——留在顶层的话 Responses 认不得，
	// 而它恰恰是这条路上信息量最大的一段。
	first := sent.Input[0]
	if first.Type != "message" || first.Role != "developer" {
		t.Errorf("首项 = %+v，期望 developer 消息项", first)
	}
	if len(first.Content) == 0 || first.Content[0].Text == "" {
		t.Errorf("系统提示正文丢了: %+v", first.Content)
	}
	if len(sent.Tools) == 0 {
		t.Fatalf("tools 丢了: %s", req.Body)
	}
	for _, tool := range sent.Tools {
		if tool.Type != "function" || tool.Name == "" || len(tool.Parameters) == 0 {
			t.Errorf("工具声明不完整: %+v", tool)
		}
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"system", "messages", "max_tokens", "temperature", "top_p", "stop_sequences",
		"metadata", "thinking", "instructions", "reasoning", "previous_response_id",
	} {
		if _, ok := keys[forbidden]; ok {
			t.Errorf("顶层字段 %q 漏进了 Responses 请求体", forbidden)
		}
	}
}

// Anthropic 把 tool_result 放在 user 消息的块里，到 Responses 侧要摊成顶层
// function_call_output 项，且排在同轮正文之前。
func TestA2RToolResultBecomesTopLevelOutput(t *testing.T) {
	gw, up := newA2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/messages", ccSampleBody(t, "in-anthropic-tool-turn2", false), nil)

	var sent struct {
		Input []struct {
			Type   string `json:"type"`
			Role   string `json:"role"`
			CallID string `json:"call_id"`
			Output any    `json:"output"`
		} `json:"input"`
	}
	body := up.Last(t).Body
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("上游收到的不是 Responses 请求: %v\n%s", err, body)
	}

	calls := map[string]bool{}
	var results int
	var lastOutputIdx = -1
	for i, it := range sent.Input {
		switch it.Type {
		case "function_call":
			calls[it.CallID] = true
		case "function_call_output":
			results++
			lastOutputIdx = i
			if !calls[it.CallID] {
				t.Errorf("结果 %q 对不上任何调用", it.CallID)
			}
			if _, ok := it.Output.(string); !ok {
				t.Errorf("output = %T，Responses 收的是纯字符串", it.Output)
			}
		}
		// tool_result 块不该以内容块的形式留在消息项里。
		if it.Type == "message" && it.Role == "" {
			t.Errorf("input 项 %d 没有角色: %+v", i, it)
		}
	}
	if results == 0 {
		t.Fatalf("tool_result 没摊成 function_call_output:\n%s", body)
	}
	// 结果项必须排在它所属那轮的调用项之后——顺序错了上游看到的是「先有结果后有调用」。
	if lastOutputIdx == 0 {
		t.Error("function_call_output 排在了整个 input 最前面")
	}
	if strings.Contains(string(body), "tool_result") || strings.Contains(string(body), "tool_use_id") {
		t.Errorf("Anthropic 的块形态漏进了 Responses 请求体:\n%s", body)
	}
}

// thinking 块只能丢，不得伪造成 reasoning 项（口径层 v0.10）。
//
// 走全链而不是单测：Anthropic 解码侧产的是带正文与 signature 的 thinking 块，
// 出口侧丢它——两边分开看都「对」，错的是它们之间那一环。
func TestA2RThinkingIsDroppedNotFabricated(t *testing.T) {
	gw, up := newA2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	gw.Post(t, "/v1/messages", `{"model":"`+accessPointModel+`","max_tokens":64,"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"先想一想","signature":"EuwDCokBCBAYAipA"},
			{"type":"text","text":"想好了"}]},
		{"role":"user","content":"继续"}]}`, nil)

	body := string(up.Last(t).Body)
	if strings.Contains(body, "先想一想") {
		t.Errorf("推理正文发给了 Responses 上游（本路径应当丢弃）:\n%s", body)
	}
	if strings.Contains(body, "EuwDCokBCBAYAipA") {
		t.Errorf("signature 发给了 Responses 上游:\n%s", body)
	}
	if strings.Contains(body, "encrypted_content") {
		t.Errorf("凭空合成了 reasoning 项的密文——那是上游侧的东西，造不出来:\n%s", body)
	}
	if !strings.Contains(body, "想好了") {
		t.Errorf("同一条消息里的正文被连坐丢了:\n%s", body)
	}
}

// 下行流：真实 Responses 转录要变成 Anthropic 线格式。
func TestA2RStreamIsAnthropicWireFormat(t *testing.T) {
	gw, up := newA2RGateway(t)
	respondWithGolden(t, up, "responses-stream-text")

	resp := gw.Post(t, "/v1/messages", ccSampleBody(t, "in-anthropic-text", true), nil)
	body := gatewaytest.ReadBody(t, resp)

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	for _, want := range []string{
		"event: message_start", "event: content_block_start", "event: content_block_delta",
		"event: message_delta", "event: message_stop", "pong",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Anthropic 线格式缺 %q:\n%s", want, body)
		}
	}
	// Responses 的事件名一个都不许漏给 Claude Code。
	for _, leaked := range []string{
		"response.created", "response.completed", "output_text.delta",
		"sequence_number", "obfuscation", "data: [DONE]",
	} {
		if strings.Contains(body, leaked) {
			t.Errorf("Responses 线格式 %q 漏给了 Anthropic 客户端:\n%s", leaked, body)
		}
	}
	// 样本 expect 核过的数：input 毛值 14521（含缓存读 3840）/ output 5。
	// Anthropic 的 input_tokens 是**净值**，缓存读要拆出去，否则会被重复计数。
	if !strings.Contains(body, `"cache_read_input_tokens":3840`) {
		t.Errorf("缓存读没拆成 cache_read_input_tokens:\n%s", body)
	}
	if !strings.Contains(body, `"output_tokens":5`) {
		t.Errorf("output_tokens 没对上样本 expect:\n%s", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Errorf("stop_reason 没给出来:\n%s", body)
	}
}

// 真实工具轮：Responses 的 custom_tool_call 到 Anthropic 侧必须是合法 JSON 的
// tool_use.input，而推理一个字都不许漏。
func TestA2RToolStreamGivesValidJSONInput(t *testing.T) {
	gw, up := newA2RGateway(t)
	respondWithGolden(t, up, "responses-stream-tool-turn1")

	resp := gw.Post(t, "/v1/messages", ccSampleBody(t, "in-anthropic-tool-turn1", true), nil)
	body := gatewaytest.ReadBody(t, resp)

	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Errorf("stop_reason 没映成 tool_use，Claude Code 不会去跑工具:\n%s", body)
	}
	// 上游那段 reasoning 密文与摘要一个字都不许漏——Claude Code 会把它当正文渲染。
	for _, leaked := range []string{"encrypted_content", "gAAAAAB", "reasoning", "thinking_delta"} {
		if strings.Contains(body, leaked) {
			t.Errorf("推理相关的 %q 漏给了 Anthropic 客户端:\n%s", leaked, body)
		}
	}

	// 把 input_json_delta 的分片拼回去，必须解得开。
	var args strings.Builder
	var toolName, toolID string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(line[6:]), &ev) != nil {
			continue
		}
		if ev.Type == "content_block_start" && ev.ContentBlock.Type == "tool_use" {
			toolName, toolID = ev.ContentBlock.Name, ev.ContentBlock.ID
		}
		if ev.Delta.Type == "input_json_delta" {
			args.WriteString(ev.Delta.PartialJSON)
		}
	}
	if toolName != "exec" || !strings.HasPrefix(toolID, "call_") {
		t.Fatalf("工具标识没转过来: name=%q id=%q\n%s", toolName, toolID, body)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args.String()), &parsed); err != nil {
		t.Fatalf("tool_use.input 不是合法 JSON，Anthropic 客户端解不动: %v\n%s", err, args.String())
	}
	if s, _ := parsed["input"].(string); !strings.Contains(s, "tools.exec_command") {
		t.Errorf("包装后的入参丢了原文: %v", parsed)
	}
}

// 非流式：Responses 的完整响应体要聚合成 Anthropic message。
func TestA2RBufferedResponseIsAnthropicMessage(t *testing.T) {
	gw, up := newA2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	resp := gw.Post(t, "/v1/messages", ccSampleBody(t, "in-anthropic-tool-turn1", false), nil)
	raw := gatewaytest.ReadBody(t, resp)

	var out struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("回给客户端的不是 Anthropic 响应: %v\n%s", err, raw)
	}
	if out.Type != "message" || out.Role != "assistant" || out.ID != "resp_buffered" {
		t.Errorf("响应头部没对上: %+v", out)
	}
	var text, tool int
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text++
			if b.Text != "读完了" {
				t.Errorf("正文 = %q", b.Text)
			}
		case "tool_use":
			tool++
			if b.ID != "call_9" || b.Name != "read" {
				t.Errorf("工具调用没对上: %+v", b)
			}
			if string(b.Input) != `{"filePath":"notes.md"}` {
				t.Errorf("input = %s，应是能解开的 JSON 对象", b.Input)
			}
		default:
			t.Errorf("出现了意外的块类型 %q", b.Type)
		}
	}
	if text != 1 || tool != 1 {
		t.Errorf("块数没对上: text=%d tool=%d\n%s", text, tool, raw)
	}
	if out.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q", out.StopReason)
	}
	// input_tokens 净值：毛值 10 − 缓存读 4 = 6。
	if out.Usage.InputTokens != 6 || out.Usage.CacheReadInputTokens != 4 {
		t.Errorf("usage 没对上: %+v，期望净值 6 / 缓存读 4", out.Usage)
	}
	// 上游那段密文一个字都不许漏。
	for _, leaked := range []string{"encrypted_content", "gAAAAAB", "output_text", "function_call"} {
		if strings.Contains(raw, leaked) {
			t.Errorf("Responses 字段 %q 漏给了 Anthropic 客户端:\n%s", leaked, raw)
		}
	}
}

// 闸门确实开着：这一格若还关着，回的是 501「没有对应的转换路径」。
func TestA2RGateIsOpen(t *testing.T) {
	gw, up := newA2RGateway(t)
	up.RespondWith(200, map[string]string{"Content-Type": "application/json"}, responsesOKBody)

	resp := gw.Post(t, "/v1/messages",
		`{"model":"`+accessPointModel+`","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, nil)
	body := gatewaytest.ReadBody(t, resp)

	if resp.StatusCode != 200 {
		t.Fatalf("状态码 = %d（501 说明闸没开）\n%s", resp.StatusCode, body)
	}
	if up.Count() == 0 {
		t.Error("上游一次都没被打到")
	}
}
