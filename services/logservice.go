package services

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	modelpricing "codeswitch/resources/model-pricing"

	"github.com/daodao97/xgo/xdb"
)

const timeLayout = "2006-01-02 15:04:05"

type LogService struct {
	pricing *modelpricing.Service
}

func (ls *LogService) CostSince(start string, platform string) (float64, error) {
	startTime, err := parseTimeInput(start)
	if err != nil {
		return 0, err
	}
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.WhereGte("created_at", formatRequestLogQueryBoundary(startTime)),
		xdb.Field(
			"platform",
			"model",
			"input_tokens",
			"billable_input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"ephemeral_5m_tokens",
			"ephemeral_1h_tokens",
			"service_tier",
			"usage_accounting_version",
		),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return 0, nil
		}
		return 0, err
	}
	total := 0.0
	for _, record := range records {
		usage := buildPricingSnapshotFromRecord(record)
		cost := ls.calculateCostForPricingUsage(record.GetString("model"), usage)
		total += cost.TotalCost
	}
	return total, nil
}

// buildPricingSnapshotFromRecord 优先使用请求完成时保存的普通计费输入。
// 旧记录没有该快照时才按当前协议规则推导，从而保持旧数据可查、同时避免新记录被二次扣缓存。
func buildPricingSnapshotFromRecord(record xdb.Record) modelpricing.UsageSnapshot {
	raw := buildSnapshotFromRecord(record)
	if record.GetInt("usage_accounting_version") >= requestLogUsageAccountingVersion {
		raw.InputTokens = maxTokenCount(record.GetInt("billable_input_tokens"))
		return raw
	}
	return buildPricingUsage(record.GetString("platform"), raw)
}

func buildPricingSnapshotFromLog(logEntry *ReqeustLog) modelpricing.UsageSnapshot {
	raw := modelpricing.UsageSnapshot{
		InputTokens:       logEntry.InputTokens,
		OutputTokens:      logEntry.OutputTokens,
		ReasoningTokens:   logEntry.ReasoningTokens,
		CacheCreateTokens: logEntry.CacheCreateTokens,
		CacheReadTokens:   logEntry.CacheReadTokens,
		ServiceTier:       modelpricing.ServiceTier(strings.ToLower(strings.TrimSpace(logEntry.ServiceTier))),
	}
	if logEntry.Ephemeral5mTokens > 0 || logEntry.Ephemeral1hTokens > 0 {
		raw.CacheCreation = &modelpricing.CacheCreationDetail{
			Ephemeral5mTokens: logEntry.Ephemeral5mTokens,
			Ephemeral1hTokens: logEntry.Ephemeral1hTokens,
		}
	}
	if logEntry.UsageAccountingVersion >= requestLogUsageAccountingVersion {
		raw.InputTokens = maxTokenCount(logEntry.BillableInputTokens)
		return raw
	}
	return buildPricingUsage(logEntry.Platform, raw)
}

// buildSnapshotFromRecord 从 request_log 记录构造定价输入,统一处理 ephemeral 拆分 + service_tier。
func buildSnapshotFromRecord(record xdb.Record) modelpricing.UsageSnapshot {
	total := record.GetInt("cache_create_tokens")
	fiveM := record.GetInt("ephemeral_5m_tokens")
	oneH := record.GetInt("ephemeral_1h_tokens")
	snap := modelpricing.UsageSnapshot{
		InputTokens:       record.GetInt("input_tokens"),
		OutputTokens:      record.GetInt("output_tokens"),
		ReasoningTokens:   record.GetInt("reasoning_tokens"),
		CacheCreateTokens: total,
		CacheReadTokens:   record.GetInt("cache_read_tokens"),
		ServiceTier:       modelpricing.ServiceTier(strings.ToLower(strings.TrimSpace(record.GetString("service_tier")))),
	}
	if fiveM > 0 || oneH > 0 {
		snap.CacheCreation = &modelpricing.CacheCreationDetail{
			Ephemeral5mTokens: fiveM,
			Ephemeral1hTokens: oneH,
		}
	}
	return snap
}

