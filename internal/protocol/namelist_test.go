package protocol_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 名单封顶：登记方全是**对方**说了算的东西（客户端声明的工具、回带历史里的残缺调用、
// 上游流里认不得的事件名），无上限就是让对方决定我们那行 Warn 有多大。超出的只计数，
// 好让「封顶了」与「就这么多」分得开。
func TestNameListCapsAtMaxDropNames(t *testing.T) {
	var l protocol.NameList
	for i := 0; i < protocol.MaxDropNames+6; i++ {
		l.Add(fmt.Sprintf("tool_%02d", i))
	}
	if got := len(l.Names()); got != protocol.MaxDropNames {
		t.Errorf("留名 %d 个, 期望 %d", got, protocol.MaxDropNames)
	}
	if l.Omitted() != 6 {
		t.Errorf("Omitted = %d, 期望 6", l.Omitted())
	}
	// 留的是**先来的**那批：登记序才对得上客户端声明的次序。
	if l.Names()[0] != "tool_00" {
		t.Errorf("首名 = %q, 期望 tool_00——封顶不该顶掉先来的", l.Names()[0])
	}
	// 日志形态带 +N，与 Drops 那份里名单的一截同形。
	if got := l.String(); !strings.HasSuffix(got, " +6]") {
		t.Errorf("日志值 = %q, 期望以 ` +6]` 结尾——不带 +N 就看不出封顶过", got)
	}
	if got := l.LogValue().String(); got != l.String() {
		t.Errorf("LogValue = %q, 期望与 String 一致 %q", got, l.String())
	}
}

// 同名只留一条：一个 item 的 added 与 done 各来一帧、一个认不得的事件名一条流里能来
// 几十帧，逐帧记会把一行 Warn 撑成刷屏。空名跳过（无名服务端工具由调用方退成 type，
// 见 Tool.Label）。
func TestNameListDedupesAndSkipsEmpty(t *testing.T) {
	var l protocol.NameList
	l.Add("web_search_call(ws_1)", "", "web_search_call(ws_1)", "event:response.foo")
	want := []string{"web_search_call(ws_1)", "event:response.foo"}
	got := l.Names()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Names = %v, 期望 %v", got, want)
	}
	if l.Omitted() != 0 {
		t.Errorf("Omitted = %d, 期望 0——去重掉的不算封顶", l.Omitted())
	}
	if l.String() != "[web_search_call(ws_1),event:response.foo]" {
		t.Errorf("日志值 = %q", l.String())
	}
}

// 空名单：Empty 为真，渲染成空串——Drop.String 直接把它接在档位后面，非工具类档位
// 因此仍然只打一个光秃秃的档位名。
func TestNameListEmpty(t *testing.T) {
	var l protocol.NameList
	if !l.Empty() || l.String() != "" {
		t.Errorf("空名单 Empty=%v String=%q, 期望 true 与空串", l.Empty(), l.String())
	}
	l.Add("")
	if !l.Empty() {
		t.Error("只登记过空名，Empty 该仍为真")
	}
}
