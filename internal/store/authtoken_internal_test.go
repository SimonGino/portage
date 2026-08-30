package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 消费用 DELETE ... RETURNING 一步完成——这条语法要靠驱动（modernc.org/sqlite）
// 真跑一遍才算数，这个用例同时钉住「一次性」语义与驱动支持。
func TestAuthTokenConsumeIsOneShot(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleUser)

	token, err := CreateAuthToken(ctx, db, TokenVerifyEmail, &uid, "p1", TokenTTLVerifyEmail)
	if err != nil {
		t.Fatalf("发 token: %v", err)
	}
	// 用途不对不消费：验证链接不能拿去重置密码，token 也不该被烧掉。
	if _, _, err := ConsumeAuthToken(ctx, db, token, TokenResetPassword); !errors.Is(err, ErrNotFound) {
		t.Fatalf("错用途消费 = %v，期望 ErrNotFound", err)
	}
	gotID, payload, err := ConsumeAuthToken(ctx, db, token, TokenVerifyEmail)
	if err != nil {
		t.Fatalf("消费: %v", err)
	}
	if gotID == nil || *gotID != uid || payload != "p1" {
		t.Fatalf("消费结果 = (%v, %q)，期望 (%d, p1)", gotID, payload, uid)
	}
	if _, _, err := ConsumeAuthToken(ctx, db, token, TokenVerifyEmail); !errors.Is(err, ErrNotFound) {
		t.Fatalf("二次消费 = %v，期望 ErrNotFound", err)
	}
}

func TestAuthTokenPeekDoesNotConsume(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// userID 可空——OAuth 完成注册时用户还不存在。
	token, err := CreateAuthToken(ctx, db, TokenOAuthSignup, nil, `{"email":"x"}`, TokenTTLOAuthSignup)
	if err != nil {
		t.Fatalf("发 token: %v", err)
	}
	for range 2 {
		uid, payload, err := PeekAuthToken(ctx, db, token, TokenOAuthSignup)
		if err != nil || uid != nil || payload != `{"email":"x"}` {
			t.Fatalf("Peek = (%v, %q, %v)", uid, payload, err)
		}
	}
	if _, _, err := ConsumeAuthToken(ctx, db, token, TokenOAuthSignup); err != nil {
		t.Fatalf("Peek 后消费: %v", err)
	}
}

func TestAuthTokenExpiry(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleUser)
	token, err := CreateAuthToken(ctx, db, TokenResetPassword, &uid, "", TokenTTLResetPassword)
	if err != nil {
		t.Fatalf("发 token: %v", err)
	}
	if _, err := db.Exec(`UPDATE auth_tokens SET expires_at = ?`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatalf("改过期时刻: %v", err)
	}
	// 过期行可能还没被 sweep 扫走，Peek 与 Consume 都得自己再验一次。
	if _, _, err := PeekAuthToken(ctx, db, token, TokenResetPassword); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Peek 过期 token = %v，期望 ErrNotFound", err)
	}
	if _, _, err := ConsumeAuthToken(ctx, db, token, TokenResetPassword); !errors.Is(err, ErrNotFound) {
		t.Fatalf("消费过期 token = %v，期望 ErrNotFound", err)
	}
}

func TestLastAuthTokenIssueAndDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	uid := seedUser(t, db, "a@example.com", RoleUser)

	last, err := LastAuthTokenIssue(ctx, db, TokenVerifyEmail, uid)
	if err != nil || last != 0 {
		t.Fatalf("空表 LastAuthTokenIssue = (%d, %v)，期望 (0, nil)", last, err)
	}
	t1, err := CreateAuthToken(ctx, db, TokenVerifyEmail, &uid, "", TokenTTLVerifyEmail)
	if err != nil {
		t.Fatalf("发 token: %v", err)
	}
	last, err = LastAuthTokenIssue(ctx, db, TokenVerifyEmail, uid)
	if err != nil || last == 0 {
		t.Fatalf("发过之后 LastAuthTokenIssue = (%d, %v)，期望非 0", last, err)
	}

	// 重置成功后旧链接一并作废：删同人同用途全部 token。
	t2, err := CreateAuthToken(ctx, db, TokenVerifyEmail, &uid, "", TokenTTLVerifyEmail)
	if err != nil {
		t.Fatalf("发 token: %v", err)
	}
	if err := DeleteAuthTokens(ctx, db, TokenVerifyEmail, uid); err != nil {
		t.Fatalf("删 token: %v", err)
	}
	for _, token := range []string{t1, t2} {
		if _, _, err := ConsumeAuthToken(ctx, db, token, TokenVerifyEmail); !errors.Is(err, ErrNotFound) {
			t.Fatalf("删后消费 %s = %v，期望 ErrNotFound", token, err)
		}
	}
}