func NewLogService() *LogService {
	svc, err := modelpricing.DefaultService()
	if err != nil {
		log.Printf("pricing service init failed: %v", err)
	}
	return &LogService{pricing: svc}
}

func (ls *LogService) ListRequestLogs(platform string, provider string, limit int) ([]ReqeustLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.OrderByDesc("id"),
		xdb.Limit(limit),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	if provider != "" {
		options = append(options, xdb.WhereEq("provider", provider))
	}
	records, err := model.Selects(options...)
	if err != nil {
		return nil, err
	}
	logs := make([]ReqeustLog, 0, len(records))
	for _, record := range records {
		logEntry := ReqeustLog{
			ID:                     record.GetInt64("id"),
			Platform:               record.GetString("platform"),
			Model:                  record.GetString("model"),
			RequestedModel:         record.GetString("requested_model"),
			Provider:               record.GetString("provider"),
			HttpCode:               record.GetInt("http_code"),
			InputTokens:            record.GetInt("input_tokens"),
			OutputTokens:           record.GetInt("output_tokens"),
			CacheCreateTokens:      record.GetInt("cache_create_tokens"),
			Ephemeral5mTokens:      record.GetInt("ephemeral_5m_tokens"),
			Ephemeral1hTokens:      record.GetInt("ephemeral_1h_tokens"),
			CacheReadTokens:        record.GetInt("cache_read_tokens"),
			ReasoningTokens:        record.GetInt("reasoning_tokens"),
			CreatedAt:              record.GetString("created_at"),
			IsStream:               record.GetBool("is_stream"),
			DurationSec:            record.GetFloat64("duration_sec"),
			ServiceTier:            record.GetString("service_tier"),
			BillableInputTokens:    record.GetInt("billable_input_tokens"),
			UsageAccountingVersion: record.GetInt("usage_accounting_version"),
			UsageRawJSON:           record.GetString("usage_raw_json"),
		}
		ls.decorateCost(&logEntry)
		logs = append(logs, logEntry)
	}
	return logs, nil
}

func (ls *LogService) ListProviders(platform string) ([]string, error) {
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.Field("DISTINCT provider as provider"),
		xdb.WhereNotEq("provider", ""),
		xdb.OrderByAsc("provider"),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		return nil, err
	}
	providers := make([]string, 0, len(records))
	for _, record := range records {
		name := strings.TrimSpace(record.GetString("provider"))
		if name != "" {
			providers = append(providers, name)
		}
	}
	return providers, nil
}

func (ls *LogService) HeatmapStats(days int) ([]HeatmapStat, error) {
	if days <= 0 {
		days = 30
	}
	totalHours := days * 24
	if totalHours <= 0 {
		totalHours = 24
	}
	rangeStart := startOfHour(time.Now())
	if totalHours > 1 {
		rangeStart = rangeStart.Add(-time.Duration(totalHours-1) * time.Hour)
	}
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.WhereGe("created_at", formatRequestLogQueryBoundary(rangeStart)),
		xdb.Field(
			"platform",
			"model",
			"input_tokens",
			"billable_input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"ephemeral_5m_tokens",
			"ephemeral_1h_tokens",
			"service_tier",
			"usage_accounting_version",
			"created_at",
		),
		xdb.OrderByDesc("created_at"),
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return []HeatmapStat{}, nil
		}
		return nil, err
	}
	hourBuckets := map[int64]*HeatmapStat{}
	for _, record := range records {
		createdAt, _ := parseCreatedAt(record)
		if createdAt.IsZero() {
			continue
		}
		hourStart := startOfHour(createdAt)
		hourKey := hourStart.Unix()
		bucket := hourBuckets[hourKey]
		if bucket == nil {
			bucket = &HeatmapStat{Day: hourStart.Format("01-02 15")}
			hourBuckets[hourKey] = bucket
		}
		bucket.TotalRequests++
		usage := buildSnapshotFromRecord(record)
		pricingUsage := buildPricingSnapshotFromRecord(record)
		bucket.InputTokens += int64(usage.InputTokens)
		bucket.BillableInputTokens += int64(pricingUsage.InputTokens)
		bucket.OutputTokens += int64(usage.OutputTokens)
		bucket.ReasoningTokens += int64(usage.ReasoningTokens)
		cost := ls.calculateCostForPricingUsage(record.GetString("model"), pricingUsage)
		bucket.TotalCost += cost.TotalCost
	}
	if len(hourBuckets) == 0 {
		return []HeatmapStat{}, nil
	}
	hourKeys := make([]int64, 0, len(hourBuckets))
	for key := range hourBuckets {
		hourKeys = append(hourKeys, key)
	}
	sort.Slice(hourKeys, func(i, j int) bool {
		return hourKeys[i] < hourKeys[j]
	})
	stats := make([]HeatmapStat, 0, min(len(hourKeys), totalHours))
	for i := len(hourKeys) - 1; i >= 0 && len(stats) < totalHours; i-- {
		stats = append(stats, *hourBuckets[hourKeys[i]])
	}
	return stats, nil
}

