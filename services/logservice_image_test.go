package services

import (
	"database/sql"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/daodao97/xgo/xdb"
)

func insertImageLogForTest(t *testing.T, db *sql.DB, imageCount int) {
	t.Helper()
	createdAt := time.Now().UTC().Format(timeLayout)
	_, err := db.Exec(`
		INSERT INTO request_log (
			platform, model, provider, request_type, image_count, http_code,
			input_tokens, output_tokens, cache_create_tokens, cache_read_tokens,
			reasoning_tokens, is_stream, duration_sec, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, ?)
	`, "openai", "aiml/dall-e-3", "image-provider", requestLogTypeImage, imageCount, http.StatusCreated, createdAt)
	if err != nil {
		t.Fatalf("插入图片日志失败: %v", err)
	}
}

func TestLogServiceAggregatesImageUsageAndCost(t *testing.T) {
	setupRenameTestEnv(t)
	// 使用 xdb 当前连接，避免另开数据库连接导致测试数据与 LogService 看到的连接不一致。
	actualDB, err := xdb.DB("default")
	if err != nil {
		t.Fatalf("获取测试数据库失败: %v", err)
	}
	insertImageLogForTest(t, actualDB, 2)

	service := NewLogService()
	stats, err := service.StatsSince("openai")
	if err != nil {
		t.Fatalf("StatsSince() error = %v", err)
	}
	if stats.TotalRequests != 1 || stats.ImageCount != 2 || math.Abs(stats.CostImage-0.084) > 1e-9 {
		t.Fatalf("summary image stats = %+v", stats)
	}

	providerStats, err := service.ProviderDailyStats("openai")
	if err != nil {
		t.Fatalf("ProviderDailyStats() error = %v", err)
	}
	if len(providerStats) != 1 || providerStats[0].ImageCount != 2 {
		t.Fatalf("provider image stats = %+v", providerStats)
	}

	logs, err := service.ListRequestLogs("openai", "image-provider", 10)
	if err != nil {
		t.Fatalf("ListRequestLogs() error = %v", err)
	}
	if len(logs) != 1 || logs[0].RequestType != requestLogTypeImage || logs[0].ImageCount != 2 || math.Abs(logs[0].ImageCost-0.084) > 1e-9 {
		t.Fatalf("image log detail = %+v", logs)
	}
}
