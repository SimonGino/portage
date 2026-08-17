package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/SimonGino/portage/internal/protocol"
)

// inboundBodyLimit 是入站请求体的上限。定得远高于任何真实发包，是因为这个模式存在的
// 唯一理由就是拿到**完整**入站字节：gateway 那边 log_bodies 有 64 KiB 截断（排障日志
// 该有的上限），而 Claude Code 带全套 tool 定义与长上下文时轻易越过它，截断的样本对
// codec 毫无用处。超限宁可整条报错，也不静默截半——半截样本看起来是合法 JSON 才是坑。
const inboundBodyLimit = 32 << 20

// recordedHeaders 是入站样本会留档的请求头白名单：只收**影响转换语义或上游档位**的
// 那几个。
//
// 白名单而非黑名单，因为两侧代价不对等：漏掉一个指纹头（user-agent、x-stainless-*、
// installation id 之类）意味着客户端身份跟着样本进 git，漏掉一个语义头只是转换时少条
// 线索。凭证头（authorization / x-api-key）在任何模式下都不落盘。
//
// `x-codex-beta-features` 收的是**档位**不是语义（#73）：Codex remote compaction 有
// v2 内联 trigger 与 legacy `POST /v1/responses/compact` 两档，wire 不同形，而中转按
// 这个头里有没有 `remote_compaction_v2` 分流（sub2api 的
// `isOpenAIRemoteCompactionV2Request`，不带就把路径改写去 `/compact`）。样本不记它，
// 将来对不上号时分不清这份转录出自哪一档。它不是指纹：值是特性名列表，与账号无关。
var recordedHeaders = []string{
	"anthropic-beta",
	"anthropic-version",
	"openai-beta",
	"x-codex-beta-features",
	"content-type",
}

// stub 是一段手写的假响应。它是道具不是样本：不保真、不进 golden 库，只负责让 harness
// 相信模型回了话、好继续走下一轮。
type stub struct {
	name string
	body []byte
	// stream 由扩展名决定：.sse 按 text/event-stream 回，.json 按 application/json 回。
	stream bool
}

// stubScript 按文件名顺序把 stub 一个个发出去。严格顺序、不循环：发完就报错收场，
// 因为静默重放会让 harness 原地打转，而「脚本不够长」正是该立刻看见的事。
type stubScript struct {
	stubs []stub
	next  atomic.Int64
}

func (s *stubScript) take() (stub, bool) {
	i := s.next.Add(1) - 1
	if i >= int64(len(s.stubs)) {
		return stub{}, false
	}
	return s.stubs[i], true
}

func loadStubScript(dir string) (*stubScript, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".sse") || strings.HasSuffix(e.Name(), ".json")) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("%s 里没有 .sse / .json 应答脚本", dir)
	}
	script := &stubScript{}
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		script.stubs = append(script.stubs, stub{
			name:   name,
			body:   body,
			stream: strings.HasSuffix(name, ".sse"),
		})
	}
	return script, nil
}

// inboundRecorder 录 harness 的入站请求，用脚本里的假响应把它驱动到下一轮。
type inboundRecorder struct {
	proto  protocol.Protocol
	out    *sink
	script *stubScript
	entry  string
	// skipToolless 打开后，没声明 tools 的请求算副业请求，照录但不消耗 stub。见 isSideCall。
	skipToolless bool
}

func newInboundRecorder(proto protocol.Protocol, out *sink) *inboundRecorder {
	dir := os.Getenv("GOLDENREC_STUBS")
	if dir == "" {
		log.Fatal("inbound 模式需要 GOLDENREC_STUBS 指向应答脚本目录（见 testdata/goldenstub/README.md）")
	}
	script, err := loadStubScript(dir)
	if err != nil {
		log.Fatalf("读应答脚本失败: %v", err)
	}
	names := make([]string, 0, len(script.stubs))
	for _, s := range script.stubs {
		names = append(names, s.name)
	}
	log.Printf("应答脚本 %s：%s", dir, strings.Join(names, " → "))

	skipToolless := os.Getenv("GOLDENREC_SIDECALL") == "notools"
	if skipToolless {
		log.Printf("GOLDENREC_SIDECALL=notools：没声明 tools 的请求当副业请求处理，照录但不消耗 stub")
	}
	return &inboundRecorder{
		proto: proto, out: out, script: script,
		entry: entryPath(proto), skipToolless: skipToolless,
	}
}

func (r *inboundRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.Method == http.MethodPost && req.URL.Path == r.entry:
		r.serveRelay(w, req)
	case req.Method == http.MethodPost && r.proto == protocol.Anthropic &&
		req.URL.Path == protocol.EndpointCountTokens.Path:
		r.serveCountTokens(w, req)
	default:
		// 回 404 而不是顺手发个 stub：没预料到的端点该看得见，别让它悄悄吃掉脚本
		// 里的一格，把后面几轮全串位。
		log.Printf("harness 打了没预料到的端点 %s %s——未消耗 stub", req.Method, req.URL.Path)
		r.proto.WriteError(w, http.StatusNotFound, "goldenrec 入站模式不认得这个端点")
	}
}

// readBody 全量读入站请求体。上限见 inboundBodyLimit：超限报错，绝不截断。
func (r *inboundRecorder) readBody(w http.ResponseWriter, req *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, inboundBodyLimit))
	if err != nil {
		log.Printf("读入站请求体失败（上限 %d 字节）: %v", inboundBodyLimit, err)
		r.proto.WriteError(w, http.StatusBadRequest, "读取请求体失败")
		return nil, false
	}
	return body, true
}

