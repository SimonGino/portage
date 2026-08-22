package store

import (
	"context"
	"testing"

	_ "modernc.org/sqlite"
)

// 协议集不含 openai_responses 时，两个 Responses 专属能力位归位默认值（#33）。
//
// 真机撞出来的形态：渠道勾着 supports_compaction=1，随后在渠道页取消 Responses 协议
// 保存——前端此后不再渲染也不再提交这两个字段，「nil = 整列不写」会让旧值留在库里，
// 哪天协议勾回来它原样复活。compaction 残留 1 正是 v0.54 ⑨ 要杀的那格：上游忽略
// compaction_trigger，Codex 收 0 个 compaction item 即 Fatal，不重试不降级。

// openTestDB 复用 modelprotocols_internal_test.go 里的那份。

func readBits(t *testing.T, db Queryer, id int64) (compaction, stateful int) {
	t.Helper()
	err := db.QueryRowContext(context.Background(),
		`SELECT supports_compaction, supports_stateful_responses FROM channels WHERE id = ?`, id).
		Scan(&compaction, &stateful)
	if err != nil {
		t.Fatalf("读能力位失败: %v", err)
	}
	return compaction, stateful
}

func boolPtr(v bool) *bool { return &v }

func TestUpdateChannelResetsResponsesBitsWhenProtocolDropped(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	// 起点：勾了 Responses 且两位都拨离默认值（compaction 默认 0 这里是 1，
	// stateful 默认 1 这里是 0）——归位断言要能同时看见两个方向。
	id, err := CreateChannel(ctx, db, ChannelInput{
		Name:                      "qwen",
		BaseURLs:                  BaseURLs{OpenAI: "https://x", OpenAIResponses: "https://x"},
		SupportsCompaction:        boolPtr(true),
		SupportsStatefulResponses: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}

	// 取消 Responses 协议保存，请求体不带两个位字段——前端真实的提交形态。
	err = UpdateChannel(ctx, db, id, ChannelInput{
		Name:     "qwen",
		BaseURLs: BaseURLs{OpenAI: "https://x"},
	})
	if err != nil {
		t.Fatalf("改渠道失败: %v", err)
	}

	compaction, stateful := readBits(t, db, id)
	if compaction != 0 {
		t.Errorf("supports_compaction = %d, 期望 0：协议不含 Responses 时残留 1 会在勾回协议后复活（v0.54 ⑨）", compaction)
	}
	if stateful != 1 {
		t.Errorf("supports_stateful_responses = %d, 期望 1：归位方向与 compaction 位相反（v0.88 ②）", stateful)
	}
}

func TestUpdateChannelKeepsBitsWhenResponsesStays(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id, err := CreateChannel(ctx, db, ChannelInput{
		Name:                      "qwen",
		BaseURLs:                  BaseURLs{OpenAI: "https://x", OpenAIResponses: "https://x"},
		SupportsCompaction:        boolPtr(true),
		SupportsStatefulResponses: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}

	// 协议仍含 Responses、请求不带位字段：「nil = 整列不写」必须原样保住，
	// 不能把 #33 的归位改成对这条路也生效。
	err = UpdateChannel(ctx, db, id, ChannelInput{
		Name:     "qwen",
		BaseURLs: BaseURLs{OpenAI: "https://y", OpenAIResponses: "https://y"},
	})
	if err != nil {
		t.Fatalf("改渠道失败: %v", err)
	}

	compaction, stateful := readBits(t, db, id)
	if compaction != 1 || stateful != 0 {
		t.Errorf("能力位 = (%d, %d), 期望原样 (1, 0)：协议含 Responses 且缺省字段时整列不写", compaction, stateful)
	}
}

func TestCreateChannelIgnoresResponsesBitsWithoutProtocol(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	// 协议不含 Responses 而请求体带着非默认的位值——UI 造不出这种请求，直接打
	// API 能：不变式在后端，不信请求体。
	id, err := CreateChannel(ctx, db, ChannelInput{
		Name:                      "plain",
		BaseURLs:                  BaseURLs{OpenAI: "https://x"},
		SupportsCompaction:        boolPtr(true),
		SupportsStatefulResponses: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}

	compaction, stateful := readBits(t, db, id)
	if compaction != 0 {
		t.Errorf("supports_compaction = %d, 期望 0（协议不含 Responses，请求体的 true 不作数）", compaction)
	}
	if stateful != 1 {
		t.Errorf("supports_stateful_responses = %d, 期望 1（同上，落默认值）", stateful)
	}
}
