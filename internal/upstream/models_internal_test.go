package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
)

// modelsUpstream 起一个只认 /v1/models 的假上游，把每次请求的 Authorization/x-api-key
// 记下来，回一份带 name 的单模型列表——name 用来断言「哪个协议拉到的是哪台的答案」。
func modelsUpstream(t *testing.T, name string, hits *[]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": name}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// 各协议打各的出站根地址（#49）：此前 ListModelsFor 收单个 baseURL，回退序第一个
// 地址会被打给全部协议——openai/anthropic 双协议渠道的 anthropic 列表拉到 openai
// 的根上，且无任何用例覆盖。
func TestListModelsForUsesPerProtocolBaseURL(t *testing.T) {
	var hitsA, hitsB []string
	urlA := modelsUpstream(t, "gpt-x", &hitsA)
	urlB := modelsUpstream(t, "claude-x", &hitsB)

	out := ListModelsFor(context.Background(), store.BaseURLs{OpenAI: urlA, Anthropic: urlB}, "", "sk-x")

	if len(hitsA) != 1 || len(hitsB) != 1 {
		t.Fatalf("两个根各该收到一次拉取，实得 openai 根 %d 次、anthropic 根 %d 次", len(hitsA), len(hitsB))
	}
	got := map[string]string{}
	for _, r := range out {
		for _, p := range r.Protocols {
			if len(r.Models) == 1 {
				got[string(p)] = r.Models[0]
			}
		}
	}
	if got["openai"] != "gpt-x" || got["anthropic"] != "claude-x" {
		t.Errorf("列表串了根：%v", got)
	}
}

// openai 与 openai_responses 共用一次拉取的前提是**同地址**：同根时只打一趟、结果
// 对两者成立；v0.96 起两者可以各挂各的根，地址不同就是两份答案、各拉各的。
func TestListModelsForSharesFetchOnlyOnSameURL(t *testing.T) {
	t.Run("同根共享", func(t *testing.T) {
		var hits []string
		url := modelsUpstream(t, "gpt-x", &hits)
		out := ListModelsFor(context.Background(),
			store.BaseURLs{OpenAI: url, OpenAIResponses: url}, "", "sk-x")
		if len(hits) != 1 {
			t.Fatalf("同根该只拉一次，实得 %d 次", len(hits))
		}
		if len(out) != 1 || len(out[0].Protocols) != 2 {
			t.Errorf("一份结果该同时盖住两个协议：%+v", out)
		}
	})
	t.Run("异根各拉各的", func(t *testing.T) {
		var hitsA, hitsB []string
		urlA := modelsUpstream(t, "gpt-x", &hitsA)
		urlB := modelsUpstream(t, "gpt-r", &hitsB)
		out := ListModelsFor(context.Background(),
			store.BaseURLs{OpenAI: urlA, OpenAIResponses: urlB}, "", "sk-x")
		if len(hitsA) != 1 || len(hitsB) != 1 {
			t.Fatalf("异根各该拉一次，实得 %d/%d 次", len(hitsA), len(hitsB))
		}
		if len(out) != 2 {
			t.Fatalf("异根该出两份结果：%+v", out)
		}
		for _, r := range out {
			if len(r.Protocols) != 1 {
				t.Errorf("异根的结果不该跨协议共享：%+v", r)
			}
		}
	})
}

// 结果的 Protocols 收窄到渠道真声明了的协议：只声明 openai_responses 时，说这份
// 列表对 openai 也成立会让表单勾出一个渠道根本不支持的协议（既有语义，防回归）。
func TestListModelsForNarrowsProtocolsToDeclared(t *testing.T) {
	var hits []string
	url := modelsUpstream(t, "gpt-x", &hits)
	out := ListModelsFor(context.Background(), store.BaseURLs{OpenAIResponses: url}, "", "sk-x")
	if len(out) != 1 {
		t.Fatalf("该出一份结果：%+v", out)
	}
	if len(out[0].Protocols) != 1 || out[0].Protocols[0] != protocol.OpenAIResponses {
		t.Errorf("Protocols 该只剩声明过的 openai_responses：%+v", out[0].Protocols)
	}
}
