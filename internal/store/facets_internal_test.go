package store

import (
	"context"
	"testing"
)

// 取值域的三条约定（#57）：最近出现的在前、空模型名归到 UnknownModelLabel 那一档、
// forUser 只看本人。这三条前端都直接依赖——顺序决定下拉的排列，哨兵决定
// CallLogFilter.Model 能不能筛到「(未记录模型)」，归属决定用户侧看不到别人的模型名。
func TestListCallLogFacets(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO call_logs (api_key_name, client_protocol,
		upstream_protocol, model_requested, model_upstream, channel_name, status, total_ms, user_id)
		VALUES ('k', 'openai', 'openai', 'old',   'x', 'ch', 200, 1, 1),
		       ('k', 'openai', 'openai', '',      'x', 'ch', 200, 1, 1),
		       ('k', 'openai', 'openai', 'other', 'x', 'ch', 200, 1, 2),
		       ('k', 'openai', 'openai', 'new',   'x', 'ch', 200, 1, 1),
		       ('k', 'openai', 'openai', 'old',   'x', 'ch', 200, 1, 1)`); err != nil {
		t.Fatalf("插流水: %v", err)
	}
	ctx := context.Background()

	all, err := ListCallLogFacets(ctx, db, 0)
	if err != nil {
		t.Fatalf("ListCallLogFacets: %v", err)
	}
	want := []string{"old", "new", "other", UnknownModelLabel}
	if got := all.Models; !equalStrings(got, want) {
		t.Fatalf("管理端取值域 = %v，想要 %v（最近出现的在前）", got, want)
	}

	mine, err := ListCallLogFacets(ctx, db, 1)
	if err != nil {
		t.Fatalf("ListCallLogFacets(forUser): %v", err)
	}
	want = []string{"old", "new", UnknownModelLabel}
	if got := mine.Models; !equalStrings(got, want) {
		t.Fatalf("用户侧取值域 = %v，想要 %v", got, want)
	}

	// 空表回空切片而不是 nil：JSON 里得是 []，前端按数组读。
	empty, err := ListCallLogFacets(ctx, db, 99)
	if err != nil {
		t.Fatalf("ListCallLogFacets(空): %v", err)
	}
	if empty.Models == nil || len(empty.Models) != 0 {
		t.Fatalf("空取值域 = %#v，想要空切片", empty.Models)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
