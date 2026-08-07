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

	"github.com/SimonGino/ai-gateway/internal/protocol"
)

// inboundBodyLimit 是入站请求体的上限。定得远高于任何真实发包，是因为这个模式存在的
// 唯一理由就是拿到**完整**入站字节：gateway 那边 log_bodies 有 64 KiB 截断（排障日志
// 该有的上限），而 Claude Code 带全套 tool 定义与长上下文时轻易越过它，截断的样本对
// codec 毫无用处。超限宁可整条报错，也不静默截半——半截样本看起来是合法 JSON 才是坑。
const inboundBodyLimit = 32 << 20

// recordedHeaders 是入站样本会留档的请求头白名单：只收**影响转换语义**的那几个。
//
// 白名单而非黑名单，因为两侧代价不对等：漏掉一个指纹头（user-agent、x-stainless-*、
// installation id 之类）意味着客户端身份跟着样本进 git，漏掉一个语义头只是转换时少条
// 线索。凭证头（authorization / x-api-key）在任何模式下都不落盘。
var recordedHeaders = []string{
	"anthropic-beta",
	"anthropic-version",
	"openai-beta",
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
	return &inboundRecorder{proto: proto, out: out, script: script, entry: entryPath(proto)}
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