func (ls *LogService) StatsSince(platform string) (LogStats, error) {
	const seriesHours = 24

	stats := LogStats{
		Series: make([]LogStatsSeries, 0, seriesHours),
	}
	now := time.Now()
	model := xdb.New("request_log")
	seriesStart := startOfDay(now)
	seriesEnd := seriesStart.Add(seriesHours * time.Hour)
	queryStart := seriesStart
	summaryStart := seriesStart
	options := []xdb.Option{
		xdb.WhereGte("created_at", formatRequestLogQueryBoundary(queryStart)),
		xdb.Field(
			"platform",
			"model",
			"input_tokens",
			"billable_input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"ephemeral_5m_tokens",
			"ephemeral_1h_tokens",
			"service_tier",
			"usage_accounting_version",
			"created_at",
		),
		xdb.OrderByAsc("created_at"),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return stats, nil
		}
		return stats, err
	}

	seriesBuckets := make([]*LogStatsSeries, seriesHours)
	for i := 0; i < seriesHours; i++ {
		bucketTime := seriesStart.Add(time.Duration(i) * time.Hour)
		seriesBuckets[i] = &LogStatsSeries{
			Day: bucketTime.Format(timeLayout),
		}
	}

	for _, record := range records {
		createdAt, hasTime := parseCreatedAt(record)
		dayKey := dayFromTimestamp(record.GetString("created_at"))
		isToday := dayKey == seriesStart.Format("2006-01-02")

		if hasTime {
			if createdAt.Before(seriesStart) || !createdAt.Before(seriesEnd) {
				continue
			}
		} else {
			if !isToday {
				continue
			}
			createdAt = seriesStart
		}

		bucketIndex := 0
		if hasTime {
			bucketIndex = int(createdAt.Sub(seriesStart) / time.Hour)
			if bucketIndex < 0 {
				bucketIndex = 0
			}
			if bucketIndex >= seriesHours {
				bucketIndex = seriesHours - 1
			}
		}
		bucket := seriesBuckets[bucketIndex]
		usage := buildSnapshotFromRecord(record)
		pricingUsage := buildPricingSnapshotFromRecord(record)
		cost := ls.calculateCostForPricingUsage(record.GetString("model"), pricingUsage)
		cacheHitDenominator := deriveCacheHitDenominatorTokens(
			record.GetString("platform"),
			usage.InputTokens,
			usage.CacheReadTokens,
			usage.CacheCreateTokens,
		)

		bucket.TotalRequests++
		bucket.InputTokens += int64(usage.InputTokens)
		bucket.BillableInputTokens += int64(pricingUsage.InputTokens)
		bucket.OutputTokens += int64(usage.OutputTokens)
		bucket.ReasoningTokens += int64(usage.ReasoningTokens)
		bucket.CacheCreateTokens += int64(usage.CacheCreateTokens)
		bucket.CacheReadTokens += int64(usage.CacheReadTokens)
		bucket.TotalCost += cost.TotalCost

		if createdAt.IsZero() || createdAt.Before(summaryStart) {
			continue
		}
		stats.TotalRequests++
		stats.InputTokens += int64(usage.InputTokens)
		stats.BillableInputTokens += int64(pricingUsage.InputTokens)
		stats.OutputTokens += int64(usage.OutputTokens)
		stats.ReasoningTokens += int64(usage.ReasoningTokens)
		stats.CacheCreateTokens += int64(usage.CacheCreateTokens)
		stats.CacheReadTokens += int64(usage.CacheReadTokens)
		stats.CacheHitDenominatorTokens += int64(cacheHitDenominator)
		stats.CostInput += cost.InputCost
		stats.CostOutput += cost.OutputCost
		stats.CostCacheCreate += cost.CacheCreateCost
		stats.CostCacheRead += cost.CacheReadCost
		stats.CostTotal += cost.TotalCost
	}

	for i := 0; i < seriesHours; i++ {
		if bucket := seriesBuckets[i]; bucket != nil {
			stats.Series = append(stats.Series, *bucket)
		} else {
			bucketTime := seriesStart.Add(time.Duration(i) * time.Hour)
			stats.Series = append(stats.Series, LogStatsSeries{
				Day: bucketTime.Format(timeLayout),
			})
		}
	}

	return stats, nil
}

