package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
)

// ProbeChannel 的策略用例（#51）：检测作为领域操作在自己的 interface 上可测。
// 主缝 internal/server/probe_test.go 仍从管理 API 外面看整机行为；这里钉的是
// 策略本身——选择校验、fan-out 组装、保密规则。

// countingUpstream 记下收到的每一次请求；不校验内容的用例只拿它数请求。
type countingUpstream struct {
	mu   sync.Mutex
	got  []probedHit
	stat int
}

type probedHit struct {
	Path string
	Auth string
	Body string
}

func newCountingUpstream(t *testing.T, status int) (*countingUpstream, string) {
	t.Helper()
	cu := &countingUpstream{stat: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cu.mu.Lock()
		cu.got = append(cu.got, probedHit{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: string(body)})
		cu.mu.Unlock()
		w.WriteHeader(cu.stat)
	}))
	t.Cleanup(srv.Close)
	return cu, srv.URL
}

func (cu *countingUpstream) hits() []probedHit {
	cu.mu.Lock()
	defer cu.mu.Unlock()
	return append([]probedHit(nil), cu.got...)
}

func probeTarget(baseURLs store.BaseURLs) store.ProbeTarget {
	return store.ProbeTarget{
		Name:      "ch",
		BaseURLs:  baseURLs,
		Protocols: baseURLs.Protocols(),
		Credentials: []store.ProbeCredential{
			{ID: 1, Name: "主力", Value: "sk-live-secret"},
			{ID: 2, Name: "停用的", Value: "sk-dead-secret", Disabled: true},
		},
		Models: []string{"a-model", "b-model"},
	}
}

// 选择校验：参数不对整个拒掉、一个请求都不发（参数错误不该花钱），错误全归
// InvalidInput（管理端翻成 400），文案与旧 handler 逐字相同。
func TestProbeChannelRejectsBadSelection(t *testing.T) {
	cu, url := newCountingUpstream(t, http.StatusOK)
	target := probeTarget(store.BaseURLs{OpenAI: url})

	cases := map[string]struct {
		sel  ProbeSelection
		want string
	}{
		"协议一个没勾": {ProbeSelection{CredentialID: 1}, "至少勾一个协议"},
		"协议名不认识": {ProbeSelection{CredentialID: 1, Protocols: []string{"grpc"}},
			`协议 "grpc" 不是 anthropic/openai/openai_responses 之一`},
		"协议没声明": {ProbeSelection{CredentialID: 1, Protocols: []string{"anthropic"}},
			"渠道没有声明 anthropic（没填它的出站根地址），不能检测这一侧"},
		"凭证不存在": {ProbeSelection{CredentialID: 99, Protocols: []string{"openai"}},
			"这个渠道里没有这份凭证"},
		"模型不存在": {ProbeSelection{CredentialID: 1, Model: "nope", Protocols: []string{"openai"}},
			"这个渠道里没有这个启用中的纳管模型"},
	}
	for name, tc := range cases {
		_, err := ProbeChannel(context.Background(), target, tc.sel)
		if err == nil {
			t.Fatalf("%s：期望报错，得到 nil", name)
		}
		if !errors.Is(err, store.ErrInvalidInput) {
			t.Errorf("%s：错误该是 InvalidInput，得到 %T", name, err)
		}
		if err.Error() != tc.want {
			t.Errorf("%s：文案 = %q，期望 %q", name, err.Error(), tc.want)
		}
	}

	empty := target
	empty.Models = nil
	if _, err := ProbeChannel(context.Background(), empty,
		ProbeSelection{CredentialID: 1, Protocols: []string{"openai"}}); err == nil ||
		err.Error() != "这个渠道还没有启用中的纳管模型，先纳管再检测" {
		t.Errorf("没有纳管模型：错误 = %v", err)
	}

	if n := len(cu.hits()); n != 0 {
		t.Errorf("参数错误不该发请求，发了 %d 个", n)
	}
}

