package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	requestLogTypeChat  = "chat"
	requestLogTypeImage = "image"
)

// RequestLogSink 是 request_log 的唯一写入边界。
// 文本 relay 和图片服务共用同一条队列，避免统计数据出现第二个事实源。
type RequestLogSink interface {
	WriteRequestLog(context.Context, *ReqeustLog) error
}

type dbRequestLogSink struct{}

var requestLogQueueUnavailableWarning sync.Once
var defaultRequestLogSink RequestLogSink = dbRequestLogSink{}

func (dbRequestLogSink) WriteRequestLog(ctx context.Context, requestLog *ReqeustLog) error {
	if requestLog == nil {
		return errors.New("request log is nil")
	}
	if GlobalDBQueueLogs == nil {
		// 队列未初始化属于进程级故障，沿用原有行为只告警一次，不能阻断真实请求。
		requestLogQueueUnavailableWarning.Do(func() {
			fmt.Printf("⚠️  写入 request_log 失败: 队列未初始化\n")
		})
		return errors.New("request log queue unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	requestType := strings.ToLower(strings.TrimSpace(requestLog.RequestType))
	if requestType == "" {
		requestType = requestLogTypeChat
	}
	imageCount := requestLog.ImageCount
	if imageCount < 0 {
		imageCount = 0
	}
	requestLog.RequestType = requestType
	requestLog.ImageCount = imageCount

	return GlobalDBQueueLogs.ExecBatchCtx(ctx, `
		INSERT INTO request_log (
			platform, model, provider, request_type, image_count, image_width, image_height, http_code,
			input_tokens, output_tokens, cache_create_tokens, cache_read_tokens,
			reasoning_tokens, is_stream, duration_sec,
			ephemeral_5m_tokens, ephemeral_1h_tokens, service_tier,
			requested_model, billable_input_tokens, usage_accounting_version, usage_raw_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		requestLog.Platform,
		requestLog.Model,
		requestLog.Provider,
		requestType,
		imageCount,
		requestLog.ImageWidth,
		requestLog.ImageHeight,
		requestLog.HttpCode,
		requestLog.InputTokens,
		requestLog.OutputTokens,
		requestLog.CacheCreateTokens,
		requestLog.CacheReadTokens,
		requestLog.ReasoningTokens,
		boolToInt(requestLog.IsStream),
		requestLog.DurationSec,
		requestLog.Ephemeral5mTokens,
		requestLog.Ephemeral1hTokens,
		requestLog.ServiceTier,
		requestLog.RequestedModel,
		requestLog.BillableInputTokens,
		requestLog.UsageAccountingVersion,
		requestLog.UsageRawJSON,
	)
}

// enqueueRequestLogWithSink 保持旧 relay 的同步 defer 语义，但把队列等待限制在固定时长内，
// 避免数据库异常时拖住上游响应或图片生成调用；注入 sink 仍能保持测试隔离。
func enqueueRequestLogWithSink(sink RequestLogSink, requestLog *ReqeustLog) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if sink == nil {
		return errors.New("request log sink is nil")
	}
	return sink.WriteRequestLog(ctx, requestLog)
}

func enqueueRequestLog(requestLog *ReqeustLog) error {
	return enqueueRequestLogWithSink(defaultRequestLogSink, requestLog)
}
