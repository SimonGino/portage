package server_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

const streamRequest = `{"model":"gw-sonnet","max_tokens":64,"stream":true,` +
	`"messages":[{"role":"user","content":"hi"}]}`

// sseFrame 拼一个合法的 Anthropic SSE 帧。
func sseFrame(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// streamUpstream 让假上游写出首帧并 flush，然后卡住；调用返回的 release 才继续写完
// 剩下的帧。测试借此精确控制「上游写到哪了」。
//
// release 同时挂在 t.Cleanup 上兜底：测试中途 Fatal 时假上游若仍阻塞，
// httptest.Server 的 Close 会一直等它，测试进程会挂死而不是干净失败。
func streamUpstream(t *testing.T, up *gatewaytest.Upstream, frames ...string) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)

	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			panic("假上游的 ResponseWriter 不支持 Flush")
		}
		for i, frame := range frames {
			if i > 0 {
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(w, frame)
			flusher.Flush()
		}
	}
	return release
}

// 最容易悄悄回归的问题：网关用 io.Copy 之类不 flush 的写法，帧会攒在 net/http 的
// 缓冲里，客户端要等流结束才一次性看到。这里在上游还没写第二帧时就断言首帧已到客户端。
func TestStreamReachesClientBeforeUpstreamFinishes(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	first := sseFrame("message_start", `{"type":"message_start"}`)
	second := sseFrame("content_block_delta", `{"type":"content_block_delta"}`)
	release := streamUpstream(t, up, first, second)

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)

	got := gatewaytest.ReadSome(t, resp.Body, 2*time.Second)
	if !strings.Contains(got, "message_start") {
		t.Errorf("首帧内容不对: %q", got)
	}

	// 到这里上游第二帧还一个字节都没写——首帧确实是边到边发，而非攒完再发。
	release()
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读剩余流失败: %v", err)
	}
	if !strings.Contains(got+string(rest), "content_block_delta") {
		t.Error("第二帧没有到达客户端")
	}
}

// 这些样本刻意挑「按行读再重组」会改写的形态。只用 \n\n 帧是测不出来的——
// 拆成行再用 \n 拼回去，字节完全一样，一个 bufio.Scanner 实现能全绿通过。
func TestStreamBytesAreVerbatim(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	frames := []string{
		sseFrame("message_start", `{"type":"message_start","message":{"id":"msg_01"}}`),
		// SSE 允许 CRLF 行尾。bufio.ScanLines 会把 \r 吃掉，重组时补不回来。
		"event: content_block_delta\r\ndata: {\"delta\":{\"text\":\"你好\"}}\r\n\r\n",
		sseFrame("message_delta", `{"usage":{"output_tokens":3}}`),
		sseFrame("message_stop", `{"type":"message_stop"}`),
		// 收尾不带终止符：上游中途断连时就是这个形态。透传不该替它补换行，
		// 也不该把这段没读完的尾巴丢掉。
		"event: ping",
	}
	streamUpstream(t, up, frames...)()

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if want := strings.Join(frames, ""); body != want {
		t.Errorf("流式字节与上游写出的不一致\n上游: %q\n客户端: %q", want, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, 期望原样回传 text/event-stream", ct)
	}
}

// 并行工具调用的 JSON 参数单帧可以远超读写缓冲区。按字节块复制不认帧边界，
// 所以多大都只是多几轮循环；按行读再重组的实现会在这里截断。
func TestStreamHandlesFrameLargerThanBuffer(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	huge := sseFrame("content_block_delta", `{"partial_json":"`+strings.Repeat("A", 512*1024)+`"}`)
	streamUpstream(t, up, huge)()

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	body := gatewaytest.ReadBody(t, resp)

	if body != huge {
		t.Errorf("超大单帧被改动：期望 %d 字节，收到 %d 字节", len(huge), len(body))
	}
}

// 慢流不该被整体超时掐断——http.Client.Timeout 覆盖整个 body 读取周期，一旦有人
// 手滑设上它，长对话就会在中途断掉。
func TestStreamSurvivesLongSilence(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	release := streamUpstream(t, up, sseFrame("message_start", `{}`), sseFrame("message_stop", `{}`))

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	gatewaytest.ReadSome(t, resp.Body, 2*time.Second)

	time.Sleep(1500 * time.Millisecond) // 中途长时间无输出
	release()

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("静默后流被掐断: %v", err)
	}
	if !strings.Contains(string(rest), "message_stop") {
		t.Errorf("静默后的收尾帧没到: %q", rest)
	}
}

// 流式请求的头处理（§6.1）：Accept 缺省补 SSE，Accept-Encoding 显式 identity——
// 上游一压缩就会引入分块缓冲，首字延迟被拖长，逐字输出的观感就没了。
func TestStreamRequestHeaders(t *testing.T) {
	t.Run("客户端未给 Accept 时补 text/event-stream", func(t *testing.T) {
		gw, up := newAnthropicGateway(t)
		streamUpstream(t, up, sseFrame("message_stop", `{}`))()

		gw.Post(t, "/v1/messages", streamRequest, nil)

		got := up.Last(t)
		if v := got.Header.Get("Accept"); v != "text/event-stream" {
			t.Errorf("Accept = %q, 期望补 text/event-stream", v)
		}
		if v := got.Header.Get("Accept-Encoding"); v != "identity" {
			t.Errorf("Accept-Encoding = %q, 流式必须显式 identity", v)
		}
	})

	t.Run("客户端给了 Accept 就用它的", func(t *testing.T) {
		gw, up := newAnthropicGateway(t)
		streamUpstream(t, up, sseFrame("message_stop", `{}`))()

		gw.Post(t, "/v1/messages", streamRequest, map[string]string{"Accept": "application/json"})

		if v := up.Last(t).Header.Get("Accept"); v != "application/json" {
			t.Errorf("Accept = %q, 期望取客户端值", v)
		}
	})
}

// 首字节边界：一旦开始下行写流，格式承诺已生效。上游中途挂掉只能断连，
// 不能往流里追加错误对象（客户端已经在按 SSE 解析了），更不能换渠道重发。
func TestStreamFailureAfterFirstByteIsNotRewritten(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	first := sseFrame("message_start", `{"type":"message_start"}`)
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, first)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // 上游连接中途断掉
	}

	resp := gw.Post(t, "/v1/messages", streamRequest, nil)
	got, err := io.ReadAll(resp.Body)

	if err == nil {
		t.Error("上游中途断连，客户端却读到了正常的流结束")
	}
	if string(got) != first {
		t.Errorf("客户端收到的字节被改动了\n期望仅有首帧: %q\n实际: %q", first, got)
	}
	if up.Count() != 1 {
		t.Errorf("上游被调用 %d 次，首字节写出后不得重发", up.Count())
	}
}

// 客户端按 Esc 中断对话时，上游那条请求应当跟着断，而不是继续烧 token。
func TestClientCancellationPropagatesToUpstream(t *testing.T) {
	gw, up := newAnthropicGateway(t)
	upstreamCancelled := make(chan struct{})
	up.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseFrame("message_start", `{}`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(upstreamCancelled)
		case <-time.After(5 * time.Second):
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	resp := gw.PostCtx(t, ctx, "/v1/messages", streamRequest, nil)
	gatewaytest.ReadSome(t, resp.Body, 2*time.Second)
	cancel()

	select {
	case <-upstreamCancelled:
	case <-time.After(3 * time.Second):
		t.Error("客户端已取消，上游请求的 context 却没有被 cancel")
	}
}