func (ls *LogService) ProviderDailyStats(platform string) ([]ProviderDailyStat, error) {
	start := startOfDay(time.Now())
	end := start.Add(24 * time.Hour)
	queryStart := start
	model := xdb.New("request_log")
	options := []xdb.Option{
		xdb.WhereGte("created_at", formatRequestLogQueryBoundary(queryStart)),
		xdb.Field(
			"platform",
			"provider",
			"model",
			"http_code",
			"input_tokens",
			"billable_input_tokens",
			"output_tokens",
			"reasoning_tokens",
			"cache_create_tokens",
			"cache_read_tokens",
			"ephemeral_5m_tokens",
			"ephemeral_1h_tokens",
			"service_tier",
			"usage_accounting_version",
			"created_at",
		),
	}
	if platform != "" {
		options = append(options, xdb.WhereEq("platform", platform))
	}
	records, err := model.Selects(options...)
	if err != nil {
		if errors.Is(err, xdb.ErrNotFound) || isNoSuchTableErr(err) {
			return []ProviderDailyStat{}, nil
		}
		return nil, err
	}
	statMap := map[string]*ProviderDailyStat{}
	for _, record := range records {
		provider := strings.TrimSpace(record.GetString("provider"))
		if provider == "" {
			provider = "(unknown)"
		}
		createdAt, hasTime := parseCreatedAt(record)
		if hasTime {
			if createdAt.Before(start) || !createdAt.Before(end) {
				continue
			}
		} else {
			dayKey := dayFromTimestamp(record.GetString("created_at"))
			if dayKey != start.Format("2006-01-02") {
				continue
			}
		}
		stat := statMap[provider]
		if stat == nil {
			stat = &ProviderDailyStat{Provider: provider}
			statMap[provider] = stat
		}
		httpCode := record.GetInt("http_code")
		usage := buildSnapshotFromRecord(record)
		pricingUsage := buildPricingSnapshotFromRecord(record)
		cost := ls.calculateCostForPricingUsage(record.GetString("model"), pricingUsage)
		stat.TotalRequests++
		// 只有 HTTP 200-299 才算成功，其他（包括 0）都算失败
		if httpCode >= 200 && httpCode < 300 {
			stat.SuccessfulRequests++
		} else {
			stat.FailedRequests++
		}
		stat.InputTokens += int64(usage.InputTokens)
		stat.BillableInputTokens += int64(pricingUsage.InputTokens)
		stat.OutputTokens += int64(usage.OutputTokens)
		stat.ReasoningTokens += int64(usage.ReasoningTokens)
		stat.CacheCreateTokens += int64(usage.CacheCreateTokens)
		stat.CacheReadTokens += int64(usage.CacheReadTokens)
		stat.CostTotal += cost.TotalCost
	}
	stats := make([]ProviderDailyStat, 0, len(statMap))
	for _, stat := range statMap {
		if stat.TotalRequests > 0 {
			stat.SuccessRate = float64(stat.SuccessfulRequests) / float64(stat.TotalRequests)
		}
		stats = append(stats, *stat)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].TotalRequests == stats[j].TotalRequests {
			return stats[i].Provider < stats[j].Provider
		}
		return stats[i].TotalRequests > stats[j].TotalRequests
	})
	return stats, nil
}

