package declcfg_test

// 声明形态 × 多用户的互斥闸用例（#66/#73，展开层 §7.10.1）：apply 建的 key 无主、
// 认领后的归属在覆盖时保留、文件外的用户 key 照删、多用户库导出拒绝并点名、
// 往返闸在无主与「第一个 admin 名下」两种库上都成立。

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/declcfg"
)

const twoKeysYAML = `
channels:
  - name: qwen
    base_url:
      openai: https://qwen.example.internal
    credentials:
      - name: 主号
        credential: sk-upstream-1
    models:
      - upstream_model: Qwen3-27B
api_keys:
  - name: laptop
    key: sk-ptg-multiuser-a
  - name: ci
    key: sk-ptg-multiuser-b
`

// seedUser 直接落一行用户。declcfg 不管用户表，但闸的判据全在它身上。
func seedUser(t *testing.T, db *sql.DB, email, role string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (email, role) VALUES (?, ?)`, email, role)
	if err != nil {
		t.Fatalf("种用户: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("读用户 id: %v", err)
	}
	return id
}

func keyOwner(t *testing.T, db *sql.DB, name string) (int64, bool) {
	t.Helper()
	var owner sql.NullInt64
	if err := db.QueryRow(`SELECT user_id FROM api_keys WHERE name = ?`, name).Scan(&owner); err != nil {
		t.Fatalf("读 key %q 归属: %v", name, err)
	}
	return owner.Int64, owner.Valid
}

// apply 建的 key user_id 落 NULL（#66：声明文件表达不了归属）；被启动认领成第一个
// admin 的 key，再次 apply 时**归属与 id 都不动**——归属是运行期状态不进文件，与
// 凭证的停用现场同一条纪律，否则每次重启都白改一轮。
func TestApplyCreatesUnownedKeysAndKeepsClaimedOwner(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, twoKeysYAML)
	if owner, ok := keyOwner(t, db, "laptop"); ok {
		t.Fatalf("apply 建的 key 该无主，user_id=%d", owner)
	}

	adminID := seedUser(t, db, "admin@localhost", "admin")
	var id0 int64
	if err := db.QueryRow(`SELECT id FROM api_keys WHERE name = 'laptop'`).Scan(&id0); err != nil {
		t.Fatalf("读 id: %v", err)
	}
	// 模拟启动认领（认领本体在 store.migrate，有自己的用例）。
	if _, err := db.Exec(`UPDATE api_keys SET user_id = ?`, adminID); err != nil {
		t.Fatalf("认领: %v", err)
	}

	mustApply(t, db, twoKeysYAML)
	owner, ok := keyOwner(t, db, "laptop")
	if !ok || owner != adminID {
		t.Errorf("覆盖后归属变了：ok=%v owner=%d，期望仍是 admin %d", ok, owner, adminID)
	}
	var id1 int64
	if err := db.QueryRow(`SELECT id FROM api_keys WHERE name = 'laptop'`).Scan(&id1); err != nil {
		t.Fatalf("读 id: %v", err)
	}
	if id1 != id0 {
		t.Errorf("覆盖后 id 从 %d 变成 %d——对齐该按名改行，不是删了重建", id0, id1)
	}
}

// 文件外的 key 照删、**含用户名下的 key**（#66 ③）：挂载是显式切事实源动作，文件即
// 事实源纪律不为用户 key 开豁免。
func TestApplyDeletesUserKeysOutsideFile(t *testing.T) {
	db := openDB(t)
	seedUser(t, db, "admin@localhost", "admin")
	bobID := seedUser(t, db, "bob@x", "user")
	if _, err := db.Exec(
		`INSERT INTO api_keys (name, key_hash, key_plain, user_id) VALUES ('bob 的', 'h-bob', 'sk-ptg-bob', ?)`,
		bobID); err != nil {
		t.Fatalf("种 bob 的 key: %v", err)
	}

	f, err := declcfg.Parse([]byte(twoKeysYAML), "test.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	changes, err := declcfg.Apply(context.Background(), db, f, discardLogger())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE name = 'bob 的'`).Scan(&n); err != nil {
		t.Fatalf("数行: %v", err)
	}
	if n != 0 {
		t.Error("bob 的 key 还在——文件外的 key 该删，用户 key 不豁免")
	}
	if !strings.Contains(strings.Join(changes, "；"), "删除 API Key bob 的") {
		t.Errorf("变更清单没报这次删除：%v", changes)
	}
}

// 导出闸（#66 ④）：库里存在第一个 admin 之外的用户名下的 key → 拒绝导出并点名；
// 只有第一个 admin 与无主 key 的库照常导出。
func TestExportRefusesMultiUserDBNamingKeys(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, twoKeysYAML)
	adminID := seedUser(t, db, "admin@localhost", "admin")
	bobID := seedUser(t, db, "bob@x", "user")
	// laptop 归第一个 admin（合法）、ci 归 bob（违规），再留一把无主的。
	if _, err := db.Exec(`UPDATE api_keys SET user_id = ? WHERE name = 'laptop'`, adminID); err != nil {
		t.Fatalf("归属 laptop: %v", err)
	}
	if _, err := db.Exec(`UPDATE api_keys SET user_id = ? WHERE name = 'ci'`, bobID); err != nil {
		t.Fatalf("归属 ci: %v", err)
	}

	_, err := declcfg.Export(context.Background(), db)
	if err == nil {
		t.Fatal("多用户库该拒绝导出")
	}
	for _, want := range []string{"ci", "bob@x", "不支持多用户"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒绝导出的报错没点到 %q：%v", want, err)
		}
	}

	// 把 bob 的那把删掉，admin 名下 + 无主的组合照常导出。
	if _, err := db.Exec(`DELETE FROM api_keys WHERE name = 'ci'`); err != nil {
		t.Fatalf("删 bob 的 key: %v", err)
	}
	if _, err := declcfg.Export(context.Background(), db); err != nil {
		t.Errorf("单用户库该能导出: %v", err)
	}
}

// 往返闸的多用户变体：key 全归第一个 admin 的库，导出 → 空库 apply → 再导出，字节
// 相等——归属不进文件，于是「认领过的库」与「悬空的库」导出的必须是同一份文件。
// 全无主那半由 TestExportRoundtripsByteForByte 钉着（它的库从头到尾没有用户）。
func TestRoundtripHoldsWithFirstAdminOwnedKeys(t *testing.T) {
	db := openDB(t)
	applyFile(t, db, roundtripFixture())
	adminID := seedUser(t, db, "admin@localhost", "admin")
	if _, err := db.Exec(`UPDATE api_keys SET user_id = ?`, adminID); err != nil {
		t.Fatalf("认领: %v", err)
	}
	first := mustExport(t, db)

	db2 := openDB(t)
	f, err := declcfg.Parse(first, "roundtrip.yaml")
	if err != nil {
		t.Fatalf("解析导出物: %v", err)
	}
	applyFile(t, db2, f)
	second := mustExport(t, db2)
	if !bytes.Equal(first, second) {
		t.Error("认领过的库与空库 apply 后的导出物字节不等——归属漏进了导出物，或对齐动了不该动的列")
	}
}
