package declcfg_test

// 声明形态 × 多用户的互斥用例（#66/#73，展开层 §7.10.1；导出半边口径层 v1.12 放宽）：
// apply 建的 key 无主、认领后的归属在覆盖时保留、文件外的用户 key 照删、多用户库导出
// **跳过**用户 key 并点名（导入闸照拒并点名）、往返闸在无主与「第一个 admin 名下」
// 两种库上都成立。

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

// 导出对用户 key 的处置（口径层 v1.12，取代 #66 ④ 的拒绝闸）：非第一个 admin
// 名下的 key **不进文件**、名单回给调用方点名；第一个 admin 与无主 key 照常导出。
// 同名不同主（#73 的 UNIQUE(user_id, name) 允许）也不压平——文件里的 laptop 就是
// admin 那一把。导入闸（CheckSingleUser）原样保留：同库上仍拒绝并点名。
func TestExportSkipsKeysOwnedByOtherUsers(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, twoKeysYAML)
	adminID := seedUser(t, db, "admin@localhost", "admin")
	bobID := seedUser(t, db, "bob@x", "user")
	// laptop 归第一个 admin（照常导出）、ci 归 bob（跳过）。
	if _, err := db.Exec(`UPDATE api_keys SET user_id = ? WHERE name = 'laptop'`, adminID); err != nil {
		t.Fatalf("归属 laptop: %v", err)
	}
	if _, err := db.Exec(`UPDATE api_keys SET user_id = ? WHERE name = 'ci'`, bobID); err != nil {
		t.Fatalf("归属 ci: %v", err)
	}

	raw, skipped, err := declcfg.Export(context.Background(), db)
	if err != nil {
		t.Fatalf("多用户库该能导出（用户 key 跳过而不是拒绝）: %v", err)
	}
	if !strings.Contains(string(raw), "sk-ptg-multiuser-a") {
		t.Error("第一个 admin 名下的 key 该照常导出")
	}
	for _, want := range []string{"ci", "sk-ptg-multiuser-b"} {
		if strings.Contains(string(raw), want) {
			t.Errorf("导出物里不该有用户名下的 %q", want)
		}
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "ci") || !strings.Contains(skipped[0], "bob@x") {
		t.Errorf("跳过名单该点名 %q（%s），实得 %v", "ci", "bob@x", skipped)
	}

	// 同名不同主：bob 也有一把叫 laptop 的。文件里的 laptop 必须是 admin 那把，
	// bob 的进跳过名单——#66 ④ 当年拒导出的「撞名压平」由此消失。
	if _, err := db.Exec(
		`INSERT INTO api_keys (name, key_hash, key_plain, user_id) VALUES ('laptop', 'h-bob-laptop', 'sk-ptg-bob-laptop', ?)`,
		bobID); err != nil {
		t.Fatalf("种 bob 的同名 key: %v", err)
	}
	raw, skipped, err = declcfg.Export(context.Background(), db)
	if err != nil {
		t.Fatalf("同名跨用户的库也该能导出: %v", err)
	}
	if !strings.Contains(string(raw), "sk-ptg-multiuser-a") || strings.Contains(string(raw), "sk-ptg-bob-laptop") {
		t.Error("撞名时导出的必须是第一个 admin 那把，bob 的只能进跳过名单")
	}
	if len(skipped) != 2 || !strings.Contains(skipped[1], "laptop") {
		t.Errorf("跳过名单该多出 bob 的那把 laptop，实得 %v", skipped)
	}

	// 跳过不破坏往返：导出物 → 空库 apply → 再导出，字节相等。
	parsed, err := declcfg.Parse(raw, "skip.yaml")
	if err != nil {
		t.Fatalf("解析导出物: %v", err)
	}
	db2 := openDB(t)
	applyFile(t, db2, parsed)
	if second := mustExport(t, db2); !bytes.Equal(raw, second) {
		t.Error("跳过用户 key 后导出的文件往返不等——跳过名单漏进了文件或排序变了")
	}

	// 导入闸原样：同库上 CheckSingleUser 仍拒绝并点名。
	err = declcfg.CheckSingleUser(context.Background(), db)
	if err == nil {
		t.Fatal("多用户库的导入闸该仍拒绝")
	}
	for _, want := range []string{"ci", "bob@x", "不支持多用户"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("导入闸的报错没点到 %q：%v", want, err)
		}
	}
}

// 无 admin 的库（只有手写 SQL 造得出）：没有「第一个 admin」，用户名下的 key 一律
// 算跳过（口径层 v1.12 ①：firstAdminOrZero 回 0，匹配不上任何用户），无主的照常导出。
func TestExportSkipsAllUserKeysWhenNoAdmin(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, twoKeysYAML)
	bobID := seedUser(t, db, "bob@x", "user")
	// ci 归 bob，laptop 留无主。库里没有任何 admin。
	if _, err := db.Exec(`UPDATE api_keys SET user_id = ? WHERE name = 'ci'`, bobID); err != nil {
		t.Fatalf("归属 ci: %v", err)
	}

	raw, skipped, err := declcfg.Export(context.Background(), db)
	if err != nil {
		t.Fatalf("无 admin 的库该能导出（用户 key 全跳过）: %v", err)
	}
	if !strings.Contains(string(raw), "sk-ptg-multiuser-a") {
		t.Error("无主的 key 该照常导出")
	}
	if strings.Contains(string(raw), "sk-ptg-multiuser-b") {
		t.Error("无 admin 时用户名下的 key 一律不进文件")
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "ci") {
		t.Errorf("跳过名单该点名 ci，实得 %v", skipped)
	}
}

// 被跳过的用户 key 明文丢失（key_plain 空串）不炸导出：跳过判定在「拿不到原值」判定
// 之前——它反正不进文件，缺原值缺得无所谓；同一缺陷落在要导出的 key 上照旧当场失败
// （那半由 TestExportFailsNamingKeysWithoutPlaintext 钉着）。
func TestExportIgnoresLostPlaintextOnSkippedKeys(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, twoKeysYAML)
	seedUser(t, db, "admin@localhost", "admin")
	bobID := seedUser(t, db, "bob@x", "user")
	if _, err := db.Exec(
		`INSERT INTO api_keys (name, key_hash, key_plain, user_id) VALUES ('老 key', 'h-bob-old', '', ?)`,
		bobID); err != nil {
		t.Fatalf("种 bob 的无明文 key: %v", err)
	}

	raw, skipped, err := declcfg.Export(context.Background(), db)
	if err != nil {
		t.Fatalf("跳过的 key 缺明文不该让导出失败: %v", err)
	}
	if strings.Contains(string(raw), "老 key") {
		t.Error("bob 的 key 不该进文件")
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "老 key") {
		t.Errorf("跳过名单该点名「老 key」，实得 %v", skipped)
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
