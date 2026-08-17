package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/taps"
)

// proxyRecorder 是 M0 的录制反代：转发到真实上游，把上游响应的原始字节落盘。
type proxyRecorder struct {
	baseURL    string
	credential string
	proto      protocol.Protocol
	out        *sink
	client     *http.Client
}

func newProxyRecorder(proto protocol.Protocol, out *sink) *proxyRecorder {
	r := &proxyRecorder{
		baseURL:    strings.TrimRight(os.Getenv("GOLDENREC_BASE_URL"), "/"),
		credential: os.Getenv("GOLDENREC_CREDENTIAL"),
		proto:      proto,
		out:        out,
		client: &http.Client{Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 120 * time.Second,
		}},
	}
	switch {
	case r.baseURL == "":
		log.Fatal("proxy 模式需要 GOLDENREC_BASE_URL")
	case r.credential == "":
		log.Fatal("proxy 模式需要 GOLDENREC_CREDENTIAL")
	}
	return r
}

func (r *proxyRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	reqBody, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "读请求体失败", http.StatusBadRequest)
		return
	}
	stream := streamFlag(reqBody)

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
	// 转发要透明。goldenrec 不是网关：网关按白名单丢头是刻意的（口径层，防客户端
	// 指纹外泄），录制反代反过来——上游该看到 harness 本来发的东西，否则录到的就不是
	// 真实链路。实测依据：某中转站按客户端类型卡 Anthropic 端点，Claude Code 直连
	// 200、穿过只转发 anthropic-beta 的 goldenrec 就 503，一个样本也采不到。
	//
	// 脱敏在**落盘**那一侧做（save 只留白名单头），两件事分开。
	copyHeaders(out.Header, req.Header)
	out.Header.Set("Content-Type", "application/json")
	if r.proto == protocol.Anthropic {
		out.Header.Set("x-api-key", r.credential)
		out.Header.Del("Authorization")
		if out.Header.Get("anthropic-version") == "" {
			out.Header.Set("anthropic-version", envOr("GOLDENREC_ANTHROPIC_VERSION", "2023-06-01"))
		}
	} else {
		out.Header.Set("Authorization", "Bearer "+r.credential)
		out.Header.Del("x-api-key")
	}

	resp, err := r.client.Do(out)
	if err != nil {
		// 不打印 err 本身：它内嵌 URL，而 URL 上可能带查询串形式的凭证。
		log.Printf("上游请求失败: %T", err)
		http.Error(w, "上游请求失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	tap := taps.New(r.proto, stream)
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

	// 一边转发一边留档。
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
			_, _ = tap.Write(buf[:n])
			if err := flushWrite(w, buf[:n]); err != nil {
				break
			}
		}
		if readErr != nil {
			break
		}
	}

	expect := tap.Summary()
	meta := sampleMeta{
		Direction: "upstream",
		Protocol:  string(r.proto),
		Stream:    stream,
		Endpoint:  req.URL.Path,
		Status:    resp.StatusCode,
		Headers:   recordedHeaderSet(req.Header),
		Expect:    &expect,
	}
	name := string(r.proto)
	if stream {
		name += "-stream"
	}
	if err := r.out.save(name, meta, reqBody, raw); err != nil {
		log.Printf("样本落盘失败: %v", err)
	}
}
