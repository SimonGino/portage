package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// 阈值钉成毫秒级而不是真等 300s（issue #105 的验收要求）：idleReadTimeout 是私有
// 字段，白盒测试直接改，同 do_internal_test.go 的 fastClient 那套写法。

// TestIdleTimeoutBodyFiresOnStall：响应头到了之后上游只发一次数据就挂住不动、
// 也不断连——没有这层空闲超时，Read 会一直阻塞到客户端自己走人。
func TestIdleTimeoutBodyFiresOnStall(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // 挂住，直到本次请求的 ctx 被空闲超时掐断
	})

	c := NewClient(RetryPolicy{})
	c.idleReadTimeout = 30 * time.Millisecond

	// 独立兜底超时：空闲超时一旦回归，挂住的 Read 由它掐断，断言照常报错而不是卡死整个测试。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, _, err := c.Do(ctx, streamReq(up.route("a")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 64)
	var readErr error
	for readErr == nil {
		_, readErr = resp.Body.Read(buf)
	}
	if !errors.Is(readErr, errUpstreamIdleTimeout) {
		t.Fatalf("读错误 = %v，期望 errUpstreamIdleTimeout", readErr)
	}
	if readErr.Error() != "upstream idle timeout" {
		t.Fatalf("原文 = %q，期望 %q（收场记账要能跟其他断流原文分开）", readErr.Error(), "upstream idle timeout")
	}
}

// TestIdleTimeoutBodySurvivesTrickle：只要两帧间隔没超过阈值，续期要生效——长
// thinking 场景静默几分钟不该被误伤。
func TestIdleTimeoutBodySurvivesTrickle(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for i := 0; i < 6; i++ {
			_, _ = w.Write([]byte("x"))
			flusher.Flush()
			time.Sleep(15 * time.Millisecond)
		}
	})

	c := NewClient(RetryPolicy{})
	c.idleReadTimeout = 60 * time.Millisecond // 总耗时(~90ms) > 阈值，但每帧间隔(~15ms) 远小于阈值

	resp, _, err := c.Do(context.Background(), streamReq(up.route("a")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("续期应保住这次读取，实际报错: %v", err)
	}
	if string(got) != "xxxxxx" {
		t.Fatalf("body = %q，期望 6 个 x", got)
	}
}

// streamReq 同 chatReq，但 Stream 置真：空闲超时看的是流式读。
func streamReq(rt Route) Request {
	r := chatReq(rt)
	r.Stream = true
	return r
}

// TestIdleTimeoutRedactedTextOnBufferedRead 钉 issue #163 验收值的真实来源：非流式
// 转换路径 bufferConverted 用 io.ReadAll 读这份 body，落库的是 Redact(err).Error()。
// 走真实 Client（部分 body 后挂住、阈值注入），证明那段原文恰为验收值——不带
// url.Error 外壳、不带上游地址。server 包的白盒用例只钉「读错误原文照落」那一半。
func TestIdleTimeoutRedactedTextOnBufferedRead(t *testing.T) {
	up := newFakeUpstream(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"partial`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})

	c := NewClient(RetryPolicy{})
	c.idleReadTimeout = 30 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, _, err := c.Do(ctx, chatReq(up.route("a")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	_, err = io.ReadAll(resp.Body)
	if got := Redact(err); got == nil || got.Error() != "upstream idle timeout" {
		t.Fatalf("Redact(ReadAll 错误) = %v，期望 %q", got, "upstream idle timeout")
	}
}
