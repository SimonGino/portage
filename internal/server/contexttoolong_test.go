package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 转换路径上上游说「上下文超长」，要按入口协议的原话回：Claude Code 认 message 以
// prompt is too long 开头才压缩，Codex 认 code context_length_exceeded。

func TestContextTooLongSaidAnthropicWay(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(http.StatusBadRequest, map[string]string{"Content-Type": "application/json"},
		`{"error":{"message":"This model's maximum context length is 131072 tokens.","type":"invalid_request_error"}}`)

	resp := gw.Post(t, "/v1/messages", anthropicRequest, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, 期望 400", resp.StatusCode)
	}
	var env struct {
		Error struct{ Type, Message string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Type != "invalid_request_error" || !strings.HasPrefix(env.Error.Message, "prompt is too long: This model's") {
		t.Errorf("错误体 = %s", body)
	}
}

func TestContextTooLongSaidOpenAIWay(t *testing.T) {
	gw, up := newResponsesConvertGateway(t)
	// 413 也认，且改回 400：Codex 把 413 当传输失败原样重发。
	up.RespondWith(http.StatusRequestEntityTooLarge, map[string]string{"Content-Type": "application/json"},
		`{"error":{"message":"Input exceeds the context limit (1048568 tokens)"}}`)

	resp := gw.Post(t, "/v1/responses", plainResponsesRequest, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, 期望 400", resp.StatusCode)
	}
	var env struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "context_length_exceeded" || env.Error.Message != "Input exceeds the context limit (1048568 tokens)" {
		t.Errorf("错误体 = %s", body)
	}
}