func (r *inboundRecorder) serveRelay(w http.ResponseWriter, req *http.Request) {
	body, ok := r.readBody(w, req)
	if !ok {
		return
	}
	stream := streamFlag(body)

	// 旁路调用不吃脚本里的一格。理由与 serveCountTokens 同一条：harness 除了 agent
	// 轮还会自己发几个副业请求（opencode 每开一个会话先发一条「给这段对话起个标题」），
	// 让它消耗一个 stub 会把后面几轮全串位——而串位的症状是 harness 收到一个形状对
	// 但内容驴唇不对马嘴的回复，比直接报错难查得多。
	if r.isSideCall(body) {
		r.record("in-"+string(r.proto)+"-sidecall", req, body, stream, "")
		r.writeSideCallReply(w, stream)
		return
	}

	next, hasStub := r.script.take()

	// 先落盘再应答：请求字节才是这个模式要的东西，脚本发完了也不能把它丢了。
	name := "in-" + string(r.proto)
	if stream {
		name += "-stream"
	}
	r.record(name, req, body, stream, next.name)

	if !hasStub {
		log.Printf("应答脚本已发完，这一轮只录不答——要再跑一遍就重启 goldenrec")
		r.proto.WriteError(w, http.StatusServiceUnavailable, "goldenrec 应答脚本已发完")
		return
	}
	if next.stream != stream {
		log.Printf("警告：本轮请求 stream=%v，但下一个 stub %s 是 stream=%v——harness 多半会报错",
			stream, next.name, next.stream)
	}
	r.writeStub(w, next)
}

// isSideCall 判断这是不是 harness 的副业请求（起标题、做摘要之类），而非 agent 轮。
//
// **默认关闭，由 GOLDENREC_SIDECALL=notools 打开。** 这是某个 harness 的癖性，不是协议
// 事实，所以不能变成所有人的默认行为——「没声明工具」的 CC 请求完全可以是一个正当的
// agent 轮（比如一个不带工具的纯对话 harness），默认吞掉它就等于采不到那种样本。
//
// 判据本身：agent 轮总要把自己那套工具发过来——opencode 即便被要求「别调工具」也照发
// 十个（`bash`/`read`/`grep`…）；而它的标题生成那条连 `tools` 带 `tool_choice` 一起没有。
//
// 判错的方向是安全的那侧：把 agent 轮误判成副业，症状是 harness 收到一句无意义的回复、
// 脚本一格没走，日志里那行一眼看得见；反过来漏判才是灾难——串位之后 harness 收到的是
// 形状对而内容驴唇不对马嘴的回复，比直接报错难查得多。
func (r *inboundRecorder) isSideCall(body []byte) bool {
	if !r.skipToolless {
		return false
	}
	var head struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &head); err != nil || len(head.Tools) > 0 {
		return false
	}
	log.Printf("判为副业请求（没声明 tools）：照录，但不消耗 stub")
	return true
}

// writeSideCallReply 回一句最短的合法响应。内容不重要：副业请求的回答只影响 harness
// 自己的显示（会话标题之类），与要采的 agent 轮字节无关。
func (r *inboundRecorder) writeSideCallReply(w http.ResponseWriter, stream bool) {
	const text = "goldenrec"
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-goldenrec-sidecall", "object": "chat.completion", "model": "goldenrec",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": text},
				"finish_reason": "stop",
			}},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	chunk := func(delta map[string]any, finish any) {
		payload, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-goldenrec-sidecall", "object": "chat.completion.chunk", "model": "goldenrec",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		_ = flushWrite(w, append(append([]byte("data: "), payload...), '\n', '\n'))
	}
	chunk(map[string]any{"role": "assistant", "content": text}, nil)
	chunk(map[string]any{}, "stop")
	_ = flushWrite(w, []byte("data: [DONE]\n\n"))
}

// serveCountTokens 就地估算，不消耗 stub。
//
// Claude Code 每轮都打这个端点，让它吃脚本里的一格会把后面几轮全串位；而它的返回值
// 只影响 harness 侧的上下文占比显示，估得准不准与要采的样本无关。
func (r *inboundRecorder) serveCountTokens(w http.ResponseWriter, req *http.Request) {
	body, ok := r.readBody(w, req)
	if !ok {
		return
	}
	r.record("in-"+string(r.proto)+"-counttokens", req, body, false, "")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]int{"input_tokens": max(len(body)/4, 1)})
}

func (r *inboundRecorder) record(name string, req *http.Request, body []byte, stream bool, stubName string) {
	meta := sampleMeta{
		Direction: "inbound",
		Protocol:  string(r.proto),
		Stream:    stream,
		Endpoint:  req.URL.Path,
		Headers:   recordedHeaderSet(req.Header),
		Stub:      stubName,
	}
	if err := r.out.save(sanitizeName(name), meta, body, nil); err != nil {
		log.Printf("样本落盘失败: %v", err)
	}
}

// writeStub 把假响应逐帧发出去。按 SSE 帧切并 flush，是为了让 harness 走的是真正的
// 增量路径而不是一次性收全——字节仍逐字保真：切出来的片段拼回去与原文件相同。
func (r *inboundRecorder) writeStub(w http.ResponseWriter, s stub) {
	if s.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(http.StatusOK)

	rest := s.body
	for s.stream {
		i := bytes.Index(rest, []byte("\n\n"))
		if i < 0 {
			break
		}
		if err := flushWrite(w, rest[:i+2]); err != nil {
			return
		}
		rest = rest[i+2:]
	}
	if len(rest) > 0 {
		_ = flushWrite(w, rest)
	}
}
