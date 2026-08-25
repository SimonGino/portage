package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// 这组用例钉「候选可用」谓词的停用/凭证边界（#50）：渠道启用 ∧ 模型启用 ∧
// ≥1 启用凭证 ∧ weight>0，在 Resolve 两条路径、/v1/models 直连半边与启动闸
// checkCandidateReachable 上必须判成一致。协议交集那一项已有用例
// （modelprotocols_internal_test.go），这里不重复。

// unusableMutations 是把 seedChannel 灌出的健康配置改成「有但用不了」的三种改法。
var unusableMutations = []struct{ name, sql string }{
	{"渠道停用", `UPDATE channels SET disabled = 1`},
	{"纳管模型停用", `UPDATE channel_models SET disabled = 1`},
	{"凭证全停用", `UPDATE channel_keys SET disabled = 1`},
}

func TestResolveUnusableEdgesOnBothPaths(t *testing.T) {
	for _, path := range []struct{ name, model string }{
		{"接入点", "ap"},
		{"限定名直连", "ch/gpt-4o"},
	} {
		for _, m := range unusableMutations {
			t.Run(path.name+"/"+m.name, func(t *testing.T) {
				db := openTestDB(t)
				seedChannel(t, db, "openai", "")
				if _, err := db.Exec(m.sql); err != nil {
					t.Fatalf("改库: %v", err)
				}
				_, err := Resolve(context.Background(), db, path.model, protocol.OpenAI)
				if !errors.Is(err, ErrNoUsableCandidate) {
					t.Fatalf("err = %v，期望 ErrNoUsableCandidate", err)
				}
			})
		}
	}
}

// weight 归零只存在于接入点路径——直连没有 candidates 行，谓词里没有这一项。
func TestResolveZeroWeightIsNoUsableCandidate(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	if _, err := db.Exec(`UPDATE candidates SET weight = 0`); err != nil {
		t.Fatalf("改库: %v", err)
	}
	_, err := Resolve(context.Background(), db, "ap", protocol.OpenAI)
	if !errors.Is(err, ErrNoUsableCandidate) {
		t.Fatalf("err = %v，期望 ErrNoUsableCandidate", err)
	}
}

// 「没有这个名字」与「有但用不了」分档：前者 404 后者 503，混了会把配置问题
// 报成名字打错。
func TestResolveUnknownDirectNameIsNotFound(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "openai", "")
	_, err := Resolve(context.Background(), db, "ch/nope", protocol.OpenAI)
	if !errors.Is(err, ErrAccessPointNotFound) {
		t.Fatalf("err = %v，期望 ErrAccessPointNotFound", err)
	}
}

// /v1/models 直连半边与 Resolve 同谓词：有但用不了的限定名不列。接入点半边
// **故意不判这三项**——渠道停用/凭证归零归启动闸管（v0.38 二修），运行期只过滤
// 协议交集；这里把两半边的分工一起钉住，防止日后收敛时顺手「统一」掉。
func TestListExposedModelsHidesUnusableDirectNameKeepsAccessPoint(t *testing.T) {
	for _, m := range unusableMutations {
		t.Run(m.name, func(t *testing.T) {
			db := openTestDB(t)
			seedChannel(t, db, "openai", "")
			if _, err := db.Exec(m.sql); err != nil {
				t.Fatalf("改库: %v", err)
			}
			models, err := ListExposedModels(context.Background(), db)
			if err != nil {
				t.Fatalf("列清单: %v", err)
			}
			var hasAP, hasDirect bool
			for _, mm := range models {
				switch mm.ID {
				case "ap":
					hasAP = true
				case "ch/gpt-4o":
					hasDirect = true
				}
			}
			if hasDirect {
				t.Errorf("限定名 ch/gpt-4o 仍在清单里，期望隐藏")
			}
			if !hasAP {
				t.Errorf("接入点 ap 不在清单里，期望照列（启动闸的辖区）")
			}
		})
	}
}

// 启动闸 checkCandidateReachable 与 Resolve 判同一份谓词：三种「有但用不了」
// 各报各的原因。
func TestCheckCandidateReachableFlagsEachCause(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"渠道停用", `UPDATE channels SET disabled = 1`, "该渠道已停用"},
		{"纳管模型停用", `UPDATE channel_models SET disabled = 1`, "该纳管模型已停用"},
		{"凭证全停用", `UPDATE channel_keys SET disabled = 1`, "没有启用凭证"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			seedChannel(t, db, "openai", "")
			if _, err := db.Exec(tc.sql); err != nil {
				t.Fatalf("改库: %v", err)
			}
			found, err := checkCandidateReachable(context.Background(), db)
			if err != nil {
				t.Fatalf("检查: %v", err)
			}
			if len(found) != 1 || !strings.Contains(found[0], tc.want) {
				t.Fatalf("found = %q，期望恰一条且含 %q", found, tc.want)
			}
		})
	}

	t.Run("健康配置不报", func(t *testing.T) {
		db := openTestDB(t)
		seedChannel(t, db, "openai", "")
		found, err := checkCandidateReachable(context.Background(), db)
		if err != nil {
			t.Fatalf("检查: %v", err)
		}
		if len(found) != 0 {
			t.Fatalf("found = %q，期望空", found)
		}
	})
}
