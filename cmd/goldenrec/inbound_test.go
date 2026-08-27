package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

func newTestRecorder(t *testing.T, proto protocol.Protocol, stubs ...stub) (*inboundRecorder, string) {
	t.Helper()
	dir := t.TempDir()
	return &inboundRecorder{
		proto:  proto,
		out:    &sink{dir: dir},
		script: &stubScript{stubs: stubs},
		entry:  entryPath(proto),
	}, dir
}

func post(t *testing.T, h http.Handler, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

type recorded struct {
	name    string
	meta    sampleMeta
	request []byte
}

func readSamples(t *testing.T, dir string) []recorded {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)

	out := make([]recorded, 0, len(names))
	for _, name := range names {
		metaRaw, err := os.ReadFile(filepath.Join(dir, name, "meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		var meta sampleMeta
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			t.Fatalf("%s 的 meta.json 解析失败: %v", name, err)
		}
		body, err := os.ReadFile(filepath.Join(dir, name, "request.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, name, "response.raw")); err == nil {
			t.Errorf("%s 落了 response.raw——入站样本的响应是 stub，留档会被误当成真实转录", name)
		}
		out = append(out, recorded{name: name, meta: meta, request: body})
	}
	return out
}

// TestInboundRecordsFullBody 钉住这个模式存在的理由：入站请求体逐字全录，不截断。
//
// gateway 那边 log_bodies 有 64 KiB 上限（排障日志该有的上限），而 Claude Code 带全套
// tool 定义与长上下文时轻易越过它。半截样本看起来还像回事，喂给 codec 才发现是坑，
// 所以这条边界要有测试压着。
func TestInboundRecordsFullBody(t *testing.T) {
	rec, dir := newTestRecorder(t, protocol.Anthropic, stub{name: "01.sse", body: []byte("event: x\ndata: {}\n\n"), stream: true})

	filler := strings.Repeat("超过六十四 KiB 的上下文。", 20000)
	body, err := json.Marshal(map[string]any{"model": "m", "stream": true, "system": filler})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 64<<10 {
		t.Fatalf("测试样本只有 %d 字节，没越过 64 KiB，压不住这条边界", len(body))
	}

	if w := post(t, rec, protocol.EndpointMessages.Path, body, nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d, 期望 200", w.Code)
	}
	samples := readSamples(t, dir)
	if len(samples) != 1 {
		t.Fatalf("落盘 %d 个样本，期望 1 个", len(samples))
	}
	if !bytes.Equal(samples[0].request, body) {
		t.Errorf("request.json 与入站字节不一致：录到 %d 字节，发出去 %d 字节",
			len(samples[0].request), len(body))
	}
}

// TestInboundServesStubsInOrder 覆盖工具整轮的核心：第二轮请求必须拿到第二个 stub。
func TestInboundServesStubsInOrder(t *testing.T) {
	first := []byte("event: a\ndata: {\"n\":1}\n\nevent: b\ndata: {\"n\":2}\n\n")
	second := []byte("event: c\ndata: {\"n\":3}\n\n")
	rec, dir := newTestRecorder(t, protocol.Anthropic,
		stub{name: "01-tool_use.sse", body: first, stream: true},
		stub{name: "02-final.sse", body: second, stream: true},
	)

	body := []byte(`{"model":"m","stream":true}`)
	w1 := post(t, rec, protocol.EndpointMessages.Path, body, nil)
	w2 := post(t, rec, protocol.EndpointMessages.Path, body, nil)

	// 逐字保真：按帧切开发出去，拼回来必须与脚本文件一模一样。
	if got := w1.Body.Bytes(); !bytes.Equal(got, first) {
		t.Errorf("第一轮响应 = %q, 期望 %q", got, first)
	}
	if got := w2.Body.Bytes(); !bytes.Equal(got, second) {
		t.Errorf("第二轮响应 = %q, 期望 %q", got, second)
	}
	if ct := w1.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, 期望 text/event-stream", ct)
	}

	samples := readSamples(t, dir)
	if len(samples) != 2 {
		t.Fatalf("落盘 %d 个样本，期望 2 个", len(samples))
	}
	for i, want := range []string{"01-tool_use.sse", "02-final.sse"} {
		if samples[i].meta.Stub != want {
			t.Errorf("样本 %d 的 stub = %q, 期望 %q", i, samples[i].meta.Stub, want)
		}
		if samples[i].meta.Direction != "inbound" {
			t.Errorf("样本 %d 的 direction = %q, 期望 inbound", i, samples[i].meta.Direction)
		}
	}
}

// TestInboundCountTokensDoesNotConsumeStub 钉住串位这个坑：Claude Code 每轮都打
// count_tokens，它要是吃掉脚本里的一格，后面每一轮拿到的都是上一轮该拿的 stub。
func TestInboundCountTokensDoesNotConsumeStub(t *testing.T) {
	rec, dir := newTestRecorder(t, protocol.Anthropic,
		stub{name: "01-tool_use.sse", body: []byte("event: a\ndata: {}\n\n"), stream: true},
	)

	w := post(t, rec, protocol.EndpointCountTokens.Path, []byte(`{"model":"m","messages":[]}`), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("count_tokens status = %d, 期望 200", w.Code)
	}
	var counted struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &counted); err != nil {
		t.Fatalf("count_tokens 响应不是合法 JSON: %v", err)
	}
	if counted.InputTokens < 1 {
		t.Errorf("input_tokens = %d, 期望 ≥1", counted.InputTokens)
	}

	// 脚本没被动过：紧接着的正经请求应当拿到第一个 stub。
	w2 := post(t, rec, protocol.EndpointMessages.Path, []byte(`{"model":"m","stream":true}`), nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("messages status = %d, 期望 200——脚本多半被 count_tokens 吃掉了", w2.Code)
	}

	samples := readSamples(t, dir)
	if len(samples) != 2 {
		t.Fatalf("落盘 %d 个样本，期望 2 个（count_tokens 也要留档）", len(samples))
	}
	if samples[0].meta.Stub != "" {
		t.Errorf("count_tokens 样本的 stub = %q, 期望空", samples[0].meta.Stub)
	}
	if samples[1].meta.Stub != "01-tool_use.sse" {
		t.Errorf("messages 样本拿到的是 %q, 期望 01-tool_use.sse", samples[1].meta.Stub)
	}
}

