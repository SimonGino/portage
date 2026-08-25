package exchange

import (
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	// writeDeadline 是单次向客户端写出的上限，每写一块推进一次。它约束的是「客户端
	// 收得多慢」，不是「流总共多长」——所以长流不会被它掐断，挂死的慢客户端会。
	writeDeadline = 30 * time.Second

	// copyBufferSize 是透传的读写块大小。按字节块复制、永不按帧切分：透传路径对 SSE
	// 帧边界一无所知，因此并行工具调用那种远超缓冲区的大参数帧也不会被截断。
	copyBufferSize = 32 * 1024
)

// Writer 是给客户端写响应的纪律，三件事收在一处：首字节回调（ttfb 日志）、每写
// 一块推进写超时、flush（透传逐块，转换按帧由编码器调）。少任何一件，要么日志缺
// ttfb，要么慢客户端挂死连接，要么「逐字输出」变成攒完一次性吐出。此前它写了三遍
// （relayBody / clientStream / bufferConverted），其中缓冲那份丢了 deadline——收成
// 一份之后按构造齐全。
type Writer struct {
	w           http.ResponseWriter
	rc          *http.ResponseController
	onFirstByte func()
	first       bool
}

// NewWriter 造一个带写盘纪律的客户端 writer。onFirstByte 可为 nil（调用方自己管
// 首字节时刻时）。
func NewWriter(w http.ResponseWriter, onFirstByte func()) *Writer {
	return &Writer{w: w, rc: http.NewResponseController(w), onFirstByte: onFirstByte}
}

func (cw *Writer) Write(p []byte) (int, error) {
	if !cw.first {
		cw.first = true
		if cw.onFirstByte != nil {
			cw.onFirstByte()
		}
	}
	if err := cw.Advance(); err != nil {
		return 0, err
	}
	return cw.w.Write(p)
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

// Advance 把「这一次写出」的截止时间往后推。ErrNotSupported 说明底层 writer 不支持
// deadline（本项目的 gin ResponseWriter 支持），不该因此中断写出。
func (cw *Writer) Advance() error {
	if err := cw.rc.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// Copy 把上游响应按字节块复制给客户端，每块 flush 一次（透传路径的主循环）。
//
// 不用 io.Copy：它不 flush，SSE 帧会攒在 net/http 的缓冲里，客户端要等攒满或流结束
// 才看得到——正是「逐字输出」失效的成因。也不用 bufio.Scanner 按行读再重组：那会引入
// 换行/空行的重写风险，且 Scanner 的 token 上限会变成透传路径的截断上限。
func (cw *Writer) Copy(body io.Reader) error {
	// 先兜住响应头本身与空 body 的情形，之后每写一块再推进一次。
	if err := cw.Advance(); err != nil {
		return err
	}
	buf := make([]byte, copyBufferSize)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := cw.Write(buf[:n]); err != nil {
				return err
			}
			if err := cw.flush(); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
