package services

import (
	"database/sql"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/daodao97/xgo/xdb"
)

func insertImageLogForTest(t *testing.T, db *sql.DB, model string, imageCount, imageWidth, imageHeight int) {
	t.Helper()
	createdAt := time.Now().UTC().Format(timeLayout)
	_, err := db.Exec(`
		INSERT INTO request_log (
			platform, model, provider, request_type, image_count, image_width, image_height, http_code,
			input_tokens, output_tokens, cache_create_tokens, cache_read_tokens,
			reasoning_tokens, is_stream, duration_sec, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, ?)
	`, "openai", model, "image-provider", requestLogTypeImage, imageCount, imageWidth, imageHeight, http.StatusCreated, createdAt)
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
	insertImageLogForTest(t, actualDB, "aiml/dall-e-3", 2, 0, 0)

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

func TestLogServiceChargesGPTImageLogsWithoutDimensions(t *testing.T) {
	setupRenameTestEnv(t)
	actualDB, err := xdb.DB("default")
	if err != nil {
		t.Fatalf("获取测试数据库失败: %v", err)
	}
	// 模拟截图对应的历史记录：只有 gpt-image-1 和 image_count，没有新尺寸列数据。
	insertImageLogForTest(t, actualDB, "gpt-image-1", 1, 0, 0)

	stats, err := NewLogService().StatsSince("openai")
	if err != nil {
		t.Fatalf("StatsSince() error = %v", err)
	}
	if stats.ImageCount != 1 || math.Abs(stats.CostImage-0.042) > 1e-9 {
		t.Fatalf("gpt-image-1 historical stats = %+v", stats)
	}
}
