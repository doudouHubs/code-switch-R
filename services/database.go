package services

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daodao97/xgo/xdb"
	_ "modernc.org/sqlite"
)

// InitDatabase 初始化数据库连接（必须在所有服务构造之前调用）
// 【修复】解决数据库初始化时序问题：
// 1. 确保配置目录存在
// 2. 初始化 xdb 连接池
// 3. 显式设置 PRAGMA（WAL 模式 + busy_timeout）
// 4. 确保表结构存在
// 5. 预热连接池
func InitDatabase() error {
	home, err := getUserHomeDir()
	if err != nil {
		return fmt.Errorf("获取用户目录失败: %w", err)
	}

	// 1. 确保配置目录存在（SQLite 不会自动创建父目录）
	configDir := filepath.Join(home, ".code-switch")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}

	// 2. 初始化 xdb 连接池
	// 【修复】移除 DSN 中的 PRAGMA 参数，modernc.org/sqlite 需要显式执行 PRAGMA
	dbPath := filepath.Join(configDir, "app.db?cache=shared&mode=rwc")
	if err := xdb.Inits([]xdb.Config{
		{
			Name:   "default",
			Driver: "sqlite",
			DSN:    dbPath,
		},
	}); err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}

	// 3. 显式设置 PRAGMA（解决 SQLITE_BUSY 问题）
	db, err := xdb.DB("default")
	if err != nil {
		return fmt.Errorf("获取数据库连接失败: %w", err)
	}

	// 3.1 设置 busy_timeout（30秒，确保高并发下有足够等待时间）
	if _, err := db.Exec("PRAGMA busy_timeout = 30000"); err != nil {
		return fmt.Errorf("设置 busy_timeout 失败: %w", err)
	}

	// 3.2 设置 WAL 模式（允许读写并发）
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("设置 WAL 模式失败: %w", err)
	}
	fmt.Printf("✅ SQLite PRAGMA 已设置: journal_mode=%s, busy_timeout=30000ms\n", journalMode)

	// 可用性检测模块已经退役，必须在其它服务启动前删除其历史表。
	// DROP TABLE IF EXISTS 保证新安装和已迁移安装都能重复安全启动。
	if err := dropRetiredHealthCheckHistoryTable(db); err != nil {
		return fmt.Errorf("删除已退役的 health_check_history 表失败: %w", err)
	}

	// 4. 确保仍在使用的表结构存在
	if err := ensureRequestLogTable(); err != nil {
		return fmt.Errorf("初始化 request_log 表失败: %w", err)
	}
	if err := ensureBlacklistTables(); err != nil {
		return fmt.Errorf("初始化黑名单表失败: %w", err)
	}
	if err := ensureProviderAliasTable(); err != nil {
		return fmt.Errorf("初始化 provider_alias 表失败: %w", err)
	}

	// 5. 预热连接池：强制建立数据库连接，避免首次写入时失败
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM request_log").Scan(&count); err != nil {
		fmt.Printf("⚠️  连接池预热查询失败: %v\n", err)
	} else {
		fmt.Printf("✅ 数据库连接已预热（request_log 记录数: %d）\n", count)
	}

	return nil
}

// dropRetiredHealthCheckHistoryTable 删除已明确废弃且不再读取的健康检查历史数据。
// 该操作只处理退役模块自己的表，不触碰真实请求日志和供应商黑名单。
func dropRetiredHealthCheckHistoryTable(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS health_check_history`)
	return err
}

// ensureBlacklistTables 确保黑名单相关表存在
func ensureBlacklistTables() error {
	db, err := xdb.DB("default")
	if err != nil {
		return fmt.Errorf("获取数据库连接失败: %w", err)
	}

	// 1. 创建 app_settings 表
	const createAppSettingsSQL = `CREATE TABLE IF NOT EXISTS app_settings (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		key TEXT UNIQUE NOT NULL,
		value TEXT
	)`
	if _, err := db.Exec(createAppSettingsSQL); err != nil {
		return fmt.Errorf("创建 app_settings 表失败: %w", err)
	}

	// 2. 创建 provider_blacklist 表
	const createBlacklistSQL = `CREATE TABLE IF NOT EXISTS provider_blacklist (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform TEXT NOT NULL,
		provider_name TEXT NOT NULL,
		failure_count INTEGER DEFAULT 0,
		blacklisted_at DATETIME,
		blacklisted_until DATETIME,
		last_failure_at DATETIME,
		blacklist_level INTEGER DEFAULT 0,
		last_recovered_at DATETIME,
		last_degrade_hour INTEGER DEFAULT 0,
		last_failure_window_start DATETIME,
		auto_recovered INTEGER DEFAULT 0,
		UNIQUE(platform, provider_name)
	)`
	if _, err := db.Exec(createBlacklistSQL); err != nil {
		return fmt.Errorf("创建 provider_blacklist 表失败: %w", err)
	}

	// 3. 确保 app_settings 中有默认的黑名单配置
	defaultSettings := []struct {
		key   string
		value string
	}{
		{"enable_blacklist", "false"},
		{"blacklist_failure_threshold", "3"},
		{"blacklist_duration_minutes", "30"},
	}

	for _, s := range defaultSettings {
		_, err := db.Exec(`
			INSERT OR IGNORE INTO app_settings (key, value) VALUES (?, ?)
		`, s.key, s.value)
		if err != nil {
			return fmt.Errorf("插入默认设置 %s 失败: %w", s.key, err)
		}
	}

	return nil
}

// ensureProviderAliasTable 创建 provider_alias 表,用于 rename 后 48h 内承接旧名 in-flight 写入。
func ensureProviderAliasTable() error {
	db, err := xdb.DB("default")
	if err != nil {
		return fmt.Errorf("获取数据库连接失败: %w", err)
	}

	const createSQL = `CREATE TABLE IF NOT EXISTS provider_alias (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform TEXT NOT NULL,
		provider_id INTEGER NOT NULL,
		alias_name TEXT NOT NULL COLLATE NOCASE,
		canonical_name TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL,
		UNIQUE(platform, alias_name)
	)`
	if _, err := db.Exec(createSQL); err != nil {
		return fmt.Errorf("创建 provider_alias 表失败: %w", err)
	}

	const createIndexSQL = `
		CREATE INDEX IF NOT EXISTS idx_provider_alias_pid ON provider_alias(platform, provider_id);
		CREATE INDEX IF NOT EXISTS idx_provider_alias_expires ON provider_alias(expires_at);
	`
	if _, err := db.Exec(createIndexSQL); err != nil {
		return fmt.Errorf("创建 provider_alias 索引失败: %w", err)
	}

	return nil
}
