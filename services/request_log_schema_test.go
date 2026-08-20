package services

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEnsureRequestLogTableMigratesImageColumns(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE request_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			platform TEXT,
			model TEXT,
			provider TEXT,
			http_code INTEGER,
			input_tokens INTEGER,
			output_tokens INTEGER,
			cache_create_tokens INTEGER,
			cache_read_tokens INTEGER,
			reasoning_tokens INTEGER,
			is_stream INTEGER DEFAULT 0,
			duration_sec REAL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("创建旧 request_log schema 失败: %v", err)
	}
	if err := ensureRequestLogTableWithDB(db); err != nil {
		t.Fatalf("迁移 request_log schema 失败: %v", err)
	}

	var columnCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('request_log') WHERE name IN ('request_type', 'image_count', 'image_width', 'image_height')`).Scan(&columnCount); err != nil {
		t.Fatalf("读取迁移列信息失败: %v", err)
	}
	if columnCount != 4 {
		t.Fatalf("image log columns = %d, want 4", columnCount)
	}
	if _, err := db.Exec(`INSERT INTO request_log (platform, model, provider) VALUES ('codex', 'gpt-5', 'legacy')`); err != nil {
		t.Fatalf("验证迁移列默认值时插入失败: %v", err)
	}
	var requestType string
	var imageCount, imageWidth, imageHeight int
	if err := db.QueryRow(`SELECT request_type, image_count, image_width, image_height FROM request_log LIMIT 1`).Scan(&requestType, &imageCount, &imageWidth, &imageHeight); err != nil {
		t.Fatalf("读取迁移列默认值失败: %v", err)
	}
	if requestType != requestLogTypeChat || imageCount != 0 || imageWidth != 0 || imageHeight != 0 {
		t.Fatalf("迁移列默认值 = %q/%d/%d/%d", requestType, imageCount, imageWidth, imageHeight)
	}
}
