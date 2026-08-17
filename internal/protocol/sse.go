package protocol

import "bytes"

// FrameScanner 把字节块重组成完整的 SSE 帧。
//
// 它只服务 Tap（旁路解析），**绝不出现在转发路径上**：转发按字节块整块复制，对帧
// 边界一无所知（§6.1）。两条路径共用同一份字节，但只有这一侧解析。
//
// 不用 bufio.Scanner：它的 token 上限一旦触发就是 ErrTooLong，调用方要么放弃整条
// 流要么自己接管缓冲——而这里超限后必须能重新对齐、继续解析后面的帧。
type FrameScanner struct {
	// Limit 是单帧字节上限，0 表示用 BufferLimit。
	Limit int

	buf []byte
	// skipping 表示正在丢弃一个超限帧的剩余部分，等下一个帧边界重新对齐。
	skipping bool
	overflow bool
}

// BufferLimit 是 Tap 单帧缓冲上限。并行工具调用的参数帧可以很大（几百 KB 级），
// 4 MB 留足余量；再大就不是正常响应了，宁可降级也不让旁路解析吃满内存。
const BufferLimit = 4 << 20

// maxSepLen 是最长帧分隔符 \r\n\r\n 的长度。缓冲里随时可能压着最多 maxSepLen-1 个
// 「差一点就凑成分隔符」的字节，判超限时得把它们排除在外。
const maxSepLen = 4

// Overflowed 报告是否发生过超限丢帧。
func (s *FrameScanner) Overflowed() bool { return s.overflow }

func (s *FrameScanner) limit() int {
	if s.Limit > 0 {
		return s.Limit
	}
	return BufferLimit
}

// Push 吃进一块字节，对其中每个凑齐的帧调用 onFrame。
//
// onFrame 拿到的切片指向内部缓冲，返回后即失效——需要留存请自行拷贝。
func (s *FrameScanner) Push(p []byte, onFrame func(frame []byte)) {
	s.buf = append(s.buf, p...)
	for {
		start, end := frameBoundary(s.buf)
		if start < 0 {
			break
		}
		switch {
		case s.skipping:
			// 超限帧的尾巴，丢掉。
		case start > s.limit():
			// 整帧在一次 Push 里凑齐、但本身就超限。也得丢——否则「帧是否超限」
			// 会取决于 TCP 恰好怎么切块，同一条流换个切法结论就变了。
			s.overflow = true
		default:
			onFrame(s.buf[:start])
		}
		// 超限帧的尾巴已经吞掉，从下一帧起恢复解析。
		s.skipping = false
		s.buf = append(s.buf[:0], s.buf[end:]...)
	}
	// 比的是 limit+maxSepLen-1 而不是 limit：缓冲里除了帧内容，还可能压着最多
	// maxSepLen-1 个尚未凑成分隔符的字节。拿它们一起去比上限，帧长落在
	// (limit-maxSepLen+1, limit] 区间时结论就会随上游怎么切块而翻转——
	// 整帧一次到达走上面的 start > limit（不含分隔符）判定为不超限，
	// 逐字节到达却会在这里被裁掉。
	if len(s.buf) <= s.limit()+maxSepLen-1 {
		return
	}
	// 超限：丢掉已攒的部分，只留可能跨块的边界前缀，进入丢弃模式等下一个边界。
	s.overflow = true
	s.skipping = true
	const keep = maxSepLen - 1
	if n := len(s.buf); n > keep {
		s.buf = append(s.buf[:0], s.buf[n-keep:]...)
	}
}

// Flush 把流结束时残留的、没有结尾空行的最后一帧交出去。上游并不总以空行收尾。
func (s *FrameScanner) Flush(onFrame func(frame []byte)) {
	switch {
	case s.skipping || len(s.buf) == 0:
	case len(s.buf) > s.limit():
		s.overflow = true
	default:
		onFrame(s.buf)
	}
	s.buf = nil
	s.skipping = false
}

// frameBoundary 找最早的帧分隔空行，返回 (帧尾, 下一帧头)。找不到返回 (-1, -1)。
//
// 三种空行都认：\n\n、\r\n\r\n、\r\r。SSE 规范三种换行都合法，而真实上游混着用
// （见 openai_test.go 里的流式用例）。
func frameBoundary(b []byte) (int, int) {
	start, end := -1, -1
	for _, sep := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n"), []byte("\r\r")} {
		i := bytes.Index(b, sep)
		if i < 0 || (start >= 0 && i >= start) {
			continue
		}
		start, end = i, i+len(sep)
	}
	return start, end
}

// sseLines 按 SSE 规范的三种行尾切行：\r\n、\n、裸 \r。
//
// 必须与 frameBoundary 认的分隔保持一致。此前这里按 \n 切、再削掉行尾的 \r，
// 裸 \r 的上游整帧会被当成一行：event 值粘着后面全部内容、data 根本取不到，
// 而 Tap 又不会置 Degraded——它不是解析失败，是压根没找到 data 字段。静默丢
// 失是本项目最不想要的降级形态（约定是降级必须可见），故两处认同一套行尾。
func sseLines(frame []byte) [][]byte {
	var lines [][]byte
	for len(frame) > 0 {
		i := bytes.IndexAny(frame, "\r\n")
		if i < 0 {
			return append(lines, frame)
		}
		lines = append(lines, frame[:i])
		if frame[i] == '\r' && i+1 < len(frame) && frame[i+1] == '\n' {
			i++
		}
		frame = frame[i+1:]
	}
	return lines
}

// SSEFields 从一帧里取出 event 名与拼好的 data 负载。
//
// 按 SSE 规范：多条 data 行以 \n 连接，冒号后的一个空格属于分隔符要去掉，以 :
// 开头的是注释（心跳）直接丢。
func SSEFields(frame []byte) (event string, data []byte) {
	seenData := false
	for _, line := range sseLines(frame) {
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		field, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue // 无冒号的行是「字段名 + 空值」，本项目用不到
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			event = string(value)
		case "data":
			if seenData {
				data = append(data, '\n')
			}
			seenData = true
			data = append(data, value...)
		}
	}
	return event, data
}