func (ls *LogService) decorateCost(logEntry *ReqeustLog) {
	if ls == nil || ls.pricing == nil || logEntry == nil {
		return
	}
	usage := buildPricingSnapshotFromLog(logEntry)
	cost := ls.calculateCostForPricingUsage(logEntry.Model, usage)
	logEntry.HasPricing = cost.HasPricing
	logEntry.InputCost = cost.InputCost
	logEntry.OutputCost = cost.OutputCost
	logEntry.ReasoningCost = cost.ReasoningCost
	logEntry.CacheCreateCost = cost.CacheCreateCost
	logEntry.CacheReadCost = cost.CacheReadCost
	logEntry.Ephemeral5mCost = cost.Ephemeral5mCost
	logEntry.Ephemeral1hCost = cost.Ephemeral1hCost
	logEntry.TotalCost = cost.TotalCost
}

func (ls *LogService) calculateCost(platform string, model string, usage modelpricing.UsageSnapshot) modelpricing.CostBreakdown {
	if ls == nil || ls.pricing == nil {
		return modelpricing.CostBreakdown{}
	}
	return ls.calculateCostForPricingUsage(model, buildPricingUsage(platform, usage))
}

func (ls *LogService) calculateCostForPricingUsage(model string, usage modelpricing.UsageSnapshot) modelpricing.CostBreakdown {
	if ls == nil || ls.pricing == nil {
		return modelpricing.CostBreakdown{}
	}
	return ls.pricing.CalculateCost(model, usage)
}

// buildPricingUsage 只转换计价口径,不修改日志保存和页面展示使用的原始 token 快照。
// Codex 和 Gemini 的原始 input 计数包含缓存命中，计费前必须扣除缓存读取部分；
// Anthropic 的缓存字段独立上报，不能重复扣减。
func buildPricingUsage(platform string, usage modelpricing.UsageSnapshot) modelpricing.UsageSnapshot {
	pricingUsage := usage
	pricingUsage.InputTokens = deriveBillableInputTokens(platform, usage.InputTokens, usage.CacheReadTokens, usage.CacheCreateTokens)
	return pricingUsage
}

func parseCreatedAt(record xdb.Record) (time.Time, bool) {
	raw := strings.TrimSpace(record.GetString("created_at"))
	if raw != "" {
		if parsed, hasTime := parseStoredRequestLogTimestamp(raw); !parsed.IsZero() {
			return parsed, hasTime
		}
	}
	if t := record.GetTime("created_at"); t != nil {
		return t.In(time.Local), true
	}
	return time.Time{}, false
}

func parseTimeInput(value string) (time.Time, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return startOfDay(time.Now()), nil
	}
	parsed, _ := parseLocalTimeInput(raw)
	if !parsed.IsZero() {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("invalid time format: %s", raw)
}

// parseStoredRequestLogTimestamp 解释 request_log 的存储时间。
// SQLite DEFAULT CURRENT_TIMESTAMP 固定以 UTC 写入；解析后再转换本地时区，才能让日报、热力图按用户日历日归桶。
func parseStoredRequestLogTimestamp(raw string) (time.Time, bool) {
	return parseTimestampInLocation(raw, time.UTC)
}

