package services

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDropRetiredHealthCheckHistoryTable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE health_check_history (id INTEGER PRIMARY KEY, status TEXT NOT NULL);
		INSERT INTO health_check_history (status) VALUES ('ok');
	`); err != nil {
		t.Fatalf("准备退役表失败: %v", err)
	}

	if err := dropRetiredHealthCheckHistoryTable(db); err != nil {
		t.Fatalf("删除退役表失败: %v", err)
	}

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'health_check_history'`).Scan(&count)
	if err != nil {
		t.Fatalf("查询表状态失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("health_check_history 仍然存在，count=%d", count)
	}

	// 迁移会在每次启动时执行，重复调用必须保持幂等。
	if err := dropRetiredHealthCheckHistoryTable(db); err != nil {
		t.Fatalf("重复删除退役表失败: %v", err)
	}
}
