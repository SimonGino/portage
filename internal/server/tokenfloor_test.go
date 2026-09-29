package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/config"
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

// 重发撞上渠道并发闸（#149，PO 裁定「留着」）：第一次已经真的打到上游、拿到那个
// 400，重发再被闸拒的那一行，出站端点**非空**——「非空 ⟺ 打过上游」，闸拒那档
// 「一律空串」只是没打过的推论。retry_count 记那次重发。补回端点这件事由 exchange
// 自己做，convert.go 不再伸手改 Recorder。
func TestTokenFloorResendRejectedByGateKeepsUpstreamEndpoint(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	db := gatewaytest.NewDB(t)
	gatewaytest.SeedPassthrough(t, db, accessPointModel, "openai", up.URL, ccUpstreamModel, openaiCredential)
	gatewaytest.SetChannelConcurrency(t, db, 1, 1)
	gw := gatewaytest.StartWith(t, db, gatewaytest.Options{
		Queue: config.Queue{Factor: 1, Wait: 300 * time.Millisecond, RetryAfter: 10 * time.Second},
	})

	// 占坑请求：拿到坑就挂住，直到用例收场。
	hold := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(hold) }) })
	const blockerRequest = `{"model":"gw-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hold"}]}`
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		var q map[string]any
		_ = json.Unmarshal(up.Last(t).Body, &q)
		w.Header().Set("Content-Type", "application/json")
		if n, _ := q["max_tokens"].(float64); n > 2 {
			<-hold
			_, _ = w.Write([]byte(`{"id":"c2","object":"chat.completion","model":"m","choices":[]}`))
			return
		}
		// 第一次（max_tokens=1）：先让占坑请求排进闸的队列，再回 400 让网关重发。
		// 坑一放，闸把它移交给排队的那位；重发只能排队，300ms 后超时被拒。
		_ = postAsync(gw, context.Background(), blockerRequest)
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(tokenFloorError))
	}

	resp := gw.Post(t, "/v1/messages", tinyMaxTokensRequest, nil)
	gatewaytest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, 期望重发被闸拒 429", resp.StatusCode)
	}
	row := gw.LastCallRow(t)
	if row.Error.String != "queue_timeout" {
		t.Errorf("error = %q, 期望 queue_timeout", row.Error.String)
	}
	if row.RetryCount != 1 {
		t.Errorf("retry_count = %d, 期望 1（那次重发）", row.RetryCount)
	}
	if row.UpstreamEndpoint == "" {
		t.Errorf("出站端点被清空了：第一次已经打到上游，这一格该留着")
	}
	once.Do(func() { close(hold) })
}
