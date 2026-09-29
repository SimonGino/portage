package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/exchange"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"

	"github.com/gin-gonic/gin"
)

// partialThenErrBody 一次 Read 吐出已到的字节，下一次 Read 报 err——即空闲超时
// 掐断之后 bufferConverted 看到的样子。真实 Client 挂住后冒出的原文经 Redact
// 恰为 "upstream idle timeout"，由 upstream 包
// TestIdleTimeoutRedactedTextOnBufferedRead 走真实 HTTP、注入阈值钉住（阈值是
// upstream 包私有字段，本包够不着）；这里只钉「读错误原文照落流水」这一半。
type partialThenErrBody struct {
	sent bool
	data []byte
	err  error
}

func (b *partialThenErrBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.data), nil
	}
	return 0, b.err
}

// TestBufferConvertedRecordsReadFailureDetail 钉的是 issue #163：非流式转换路径
// io.ReadAll(src) 读失败时，此前 rec.Failed(UpstreamError, "") 原文传空，#105 的
// upstream idle timeout 落这条路时流水里完全看不出是空闲超时还是普通断连。
//
// 只构造读失败分支要用到的那几样：inCodec/outCodec 在这条分支里从没被调用，
// 传 nil 就够，犯不上另起一整套 codec 去配齐。
func TestBufferConvertedRecordsReadFailureDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := calllog.New(protocol.EndpointMessages, s.log, nil)
	src := &partialThenErrBody{data: []byte(`{"partial`), err: errors.New("upstream idle timeout")}

	s.bufferConverted(c, rec, protocol.EndpointMessages, store.Candidate{ChannelName: "test-channel"}, nil, nil, src)

	if w.Code != http.StatusBadGateway {
		t.Errorf("状态码 = %d, 期望 502（读完前没发响应头）", w.Code)
	}
	row := rec.Row()
	if row.Error.String != string(calllog.UpstreamError) {
		t.Errorf("流水 error = %q, 期望 %q", row.Error.String, calllog.UpstreamError)
	}
	if !row.ErrorDetail.Valid || row.ErrorDetail.String != "upstream idle timeout" {
		t.Errorf("error_detail = %v, 期望 %q", row.ErrorDetail, "upstream idle timeout")
	}
}

// decodeStreamErrCodec 只用来让 outCodec.DecodeStream 报错：现有三家 codec 的
// DecodeStream 都不返错（读失败走带内 EvError），这一支只剩构造期失败，真 codec
// 造不出来。streamConverted 在这一支里从没调用别的方法，其余方法由嵌入的零值
// 接口兜底——将来误调会当场 nil panic、栈指向调用点，不会静默走错。
type decodeStreamErrCodec struct {
	protocol.Codec
	err error
}

func (c *decodeStreamErrCodec) DecodeStream(io.Reader) (<-chan protocol.Event, error) {
	return nil, c.err
}

// TestStreamConvertedRecordsDecodeFailureDetail 钉的是 issue #170：streamConverted
// 开头 outCodec.DecodeStream 解码失败时，此前 rec.Failed(UpstreamError, "") 原文
// 传空、日志 err 也没过 upstream.Redact——是 #163 修的 bufferConverted 读失败支
// 的兄弟路径，同一症状。
func TestStreamConvertedRecordsDecodeFailureDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := calllog.New(protocol.EndpointMessages, s.log, nil)
	outCodec := &decodeStreamErrCodec{err: errors.New("解码器初始化失败")}
	res := &exchange.Result{ResponseObserver: &exchange.ResponseObserver{Body: strings.NewReader("")}}

	s.streamConverted(c, rec, protocol.EndpointMessages, store.Candidate{ChannelName: "test-channel"}, nil, outCodec, res)

	if w.Code != http.StatusBadGateway {
		t.Errorf("状态码 = %d, 期望 502（解码失败，还没发响应头）", w.Code)
	}
	row := rec.Row()
	if row.Error.String != string(calllog.UpstreamError) {
		t.Errorf("流水 error = %q, 期望 %q", row.Error.String, calllog.UpstreamError)
	}
	if !row.ErrorDetail.Valid || row.ErrorDetail.String != "解码器初始化失败" {
		t.Errorf("error_detail = %v, 期望 %q", row.ErrorDetail, "解码器初始化失败")
	}
}
