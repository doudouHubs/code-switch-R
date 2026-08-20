package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daodao97/xgo/xdb"
	_ "modernc.org/sqlite"
)

// setupRenameTestEnv 为请求捕获和 provider 名称测试提供隔离 HOME/SQLite，
// 避免测试读写开发机真实配置并确保 alias 约束拥有完整表结构。
func setupRenameTestEnv(t *testing.T) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	configDir := filepath.Join(tmpHome, ".code-switch")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	if err := xdb.Inits([]xdb.Config{{Name: "default", Driver: "sqlite", DSN: filepath.Join(configDir, "app.db?cache=shared&mode=rwc")}}); err != nil {
		t.Fatalf("初始化 xdb 失败: %v", err)
	}
	db, err := xdb.DB("default")
	if err != nil {
		t.Fatalf("获取数据库失败: %v", err)
	}
	_, _ = db.Exec("PRAGMA busy_timeout = 30000")
	t.Cleanup(func() {
		if current, closeErr := xdb.DB("default"); closeErr == nil && current != nil {
			_ = current.Close()
		}
	})

	for _, schema := range []string{
		`CREATE TABLE IF NOT EXISTS request_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT, platform TEXT, model TEXT, provider TEXT,
			request_type TEXT DEFAULT 'chat', image_count INTEGER DEFAULT 0,
			http_code INTEGER, input_tokens INTEGER, output_tokens INTEGER,
			cache_create_tokens INTEGER, cache_read_tokens INTEGER, reasoning_tokens INTEGER,
			is_stream INTEGER DEFAULT 0, duration_sec REAL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS provider_blacklist (
			id INTEGER PRIMARY KEY AUTOINCREMENT, platform TEXT NOT NULL, provider_name TEXT NOT NULL,
			failure_count INTEGER DEFAULT 0, blacklisted_at DATETIME, blacklisted_until DATETIME,
			last_failure_at DATETIME, blacklist_level INTEGER DEFAULT 0, last_recovered_at DATETIME,
			last_degrade_hour INTEGER DEFAULT 0, last_failure_window_start DATETIME,
			auto_recovered INTEGER DEFAULT 0, UNIQUE(platform, provider_name)
		)`,
		`CREATE TABLE IF NOT EXISTS provider_alias (
			id INTEGER PRIMARY KEY AUTOINCREMENT, platform TEXT NOT NULL, provider_id INTEGER NOT NULL,
			alias_name TEXT NOT NULL COLLATE NOCASE, canonical_name TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME NOT NULL,
			UNIQUE(platform, alias_name)
		)`,
	} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("建表失败: %v", err)
		}
	}
	if err := ensureRequestLogTableWithDB(db); err != nil {
		t.Fatalf("补齐 request_log 迁移列失败: %v", err)
	}
	return tmpHome
}

// newTestRelayService 使用与生产入口相同的依赖装配，避免请求捕获测试绕过
// relay 对黑名单、Gemini 和应用设置的真实边界。
func newTestRelayService(providerService *ProviderService) *ProviderRelayService {
	autoStartService := NewAutoStartService()
	appSettings := NewAppSettingsService(autoStartService)
	settingsService := NewSettingsService()
	notificationService := NewNotificationService(appSettings)
	blacklistService := NewBlacklistService(settingsService, notificationService)
	geminiService := NewGeminiService("127.0.0.1:18100")
	return NewProviderRelayService(
		providerService,
		geminiService,
		blacklistService,
		notificationService,
		appSettings,
		"",
	)
}

func writeProjectManagerRolloutFixtureWithWorkspaceRoots(t *testing.T, home, sessionID, fileName, cwd string, workspaceRoots []string, lines []string) string {
	t.Helper()
	rootsJSON, err := json.Marshal(workspaceRoots)
	if err != nil {
		t.Fatalf("序列化 workspace_roots 失败: %v", err)
	}
	path := filepath.Join(home, ".codex", "sessions", "2026", "06", "16", fileName)
	baseLines := []string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-06-16T10:00:00Z","payload":{"id":%q,"cwd":%q,"timestamp":"2026-06-16T10:00:00Z"}}`, sessionID, cwd),
		fmt.Sprintf(`{"type":"turn_context","timestamp":"2026-06-16T10:00:01Z","payload":{"turn_id":"turn-workspace-root","cwd":%q,"workspace_roots":%s}}`, cwd, string(rootsJSON)),
	}
	baseLines = append(baseLines, lines...)
	if err := AtomicWriteText(path, strings.Join(baseLines, "\n")); err != nil {
		t.Fatalf("写入带 workspace_roots 的 rollout fixture 失败: %v", err)
	}
	return path
}
