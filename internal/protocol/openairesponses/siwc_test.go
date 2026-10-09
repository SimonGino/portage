package openairesponses

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件钉订阅渠道（chatgpt_account，#213）出口的 D6 改写：以真机样本
// responses-stream-siwc-text/request.json 为底，断言出站体逐字段。
//
// 改写清单的事实源是 OpenAI 文档 D6（逐字见展开层 §7.13），拒绝形态的真机证据在
// responses-siwc-evidence/（一律 {"detail": …}）。这些用例不另采样本。

// siwcGoldenRequest 读真机请求样本（#205 采集）：model gpt-6.1-sol、store:false、
// stream:true、instructions + 一条 user message。
func siwcGoldenRequest(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir, "responses-stream-siwc-text", "request.json"))
	if err != nil {
		t.Skipf("样本尚未采集：%v", err)
	}
	return raw
}

// siwcEncode 走一遍「入口解码 → 订阅渠道出口编码」，clientStream 是客户端的流式意愿。
func siwcEncode(t *testing.T, body []byte, clientStream bool) (map[string]any, protocol.Drops, *protocol.Request, error) {
	t.Helper()
	in := NewCodec(Options{Subscription: true})
	req, err := in.DecodeRequest(body, clientStream)
	if err != nil {
		return nil, nil, nil, err
	}
	// 出口另起一个实例（relayConverted 就是这么干的），Subscription 位由 codecs.Options 传入。
	out := NewCodec(Options{Subscription: true})
	raw, drops, err := out.EncodeRequestReport(req, clientStream)
	if err != nil {
		return nil, drops, req, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("出站体不是 JSON 对象: %v", err)
	}
	return m, drops, req, nil
}

