package exchange

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
)

// 这一组用例钉的是 Writer 承接下来的那条收场契约（#52）：Succeeded 只在写头之后、
// 头发出之后再断只能是 stream_aborted、首字节只认第一次。此前这些是 recorder 上的
// 散文，靠三条 relay 各自手写维持；收进 Writer 之后在这里按构造验。

// writerHarness 是一条接了假出口的流水加一个假客户端。
type writerHarness struct {
	rec  *calllog.Recorder
	rows []calllog.Row
}

func newWriterHarness() *writerHarness {
	h := &writerHarness{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.rec = calllog.New(protocol.EndpointMessages, log, func(_ context.Context, row calllog.Row) error {
		h.rows = append(h.rows, row)
		return nil
	})
	return h
}

func (h *writerHarness) row(t *testing.T) calllog.Row {
	t.Helper()
	h.rec.Finish(200)
	if len(h.rows) != 1 {
		t.Fatalf("落库行数 = %d，期望 1", len(h.rows))
	}
	return h.rows[0]
}

// brokenClient 是一个写头之后就写不进去的客户端。
type brokenClient struct {
	*httptest.ResponseRecorder
	err error
}

func (b *brokenClient) Write([]byte) (int, error) { return 0, b.err }

// 没写头就收尾，收场词还是早退分支的缺省（rejected）：Succeeded 没有别的入口。
func TestWriterSucceededOnlyAfterHeader(t *testing.T) {
	h := newWriterHarness()
	NewWriter(httptest.NewRecorder(), h.rec)
	if got := h.row(t).Error; got.String != "rejected" {
		t.Errorf("未写头就收尾 error = %q，期望 rejected", got.String)
	}

	h = newWriterHarness()
	if err := NewWriter(httptest.NewRecorder(), h.rec).WriteHeader(http.StatusOK); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if got := h.row(t).Error; got.Valid {
		t.Errorf("写头之后收尾 error = %v，期望 NULL（ok）", got)
	}
}

// 头发出之后客户端写断：收场 stream_aborted，原文是脱敏后的错误本身；编码器把同一个
// 错误再带上来调 Abort 不重记、不覆盖。
func TestWriterClientBreakAfterHeaderIsStreamAborted(t *testing.T) {
	h := newWriterHarness()
	w := NewWriter(&brokenClient{httptest.NewRecorder(), errors.New("write tcp: broken pipe")}, h.rec)
	if err := w.WriteHeader(http.StatusOK); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := w.Write([]byte("data: x\n\n")); err == nil {
		t.Fatal("Write 期望失败")
	}
	w.Abort(errors.New("encoder: 第二次"))

	row := h.row(t)
	if row.Error.String != "stream_aborted" {
		t.Errorf("error = %q，期望 stream_aborted", row.Error.String)
	}
	if !strings.Contains(row.ErrorDetail.String, "broken pipe") || strings.Contains(row.ErrorDetail.String, "第二次") {
		t.Errorf("error_detail = %q，期望是第一次写失败的原文", row.ErrorDetail.String)
	}
}

// 透传主循环里上游读断也是 stream_aborted：头已经发出去了，对客户端是同一回事。
// 首字节在第一块写出时记下，ttft 因此落值。
func TestWriterCopyUpstreamBreakIsStreamAbortedWithFirstByte(t *testing.T) {
	h := newWriterHarness()
	h.rec.RequestParsed("m", true)
	w := NewWriter(httptest.NewRecorder(), h.rec)
	if err := w.WriteHeader(http.StatusOK); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	body := io.MultiReader(strings.NewReader("data: x\n\n"), &errReader{errors.New("upstream reset")})
	if err := w.Copy(body); err == nil {
		t.Fatal("Copy 期望失败")
	}

	row := h.row(t)
	if row.Error.String != "stream_aborted" {
		t.Errorf("error = %q，期望 stream_aborted", row.Error.String)
	}
	if !strings.Contains(row.ErrorDetail.String, "upstream reset") {
		t.Errorf("error_detail = %q，期望带上游读错原文", row.ErrorDetail.String)
	}
	if !row.TTFTMs.Valid {
		t.Error("ttft_ms 应落值：第一块已写出")
	}
}

// 干净走完：ok、ttft 落值。
func TestWriterCleanCopyIsOK(t *testing.T) {
	h := newWriterHarness()
	h.rec.RequestParsed("m", true)
	w := NewWriter(httptest.NewRecorder(), h.rec)
	if err := w.WriteHeader(http.StatusOK); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if err := w.Copy(strings.NewReader("data: x\n\ndata: y\n\n")); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	row := h.row(t)
	if row.Error.Valid {
		t.Errorf("error = %v，期望 NULL", row.Error)
	}
	if !row.TTFTMs.Valid {
		t.Error("ttft_ms 应落值")
	}
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }
