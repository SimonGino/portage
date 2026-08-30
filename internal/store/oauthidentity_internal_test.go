package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// isConstraintText 与 admin 包的 isConstraint 同款文本匹配——store 不导出那个
// 判定，测试只需要认出「这确实是约束冲突而不是别的错」。
func isConstraintText(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "CONSTRAINT")
}

func TestOAuthIdentityLinkFindUnlink(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleUser)

	// 没绑过是正常一态，不是错误。
	if id, ok, err := FindOAuthUser(ctx, db, "github", "42"); id != 0 || ok || err != nil {
		t.Fatalf("空表 Find = (%d, %v, %v)，期望 (0, false, nil)", id, ok, err)
	}

	if err := LinkOAuthIdentity(ctx, db, uid, "github", "42"); err != nil {
		t.Fatalf("绑定: %v", err)
	}
	if id, ok, err := FindOAuthUser(ctx, db, "github", "42"); id != uid || !ok || err != nil {
		t.Fatalf("Find = (%d, %v, %v)，期望 (%d, true, nil)", id, ok, err, uid)
	}
	// 同家 provider 不同上游账号是另一个身份，不该撞上一条。
	if id, ok, _ := FindOAuthUser(ctx, db, "github", "43"); id != 0 || ok {
		t.Fatalf("不同 provider_user_id 撞到了已有绑定")
	}

	if err := UnlinkOAuthIdentity(ctx, db, uid, "github"); err != nil {
		t.Fatalf("解绑: %v", err)
	}
	if err := UnlinkOAuthIdentity(ctx, db, uid, "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复解绑 = %v，期望 ErrNotFound", err)
	}
}

func TestOAuthIdentityUniqueness(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleUser)
	uid2 := seedUser(t, db, "b@example.com", RoleUser)
	if err := LinkOAuthIdentity(ctx, db, uid, "github", "42"); err != nil {
		t.Fatalf("绑定: %v", err)
	}

	// 同一上游账号不挂两人。
	if err := LinkOAuthIdentity(ctx, db, uid2, "github", "42"); err == nil || !isConstraintText(err) {
		t.Fatalf("同上游账号挂第二人 = %v，期望约束冲突", err)
	}
	// 同一用户同家 provider 不挂两个上游账号。
	if err := LinkOAuthIdentity(ctx, db, uid, "github", "43"); err == nil || !isConstraintText(err) {
		t.Fatalf("同用户同 provider 挂第二个 = %v，期望约束冲突", err)
	}

	if err := LinkOAuthIdentity(ctx, db, uid, "google", "g-1"); err != nil {
		t.Fatalf("换家 provider 绑定: %v", err)
	}
	list, err := ListOAuthIdentities(ctx, db, uid)
	if err != nil {
		t.Fatalf("列绑定: %v", err)
	}
	if len(list) != 2 || list[0].Provider != "github" || list[1].Provider != "google" {
		t.Fatalf("绑定列表 = %+v，期望 github、google 各一", list)
	}
}