// TestInboundHeaderWhitelist 钉住脱敏底线：凭证与客户端指纹绝不落盘，影响转换语义的
// 头才留档。
func TestInboundHeaderWhitelist(t *testing.T) {
	rec, dir := newTestRecorder(t, protocol.Anthropic,
		stub{name: "01.sse", body: []byte("event: a\ndata: {}\n\n"), stream: true},
	)

	post(t, rec, protocol.EndpointMessages.Path, []byte(`{"model":"m","stream":true}`), map[string]string{
		"x-api-key":             "sk-ant-secret",
		"authorization":         "Bearer sk-ptg-secret",
		"user-agent":            "claude-cli/1.2.3",
		"x-stainless-arch":      "arm64",
		"x-app":                 "cli",
		"anthropic-beta":        "prompt-caching-2024-07-31",
		"anthropic-version":     "2023-06-01",
		"x-installation-id-hdr": "8f3c",
	})

	samples := readSamples(t, dir)
	if len(samples) != 1 {
		t.Fatalf("落盘 %d 个样本，期望 1 个", len(samples))
	}
	got := samples[0].meta.Headers
	want := map[string]string{
		"anthropic-beta":    "prompt-caching-2024-07-31",
		"anthropic-version": "2023-06-01",
	}
	if len(got) != len(want) {
		t.Fatalf("留档的头 = %v, 期望 %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("头 %s = %q, 期望 %q", k, got[k], v)
		}
	}
	// meta.json 整份里都不该出现凭证或指纹。
	raw, err := json.Marshal(samples[0].meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"sk-ant-secret", "sk-ptg-secret", "claude-cli/1.2.3", "arm64", "8f3c"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("meta.json 里泄漏了 %q: %s", leak, raw)
		}
	}
}

