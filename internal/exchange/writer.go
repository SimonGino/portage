package exchange

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/upstream"
)

const (
	// writeDeadline 是单次向客户端写出的上限，每写一块推进一次。它约束的是「客户端
	// 收得多慢」，不是「流总共多长」——所以长流不会被它掐断，挂死的慢客户端会。
	writeDeadline = 30 * time.Second

	// copyBufferSize 是透传的读写块大小。按字节块复制、永不按帧切分：透传路径对 SSE
	// 帧边界一无所知，因此并行工具调用那种远超缓冲区的大参数帧也不会被截断。
	copyBufferSize = 32 * 1024
)

// Writer 是给客户端写响应的纪律，连同这段路上的流水记账一起收在一处：
//
//   - WriteHeader 写出响应头，收场记成功——格式承诺在这一刻生效；
//   - 首次 Write 记首字节时刻（ttfb / ttft 的唯一来源）；
//   - 每写一块推进写超时，flush（透传逐块，转换按帧由编码器调）；
//   - 头发出之后任何一处断掉（写失败、上游读断、编码器出错）都经 Abort 记
//     stream_aborted，只认第一次。
//
// 「Succeeded 只在写头之后、之后再断只能是 stream_aborted」此前是 recorder 上一段
// 散文契约，靠三条 relay 各自手写维持（#52）。收进这里之后调用方拿不到 Succeeded 与
// StreamAborted 两个动词，想记错顺序也做不到。写盘纪律本身此前也写了三遍
// （relayBody / clientStream / bufferConverted），其中缓冲那份丢了 deadline——收成一份
// 之后按构造齐全。
type Writer struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	rec     *calllog.Recorder
	first   bool
	aborted bool
}

// NewWriter 造一个带写盘纪律与流水记账的客户端 writer。rec 不可为 nil：拿不到流水
// 的地方传 calllog.Detached()。
func NewWriter(w http.ResponseWriter, rec *calllog.Recorder) *Writer {
	return &Writer{w: w, rc: http.NewResponseController(w), rec: rec}
}

// WriteHeader 写出响应头，并把这次收场记成一次干净的成功。这一刻起格式承诺已经生效：
// 之后再断只能记 stream_aborted，不能退回别的词——所以 Succeeded 只有这里调。
//
// 顺带推进一次写超时，兜住响应头本身与空 body 的情形。推不动说明连接已经坏了，按
// 头已发出的口径记 stream_aborted 并把错误带回去，调用方据此断连。
func (cw *Writer) WriteHeader(status int) error {
	cw.w.WriteHeader(status)
	cw.rec.Succeeded()
	if err := cw.advance(); err != nil {
		cw.Abort(err)
		return err
	}
	return nil
}

// Write 写一块给客户端：首块记首字节时刻，每块推进写超时，写失败记 stream_aborted。
func (cw *Writer) Write(p []byte) (int, error) {
	if !cw.first {
		cw.first = true
		cw.rec.FirstByte()
	}
	if err := cw.advance(); err != nil {
		cw.Abort(err)
		return 0, err
	}
	n, err := cw.w.Write(p)
	if err != nil {
		cw.Abort(err)
	}
	return n, err
}

// Abort 记下「头发出之后这次没说完」：收场 stream_aborted，原文是脱敏后的错误本身
// （口径层 v0.53，与上游传输错误那一支同源）。只认第一次——Write 自己的失败已经记过，
// 编码器把同一个错误再带上来时不重记。
//
// 公开出来是给断在 Writer 之外的那两种情形用的：编码器出错（不一定是写失败），以及
// 上游读断带内传下来、EncodeStream 正常返回的那一支（StreamReadReporter）。
func (cw *Writer) Abort(err error) {
	if cw.aborted {
		return
	}
	cw.aborted = true
	detail := ""
	if err != nil {
		detail = upstream.Redact(err).Error()
	}
	cw.rec.Failed(calllog.StreamAborted, detail)
}

// Flush 满足 anthropic.Flusher：转换流式路径上每帧之后被编码器调一次。
func (cw *Writer) Flush() {
	// Flush 失败说明连接已经坏了，下一次 Write 会拿到同样的错误并把它带上来。
	// 这里不能返回错误（Flusher 没有返回值），静默是唯一选择。
	_ = cw.flush()
}

func (cw *Writer) flush() error {
	if err := cw.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// advance 把「这一次写出」的截止时间往后推。ErrNotSupported 说明底层 writer 不支持
// deadline（本项目的 gin ResponseWriter 支持），不该因此中断写出。
func (cw *Writer) advance() error {
	if err := cw.rc.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// Copy 把上游响应按字节块复制给客户端，每块 flush 一次（透传路径的主循环）。
// 上游读断与客户端写断都记 stream_aborted：头已经发出去了，两种断法对客户端是
// 同一回事。
//
// 不用 io.Copy：它不 flush，SSE 帧会攒在 net/http 的缓冲里，客户端要等攒满或流结束
// 才看得到——正是「逐字输出」失效的成因。也不用 bufio.Scanner 按行读再重组：那会引入
// 换行/空行的重写风险，且 Scanner 的 token 上限会变成透传路径的截断上限。
func (cw *Writer) Copy(body io.Reader) error {
	buf := make([]byte, copyBufferSize)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := cw.Write(buf[:n]); err != nil {
				return err
			}
			if err := cw.flush(); err != nil {
				cw.Abort(err)
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			cw.Abort(readErr)
			return readErr
		}
	}
}