// parseLocalTimeInput 解释页面传入的预算起点。无时区值由用户在本地界面输入，必须按本地时区理解。
func parseLocalTimeInput(raw string) (time.Time, bool) {
	return parseTimestampInLocation(raw, time.Local)
}

// parseTimestampInLocation 只为无时区字符串指定默认时区；带偏移量的输入始终以自身偏移量为准。
// 这样数据库 UTC 时间与用户本地输入共用格式兼容逻辑，却不会再次混淆各自的时间语义。
func parseTimestampInLocation(raw string, naiveLocation *time.Location) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 MST",
		"2006-01-02T15:04:05-0700",
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.In(time.Local), true
		}
	}

	for _, layout := range []string{timeLayout, "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, raw, naiveLocation); err == nil {
			return parsed.In(time.Local), true
		}
	}

	if len(raw) >= len("2006-01-02") {
		if parsed, err := time.ParseInLocation("2006-01-02", raw[:10], naiveLocation); err == nil {
			return parsed.In(time.Local), false
		}
	}
	return time.Time{}, false
}

func dayFromTimestamp(value string) string {
	parsed, hasTime := parseStoredRequestLogTimestamp(value)
	if hasTime {
		return parsed.Format("2006-01-02")
	}
	if len(value) >= len("2006-01-02") {
		return value[:10]
	}
	return value
}

// formatRequestLogQueryBoundary 把本地统计边界转换为 SQLite CURRENT_TIMESTAMP 的 UTC 文本格式。
// 直接用本地格式比较会漏掉本地日界线与 UTC 日界线之间的历史记录。
func formatRequestLogQueryBoundary(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func startOfHour(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, t.Hour(), 0, 0, 0, t.Location())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isNoSuchTableErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no such table")
}

type HeatmapStat struct {
	Day                 string  `json:"day"`
	TotalRequests       int64   `json:"total_requests"`
	InputTokens         int64   `json:"input_tokens"`
	BillableInputTokens int64   `json:"billable_input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	TotalCost           float64 `json:"total_cost"`
}

type LogStats struct {
	TotalRequests             int64            `json:"total_requests"`
	InputTokens               int64            `json:"input_tokens"`
	BillableInputTokens       int64            `json:"billable_input_tokens"`
	OutputTokens              int64            `json:"output_tokens"`
	ReasoningTokens           int64            `json:"reasoning_tokens"`
	CacheCreateTokens         int64            `json:"cache_create_tokens"`
	CacheReadTokens           int64            `json:"cache_read_tokens"`
	CacheHitDenominatorTokens int64            `json:"cache_hit_denominator_tokens"`
	CostTotal                 float64          `json:"cost_total"`
	CostInput                 float64          `json:"cost_input"`
	CostOutput                float64          `json:"cost_output"`
	CostCacheCreate           float64          `json:"cost_cache_create"`
	CostCacheRead             float64          `json:"cost_cache_read"`
	Series                    []LogStatsSeries `json:"series"`
}

type ProviderDailyStat struct {
	Provider            string  `json:"provider"`
	TotalRequests       int64   `json:"total_requests"`
	SuccessfulRequests  int64   `json:"successful_requests"`
	FailedRequests      int64   `json:"failed_requests"`
	SuccessRate         float64 `json:"success_rate"`
	InputTokens         int64   `json:"input_tokens"`
	BillableInputTokens int64   `json:"billable_input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CacheCreateTokens   int64   `json:"cache_create_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CostTotal           float64 `json:"cost_total"`
}

type LogStatsSeries struct {
	Day                 string  `json:"day"`
	TotalRequests       int64   `json:"total_requests"`
	InputTokens         int64   `json:"input_tokens"`
	BillableInputTokens int64   `json:"billable_input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CacheCreateTokens   int64   `json:"cache_create_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	TotalCost           float64 `json:"total_cost"`
}
