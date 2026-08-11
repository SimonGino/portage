// Command goldenrec records golden samples (展开层 §9). It is deliberately
// outside internal/: it exists to feed the test corpus, never to serve traffic.
//
// 两种模式，采的是两类样本：
//
// **proxy（默认，M0 起）** —— 录**上游响应**。转发到真实上游，把回来的原始字节落盘。
//
//	GOLDENREC_BASE_URL=https://api.anthropic.com \
//	GOLDENREC_PROTOCOL=anthropic \
//	GOLDENREC_CREDENTIAL=sk-ant-... \
//	go run ./cmd/goldenrec
//
//	ANTHROPIC_BASE_URL=http://127.0.0.1:8318 claude
//
// **inbound（M2 起）** —— 录 **harness 入站请求**。不碰上游、不要凭证，按脚本回预置
// 的假响应（stub），只为把 harness 驱动到下一轮——`A→CC` 真正难啃的输入是第二轮那个
// 带 tool_result 的请求体，而 harness 只有先收到过一个合法的 tool_use 响应才会发出它。
//
//	GOLDENREC_MODE=inbound \
//	GOLDENREC_PROTOCOL=anthropic \
//	GOLDENREC_STUBS=./testdata/goldenstub/anthropic-tool-round \
//	go run ./cmd/goldenrec
//
//	ANTHROPIC_BASE_URL=http://127.0.0.1:8318 claude
//
// stub 是道具不是样本：它手写、不保真、不进 golden 库。入库的只有 harness 发出来的
// request.json，那仍是 100% 真实字节。
//
// 两种模式落盘的样本都**必须人工过一遍**再进 testdata/golden/：去掉真实凭证与个人对话
// 内容，核对 meta.json，然后把 verified 改成 true。golden_test.go 会拒绝
// verified=false 的样本——这道人工关卡是刻意的。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
)

