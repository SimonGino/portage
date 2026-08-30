package calllog_test

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
)

// 本文件测的是这条流水**自己**的规矩：三段可空映射的判据、错误原文先到先得、
// 收场词后写覆盖、收尾幂等、落库失败不发作。
//
// 这些规矩以前只在 internal/server 里被间接验到——要架一个网关加一个假上游才碰得
// 到一条，而「落库失败」「Finish 被调两次」这两条压根没有用例（假上游造不出
// SQLite 抖动，中间件的 defer 也不会调两次）。收编成模块之后它们是十几行的直白
// 断言，这正是把记账从 41 个裸赋值收成一个模块换来的东西。
//
// 网关全链路上的表现仍归 internal/server 的 217 条用例管，这里不重复。

// harness 是一条接了假出口的流水：sink 把行攒进 rows，slog 打进 logs。
type harness struct {
	rec  *calllog.Recorder
	rows []calllog.Row
	// sinkErr 让「落库失败」变成一行赋值。真库上这一档要等 SQLite 抖动。
	sinkErr error
	logs    *bytes.Buffer
}

func newHarness() *harness {
	h := &harness{logs: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(h.logs, nil))
	h.rec = calllog.New(protocol.EndpointMessages, log, func(_ context.Context, row calllog.Row) error {
		h.rows = append(h.rows, row)
		return h.sinkErr
	})
	return h
}

// row 收尾并交出落库的那一行。
func (h *harness) row(t *testing.T, status int) calllog.Row {
	t.Helper()
	h.rec.Finish(status)
	if len(h.rows) != 1 {
		t.Fatalf("落库行数 = %d，期望 1", len(h.rows))
	}
	return h.rows[0]
}

func (h *harness) log() string { return h.logs.String() }

// —— 三段可空映射的判据 ——————————————————————————————————————————

// is_stream 的判据是「解析到请求头部没有」，不是 stream 的值。没走到那一步的行
// 落 false 就是把「不知道」说成「同步」，而两者在按流式分组的统计里是两回事。
func TestIsStreamIsNullUntilTheRequestIsParsed(t *testing.T) {
	h := newHarness()
	h.rec.Refused(calllog.Unauthorized)
	if row := h.row(t, 401); row.IsStream.Valid {
		t.Errorf("没解析过请求体的行 is_stream = %v，期望 NULL", row.IsStream)
	}

	for _, stream := range []bool{false, true} {
		h := newHarness()
		h.rec.RequestParsed("claude-sonnet-4", stream)
		row := h.row(t, 200)
		if !row.IsStream.Valid || row.IsStream.Bool != stream {
			t.Errorf("RequestParsed(stream=%v) 之后 is_stream = %v，期望 %v", stream, row.IsStream, stream)
		}
	}
}

