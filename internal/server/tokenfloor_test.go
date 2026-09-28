package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

const tinyMaxTokensRequest = `{"model":"gw-sonnet","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

const tokenFloorError = `{"error":{"message":"max_tokens must be greater than 2","type":"invalid_request_error"}}`

// 转换路径：上游嫌 max_tokens 太小，按它给的下限重发一次，客户端拿到的是 200。
func TestTokenFloorRetriedOnConvertedPath(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		last := up.Last(t)
		var q map[string]any
		_ = json.Unmarshal(last.Body, &q)
		w.Header().Set("Content-Type", "application/json")
		if n, _ := q["max_tokens"].(float64); n <= 2 {
			if n, _ := q["max_completion_tokens"].(float64); n <= 2 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tokenFloorError))
				return
			}
		}
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"ok"},"finish_reason":"length"}],` +
			`"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`))
	}

	resp := gw.Post(t, "/v1/messages", tinyMaxTokensRequest, nil)
	body := gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, 期望重发后 200：%s", resp.StatusCode, body)
	}
	if up.Count() != 2 {
		t.Errorf("上游收到 %d 次, 期望恰好 2 次（原请求 + 一次重发）", up.Count())
	}
	if row := gw.LastCallRow(t); row.RetryCount != 1 {
		t.Errorf("流水 retry_count = %d, 期望 1（那次重发）", row.RetryCount)
	}
}

// 下限重发之后仍被拒：不再重发第二次，原样把上游的 400 回给客户端。
func TestTokenFloorRetriedOnlyOnce(t *testing.T) {
	gw, up := newConvertGateway(t)
	up.RespondWith(http.StatusBadRequest, map[string]string{"Content-Type": "application/json"}, tokenFloorError)

	resp := gw.Post(t, "/v1/messages", tinyMaxTokensRequest, nil)
	gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || up.Count() != 2 {
		t.Errorf("status = %d, 上游收到 %d 次; 期望 400 且恰好 2 次", resp.StatusCode, up.Count())
	}
}

// 透传路径不改请求体（透传保真），上游的 400 原样回。
func TestTokenFloorNotRetriedOnPassthrough(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	up.RespondWith(http.StatusBadRequest, map[string]string{"Content-Type": "application/json"}, tokenFloorError)

	resp := gw.Post(t, "/v1/messages", tinyMaxTokensRequest, nil)
	gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || up.Count() != 1 {
		t.Errorf("status = %d, 上游收到 %d 次; 期望 400 且只 1 次", resp.StatusCode, up.Count())
	}
}
