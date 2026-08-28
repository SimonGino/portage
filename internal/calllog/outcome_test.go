package calllog_test

import (
	"database/sql"
	"testing"

	"github.com/SimonGino/portage/internal/calllog"
)

// words 是词表全集，**11 个词**（CONTEXT.md「outcome 词表」，口径层 v0.70 定 10 词、
// v0.99 加 request_too_large），哨兵 ok 不在其中。加第 12 个词要同步这里——下面几条
// 断言就是靠它变成必答题的。
var words = []calllog.Outcome{
	calllog.UpstreamError,
	calllog.StreamAborted,
	calllog.Unauthorized,
	calllog.Rejected,
	calllog.QueueFull,
	calllog.QueueTimeout,
	calllog.QueueAbandoned,
	calllog.CompactionUnsupported,
	calllog.ModelNotAllowed,
	calllog.RateLimited,
	calllog.RequestTooLarge,
}

// 词表是 11 个词。数字写死在这里，是为了让「悄悄多一个/少一个」当场红——
// 这份词表与 CONTEXT.md 的词条、口径层 v0.70/v0.99 是同一件事，改一边就得改另一边。
func TestOutcomeVocabularyHasElevenWords(t *testing.T) {
	if len(words) != 11 {
		t.Fatalf("词表有 %d 个词, 期望 11", len(words))
	}
	seen := map[calllog.Outcome]bool{}
	for _, w := range words {
		if seen[w] {
			t.Errorf("词表里 %q 重复了", w)
		}
		seen[w] = true
	}
	if seen[calllog.OK] {
		t.Error("ok 是哨兵，不是第 12 个词")
	}
}

// String 就是落进库与 slog 的那串字节，逐字钉住：这些字符串是外部契约
// （管理端按它 group by、口径层文档逐字列了它们），改名不是重构。
func TestOutcomeStringIsTheWireWord(t *testing.T) {
	for word, want := range map[calllog.Outcome]string{
		calllog.OK:                    "ok",
		calllog.UpstreamError:         "upstream_error",
		calllog.StreamAborted:         "stream_aborted",
		calllog.Unauthorized:          "unauthorized",
		calllog.Rejected:              "rejected",
		calllog.QueueFull:             "queue_full",
		calllog.QueueTimeout:          "queue_timeout",
		calllog.QueueAbandoned:        "queue_abandoned",
		calllog.CompactionUnsupported: "compaction_unsupported",
		calllog.ModelNotAllowed:       "model_not_allowed",
		calllog.RateLimited:           "rate_limited",
		calllog.RequestTooLarge:       "request_too_large",
	} {
		if got := word.String(); got != want {
			t.Errorf("String() = %q, 期望 %q", got, want)
		}
	}
}

// 写向：ok 落 NULL，其余落词本身。这是 `error` 列可空性的唯一定义处——
// store 那边的注释只是指过来的路牌。
func TestOnlyOKColumnIsNull(t *testing.T) {
	if col := calllog.OK.Column(); col.Valid {
		t.Errorf("ok 的 Column() = %q, 哨兵不落库，期望 NULL", col.String)
	}
	for _, w := range words {
		col := w.Column()
		if !col.Valid {
			t.Errorf("%q 的 Column() 是 NULL, 词表里的词都要落库", w)
			continue
		}
		if col.String != w.String() {
			t.Errorf("%q 的 Column() = %q, 落库的就该是词本身", w, col.String)
		}
	}
}

// 读向：NULL → 空串，有值 → 原样。与 Column 互为一对，两个方向合起来才是
// 「NULL 即这行没有错误词」这条规则的全部。
func TestErrorWordReadsNullAsEmpty(t *testing.T) {
	if got := calllog.ErrorWord(sql.NullString{}); got != "" {
		t.Errorf("ErrorWord(NULL) = %q, 期望空串", got)
	}
	for _, w := range words {
		if got := calllog.ErrorWord(w.Column()); got != w.String() {
			t.Errorf("ErrorWord(%q 的列值) = %q, 期望 %q", w, got, w)
		}
	}
	// 老行、手工改过的行可能有词表外的字节：读侧原样给出去，不假装类型收窄成立。
	if got := calllog.ErrorWord(sql.NullString{String: "某个词表外的词", Valid: true}); got != "某个词表外的词" {
		t.Errorf("ErrorWord(词表外) = %q, 读侧该原样给出", got)
	}
}

// 每个词**恰好**属于两半中的一半。加第 11 个词时这条会红，「它属于哪一半」
// 于是变成必答题——那正是本次把词表收进 module 想买到的东西。
func TestEveryWordBelongsToExactlyOneHalf(t *testing.T) {
	for _, w := range words {
		switch {
		case w.Refusal() && w.Failure():
			t.Errorf("%q 同时算回绝与失败", w)
		case !w.Refusal() && !w.Failure():
			t.Errorf("%q 两半都不属于——新加的词要先回答「它属于哪一半」", w)
		}
	}
	// 哨兵在两半之外：它既没被回绝也没失败。
	if calllog.OK.Refusal() || calllog.OK.Failure() {
		t.Error("ok 是哨兵，不属于任何一半")
	}
}

// 分半的判据是**有没有真的向上游发起过**，与「客户端拿到几开头的状态码」无关。
// 逐词钉住，免得哪天有人按状态码重新分一遍。
func TestHalvesSplitOnWhetherUpstreamWasDialed(t *testing.T) {
	for _, w := range []calllog.Outcome{
		calllog.Unauthorized, calllog.Rejected, calllog.ModelNotAllowed,
		calllog.RateLimited, calllog.CompactionUnsupported,
		calllog.QueueFull, calllog.QueueTimeout, calllog.QueueAbandoned,
		calllog.RequestTooLarge,
	} {
		if !w.Refusal() {
			t.Errorf("%q 该算回绝：这一档一个字节都没到上游", w)
		}
	}
	for _, w := range []calllog.Outcome{calllog.UpstreamError, calllog.StreamAborted} {
		if !w.Failure() {
			t.Errorf("%q 该算失败：这一档真的向上游发起过", w)
		}
	}
}
