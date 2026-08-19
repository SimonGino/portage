package protocol_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// code/param 落进 OpenAI 系错误体（CC 与 Responses 共用同一个 error 对象）。
// 这两位是**契约**：客户端按 code 决定怎么降级，落错键或落丢了就等于回到只有文案可读。
func TestWriteRequestErrorCarriesCodeAndParam(t *testing.T) {
	for _, p := range []protocol.Protocol{protocol.OpenAI, protocol.OpenAIResponses} {
		w := httptest.NewRecorder()
		p.WriteRequestError(w, &protocol.RequestError{
			Message: "不要带 previous_response_id",
			Code:    "previous_response_not_found",
			Param:   "previous_response_id",
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: 状态码 = %d, 期望 400", p, w.Code)
		}
		var env struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: 错误体不是 JSON: %v", p, err)
		}
		for key, want := range map[string]any{
			"message": "不要带 previous_response_id",
			"type":    "invalid_request_error",
			"code":    "previous_response_not_found",
			"param":   "previous_response_id",
		} {
			if got := env.Error[key]; got != want {
				t.Errorf("%s: error.%s = %v, 期望 %v", p, key, got, want)
			}
		}
	}
}

// 没填的那两位落 JSON null，不落空串：这两键的官方形态就是「要么有值、要么 null」，
// 空串会让按真值判断的客户端把「没有 code」读成一个空 code。
func TestWriteErrorLeavesCodeAndParamNull(t *testing.T) {
	w := httptest.NewRecorder()
	protocol.OpenAI.WriteError(w, http.StatusBadGateway, "上游渠道请求失败")
	var env struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("错误体不是 JSON: %v", err)
	}
	for _, key := range []string{"code", "param"} {
		if got := string(env.Error[key]); got != "null" {
			t.Errorf("error.%s = %s, 期望 null", key, got)
		}
	}
}

// Anthropic 的 error 对象只有 type 与 message 两键，**不给它塞** code/param——那两个
// 键没有任何 Anthropic 客户端会读，凭空多出来只会让「回显与官方同形」不再成立。
func TestAnthropicErrorBodyHasNoCodeOrParam(t *testing.T) {
	w := httptest.NewRecorder()
	protocol.Anthropic.WriteRequestError(w, &protocol.RequestError{
		Message: "不要带 previous_response_id",
		Code:    "previous_response_not_found",
		Param:   "previous_response_id",
	})
	var env struct {
		Type  string         `json:"type"`
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("错误体不是 JSON: %v", err)
	}
	if env.Type != "error" || env.Error["type"] != "invalid_request_error" {
		t.Errorf("Anthropic 错误体不是原生形状: %s", w.Body.String())
	}
	for _, key := range []string{"code", "param"} {
		if _, ok := env.Error[key]; ok {
			t.Errorf("Anthropic 错误体多出了 %s 键: %s", key, w.Body.String())
		}
	}
}
