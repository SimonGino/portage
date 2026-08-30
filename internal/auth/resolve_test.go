package auth_test

// Resolve 的 LEFT JOIN users 用例（#73，展开层 §7.10「停用用户」条）：无主 key 照常
// 通过、归属 key 带回 user_id、停用用户的全部 key 当场失效——最后这条是热路径联查
// 而不是「停用时批量 disable key」，所以停用必须**不重启不发新请求**就生效，用例里
// 就是同一个库上 UPDATE 完立刻再 Resolve 一次。
//
// 放在 auth_test 而不是 auth：要一个真库（store.Open 的 schema + migrate），而 auth
// 本体只依赖 database/sql——依赖方向不该为测试反过来。

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/store"
)

func TestResolveJoinsUsers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	res, err := db.Exec(`INSERT INTO users (email) VALUES ('alice@x')`)
	if err != nil {
		t.Fatalf("种用户: %v", err)
	}
	aliceID, _ := res.LastInsertId()
	for _, k := range []struct {
		name, plain string
		owner       any
		disabled    int
	}{
		{"无主的", "sk-ptg-orphan", nil, 0},
		{"alice 的", "sk-ptg-alice", aliceID, 0},
		{"停用的", "sk-ptg-off", nil, 1},
	} {
		if _, err := db.Exec(
			`INSERT INTO api_keys (name, key_hash, user_id, disabled) VALUES (?,?,?,?)`,
			k.name, auth.Hash(k.plain), k.owner, k.disabled); err != nil {
			t.Fatalf("种 key %s: %v", k.name, err)
		}
	}
	present := func(plain string) http.Header {
		h := http.Header{}
		h.Set("x-api-key", plain)
		return h
	}

	// 无主 key 照常通过（声明形态的合法形态），UserID 为 nil——不入用户账不受配额。
	k, err := auth.Resolve(t.Context(), db, present("sk-ptg-orphan"))
	if err != nil {
		t.Fatalf("无主 key 该通过: %v", err)
	}
	if k.UserID != nil {
		t.Errorf("无主 key 的 UserID = %d，期望 nil", *k.UserID)
	}

	// 归属 key 通过且带回 user_id（#75 的流水维度、#74 的配额闸都从这里取）。
	k, err = auth.Resolve(t.Context(), db, present("sk-ptg-alice"))
	if err != nil {
		t.Fatalf("alice 的 key 该通过: %v", err)
	}
	if k.UserID == nil || *k.UserID != aliceID {
		t.Errorf("UserID = %v，期望 %d", k.UserID, aliceID)
	}

	// 停用的 key 照旧不认，与 key 不存在同一个错误——不给扫描者分辨的余地。
	if _, err := auth.Resolve(t.Context(), db, present("sk-ptg-off")); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("停用 key 期望 ErrUnauthorized，得到 %v", err)
	}

	// 停用用户：不重启、不等任何缓存，下一个请求就拒——这正是「热路径联查」买来的。
	if _, err := db.Exec(`UPDATE users SET disabled = 1 WHERE id = ?`, aliceID); err != nil {
		t.Fatalf("停用用户: %v", err)
	}
	if _, err := auth.Resolve(t.Context(), db, present("sk-ptg-alice")); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("停用用户的 key 期望 ErrUnauthorized，得到 %v", err)
	}
	// 重新启用即恢复：停用是冻结不是删除，key 一把没动。
	if _, err := db.Exec(`UPDATE users SET disabled = 0 WHERE id = ?`, aliceID); err != nil {
		t.Fatalf("恢复用户: %v", err)
	}
	if _, err := auth.Resolve(t.Context(), db, present("sk-ptg-alice")); err != nil {
		t.Errorf("恢复后该通过: %v", err)
	}
}