// TestSIWCEncodeRewritesOutboundBody：真机请求过订阅渠道出口——强制 store:false /
// stream:true（客户端要非流式也强制）、instructions 与 input 的 system 项都落 developer
// 消息项、正文原样保留、出站头之外的一切改写都在体内看得见。
func TestSIWCEncodeRewritesOutboundBody(t *testing.T) {
	sample := siwcGoldenRequest(t)

	// 客户端要非流式（stream:false）：出口仍必须 stream:true（D6：上游只回 SSE）。
	body, drops, _, err := siwcEncode(t, sample, false)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if v, ok := body["store"].(bool); !ok || v {
		t.Errorf("store = %v, 期望 false（恒发）", body["store"])
	}
	if v, _ := body["stream"].(bool); !v {
		t.Errorf("stream = %v, 期望 true——订阅渠道上游只认 SSE，客户端的非流式由聚合路径兜", body["stream"])
	}
	// 对照：非订阅渠道出口在客户端要非流式时不发 stream 键（不替客户端开流）。
	plain := NewCodec()
	req, err := NewCodec().DecodeRequest(sample, false)
	if err != nil {
		t.Fatal(err)
	}
	plainBody, _, err := plain.EncodeRequestReport(req, false)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(plainBody, &m)
	if _, ok := m["stream"]; ok {
		t.Errorf("非订阅渠道出口发了 stream 键: %v", m["stream"])
	}

	// instructions 是 Responses 的系统提示位，出口不上提、落 input 的 developer 项。
	input, _ := body["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("input 为空: %v", body["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["role"] != "developer" {
		t.Errorf("input[0].role = %v, 期望 developer（system 一律改 developer）", first["role"])
	}
	if body["instructions"] != nil {
		t.Errorf("出站体带了顶层 instructions: %v", body["instructions"])
	}
	// 正文（user 消息）原样保留在 input 里。
	var sawUser bool
	for _, item := range input {
		im, _ := item.(map[string]any)
		if im["role"] == "user" {
			sawUser = true
		}
	}
	if !sawUser {
		t.Errorf("user 消息项丢了: %v", body["input"])
	}
	// 基线请求（store:false 是九份真实 Codex 请求的常态）不该有任何丢弃登记——
	// store 归改写接管，翻值才记账；丢字段的登记在下一个用例逐个钉。
	if len(drops) != 0 {
		t.Errorf("基线请求不该有任何丢弃登记, got %v", drops.Strings())
	}
}

// TestSIWCEncodeInputSystemRoleBecomesDeveloper：input 里 role:"system" 的消息项
// （reject-system-role 的真机拒绝形态就是它）出口改写成 developer——不是丢弃。
func TestSIWCEncodeInputSystemRoleBecomesDeveloper(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","stream":true,` +
		`"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"be brief"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	out, drops, _, err := siwcEncode(t, body, true)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	input, _ := out["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input 长度 = %d, 期望 2: %v", len(input), out["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["role"] != "developer" {
		t.Errorf("system 项出口 role = %v, 期望 developer", first["role"])
	}
	if len(drops) != 0 {
		t.Errorf("system→developer 是改写不是丢弃, 却登记了 %v", drops.Strings())
	}
}

// siwcForbiddenFields 是 D6 清单的 15 个字段与其注入值（逐字见展开层 §7.13）。
var siwcForbiddenFields = map[string]any{
	"background":             true,
	"conversation":           "conv_1",
	"max_output_tokens":      50,
	"max_tool_calls":         5,
	"metadata":               map[string]any{"user_id": "u1"},
	"moderation":             map[string]any{"mode": "auto"},
	"multi_agent":            true,
	"prompt":                 map[string]any{"id": "p1"},
	"prompt_cache_retention": "24h",
	"safety_identifier":      "user-1",
	"temperature":            0.7,
	"top_logprobs":           2,
	"top_p":                  0.9,
	"truncation":             "auto",
	"user":                   "user-1",
}

// TestSIWCEncodeDropsForbiddenFields：15 个字段逐个注入真机请求，逐个断言——出站体
// 没有它、siwc_field 档登记了它的字段名。
func TestSIWCEncodeDropsForbiddenFields(t *testing.T) {
	// 名单 parity：测试表是手抄的，必须与生产表 siwcFields + 两个一等字段
	// （max_output_tokens / temperature）逐字对齐——生产表加字段而测试没跟上时，
	// 丢弃方向的用例全照绿（出站体少了谁、名单少了谁，逐字段断言都测不出），
	// 靠这条当场红。
	for k := range siwcFields {
		if _, ok := siwcForbiddenFields[k]; !ok {
			t.Errorf("siwcFields 里的 %s 没进测试表——生产名单加了字段、用例没跟上", k)
		}
	}
	for k := range siwcForbiddenFields {
		if k == "max_output_tokens" || k == "temperature" {
			continue // 一等字段：canonical 里拦，不在 siwcFields 表里也丢
		}
		if !siwcFields[k] {
			t.Errorf("测试表里的 %s 不在 siwcFields——两份名单漂了", k)
		}
	}

	sample := siwcGoldenRequest(t)
	var base map[string]any
	if err := json.Unmarshal(sample, &base); err != nil {
		t.Fatal(err)
	}

	for field, value := range siwcForbiddenFields {
		t.Run(field, func(t *testing.T) {
			injected := make(map[string]any, len(base)+1)
			for k, v := range base {
				injected[k] = v
			}
			injected[field] = value
			raw, err := json.Marshal(injected)
			if err != nil {
				t.Fatal(err)
			}
			out, drops, _, err := siwcEncode(t, raw, false)
			if err != nil {
				t.Fatalf("带 %s 的请求编码失败: %v", field, err)
			}
			if _, present := out[field]; present {
				t.Errorf("出站体带了 D6 禁发字段 %s: %v", field, out[field])
			}
			if !drops.Has(DropSIWCField) {
				t.Fatalf("丢 %s 没登记 siwc_field 档, got %v", field, drops.Strings())
			}
			var sawName bool
			for _, n := range drops.Names(DropSIWCField) {
				if n == field {
					sawName = true
				}
			}
			if !sawName {
				t.Errorf("siwc_field 名单里没有 %s: %v", field, drops.Names(DropSIWCField))
			}
		})
	}
}

// TestSIWCEncodeDropsServerTools：托管工具（web_search 这类上游自带能力）照既有
// server_tool 档丢并记 type，订阅渠道出口不例外。
func TestSIWCEncodeDropsServerTools(t *testing.T) {
	sample := siwcGoldenRequest(t)
	var base map[string]any
	if err := json.Unmarshal(sample, &base); err != nil {
		t.Fatal(err)
	}
	base["tools"] = []any{
		map[string]any{"type": "function", "name": "Read", "parameters": map[string]any{"type": "object"}},
		map[string]any{"type": "web_search"},
	}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	out, drops, _, err := siwcEncode(t, raw, true)
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := out["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("出站 tools = %v, 期望只剩 function 那条", out["tools"])
	}
	if !drops.Has(DropServerTool) {
		t.Errorf("托管工具该记 server_tool 档, got %v", drops.Strings())
	}
	if got := drops.Names(DropServerTool); len(got) != 1 || got[0] != "web_search" {
		t.Errorf("server_tool 名单 = %v, 期望 [web_search]", got)
	}
}

// TestSIWCEncodeKeepsConvertibleFields：D6 清单**外**的可转字段照发——改写不等于
// 过杀。prompt_cache_key / reasoning.effort / function 工具声明与点名调用都要活着
// 到上游（真机请求即带 prompt_cache_key 时也 200，#205）。
func TestSIWCEncodeKeepsConvertibleFields(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","stream":true,` +
		`"prompt_cache_key":"sess-1","reasoning":{"effort":"medium"},` +
		`"tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}],` +
		`"tool_choice":"auto",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	out, drops, _, err := siwcEncode(t, body, true)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if out["prompt_cache_key"] != "sess-1" {
		t.Errorf("prompt_cache_key = %v, 期望原值直传", out["prompt_cache_key"])
	}
	reasoning, _ := out["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "medium" {
		t.Errorf("reasoning.effort = %v, 期望直传", out["reasoning"])
	}
	tools, _ := out["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("tools = %v, 期望 function 声明活着", out["tools"])
	}
	if out["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, 期望 auto", out["tool_choice"])
	}
	if drops.Has(DropSIWCField) {
		t.Errorf("D6 清单外的字段不该记 siwc_field: %v", drops.Strings())
	}
}

// TestSIWCEncodePreviousResponseIDRejection：previous_response_id 非空 400——订阅渠道
// supports_stateful_responses 强制否（reject-previous-response-id 的真机形态）。
// code 与 param 沿用 v0.88 那条闸的既定信号，文案换成订阅渠道的准确说法。
func TestSIWCEncodePreviousResponseIDRejection(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","previous_response_id":"resp_1",` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)

	_, _, _, err := siwcEncode(t, body, false)
	reqErr, ok := err.(*protocol.RequestError)
	if !ok {
		t.Fatalf("期望 *RequestError, got %T: %v", err, err)
	}
	if reqErr.Code != CodePreviousResponseNotFound || reqErr.Param != ParamPreviousResponseID {
		t.Errorf("code/param = %q/%q, 期望 previous_response_not_found/previous_response_id",
			reqErr.Code, reqErr.Param)
	}
	if !strings.Contains(reqErr.Message, "订阅渠道") {
		t.Errorf("文案该说订阅渠道不支持有状态续链, got %q", reqErr.Message)
	}

	// 对照：非订阅渠道的转换路径沿用 v0.88 的原文案。
	plain, err := NewCodec().DecodeRequest(body, false)
	if err == nil {
		t.Fatal("非订阅转换路径也该拒 previous_response_id")
	}
	plainErr, _ := err.(*protocol.RequestError)
	if plainErr == nil || !strings.Contains(plainErr.Message, "转换") {
		t.Errorf("非订阅文案该保留「转换路径」说法, got %v", err)
	}
	_ = plain
}

// TestSIWCEncodeFlipsStoreTrue：客户端明说 store:true 时强改成 false（D6：上游对
// store:true 直接 400，见 reject-store-true 证据），并记账——这是改写清单里唯一一处
// 「客户端明说了、我们改掉了」的静默改写。
func TestSIWCEncodeFlipsStoreTrue(t *testing.T) {
	sample := siwcGoldenRequest(t)
	var base map[string]any
	if err := json.Unmarshal(sample, &base); err != nil {
		t.Fatal(err)
	}
	base["store"] = true
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	out, drops, _, err := siwcEncode(t, raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := out["store"].(bool); v {
		t.Errorf("store = %v, 期望被强改成 false", out["store"])
	}
	if !drops.Has(DropSIWCField) {
		t.Errorf("store:true 被翻掉该记账, got %v", drops.Strings())
	}
	if got := drops.Names(DropSIWCField); len(got) != 1 || got[0] != "store" {
		t.Errorf("siwc_field 名单 = %v, 期望 [store]", got)
	}
}
