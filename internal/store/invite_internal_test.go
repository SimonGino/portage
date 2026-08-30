package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInviteCodeLifecycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	codes, err := CreateInviteCodes(ctx, db, 3, 0)
	if err != nil {
		t.Fatalf("生成邀请码: %v", err)
	}
	if len(codes) != 3 {
		t.Fatalf("生成数 = %d，期望 3", len(codes))
	}
	for _, c := range codes {
		if len(c) != 16 {
			t.Errorf("码 %q 长度 = %d，期望 16", c, len(c))
		}
	}

	uid := seedUser(t, db, "a@example.com", RoleUser)
	if err := ConsumeInviteCode(ctx, db, codes[0], uid); err != nil {
		t.Fatalf("消费邀请码: %v", err)
	}
	// 一码一人：同一个码第二次消费必须失败，哪怕换个人。
	uid2 := seedUser(t, db, "b@example.com", RoleUser)
	if err := ConsumeInviteCode(ctx, db, codes[0], uid2); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("重复消费 = %v，期望 ErrInvalidInput", err)
	}

	list, err := ListInviteCodes(ctx, db)
	if err != nil {
		t.Fatalf("列邀请码: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("列表行数 = %d，期望 3", len(list))
	}
	var used *InviteCode
	for i := range list {
		if list[i].Code == codes[0] {
			used = &list[i]
		}
	}
	if used == nil || used.UsedByEmail != "a@example.com" || used.UsedAt == "" {
		t.Fatalf("已用码的记录不对: %+v", used)
	}
}

func TestInviteCodeRevoke(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	codes, err := CreateInviteCodes(ctx, db, 2, 0)
	if err != nil {
		t.Fatalf("生成邀请码: %v", err)
	}
	list, err := ListInviteCodes(ctx, db)
	if err != nil {
		t.Fatalf("列邀请码: %v", err)
	}
	byCode := map[string]int64{}
	for _, c := range list {
		byCode[c.Code] = c.ID
	}

	// 未用的码可撤销，撤销后消费不进来。
	if err := RevokeInviteCode(ctx, db, byCode[codes[0]]); err != nil {
		t.Fatalf("撤销未用码: %v", err)
	}
	uid := seedUser(t, db, "a@example.com", RoleUser)
	if err := ConsumeInviteCode(ctx, db, codes[0], uid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("消费已撤销码 = %v，期望 ErrInvalidInput", err)
	}

	// 已用的码拒绝撤销——「谁用的」这条记录要留着。
	if err := ConsumeInviteCode(ctx, db, codes[1], uid); err != nil {
		t.Fatalf("消费: %v", err)
	}
	if err := RevokeInviteCode(ctx, db, byCode[codes[1]]); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("撤销已用码 = %v，期望 ErrInvalidInput", err)
	}
	if err := RevokeInviteCode(ctx, db, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("撤销不存在的码 = %v，期望 ErrNotFound", err)
	}
}

func TestInviteCodeExpiry(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	codes, err := CreateInviteCodes(ctx, db, 1, time.Hour)
	if err != nil {
		t.Fatalf("生成邀请码: %v", err)
	}
	// 把过期时刻拨到过去——比 sleep 稳，也不用等。
	if _, err := db.Exec(`UPDATE invite_codes SET expires_at = ?`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatalf("改过期时刻: %v", err)
	}
	uid := seedUser(t, db, "a@example.com", RoleUser)
	if err := ConsumeInviteCode(ctx, db, codes[0], uid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("消费过期码 = %v，期望 ErrInvalidInput", err)
	}
}
