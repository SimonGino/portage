package protocol

import (
	"fmt"
	"strings"
	"testing"
)

// push 逐字节喂：真实的分块边界落在哪里由 TCP 决定，逐字节是最恶劣也最有代表性的
// 一种切法——任何「假设一次 Write 至少含一个完整帧」的实现都会在这里露馅。
func pushByBytes(s *FrameScanner, in string) []string {
	var got []string
	for i := range len(in) {
		s.Push([]byte(in[i:i+1]), func(f []byte) { got = append(got, string(f)) })
	}
	return got
}

func TestFrameScannerSplitsOnEveryLineEndingStyle(t *testing.T) {
	// 三种空行混用，且故意让 \r\n\r\n 出现在 \n\n 之后——边界查找必须取最早的那个，
	// 而不是按分隔符列表的顺序先撞上谁算谁。
	in := "data: a\n\n" + "data: b\r\n\r\n" + "data: c\r\r" + "data: d\n\n"

	var s FrameScanner
	got := pushByBytes(&s, in)

	want := []string{"data: a", "data: b", "data: c", "data: d"}
	if len(got) != len(want) {
		t.Fatalf("切出 %d 帧，期望 %d：%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 帧 = %q, 期望 %q", i, got[i], want[i])
		}
	}
	if s.Overflowed() {
		t.Error("没有超限却报了 overflow")
	}
}

func TestFrameScannerFlushesUnterminatedTail(t *testing.T) {
	// 上游不保证以空行收尾。最后一帧要是丢了，非流式路径上整个 usage 就没了。
	var s FrameScanner
	got := pushByBytes(&s, "data: a\n\ndata: tail")
	s.Flush(func(f []byte) { got = append(got, string(f)) })

	if len(got) != 2 || got[1] != "data: tail" {
		t.Fatalf("尾帧没有被 Flush 出来: %q", got)
	}
}

func TestFrameScannerDropsOversizedFrameThenResyncs(t *testing.T) {
	// 超限那一帧丢掉，但**后面的帧必须照常解析**——Anthropic 的 usage 在
	// message_delta 里，也就是流的最后。一超限就整条流罢工的话，一个畸形的大帧
	// 会把整次调用的 usage 全带走。
	s := FrameScanner{Limit: 64}
	huge := strings.Repeat("x", 500)

	var got []string
	onFrame := func(f []byte) { got = append(got, string(f)) }
	s.Push([]byte("data: ok1\n\n"), onFrame)
	s.Push([]byte("data: "+huge), onFrame)
	s.Push([]byte("\n\ndata: ok2\n\n"), onFrame)

	if len(got) != 2 || got[0] != "data: ok1" || got[1] != "data: ok2" {
		t.Fatalf("超限帧应被丢弃、其余帧照常切出，实得: %q", got)
	}
	if !s.Overflowed() {
		t.Error("超限了却没有置 overflow，调用方无从知道该降级")
	}
}

// 超限判定不能取决于 TCP 恰好怎么切块：同一帧一次喂完和逐字节喂完，结论必须一样。
//
// 帧长必须**贴着上限**扫一遍：只测远超上限的帧，两种判定路径（凑齐边界时按帧长判、
// 攒不出边界时按缓冲长判）的分歧区间恰好被跳过——那正是先前假绿的成因。分歧区间是
// (limit-maxSepLen+1, limit]，所以 limit±maxSepLen 这一段每个长度都要试。
func TestFrameScannerOversizeVerdictDoesNotDependOnChunking(t *testing.T) {
	const limit = 32
	for _, sep := range []string{"\n\n", "\r\n\r\n", "\r\r"} {
		for length := limit - maxSepLen; length <= limit+maxSepLen; length++ {
			t.Run(fmt.Sprintf("sep=%q/len=%d", sep, length), func(t *testing.T) {
				frame := strings.Repeat("x", length) + sep

				// 同一帧、同一上限，只是切块方式不同，结论必须一致。
				oneShot := scanOnce(frame, limit, len(frame))
				byByte := scanOnce(frame, limit, 1)
				if oneShot != byByte {
					t.Fatalf("整帧一次到达 emitted=%v，逐字节到达 emitted=%v——超限判定被切块方式左右了",
						oneShot, byByte)
				}
				// 且结论要对：帧长不超上限就得交出去。
				if want := length <= limit; oneShot != want {
					t.Errorf("emitted = %v, 期望 %v（帧长 %d，上限 %d）", oneShot, want, length, limit)
				}
			})
		}
	}
}

