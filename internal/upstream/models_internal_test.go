package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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

	out := ListModelsFor(context.Background(), store.BaseURLs{OpenAI: urlA, Anthropic: urlB}, "", "sk-x", nil)

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
			store.BaseURLs{OpenAI: url, OpenAIResponses: url}, "", "sk-x", nil)
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
			store.BaseURLs{OpenAI: urlA, OpenAIResponses: urlB}, "", "sk-x", nil)
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
	out := ListModelsFor(context.Background(), store.BaseURLs{OpenAIResponses: url}, "", "sk-x", nil)
	if len(out) != 1 {
		t.Fatalf("该出一份结果：%+v", out)
	}
	if len(out[0].Protocols) != 1 || out[0].Protocols[0] != protocol.OpenAIResponses {
		t.Errorf("Protocols 该只剩声明过的 openai_responses：%+v", out[0].Protocols)
	}
}

// data[] 解析沿用旧义「只解第一个 JSON 值、容忍尾部多余字节」（#214 拆出
// parseDataModels 时钉的正是这条：Decoder 而不是 Unmarshal）——正文后跟多余字节
// 不该按形状错收场。
func TestListModelsToleratesTrailingBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-x"}]} 紧跟着的字节流不炸`))
	}))
	t.Cleanup(srv.Close)

	res := ListModels(context.Background(), srv.URL, protocol.OpenAI, "", "sk-x", nil)

	if len(res.Models) != 1 || res.Models[0] != "gpt-x" {
		t.Errorf("尾部多余字节不该把成功解析变形状错：%+v", res)
	}
}

// ── 订阅渠道的模型列表（#214，口径层 §2.2 v1.52）──────────────────────────

// golden models.json 里 visibility=="list" 的 slug，按文件里的顺序（hide 的
// gpt-reserve / gpt-5.5 / codex-auto-review 不进候选）。样本是真机转录，期望值
// 由此独立于实现推导——这正是 golden 的用途。
var goldenListedSlugs = []string{
	"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna",
	"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
}

// 订阅侧拉取带 Bearer access、只认官方 models[] 形状：golden models.json 整份
// 回出去，回来的候选恰好是 visibility=="list" 的那七个 slug。
func TestListSubscriptionModelsParsesGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..",
		"testdata", "golden", "responses-siwc-evidence", "models.json"))
	if err != nil {
		t.Fatalf("读 golden models.json: %v", err)
	}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)

	res := listSubscriptionModels(context.Background(), srv.URL, protocol.OpenAIResponses, "at-1")

	if auth != "Bearer at-1" {
		t.Errorf("上游收到的 Authorization = %q，期望只带 Bearer access", auth)
	}
	if res.Status != http.StatusOK {
		t.Fatalf("Status = %d，期望 200", res.Status)
	}
	if !slices.Equal(res.Models, goldenListedSlugs) {
		t.Errorf("候选 = %v，期望只留 visibility==list 的 slug：%v", res.Models, goldenListedSlugs)
	}
}

// 只认 models[] 一种形状：data[] 不回退去解——聚合中转那份写死的大列表在官方
// 直连上不存在，把别的形状混进来冒充目录比报「拉不到」更坏。
func TestListSubscriptionModelsRejectsDataShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-x"}]}`))
	}))
	t.Cleanup(srv.Close)

	res := listSubscriptionModels(context.Background(), srv.URL, protocol.OpenAI, "at-1")

	if res.Models != nil {
		t.Errorf("data[] 形状不该被当成目录：%v", res.Models)
	}
	if res.Detail != "上游返回的不是模型列表的形状" {
		t.Errorf("Detail = %q，期望报形状错", res.Detail)
	}
}

// 全 hide 或空 models[] 走既有「空列表」收场，不与形状错混淆。
func TestListSubscriptionModelsEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-reserve","visibility":"hide"}]}`))
	}))
	t.Cleanup(srv.Close)

	res := listSubscriptionModels(context.Background(), srv.URL, protocol.OpenAI, "at-1")

	if res.Models != nil || res.Detail != "上游回了一张空列表" {
		t.Errorf("全 hide 该按空列表收场：Models=%v Detail=%q", res.Models, res.Detail)
	}
}

// 分协议取地址、同址共享一次拉取的语义照旧（ListModelsFor 同一条秩序）：只声明
// openai_responses 时拉一次、Protocols 收窄到声明集。
func TestListSubscriptionModelsForNarrowsToDeclared(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6.1-sol","visibility":"list"}]}`))
	}))
	t.Cleanup(srv.Close)

	out := ListSubscriptionModelsFor(context.Background(),
		store.BaseURLs{OpenAIResponses: srv.URL}, "at-1")

	if len(hits) != 1 {
		t.Fatalf("该只拉一次，实得 %d 次", len(hits))
	}
	if len(out) != 1 || len(out[0].Protocols) != 1 || out[0].Protocols[0] != protocol.OpenAIResponses {
		t.Errorf("Protocols 该收窄到声明集：%+v", out)
	}
	if !slices.Equal(out[0].Models, []string{"gpt-6.1-sol"}) {
		t.Errorf("候选 = %v", out[0].Models)
	}
}
