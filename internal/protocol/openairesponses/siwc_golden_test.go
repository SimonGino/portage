package openairesponses

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件钉两件事（#213）：
//  1. 真机样本 responses-stream-siwc-text 的 response.raw 过解码侧——usage 在
//     response.completed、completed.output 是空数组、正文只在 delta（meta.json 的
//     source 记过这三条，解码不能依赖 completed.output）；
//  2. 流中 response.failed / 裸 error 帧的 error.code 由解码器与 Tap 透出（Event.Code /
//     Summary.ErrorCode），词表映射归记账票（#215），这里只断言「没丢」。
func siwcGoldenRaw(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(goldenDir, "responses-stream-siwc-text", "response.raw"))
	if err != nil {
		t.Skipf("样本尚未采集：%v", err)
	}
	return raw
}

// TestSIWCGoldenReplayDecodes：真机 SSE → canonical 事件。usage 出自 response.completed
// （input 16 / output 5），completed.output 是空数组——正文 "pong" 只能来自
// output_text.delta，解码若去读 completed.output 就会重复或丢正文。
func TestSIWCGoldenReplayDecodes(t *testing.T) {
	c := NewCodec()
	events := collectWith(t, c, siwcGoldenRaw(t))

	var texts []string
	var usage *protocol.Usage
	var done *protocol.Event
	for _, ev := range events {
		switch ev.Type {
		case protocol.EvTextDelta:
			texts = append(texts, ev.Text)
		case protocol.EvUsage:
			usage = ev.Usage
		case protocol.EvDone:
			e := ev
			done = &e
		}
	}
	if len(texts) != 1 || texts[0] != "pong" {
		t.Errorf("正文 = %q，期望恰好一条 \"pong\"（completed.output 为空数组，正文只在 delta）", texts)
	}
	if usage == nil {
		t.Fatal("没有 EvUsage——usage 在 response.completed 里")
	}
	// canonical Usage 是毛值直映：golden meta 的 expect 同源（input 16 / output 5）。
	if usage.InputTokens != 16 || usage.OutputTokens != 5 {
		t.Errorf("usage = %d/%d，期望 16/5", usage.InputTokens, usage.OutputTokens)
	}
	if done == nil {
		t.Fatal("没有 EvDone")
	}
	// canonical 把 Responses 的 status:"completed" 归一成 stop（protocol.Event 的
	// 词表；上游原话 "completed" 由 Tap 的 StopReason 保留——golden 回放那道闸钉过）。
	if done.StopReason != "stop" {
		t.Errorf("StopReason = %q，期望 stop（completed 的 canonical 取值）", done.StopReason)
	}
	if done.Truncated {
		t.Error("真机流收完不该记 Truncated")
	}
}

// siwcFailedStream 是流中 response.failed 的构造形态（error 对象带 code）。
const siwcFailedStream = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","model":"gpt-6.1-sol","status":"in_progress","output":[]}}

event: response.failed
data: {"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","object":"response","model":"gpt-6.1-sol","status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"You've hit your usage limit"},"output":[]}}

`

// TestDecodeStreamCarriesErrorCode：response.failed 的 error.code 进 EvError.Code
// （#213 的管道前半：事件透出；词表映射在记账票 #215）。
func TestDecodeStreamCarriesErrorCode(t *testing.T) {
	c := NewCodec()
	events := collectWith(t, c, []byte(siwcFailedStream))
	var saw *protocol.Event
	for _, ev := range events {
		if ev.Type == protocol.EvError {
			e := ev
			saw = &e
		}
	}
	if saw == nil {
		t.Fatal("没有 EvError")
	}
	if saw.Code != "subscription_sharing_usage_limit_exceeded" {
		t.Errorf("EvError.Code = %q，期望上游的 error.code 原样透出", saw.Code)
	}
	if saw.Message != "You've hit your usage limit" {
		t.Errorf("EvError.Message = %q，期望上游的 message", saw.Message)
	}
}

// TestDecodeFullBodyCarriesErrorCode：非流式形态（status:failed 的完整 response 对象）
// 同样透出 code——聚合路径收的是事件流，这一格与流式必须同源。
func TestDecodeFullBodyCarriesErrorCode(t *testing.T) {
	c := NewCodec()
	events, err := c.DecodeFullBody([]byte(`{"id":"resp_1","object":"response",` +
		`"model":"gpt-6.1-sol","status":"failed",` +
		`"error":{"code":"usage_unavailable","message":"try again"},"output":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == protocol.EvError {
			if ev.Code != "usage_unavailable" {
				t.Errorf("EvError.Code = %q，期望 usage_unavailable", ev.Code)
			}
			return
		}
	}
	t.Fatal("没有 EvError")
}

// TestTapStreamExtractsErrorCode：Tap 从原始字节里把 error.code 透出到 Summary.ErrorCode
// （管道的后半：记账层从 Summary 里取，词表映射归 #215）。裸 error 帧的 code 在顶层。
func TestTapStreamExtractsErrorCode(t *testing.T) {
	t.Run("response.failed", func(t *testing.T) {
		got := feed(t, NewTap(true), siwcFailedStream)
		if got.ErrorCode != "subscription_sharing_usage_limit_exceeded" {
			t.Errorf("Summary.ErrorCode = %q，期望 error.code 原样透出", got.ErrorCode)
		}
		if got.StopReason != "failed" {
			t.Errorf("StopReason = %q，期望 failed（既有行为，顺带钉住）", got.StopReason)
		}
	})
	t.Run("bare error frame", func(t *testing.T) {
		const raw = `event: error
data: {"type":"error","code":"server_error","message":"boom"}

`
		got := feed(t, NewTap(true), raw)
		if got.ErrorCode != "server_error" {
			t.Errorf("Summary.ErrorCode = %q，期望裸 error 帧顶层的 code", got.ErrorCode)
		}
	})
	t.Run("bare error frame nested", func(t *testing.T) {
		// 嵌套 error.code 是解码侧（decode_response.go）#162 起就认的形状；Tap 少认
		// 这一种会让该形态从 #215 的收场改判里漏掉——两半必须认同一套形状。
		const raw = `event: error
data: {"type":"error","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}

`
		got := feed(t, NewTap(true), raw)
		if got.ErrorCode != "subscription_sharing_usage_limit_exceeded" {
			t.Errorf("Summary.ErrorCode = %q，期望裸 error 帧嵌套 error.code 也透出", got.ErrorCode)
		}
	})
	t.Run("no error code stays empty", func(t *testing.T) {
		got := feed(t, NewTap(true), functionCallStream)
		if got.ErrorCode != "" {
			t.Errorf("Summary.ErrorCode = %q，期望空——正常收尾没有 error.code", got.ErrorCode)
		}
	})
}