// fan-out 组装：模型 × 协议每格一个请求，各协议打各的根地址；矩阵行序 = 纳管模型
// 序、列序 = 勾选序（重复勾选去重）；已停用凭证可选中（v0.38 立论）。
func TestProbeChannelFanOut(t *testing.T) {
	cuOpenAI, urlOpenAI := newCountingUpstream(t, http.StatusOK)
	cuResp, urlResp := newCountingUpstream(t, http.StatusNotFound)
	target := probeTarget(store.BaseURLs{OpenAI: urlOpenAI, OpenAIResponses: urlResp})

	m, err := ProbeChannel(context.Background(), target, ProbeSelection{
		CredentialID: 2, // 停用的那把也能选
		Protocols:    []string{"openai_responses", "openai", "openai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Credential != "停用的" {
		t.Errorf("矩阵带的凭证名 = %q，期望 %q", m.Credential, "停用的")
	}
	if got := m.Protocols.String(); got != "openai_responses,openai" {
		t.Errorf("列序该按勾选序去重 = %q", got)
	}
	if len(m.Rows) != 2 || m.Rows[0].Model != "a-model" || m.Rows[1].Model != "b-model" {
		t.Fatalf("行序该按纳管模型序：%+v", m.Rows)
	}
	for _, row := range m.Rows {
		if len(row.Results) != 2 {
			t.Fatalf("每行该有 2 格：%+v", row)
		}
		if row.Results[0].Protocol != protocol.OpenAIResponses || row.Results[0].State != ProbeMissing {
			t.Errorf("%s 第 1 格 = %+v，期望 openai_responses/missing", row.Model, row.Results[0])
		}
		if row.Results[1].Protocol != protocol.OpenAI || row.Results[1].State != ProbeOK {
			t.Errorf("%s 第 2 格 = %+v，期望 openai/ok", row.Model, row.Results[1])
		}
	}
	if n := len(cuOpenAI.hits()); n != 2 {
		t.Errorf("openai 侧该收 2 个请求（2 模型 × 去重后 1 列），收了 %d", n)
	}
	if n := len(cuResp.hits()); n != 2 {
		t.Errorf("openai_responses 侧该收 2 个请求，收了 %d", n)
	}
	for _, h := range cuResp.hits() {
		if strings.Contains(h.Body, urlOpenAI) {
			t.Errorf("responses 侧请求体不该出现 openai 的地址：%s", h.Body)
		}
	}

	// 单选一个模型：只测它。
	cuOpenAI.mu.Lock()
	cuOpenAI.got = nil
	cuOpenAI.mu.Unlock()
	m, err = ProbeChannel(context.Background(), target, ProbeSelection{
		CredentialID: 1, Model: "b-model", Protocols: []string{"openai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Rows) != 1 || m.Rows[0].Model != "b-model" {
		t.Fatalf("单选模型该只出一行：%+v", m.Rows)
	}
	if n := len(cuOpenAI.hits()); n != 1 {
		t.Errorf("单选模型该只发 1 个请求，发了 %d", n)
	}
}

// 保密规则：矩阵序列化出去的每个字节都不带凭证值与 base_url——连不上的传输错误
// 里内嵌的 URL 也要被摘掉（与 call_logs.error 同一条纪律）。
//
// 已知边界（Redact 的现状，非本票范围）：url.Error 外壳被摘掉，但内层 net.OpError
// 的 `dial tcp ip:port` 仍带地址，这里只钉「完整 base_url 字符串不出现」。
func TestProbeChannelSecrecy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // 立刻关掉：让每格都拿到内嵌 URL 的传输错误

	target := probeTarget(store.BaseURLs{OpenAI: url})
	m, err := ProbeChannel(context.Background(), target, ProbeSelection{
		CredentialID: 1, Protocols: []string{"openai"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-live-secret") {
		t.Error("矩阵里出现了凭证值")
	}
	if strings.Contains(string(raw), url) {
		t.Errorf("矩阵里出现了 base_url：%s", raw)
	}
	if m.Credential != "主力" {
		t.Errorf("矩阵该带凭证名 %q，得到 %q", "主力", m.Credential)
	}
}