// scanOnce 按 chunk 大小把 frame 喂给一个新扫描器，报告那一帧有没有被交出来。
func scanOnce(frame string, limit, chunk int) bool {
	s := FrameScanner{Limit: limit}
	emitted := false
	for i := 0; i < len(frame); i += chunk {
		end := min(i+chunk, len(frame))
		s.Push([]byte(frame[i:end]), func([]byte) { emitted = true })
	}
	return emitted
}

func TestFrameScannerResyncsWhenBoundaryStraddlesTheDropPoint(t *testing.T) {
	// 丢弃模式下只保留尾部几个字节找下一个边界。若保留得不够，跨块的 \r\n\r\n
	// 会被截断，扫描器就永远对不齐了。
	s := FrameScanner{Limit: 16}
	var got []string
	onFrame := func(f []byte) { got = append(got, string(f)) }

	s.Push([]byte(strings.Repeat("x", 40)+"\r\n"), onFrame)
	s.Push([]byte("\r\ndata: after\n\n"), onFrame)

	if len(got) != 1 || got[0] != "data: after" {
		t.Fatalf("边界跨块时没能重新对齐，实得: %q", got)
	}
}

func TestSSEFieldsJoinsMultipleDataLines(t *testing.T) {
	frame := "event: response.completed\r\n" +
		": 这是心跳注释\r\n" +
		"data: {\"a\":1,\r\n" +
		"data: \"b\":2}\r\n" +
		"id: 7"

	event, data := SSEFields([]byte(frame))

	if event != "response.completed" {
		t.Errorf("event = %q", event)
	}
	// 多条 data 以 \n 连接（SSE 规范），注释与其他字段丢弃。
	if want := "{\"a\":1,\n\"b\":2}"; string(data) != want {
		t.Errorf("data = %q, 期望 %q", data, want)
	}
}

// frameBoundary 认三种行尾（\r\n\r\n、\n\n、\r\r），SSEFields 就必须认同一套。
// 曾经它只按 \n 切行、再削掉行尾的 \r：裸 \r 的上游整帧被当成一行，event 值粘着
// 后面全部内容、data 根本取不到，而 Tap 还不置 Degraded——它不是解析失败，是压根
// 没找到 data 字段。静默丢失比报错更糟，因为日志看上去是好的。
func TestSSEFieldsHandlesEveryLineEndingStyle(t *testing.T) {
	for _, tc := range []struct {
		name string
		eol  string
	}{
		{"LF", "\n"},
		{"CRLF", "\r\n"},
		{"裸 CR", "\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := "event: message_delta" + tc.eol +
				": 心跳" + tc.eol +
				"data: {\"a\":1," + tc.eol +
				"data: \"b\":2}" + tc.eol +
				"id: 7"

			event, data := SSEFields([]byte(frame))

			if event != "message_delta" {
				t.Errorf("event = %q, 期望 %q", event, "message_delta")
			}
			if want := "{\"a\":1,\n\"b\":2}"; string(data) != want {
				t.Errorf("data = %q, 期望 %q", data, want)
			}
		})
	}
}

func TestSSEFieldsKeepsExtraSpacesAfterTheFirst(t *testing.T) {
	// 只有紧跟冒号的**一个**空格属于分隔符；再多的是负载的一部分。透传路径不碰
	// 字节，但 Tap 若把空格吃多了，解出来的 JSON 就不是上游那份。
	_, data := SSEFields([]byte("data:  {\"x\":1}"))
	if want := ` {"x":1}`; string(data) != want {
		t.Errorf("data = %q, 期望 %q", data, want)
	}
}
