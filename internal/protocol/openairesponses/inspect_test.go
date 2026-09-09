package openairesponses

import (
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// InspectPassthrough 的四格：两位各自为否才扫、为是一律放行；两样同时带只报 compaction。
// 判据函数本身的边界（input 形态、null / 空串）各在 compaction_test 与 decode_test。
func TestInspectPassthrough(t *testing.T) {
	const (
		plain      = `{"model":"m","input":[{"type":"message","role":"user","content":"hi"}]}`
		compaction = `{"model":"m","input":[{"type":"message","role":"user","content":"hi"},{"type":"compaction_trigger"}]}`
		stateful   = `{"model":"m","previous_response_id":"resp_1","input":"hi"}`
		both       = `{"model":"m","previous_response_id":"resp_1","input":[{"type":"compaction_trigger"}]}`
		malformed  = `{"model":"m","input":[`
	)
	all := protocol.ChannelCapabilities{Compaction: true, StatefulResponses: true}
	none := protocol.ChannelCapabilities{}

	cases := []struct {
		name string
		body string
		caps protocol.ChannelCapabilities
		want protocol.RejectReason // "" 放行
	}{
		{"普通 turn 两位皆否也放行", plain, none, ""},
		{"压缩 turn 位为否拒", compaction, none, protocol.RejectCompaction},
		{"压缩 turn 位为是放行", compaction, protocol.ChannelCapabilities{Compaction: true}, ""},
		{"续链 位为否拒", stateful, none, protocol.RejectStateful},
		{"续链 位为是放行", stateful, protocol.ChannelCapabilities{StatefulResponses: true}, ""},
		{"两样同时带只报 compaction", both, none, protocol.RejectCompaction},
		{"两样同时带、compaction 位为是则报 stateful", both, protocol.ChannelCapabilities{Compaction: true}, protocol.RejectStateful},
		{"两位皆是一律放行", both, all, ""},
		{"解不开的请求体放行（判不出来不拒）", malformed, none, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewCodec().InspectPassthrough([]byte(tc.body), tc.caps)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("期望放行，得到 %+v", got)
				}
				return
			}
			if got == nil || got.Reason != tc.want {
				t.Fatalf("期望拒绝 %q，得到 %+v", tc.want, got)
			}
			if got.Err == nil || got.Err.Message == "" {
				t.Fatalf("拒绝没带文案：%+v", got)
			}
			if strings.Contains(got.Err.Message, "渠道 ") {
				t.Fatalf("文案不该自带渠道名前缀（由调用方拼）：%q", got.Err.Message)
			}
			switch tc.want {
			case protocol.RejectCompaction:
				if got.Err.Code != "" || got.Err.Param != "" {
					t.Fatalf("compaction 拒绝不该带 code/param：%+v", got.Err)
				}
			case protocol.RejectStateful:
				if got.Err.Code != CodePreviousResponseNotFound || got.Err.Param != ParamPreviousResponseID {
					t.Fatalf("stateful 拒绝的 code/param 不对：%+v", got.Err)
				}
			}
		})
	}
}

// 编译期钉住：Codec 实现了 RequestInspector，server 的类型断言才接得上。
var _ protocol.RequestInspector = (*Codec)(nil)
