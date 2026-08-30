package store

// 任免与停用的守门条件（#72 补裁）：最后一个启用的 admin 不许降级、不许停用——
// 治理面只有 admin 进得来，最后一个拿掉等于把整套用户治理锁死。

import (
	"context"
	"errors"
	"testing"
)

func TestSetUserRoleGuardsLastAdmin(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	admin := seedUser(t, db, "a@example.com", RoleAdmin)
	other := seedUser(t, db, "b@example.com", RoleUser)

	// 只剩一个启用的 admin：降级被拦，角色原样。
	if err := SetUserRole(ctx, db, admin, RoleUser); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("降级最后一个 admin = %v，期望 ErrInvalidInput", err)
	}
	if u, _ := GetUser(ctx, db, admin); u.Role != RoleAdmin {
		t.Fatalf("被拦的降级不该真的改了角色：%+v", u)
	}

	// 升一个上来之后，原 admin 可以降——多 admin 允许，交接是常态。
	if err := SetUserRole(ctx, db, other, RoleAdmin); err != nil {
		t.Fatalf("升级: %v", err)
	}
	if err := SetUserRole(ctx, db, admin, RoleUser); err != nil {
		t.Fatalf("有替补后降级: %v", err)
	}

	if err := SetUserRole(ctx, db, other, "root"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("野角色 = %v，期望 ErrInvalidInput", err)
	}
	if err := SetUserRole(ctx, db, 9999, RoleUser); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的用户 = %v，期望 ErrNotFound", err)
	}
}

func TestSetUserDisabledGuardsLastAdmin(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	admin := seedUser(t, db, "a@example.com", RoleAdmin)
	user := seedUser(t, db, "b@example.com", RoleUser)

	// 停用最后一个启用的 admin 与降级同罪。
	if err := SetUserDisabled(ctx, db, admin, true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("停用最后一个 admin = %v，期望 ErrInvalidInput", err)
	}

	// 普通用户随便停、随便启。
	if err := SetUserDisabled(ctx, db, user, true); err != nil {
		t.Fatalf("停用普通用户: %v", err)
	}
	if u, _ := GetUser(ctx, db, user); !u.Disabled {
		t.Fatal("停用没有落库")
	}
	if err := SetUserDisabled(ctx, db, user, false); err != nil {
		t.Fatalf("启用: %v", err)
	}

	// 已停用的 admin 不算「启用的 admin」：second 升上来又停掉后，唯一还启用的
	// admin 同样受保护——守门条件数的是启用数，不是头衔数。
	if err := SetUserRole(ctx, db, user, RoleAdmin); err != nil {
		t.Fatalf("升级: %v", err)
	}
	if err := SetUserDisabled(ctx, db, user, true); err != nil {
		t.Fatalf("停用替补 admin: %v", err)
	}
	if err := SetUserDisabled(ctx, db, admin, true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("替补已停用时停用原 admin = %v，期望 ErrInvalidInput", err)
	}
	if err := SetUserRole(ctx, db, admin, RoleUser); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("替补已停用时降级原 admin = %v，期望 ErrInvalidInput", err)
	}

	if err := SetUserDisabled(ctx, db, 9999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的用户 = %v，期望 ErrNotFound", err)
	}
}