func main() {
	log.SetFlags(0)

	// Normalize 先折旧名：Valid 故意不收 v0.36 之前的 `openai_cc`，而采集脚本是
	// 手写的、不经过库迁移，这里不折就等于把已有的采集环境当场打死。
	proto := protocol.Normalize(protocol.Protocol(os.Getenv("GOLDENREC_PROTOCOL")))
	if !proto.Valid() {
		log.Fatalf("GOLDENREC_PROTOCOL=%q 不是 anthropic/openai/openai_responses 之一", proto)
	}
	out := &sink{dir: envOr("GOLDENREC_OUT", "./testdata/golden/raw")}
	if err := os.MkdirAll(out.dir, 0o755); err != nil {
		log.Fatal(err)
	}

	var handler http.Handler
	mode := envOr("GOLDENREC_MODE", "proxy")
	switch mode {
	case "proxy":
		handler = newProxyRecorder(proto, out)
	case "inbound":
		handler = newInboundRecorder(proto, out)
	default:
		log.Fatalf("GOLDENREC_MODE=%q 不是 proxy/inbound 之一", mode)
	}

	listen := envOr("GOLDENREC_LISTEN", "127.0.0.1:8318")
	log.Printf("goldenrec[%s] 监听 %s（协议 %s），样本落在 %s", mode, listen, proto, out.dir)
	log.Print("提醒：样本进 testdata/golden/ 前必须人工脱敏并核对。")
	srv := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 15 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// sampleMeta 是每个样本的元信息，也是 golden_test.go 读的那份。
type sampleMeta struct {
	// Direction 分开两类样本：upstream = 上游响应转录（M0 起，驱动 Tap 测试），
	// inbound = harness 入站请求转录（M2 起，驱动 codec 的解码侧）。
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	Stream    bool   `json:"stream"`
	Endpoint  string `json:"endpoint"`
	// Status 只对 upstream 样本有意义：入站样本的状态码来自 stub，是道具不是事实。
	Status int `json:"status,omitempty"`
	// Headers 只对 inbound 样本有意义，且只收白名单里那几个**影响转换语义**的头
	// （anthropic-beta 之类）。白名单而非黑名单：漏掉一个指纹头的代价是样本带着
	// 客户端身份进 git，而漏掉一个语义头只是转换时少个线索，两边不对等。
	Headers map[string]string `json:"headers,omitempty"`
	// Stub 记下本轮回了哪个假响应，只为让录制流程可复现；空表示没消耗 stub。
	Stub string `json:"stub,omitempty"`
	// Expect 由录制时的 Tap 预填，仅作人工核对的**草稿**：它来自被测代码本身，
	// 没经人核对就当期望值用，等于让实现给自己判卷。入站样本没有这一项——
	// 那边 Tap 看的是 stub 的字节，算出来的数是道具的数，不是任何事实。
	Expect *protocol.Summary `json:"expect,omitempty"`
	// Verified 必须由人改成 true：确认已脱敏、且 meta 与原始字节相符。
	Verified bool `json:"verified"`
}

// sink 是两种模式共用的落盘端：目录、序号、meta 写法全仓只有一份。
type sink struct {
	dir string
	seq atomic.Int64
}

// save 落一个样本。raw 为 nil 时不写 response.raw——入站模式那边响应是 stub，
// 留档只会让人误以为它是真实转录。
func (s *sink) save(name string, meta sampleMeta, reqBody, raw []byte) error {
	dir := filepath.Join(s.dir, fmt.Sprintf("%s-%03d", name, s.seq.Add(1)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"request.json": reqBody,
		"meta.json":    append(metaJSON, '\n'),
	}
	if raw != nil {
		files["response.raw"] = raw
	}
	for file, content := range files {
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o600); err != nil {
			return err
		}
	}
	log.Printf("已录制 %s（%s, stream=%v）", dir, meta.Direction, meta.Stream)
	return nil
}

// entryPath 是该协议的入站端点。入口协议由路径决定，不猜、不嗅探请求体
// （protocol.Endpoint 的口径），录制端跟着同一张表走。
func entryPath(p protocol.Protocol) string {
	switch p {
	case protocol.Anthropic:
		return protocol.EndpointMessages.Path
	case protocol.OpenAI:
		return protocol.EndpointChatCompletions.Path
	default:
		return protocol.EndpointResponses.Path
	}
}

func streamFlag(body []byte) bool {
	var head struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &head)
	return head.Stream
}

// skipOnForward 是转发时不照抄的入站头。
//
// 逐条都有理由，不是凑数：hop-by-hop 那几个按 RFC 就不该跨跳转发；Host 与
// Content-Length 由 net/http 自己按目标地址与实际 body 重算；**Accept-Encoding 是
// 个坑**——照抄过去上游会返回 gzip，而 Go 的 Transport 只在自己加了这个头时才自动解
// 压，于是 response.raw 里存的会是压缩字节，golden 当场作废。凭证头不在这里列，是
// 因为它们在调用方那边被无条件覆盖掉了。
var skipOnForward = map[string]bool{
	"host": true, "content-length": true, "accept-encoding": true,
	"connection": true, "keep-alive": true, "proxy-authenticate": true,
	"proxy-authorization": true, "te": true, "trailer": true,
	"transfer-encoding": true, "upgrade": true,
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if skipOnForward[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// recordedHeaderSet 从入站请求里挑出**该留档**的那几个头。落盘与转发是两回事：
// 转发要透明，落盘只留白名单——见 recordedHeaders 的理由。
func recordedHeaderSet(src http.Header) map[string]string {
	out := map[string]string{}
	for _, h := range recordedHeaders {
		if v := src.Get(h); v != "" {
			out[h] = v
		}
	}
	return out
}

// flushWrite 写一段字节并立刻 flush；不 flush 的话 harness 会以为流卡住了。
func flushWrite(w http.ResponseWriter, p []byte) error {
	if _, err := w.Write(p); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, s)
}