// ttft 只记流式且真见过首字节的行。四格真值表逐一钉住——这一格以前的判据是
// 「firstByte 是不是零值」，那是 time.Time 的性质不是这条流水的语义。
func TestTTFTLandsOnlyForStreamingCallsThatSawAFirstByte(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stream    bool
		firstByte bool
		want      bool
	}{
		{"流式且见过首字节", true, true, true},
		{"流式但没见过首字节（拨不通、上游直接 4xx）", true, false, false},
		{"非流式见过首字节：仍不落库，只进 slog 的 ttfb_ms", false, true, false},
		{"非流式也没首字节", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			h.rec.RequestParsed("claude-sonnet-4", tc.stream)
			if tc.firstByte {
				h.rec.FirstByte()
			}
			if got := h.row(t, 200).TTFTMs.Valid; got != tc.want {
				t.Errorf("ttft_ms 落库 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// FirstByte 只认第一次：一条流有成千上万次写，认最后一次的话 ttft 就成了总耗时。
func TestFirstByteKeepsTheEarliestMoment(t *testing.T) {
	h := newHarness()
	h.rec.RequestParsed("claude-sonnet-4", true)
	h.rec.FirstByte()
	time.Sleep(20 * time.Millisecond)
	h.rec.FirstByte()

	row := h.row(t, 200)
	if !row.TTFTMs.Valid {
		t.Fatalf("ttft_ms 没落库")
	}
	if row.TTFTMs.Int64 >= 20 {
		t.Errorf("ttft_ms = %d，第二次 FirstByte 把时刻推后了", row.TTFTMs.Int64)
	}
}

// token 五列的判据是「Tap 交过 usage 没有」。上游回了一份全零的 usage 与压根没
// 走到上游，落库上必须分得开——后者是 NULL，前者是确凿的 0。
func TestTokenColumnsAreNullWithoutASummary(t *testing.T) {
	h := newHarness()
	row := h.row(t, 401)
	for name, col := range map[string]bool{
		"input_tokens":       row.InputTokens.Valid,
		"output_tokens":      row.OutputTokens.Valid,
		"cache_read_tokens":  row.CacheReadTokens.Valid,
		"cache_write_tokens": row.CacheWriteTokens.Valid,
		"reasoning_tokens":   row.ReasoningTokens.Valid,
	} {
		if col {
			t.Errorf("没有 summary 的行 %s 落了值，期望 NULL", name)
		}
	}

	h = newHarness()
	h.rec.Summarized(protocol.Summary{}) // 上游报了，全是 0
	row = h.row(t, 200)
	if !row.InputTokens.Valid || row.InputTokens.Int64 != 0 {
		t.Errorf("上游报了全零的 usage，input_tokens = %v，期望确凿的 0", row.InputTokens)
	}
}

// 思考 token 单独判（口径层 v0.66）：有 summary 不等于有这一格。上游整个 details
// 容器都不发时留 NULL，落 0 是在替上游说「这次没思考」。
func TestReasoningTokensFollowTheReportedFlagNotTheValue(t *testing.T) {
	h := newHarness()
	h.rec.Summarized(protocol.Summary{ReasoningTokens: 512})
	if row := h.row(t, 200); row.ReasoningTokens.Valid {
		t.Errorf("上游没报这一格，reasoning_tokens = %v，期望 NULL", row.ReasoningTokens)
	}

	h = newHarness()
	h.rec.Summarized(protocol.Summary{HasReasoningTokens: true}) // 报了，是 0
	row := h.row(t, 200)
	if !row.ReasoningTokens.Valid || row.ReasoningTokens.Int64 != 0 {
		t.Errorf("上游报了 0，reasoning_tokens = %v，期望确凿的 0", row.ReasoningTokens)
	}
}

// —— 错误原文 ————————————————————————————————————————————————————

// 先到先得：透传路径的旁路收的是上游原话，收尾分支给的是一句概括，后者不该盖掉
// 前者。
func TestErrorDetailIsFirstWins(t *testing.T) {
	h := newHarness()
	h.rec.Failed(calllog.UpstreamError, "上游原话")
	h.rec.Failed(calllog.StreamAborted, "一句概括")

	if got := h.row(t, 500).ErrorDetail.String; got != "上游原话" {
		t.Errorf("error_detail = %q，期望先到的那一份", got)
	}
}

// 空串不占坑：传空串的意思是「这一支没有原文可记」，不是「原文是空的」。占了坑
// 之后真有原文的那一支就再也写不进来了。
func TestEmptyErrorDetailDoesNotClaimTheSlot(t *testing.T) {
	h := newHarness()
	h.rec.Failed(calllog.StreamAborted, "")
	if row := h.row(t, 200); row.ErrorDetail.Valid {
		t.Errorf("只传过空串，error_detail = %v，期望 NULL", row.ErrorDetail)
	}

	h = newHarness()
	h.rec.Failed(calllog.StreamAborted, "")
	h.rec.Failed(calllog.UpstreamError, "后来真有原文了")
	if got := h.row(t, 500).ErrorDetail.String; got != "后来真有原文了" {
		t.Errorf("error_detail = %q，空串把坑占了", got)
	}
}

// 内存里收完整的一段，落库那一刻才截到 2KB（口径层 v0.53 + v0.74）：request_id
// 排在错误信封末尾，按 2KB 截完正好把它截在外面，取键必须在截断前的字节上做。
func TestErrorDetailIsTruncatedAtPersistTimeNotAtCaptureTime(t *testing.T) {
	// 一个 3KB 的信封，request_id 在末尾——超过落库上限，没超过内存上限。
	body := `{"type":"error","error":{"type":"overloaded_error","message":"` +
		strings.Repeat("x", 3<<10) + `"},"request_id":"req_011CeTruncated"}`

	h := newHarness()
	h.rec.UpstreamRejected(strings.NewReader(body))
	row := h.row(t, 529)

	if n := len(row.ErrorDetail.String); n < 2<<10 || n > (2<<10)+64 {
		t.Errorf("error_detail 长 %d 字节，期望截在 2KB 上下", n)
	}
	if !strings.HasSuffix(row.ErrorDetail.String, "…[truncated]") {
		t.Errorf("截断没说出口：%q…", row.ErrorDetail.String[:64])
	}
	// 关键的一条：截断发生在落库那一刻，所以取键还看得到信封末尾的 id。
	if row.UpstreamRequestID != "req_011CeTruncated" {
		t.Errorf("upstream_request_id = %q，被截断连累了", row.UpstreamRequestID)
	}
}

// UpstreamRejected 把读到的字节还给调用方——它还要从里面取一句 error.message
// 回给客户端，不能让它为此再读一次（流已经读完了）。
func TestUpstreamRejectedHandsTheBytesBack(t *testing.T) {
	h := newHarness()
	got := h.rec.UpstreamRejected(strings.NewReader(`{"error":{"message":"nope"}}`))
	if string(got) != `{"error":{"message":"nope"}}` {
		t.Errorf("回给调用方的字节 = %q", got)
	}
	if word := h.row(t, 502).Error; word.String != string(calllog.UpstreamError) {
		t.Errorf("收场词 = %v，期望 upstream_error", word)
	}
}

// —— 收场词 ————————————————————————————————————————————————————

// 初值是 rejected 而不是 ok：走到收尾还没被任何分支覆盖，说明停在某个早退分支
// 上了。设成 ok 的话，任何漏写收场的新分支都会静默记成一次干净的成功。
func TestOutcomeStartsAsRejected(t *testing.T) {
	h := newHarness()
	if got := h.row(t, 400).Error; got.String != string(calllog.Rejected) {
		t.Errorf("没写过收场的行 error = %v，期望 rejected", got)
	}
}

// 后写覆盖：写出响应头之后 Succeeded，中途断流再 Failed(stream_aborted)——
// 状态码已经发出去了，只有收场词能看出它其实没说完。
func TestOutcomeIsLastWriteWins(t *testing.T) {
	h := newHarness()
	h.rec.Succeeded()
	h.rec.Failed(calllog.StreamAborted, "")
	if got := h.row(t, 200).Error; got.String != string(calllog.StreamAborted) {
		t.Errorf("error = %v，期望 stream_aborted", got)
	}
}

// 干净的成功落 NULL，不落 "ok"：那一列是「这行为什么不是一次干净的成功」，
// 成功行没有答案。
func TestSuccessLeavesTheErrorColumnNull(t *testing.T) {
	h := newHarness()
	h.rec.Succeeded()
	if got := h.row(t, 200).Error; got.Valid {
		t.Errorf("成功行 error = %v，期望 NULL", got)
	}
}

// 并发闸在拨号之前就回绝，所以它要把出站端点清回空串——那一格的不变量是
// 「非空 ⟺ 真的向上游发起过」。清在这个动词里而不是两个调用点各写一遍。
func TestQueueRejectedClearsTheUpstreamEndpoint(t *testing.T) {
	h := newHarness()
	h.rec.Dialing("/v1/messages")
	h.rec.QueueRejected(calllog.QueueTimeout)

	row := h.row(t, 503)
	if row.UpstreamEndpoint != "" {
		t.Errorf("闸拒的行 upstream_endpoint = %q，期望空串", row.UpstreamEndpoint)
	}
	if row.Error.String != string(calllog.QueueTimeout) {
		t.Errorf("error = %v，期望 queue_timeout", row.Error)
	}
}

// —— 开分类守卫（C14）——————————————————————————————————————————

// 正常路径**零输出**：这是「行为一字不变」的前提，守卫在对的用法上必须一声不吭。
func TestCorrectlyClassifiedOutcomesAreSilent(t *testing.T) {
	h := newHarness()
	h.rec.Refused(calllog.RateLimited)
	h.rec.Failed(calllog.UpstreamError, "")
	h.rec.Succeeded()
	h.rec.Finish(200)

	if strings.Contains(h.log(), "用错了半区") {
		t.Errorf("对的用法也报了警:\n%s", h.log())
	}
}

// 用错半区时多一行 slog.Error，而请求本身照常收场——不 panic 是硬要求：这些动词
// 多数在 panic 展开路径上跑。
func TestMisusedOutcomeHalvesAreReportedWithoutDerailingTheCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(r *calllog.Recorder)
		want string
	}{
		{"Refused 收到失败半区的词", func(r *calllog.Recorder) { r.Refused(calllog.StreamAborted) }, "Refused(stream_aborted)"},
		{"Failed 收到回绝半区的词", func(r *calllog.Recorder) { r.Failed(calllog.QueueFull, "") }, "Failed(queue_full)"},
		{"QueueRejected 收到失败半区的词", func(r *calllog.Recorder) { r.QueueRejected(calllog.UpstreamError) }, "QueueRejected(upstream_error)"},
		{"Refused 收到 ok：它哪一半都不是", func(r *calllog.Recorder) { r.Refused(calllog.OK) }, "Refused(ok)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			tc.call(h.rec)
			row := h.row(t, 500) // 照常落一行，没被守卫打断

			if !strings.Contains(h.log(), tc.want) {
				t.Errorf("日志里没报出用错的那次调用 %s:\n%s", tc.want, h.log())
			}
			if !strings.Contains(h.log(), "msg=call ") {
				t.Errorf("守卫把正经那一行日志顶掉了:\n%s", h.log())
			}
			if row.Status != 500 {
				t.Errorf("守卫改动了落库的行：status = %d", row.Status)
			}
		})
	}
}

// —— request id 三档（口径层 v0.74）————————————————————————————

func TestUpstreamRequestIDPrefersTheOfficialHeader(t *testing.T) {
	h := newHarness()
	h.rec.RequestIDs("req_official", "proxy-编的号")
	h.rec.UpstreamRejected(strings.NewReader(`{"request_id":"req_inBody"}`))

	if got := h.row(t, 500).UpstreamRequestID; got != "req_official" {
		t.Errorf("upstream_request_id = %q，期望头上那一档", got)
	}
}

func TestUpstreamRequestIDFallsBackToTheErrorBody(t *testing.T) {
	// 中转把官方响应头裁掉了，只回一个自己编的号——而官方那个号还在错误信封里。
	h := newHarness()
	h.rec.RequestIDs("", "proxy-编的号")
	h.rec.UpstreamRejected(strings.NewReader(`{"type":"error","request_id":"req_inBody"}`))

	if got := h.row(t, 500).UpstreamRequestID; got != "req_inBody" {
		t.Errorf("upstream_request_id = %q，期望错误体里那一档", got)
	}
}

func TestUpstreamRequestIDFallsBackToTheProxyHeader(t *testing.T) {
	h := newHarness()
	h.rec.RequestIDs("", "proxy-编的号")
	if got := h.row(t, 200).UpstreamRequestID; got != "proxy-编的号" {
		t.Errorf("upstream_request_id = %q，期望第三档", got)
	}
}

// 三档都落空时是空串，不是别的什么占位符——这一列上「没走到上游」与「上游没回
// 这个头」同档。
func TestUpstreamRequestIDIsEmptyWhenNoTierHasOne(t *testing.T) {
	h := newHarness()
	if got := h.row(t, 401).UpstreamRequestID; got != "" {
		t.Errorf("upstream_request_id = %q，期望空串", got)
	}
}

// —— 收尾 ——————————————————————————————————————————————————————

// 幂等：中间件的 defer 保证它恰好被调一次，但 panic 展开路径上多一条保险不亏。
// 不幂等的表现是账翻倍，而所有既有断言照常绿——只有这里逮得到。
func TestFinishIsIdempotent(t *testing.T) {
	h := newHarness()
	h.rec.Succeeded()
	h.rec.Finish(200)
	h.rec.Finish(499) // 譬如 panic 展开时又走了一次

	if len(h.rows) != 1 {
		t.Errorf("落库 %d 行，期望 1", len(h.rows))
	}
	if n := strings.Count(h.log(), "msg=call "); n != 1 {
		t.Errorf("slog 打了 %d 行 call，期望 1", n)
	}
	if h.rows[0].Status != 200 {
		t.Errorf("status = %d，第二次 Finish 改了第一次的账", h.rows[0].Status)
	}
}

// 落库失败只记一条 slog.Error：这里跑的时候响应早已写出去了，改写不了也中断不
// 了。一次 SQLite 抖动不该把一次成功的转发变成客户端眼里的失败。
func TestSinkFailureIsLoggedAndSwallowed(t *testing.T) {
	h := newHarness()
	h.sinkErr = context.DeadlineExceeded
	h.rec.Succeeded()
	h.rec.Finish(200) // 不 panic 本身就是断言的一部分

	if !strings.Contains(h.log(), "调用流水落库失败") {
		t.Errorf("落库失败没留痕:\n%s", h.log())
	}
	if !strings.Contains(h.log(), "msg=call ") {
		t.Errorf("落库失败把那一行 slog 也带走了:\n%s", h.log())
	}
}

// Detached 是「上下文里取不到记录」的黑洞：动词照常收，收尾什么都不做。它一条
// 用例都没有过——网关全链路上走不到这条路（走到了就说明路由被改坏了）。
func TestDetachedRecorderSwallowsEverything(t *testing.T) {
	rec := calllog.Detached()
	rec.Authenticated("k")
	rec.RequestParsed("claude-sonnet-4", true)
	rec.RecordRequestBody([]byte("{}"))
	rec.Routed("ch", protocol.OpenAI, "gpt-4o", calllog.Prices{})
	rec.Dialing("/v1/chat/completions")
	rec.Attempted(2, "cred", 30*time.Millisecond)
	rec.RequestIDs("a", "b")
	rec.Summarized(protocol.Summary{InputTokens: 1})
	_, _ = rec.TapResponseBody().Write([]byte("x"))
	_, _ = rec.TapUpstreamErrorBody().Write([]byte("y"))
	rec.FirstByte()
	rec.UpstreamRejected(strings.NewReader("{}"))
	rec.Failed(calllog.UpstreamError, "d")
	rec.Succeeded()
	rec.Finish(200)

	// 唯一露出去的读口子照常应答——闸拒那两条 slog 靠它。
	if got := rec.QueueWaitMs(); got != 30 {
		t.Errorf("QueueWaitMs = %d，期望 30", got)
	}
}

// QueueWaitMs 是这条流水唯一的读口子：闸拒那两条 slog 要报「排了多久才被拒」，
// 而这个数只有这里有。
func TestQueueWaitMsReportsWhatAttemptedRecorded(t *testing.T) {
	h := newHarness()
	if got := h.rec.QueueWaitMs(); got != 0 {
		t.Errorf("没排过队时 QueueWaitMs = %d，期望 0", got)
	}
	h.rec.Attempted(1, "cred", 250*time.Millisecond)
	if got := h.rec.QueueWaitMs(); got != 250 {
		t.Errorf("QueueWaitMs = %d，期望 250", got)
	}
	// 库里那一列不可空：没排就是 0，不像 ttft 那样要区分「没有」。
	if got := h.row(t, 503).QueueWaitMs; got != 250 {
		t.Errorf("queue_wait_ms 落库 = %d，期望 250", got)
	}
}

// 凭证与端点这两格是硬约束的落点：日志与流水里出现的永远只是**名字**与路径，
// 不是凭证值也不是 base_url。这条用一次装配跑满的调用钉住。
func TestRowCarriesNamesNeverSecrets(t *testing.T) {
	h := newHarness()
	h.rec.Authenticated("gateway-key-名")
	h.rec.RequestParsed("claude-sonnet-4", true)
	h.rec.Routed("渠道甲", protocol.OpenAI, "gpt-4o", calllog.Prices{})
	h.rec.Dialing("/v1/chat/completions")
	h.rec.Attempted(2, "凭证乙", 0)
	h.rec.Summarized(protocol.Summary{InputTokens: 11, OutputTokens: 22, Model: "gpt-4o-2024-11-20"})
	h.rec.FirstByte()
	h.rec.Succeeded()

	row := h.row(t, 200)
	want := calllog.Row{
		APIKeyName:       "gateway-key-名",
		Endpoint:         "/v1/messages",
		UpstreamEndpoint: "/v1/chat/completions",
		ClientProtocol:   "anthropic",
		UpstreamProtocol: "openai",
		ModelRequested:   "claude-sonnet-4",
		ModelUpstream:    "gpt-4o",
		ChannelName:      "渠道甲",
		ChannelKeyName:   "凭证乙",
		Status:           200,
		RetryCount:       2,
		// 有 summary 就有账：未定价（Routed 给的是零值 Prices）记 0 不留 NULL
		// （口径层 §2.10，#65「有用量但未定价记 0」）。
		Cost: sql.NullFloat64{Valid: true},
	}
	got := row
	// 三个带时间的格子不参与逐字比对（它们每次都不一样），单独看落没落。
	got.TotalMs, got.IsStream, got.TTFTMs = 0, want.IsStream, want.TTFTMs
	got.InputTokens, got.OutputTokens = want.InputTokens, want.OutputTokens
	got.CacheReadTokens, got.CacheWriteTokens = want.CacheReadTokens, want.CacheWriteTokens
	if got != want {
		t.Errorf("落库的行装配不对\n实得: %+v\n期望: %+v", got, want)
	}
	if !row.IsStream.Valid || !row.IsStream.Bool || !row.TTFTMs.Valid {
		t.Errorf("流式那两格没落上：is_stream=%v ttft=%v", row.IsStream, row.TTFTMs)
	}
	if row.InputTokens.Int64 != 11 || row.OutputTokens.Int64 != 22 {
		t.Errorf("token 两格 = %v / %v", row.InputTokens, row.OutputTokens)
	}
}
