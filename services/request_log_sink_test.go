package services

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestDBRequestLogSinkWritesAllRequestLogFields(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "request-log.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}

	if err := ensureRequestLogTableWithDB(db); err != nil {
		_ = db.Close()
		t.Fatalf("初始化 request_log schema 失败: %v", err)
	}

	queue := NewDBWriteQueue(db, 8, true)
	previousQueue := GlobalDBQueueLogs
	GlobalDBQueueLogs = queue
	t.Cleanup(func() {
		if err := queue.Shutdown(2 * time.Second); err != nil {
			t.Errorf("关闭测试 request_log 队列失败: %v", err)
		}
		GlobalDBQueueLogs = previousQueue
		_ = db.Close()
	})

	entry := &ReqeustLog{
		Platform:               "codex",
		Model:                  "gpt-5.6-luna",
		Provider:               "C30",
		RequestType:            "IMAGE",
		ImageCount:             2,
		ImageWidth:             1024,
		ImageHeight:            1024,
		HttpCode:               200,
		InputTokens:            120,
		OutputTokens:           45,
		CacheCreateTokens:      8,
		CacheReadTokens:        13,
		ReasoningTokens:        21,
		IsStream:               true,
		DurationSec:            1.25,
		Ephemeral5mTokens:      3,
		Ephemeral1hTokens:      4,
		ServiceTier:            "priority",
		RequestedModel:         "gpt-5.6-luna",
		BillableInputTokens:    107,
		UsageAccountingVersion: 2,
		UsageRawJSON:           `{"usage":{"input_tokens":120}}`,
	}

	if err := (dbRequestLogSink{}).WriteRequestLog(context.Background(), entry); err != nil {
		t.Fatalf("WriteRequestLog() error = %v", err)
	}

	var (
		platform, model, provider, requestType, serviceTier, requestedModel, usageRawJSON string
		imageCount, imageWidth, imageHeight, httpCode                                     int
		inputTokens, outputTokens, cacheCreateTokens, cacheReadTokens                     int
		reasoningTokens, isStream, ephemeral5mTokens, ephemeral1hTokens                   int
		billableInputTokens, usageAccountingVersion                                       int
		durationSec                                                                       float64
	)
	err = db.QueryRow(`
		SELECT platform, model, provider, request_type, image_count, image_width, image_height,
			http_code, input_tokens, output_tokens, cache_create_tokens, cache_read_tokens,
			reasoning_tokens, is_stream, duration_sec, ephemeral_5m_tokens, ephemeral_1h_tokens,
			service_tier, requested_model, billable_input_tokens, usage_accounting_version, usage_raw_json
		FROM request_log
		ORDER BY id DESC
		LIMIT 1
	`).Scan(
		&platform, &model, &provider, &requestType, &imageCount, &imageWidth, &imageHeight,
		&httpCode, &inputTokens, &outputTokens, &cacheCreateTokens, &cacheReadTokens,
		&reasoningTokens, &isStream, &durationSec, &ephemeral5mTokens, &ephemeral1hTokens,
		&serviceTier, &requestedModel, &billableInputTokens, &usageAccountingVersion, &usageRawJSON,
	)
	if err != nil {
		t.Fatalf("读取 request_log 失败: %v", err)
	}

	if platform != "codex" || model != "gpt-5.6-luna" || provider != "C30" || requestType != requestLogTypeImage {
		t.Fatalf("基础日志字段 = %q/%q/%q/%q", platform, model, provider, requestType)
	}
	if imageCount != 2 || imageWidth != 1024 || imageHeight != 1024 || httpCode != 200 {
		t.Fatalf("图片/状态字段 = %d/%d/%d/%d", imageCount, imageWidth, imageHeight, httpCode)
	}
	if inputTokens != 120 || outputTokens != 45 || cacheCreateTokens != 8 || cacheReadTokens != 13 || reasoningTokens != 21 {
		t.Fatalf("Token 字段 = %d/%d/%d/%d/%d", inputTokens, outputTokens, cacheCreateTokens, cacheReadTokens, reasoningTokens)
	}
	if isStream != 1 || durationSec != 1.25 || ephemeral5mTokens != 3 || ephemeral1hTokens != 4 {
		t.Fatalf("请求状态字段 = %d/%v/%d/%d", isStream, durationSec, ephemeral5mTokens, ephemeral1hTokens)
	}
	if serviceTier != "priority" || requestedModel != "gpt-5.6-luna" || billableInputTokens != 107 || usageAccountingVersion != 2 || usageRawJSON == "" {
		t.Fatalf("计费字段 = %q/%q/%d/%d/%q", serviceTier, requestedModel, billableInputTokens, usageAccountingVersion, usageRawJSON)
	}
}

func TestDBRequestLogSinkReportsUnavailableQueue(t *testing.T) {
	previousQueue := GlobalDBQueueLogs
	GlobalDBQueueLogs = nil
	t.Cleanup(func() {
		GlobalDBQueueLogs = previousQueue
	})

	err := (dbRequestLogSink{}).WriteRequestLog(context.Background(), &ReqeustLog{})
	if err == nil {
		t.Fatal("WriteRequestLog() error = nil, want queue unavailable error")
	}
}
