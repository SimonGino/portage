package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 纯数据迁移的登记表（#10）：累加型 UPDATE 放进 migrate() 就是「重启一次翻一倍」，
// runOnce 按步骤名登记，跑过的第二次一行不动。
func TestRunOnceRunsExactlyOnce(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE counter (n INTEGER NOT NULL)`); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO counter (n) VALUES (1)`); err != nil {
		t.Fatalf("种数据失败: %v", err)
	}
	double := func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE counter SET n = n * 2`)
		return err
	}
	for i := 0; i < 3; i++ {
		if err := runOnce(db, "double_counter", double); err != nil {
			t.Fatalf("第 %d 次 runOnce 失败: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT n FROM counter`).Scan(&n); err != nil {
		t.Fatalf("读结果失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("跑了三次 runOnce，n=%d，想要 2（只翻一次倍）", n)
	}
	var rows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE name = 'double_counter'`).Scan(&rows); err != nil {
		t.Fatalf("读登记失败: %v", err)
	}
	if rows != 1 {
		t.Fatalf("登记 %d 行，想要 1", rows)
	}
}

// fn 报错时数据与登记一起回滚：下次启动要能接着跑，而不是「改了一半、登记为跑过」。
func TestRunOnceRollsBackOnError(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE counter (n INTEGER NOT NULL)`); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO counter (n) VALUES (1)`); err != nil {
		t.Fatalf("种数据失败: %v", err)
	}
	boom := errors.New("boom")
	err = runOnce(db, "half_done", func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE counter SET n = n * 2`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("想要 boom 透出，得到 %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT n FROM counter`).Scan(&n); err != nil {
		t.Fatalf("读结果失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("失败后 n=%d，想要 1（数据改动已回滚）", n)
	}
	var rows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE name = 'half_done'`).Scan(&rows); err != nil {
		t.Fatalf("读登记失败: %v", err)
	}
	if rows != 0 {
		t.Fatalf("失败后登记 %d 行，想要 0", rows)
	}
	// 修好之后重跑要能真的跑。
	if err := runOnce(db, "half_done", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE counter SET n = n * 2`)
		return err
	}); err != nil {
		t.Fatalf("重跑失败: %v", err)
	}
	if err := db.QueryRow(`SELECT n FROM counter`).Scan(&n); err != nil {
		t.Fatalf("读结果失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("重跑后 n=%d，想要 2", n)
	}
}