// TestInboundExhaustedScriptStillRecords 脚本发完时请求字节仍要落盘——它才是这个模式
// 要的东西，不能因为没道具可发就一起丢了。
func TestInboundExhaustedScriptStillRecords(t *testing.T) {
	rec, dir := newTestRecorder(t, protocol.Anthropic,
		stub{name: "01.sse", body: []byte("event: a\ndata: {}\n\n"), stream: true},
	)

	body := []byte(`{"model":"m","stream":true}`)
	post(t, rec, protocol.EndpointMessages.Path, body, nil)
	w := post(t, rec, protocol.EndpointMessages.Path, body, nil)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("脚本发完后 status = %d, 期望 503（不静默重放）", w.Code)
	}
	samples := readSamples(t, dir)
	if len(samples) != 2 {
		t.Fatalf("落盘 %d 个样本，期望 2 个——脚本发完那一轮的请求体不能丢", len(samples))
	}
	if !bytes.Equal(samples[1].request, body) {
		t.Error("脚本发完那一轮的 request.json 与入站字节不一致")
	}
}

// TestInboundUnknownEndpointKeepsScriptIntact 没预料到的端点不能悄悄吃掉一格，否则
// 后面每一轮都串位，而串位在样本里是看不出来的。
func TestInboundUnknownEndpointKeepsScriptIntact(t *testing.T) {
	rec, dir := newTestRecorder(t, protocol.Anthropic,
		stub{name: "01.sse", body: []byte("event: a\ndata: {}\n\n"), stream: true},
	)

	w := post(t, rec, "/v1/organizations/whoami", []byte(`{}`), nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("未知端点 status = %d, 期望 404", w.Code)
	}
	if got := readSamples(t, dir); len(got) != 0 {
		t.Errorf("未知端点落了 %d 个样本，期望 0 个", len(got))
	}

	w2 := post(t, rec, protocol.EndpointMessages.Path, []byte(`{"model":"m","stream":true}`), nil)
	if w2.Code != http.StatusOK {
		t.Errorf("status = %d, 期望 200——脚本被未知端点吃掉了一格", w2.Code)
	}
}

// TestInboundNonStreamStub 非流式脚本按 application/json 回，不套 SSE 那层。
func TestInboundNonStreamStub(t *testing.T) {
	payload := []byte(`{"type":"message","role":"assistant"}`)
	rec, _ := newTestRecorder(t, protocol.OpenAI, stub{name: "01.json", body: payload})

	w := post(t, rec, protocol.EndpointChatCompletions.Path, []byte(`{"model":"m"}`), nil)
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, 期望 application/json", ct)
	}
	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Errorf("响应 = %q, 期望 %q", w.Body.Bytes(), payload)
	}
}

