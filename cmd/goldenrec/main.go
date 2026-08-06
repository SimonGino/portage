// Command goldenrec is a one-off recording reverse proxy for collecting golden
// samples (展开层 §9). It is deliberately outside internal/: it exists to feed
// the test corpus, never to serve traffic.
//
// 用法：把 harness 指到它，它转发到真实上游并把每次调用的原始字节落盘。
//
//	GOLDENREC_BASE_URL=https://api.anthropic.com \
//	GOLDENREC_PROTOCOL=anthropic \
//	GOLDENREC_CREDENTIAL=sk-ant-... \
//	go run ./cmd/goldenrec
//
//	ANTHROPIC_BASE_URL=http://127.0.0.1:8318 claude
//
// 落盘的样本**必须人工过一遍**再进 testdata/golden/：去掉真实凭证与个人对话内容，
// 核对 meta.json 里的 expect 与原始字节一致，然后把 verified 改成 true。
// golden_test.go 会拒绝 verified=false 的样本——这道人工关卡是刻意的。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SimonGino/ai-gateway/internal/protocol"
	"github.com/SimonGino/ai-gateway/internal/protocol/taps"
)

type recorder struct {
	baseURL    string
	credential string
	proto      protocol.Protocol
	outDir     string
	client     *http.Client
	seq        atomic.Int64
}

// sampleMeta 是每个样本的元信息，也是 golden_test.go 读的那份。
type sampleMeta struct {
	Protocol string `json:"protocol"`
	Stream   bool   `json:"stream"`
	Endpoint string `json:"endpoint"`
	Status   int    `json:"status"`
	// Expect 由录制时的 Tap 预填，仅作人工核对的**草稿**：它来自被测代码本身，
	// 没经人核对就当期望值用，等于让实现给自己判卷。
	Expect protocol.Summary `json:"expect"`
	// Verified 必须由人改成 true：确认已脱敏、且 expect 与原始字节相符。
	Verified bool `json:"verified"`
}

func main() {
	log.SetFlags(0)
	r := &recorder{
		baseURL:    strings.TrimRight(os.Getenv("GOLDENREC_BASE_URL"), "/"),
		credential: os.Getenv("GOLDENREC_CREDENTIAL"),
		proto:      protocol.Protocol(os.Getenv("GOLDENREC_PROTOCOL")),
		outDir:     envOr("GOLDENREC_OUT", "./testdata/golden/raw"),
		client: &http.Client{Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 120 * time.Second,
		}},
	}
	switch {
	case r.baseURL == "":
		log.Fatal("需要 GOLDENREC_BASE_URL")
	case r.credential == "":
		log.Fatal("需要 GOLDENREC_CREDENTIAL")
	case !r.proto.Valid():
		log.Fatalf("GOLDENREC_PROTOCOL=%q 不是 anthropic/openai_cc/openai_responses 之一", r.proto)
	}
	if err := os.MkdirAll(r.outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	listen := envOr("GOLDENREC_LISTEN", "127.0.0.1:8318")
	log.Printf("goldenrec 监听 %s → %s（协议 %s），样本落在 %s", listen, r.baseURL, r.proto, r.outDir)
	log.Print("提醒：样本进 testdata/golden/ 前必须人工脱敏并核对 expect。")
	srv := &http.Server{Addr: listen, Handler: r, ReadHeaderTimeout: 15 * time.Second}
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

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	reqBody, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "读请求体失败", http.StatusBadRequest)
		return
	}
	var head struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(reqBody, &head)

	// 带上查询串：部分兼容端点（Azure 的 api-version 之类）把参数放在 URL 上。
	target := r.baseURL + req.URL.Path
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	out, err := http.NewRequestWithContext(req.Context(), req.Method, target, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "构造上游请求失败", http.StatusBadGateway)
		return
	}
	out.Header.Set("Content-Type", "application/json")
	if r.proto == protocol.Anthropic {
		out.Header.Set("x-api-key", r.credential)
		out.Header.Set("anthropic-version", envOr("GOLDENREC_ANTHROPIC_VERSION", "2023-06-01"))
		if v := req.Header.Get("anthropic-beta"); v != "" {
			out.Header.Set("anthropic-beta", v)
		}
	} else {
		out.Header.Set("Authorization", "Bearer "+r.credential)
	}

	resp, err := r.client.Do(out)
	if err != nil {
		// 不打印 err 本身：它内嵌 URL，而 URL 上可能带查询串形式的凭证。
		log.Printf("上游请求失败: %T", err)
		http.Error(w, "上游请求失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	tap := taps.New(r.proto, head.Stream)
	var raw []byte
	for k, vs := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// 一边转发一边留档；不 flush 的话 harness 会以为流卡住了。
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
			_, _ = tap.Write(buf[:n])
			if _, err := w.Write(buf[:n]); err != nil {
				break
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}

	meta := sampleMeta{
		Protocol: string(r.proto),
		Stream:   head.Stream,
		Endpoint: req.URL.Path,
		Status:   resp.StatusCode,
		Expect:   tap.Summary(),
	}
	if err := r.save(reqBody, raw, meta); err != nil {
		log.Printf("样本落盘失败: %v", err)
	}
}

func (r *recorder) save(reqBody, raw []byte, meta sampleMeta) error {
	name := fmt.Sprintf("%s-%03d", meta.Protocol, r.seq.Add(1))
	if meta.Stream {
		name += "-stream"
	}
	dir := filepath.Join(r.outDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	for file, content := range map[string][]byte{
		"request.json": reqBody,
		"response.raw": raw,
		"meta.json":    append(metaJSON, '\n'),
	} {
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o600); err != nil {
			return err
		}
	}
	log.Printf("已录制 %s（status=%d, stream=%v）", dir, meta.Status, meta.Stream)
	return nil
}