// TestLoadStubScript 排序按文件名，非脚本文件（README 之类）跳过。
func TestLoadStubScript(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"02-final.sse":    "second",
		"01-tool_use.sse": "first",
		"03-extra.json":   "third",
		"README.md":       "不是脚本",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	script, err := loadStubScript(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(script.stubs) != 3 {
		t.Fatalf("载入 %d 个 stub，期望 3 个（README.md 该被跳过）", len(script.stubs))
	}
	for i, want := range []struct {
		name   string
		stream bool
	}{{"01-tool_use.sse", true}, {"02-final.sse", true}, {"03-extra.json", false}} {
		if script.stubs[i].name != want.name || script.stubs[i].stream != want.stream {
			t.Errorf("stub %d = %+v, 期望 %v", i, script.stubs[i], want)
		}
	}
	if _, err := loadStubScript(t.TempDir()); err == nil {
		t.Error("空目录该报错——脚本目录写错了要立刻看见，不能等 harness 跑到一半")
	}
}

// TestShippedStubScripts 仓库里带的脚本得真能载入，且每一帧的 data 是合法 JSON——
// stub 不保真，但必须合法，否则 harness 当场就断。
func TestShippedStubScripts(t *testing.T) {
	root := "../../testdata/goldenstub"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			script, err := loadStubScript(filepath.Join(root, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range script.stubs {
				if !s.stream {
					var any map[string]any
					if err := json.Unmarshal(s.body, &any); err != nil {
						t.Errorf("%s 不是合法 JSON: %v", s.name, err)
					}
					continue
				}
				for _, frame := range bytes.Split(bytes.TrimRight(s.body, "\n"), []byte("\n\n")) {
					_, data := protocol.SSEFields(frame)
					if len(data) == 0 {
						t.Errorf("%s 有一帧没有 data 行: %q", s.name, frame)
						continue
					}
					// [DONE] 是 Chat Completions 流的收尾哨兵，按协议就不是 JSON
					// （真实转录 testdata/golden/cc-stream-* 也这么结尾）。放它过，
					// 而不是把整条断言放宽——别的帧仍然必须是合法 JSON。
					if string(data) == "[DONE]" {
						continue
					}
					var any map[string]any
					if err := json.Unmarshal(data, &any); err != nil {
						t.Errorf("%s 的一帧 data 不是合法 JSON: %v", s.name, err)
					}
				}
			}
		})
	}
}

// TestSideCallDoesNotConsumeStub 副业请求照录、照答，但不占脚本里的一格——串位是这个
// 模式最难查的失败：harness 会收到一个形状对而内容驴唇不对马嘴的回复，不报错。
func TestSideCallDoesNotConsumeStub(t *testing.T) {
	agentStub := stub{name: "01-final.sse", body: []byte("data: {\"choices\":[]}\n\n")}
	rec, dir := newTestRecorder(t, protocol.OpenAI, agentStub)
	rec.skipToolless = true

	// 先来一条没声明 tools 的（标题生成那种）。
	side := post(t, rec, protocol.EndpointChatCompletions.Path, []byte(`{"model":"m","stream":true}`), nil)
	if side.Code != http.StatusOK {
		t.Errorf("副业请求 status = %d, 期望 200", side.Code)
	}
	if body := side.Body.String(); !strings.Contains(body, "[DONE]") {
		t.Errorf("副业请求的回复不是一条完整的 CC 流: %q", body)
	}

	// 再来 agent 轮：它必须还能拿到那一格，否则就是被上面吃掉了。
	agent := post(t, rec, protocol.EndpointChatCompletions.Path,
		[]byte(`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"bash"}}]}`), nil)
	if !bytes.Equal(agent.Body.Bytes(), agentStub.body) {
		t.Errorf("agent 轮拿到 %q, 期望脚本原文 %q——那一格被副业请求吃了", agent.Body.Bytes(), agentStub.body)
	}

	// 两条都得落盘：副业请求也是 harness 发出来的真实入站字节。
	if got := readSamples(t, dir); len(got) != 2 {
		t.Errorf("落了 %d 个样本, 期望 2（副业 + agent 轮）", len(got))
	}
}

// TestSideCallOffByDefault 开关不开时，没 tools 的请求仍是正常的 agent 轮。
func TestSideCallOffByDefault(t *testing.T) {
	s := stub{name: "01-final.sse", body: []byte("data: {\"choices\":[]}\n\n")}
	rec, _ := newTestRecorder(t, protocol.OpenAI, s)

	w := post(t, rec, protocol.EndpointChatCompletions.Path, []byte(`{"model":"m","stream":true}`), nil)
	if !bytes.Equal(w.Body.Bytes(), s.body) {
		t.Errorf("默认配置下没 tools 的请求拿到 %q, 期望脚本原文——默认不该做副业判定", w.Body.Bytes())
	}
}
