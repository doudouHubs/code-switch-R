package services

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	modelpricing "codeswitch/resources/model-pricing"

	"github.com/daodao97/xgo/xdb"
)

func TestCodexParseTokenUsageUsesFinalSnapshotForStreamAndResponse(t *testing.T) {
	usage := &ReqeustLog{}
	streamPayload := `{
		"type":"response.completed",
		"response":{
			"model":"gpt-5",
			"service_tier":"priority",
			"usage":{
				"input_tokens":1000,
				"output_tokens":300,
				"input_tokens_details":{"cached_tokens":800,"cache_write_tokens":25},
				"output_tokens_details":{"reasoning_tokens":200}
			}
		}
	}`

	// 上游或中转重复传递 completed 事件时，统计必须保持同一请求的一次最终快照。
	CodexParseTokenUsageFromResponse(streamPayload, usage)
	CodexParseTokenUsageFromResponse(streamPayload, usage)
	CodexParseTokenUsageFromResponse(`{
		"model":"gpt-5.5",
		"usage":{
			"input_tokens":1200,
			"output_tokens":320,
			"input_tokens_details":{"cached_tokens":900},
			"output_tokens_details":{"reasoning_tokens":210}
		}
	}`, usage)

	if usage.InputTokens != 1200 || usage.OutputTokens != 320 || usage.CacheReadTokens != 900 || usage.CacheCreateTokens != 25 || usage.ReasoningTokens != 210 {
		t.Fatalf("Codex usage 快照不正确: %+v", usage)
	}
	if usage.Model != "gpt-5.5" || usage.ServiceTier != "priority" {
		t.Fatalf("Codex 模型或服务档位不正确: %+v", usage)
	}
}

func TestFinalizeRequestLogUsageKeepsRawAndBillableInputsSeparate(t *testing.T) {
	codex := &ReqeustLog{
		Platform:          "codex",
		InputTokens:       1000,
		CacheReadTokens:   800,
		CacheCreateTokens: 50,
		OutputTokens:      100,
	}
	finalizeRequestLogUsage(codex)
	if codex.BillableInputTokens != 150 || codex.UsageAccountingVersion != requestLogUsageAccountingVersion {
		t.Fatalf("Codex 计费输入归一化错误: %+v", codex)
	}

	var snapshot requestLogUsageSnapshot
	if err := json.Unmarshal([]byte(codex.UsageRawJSON), &snapshot); err != nil {
		t.Fatalf("usage_raw_json 不是有效快照: %v", err)
	}
	if snapshot.InputTokens != 1000 || snapshot.BillableInputTokens != 150 || snapshot.CacheReadTokens != 800 || snapshot.CacheCreateTokens != 50 {
		t.Fatalf("usage_raw_json 丢失审计字段: %+v", snapshot)
	}

	claude := &ReqeustLog{
		Platform:        "claude",
		InputTokens:     1000,
		CacheReadTokens: 800,
	}
	finalizeRequestLogUsage(claude)
	if claude.BillableInputTokens != 1000 {
		t.Fatalf("Anthropic 独立缓存字段不应从普通输入重复扣减: %+v", claude)
	}
}

func TestDeriveCacheHitDenominatorTokensUsesProviderSemantics(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		input       int
		cacheRead   int
		cacheCreate int
		want        int
	}{
		{name: "codex cache is included in input", platform: "codex", input: 1000, cacheRead: 800, cacheCreate: 50, want: 1000},
		{name: "gemini cache is included in prompt", platform: "GEMINI", input: 1000, cacheRead: 800, cacheCreate: 0, want: 1000},
		{name: "claude cache fields are independent", platform: "claude", input: 100, cacheRead: 800, cacheCreate: 100, want: 1000},
		{name: "invalid codex input cannot make rate exceed one", platform: "codex", input: 100, cacheRead: 150, cacheCreate: 0, want: 150},
		{name: "negative values are ignored", platform: "claude", input: -1, cacheRead: -2, cacheCreate: -3, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveCacheHitDenominatorTokens(tt.platform, tt.input, tt.cacheRead, tt.cacheCreate); got != tt.want {
				t.Fatalf("deriveCacheHitDenominatorTokens(%q, %d, %d, %d) = %d, want %d", tt.platform, tt.input, tt.cacheRead, tt.cacheCreate, got, tt.want)
			}
		})
	}
}

func TestClaudeAndGeminiUsageKeepProviderSpecificCacheSemantics(t *testing.T) {
	claude := &ReqeustLog{}
	ClaudeCodeParseTokenUsageFromResponse(`{
		"message":{"model":"claude-sonnet-4-5-20250929","usage":{
			"input_tokens":120,
			"output_tokens":30,
			"cache_creation_input_tokens":50,
			"cache_read_input_tokens":800,
			"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":30}
		}}
	}`, claude)
	finalizeRequestLogUsage(claude)
	if claude.Model != "claude-sonnet-4-5-20250929" || claude.CacheCreateTokens != 50 || claude.Ephemeral5mTokens != 20 || claude.Ephemeral1hTokens != 30 || claude.BillableInputTokens != 120 {
		t.Fatalf("Claude 缓存快照或计费输入不正确: %+v", claude)
	}

	gemini := &ReqeustLog{}
	GeminiParseTokenUsageFromResponse(`{
		"modelVersion":"gemini-2.5-pro",
		"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":200,"cachedContentTokenCount":700,"thoughtsTokenCount":150}
	}`, gemini)
	// Gemini 流的后续事件带累计值，低于最终快照的字段不能倒退。
	GeminiParseTokenUsageFromResponse(`{
		"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":180,"cachedContentTokenCount":600,"thoughtsTokenCount":120}
	}`, gemini)
	gemini.Platform = "gemini"
	finalizeRequestLogUsage(gemini)
	if gemini.Model != "gemini-2.5-pro" || gemini.InputTokens != 1000 || gemini.OutputTokens != 200 || gemini.CacheReadTokens != 700 || gemini.ReasoningTokens != 150 || gemini.BillableInputTokens != 300 {
		t.Fatalf("Gemini usage 累计或计费输入不正确: %+v", gemini)
	}
}

func TestEnsureRequestLogTableAddsUsageAccountingColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE request_log (id INTEGER PRIMARY KEY, model TEXT)`); err != nil {
		t.Fatalf("创建旧版 request_log 失败: %v", err)
	}
	if err := ensureRequestLogTableWithDB(db); err != nil {
		t.Fatalf("迁移 request_log 失败: %v", err)
	}

	for _, column := range []string{
		"requested_model",
		"billable_input_tokens",
		"usage_accounting_version",
		"usage_raw_json",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('request_log') WHERE name = ?`, column).Scan(&count); err != nil {
			t.Fatalf("查询列 %s 失败: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("迁移后缺少列 %s", column)
		}
	}
}

func TestParseTimeInputKeepsNaiveLocalTime(t *testing.T) {
	const raw = "2026-07-30 11:39:16"
	parsed, err := parseTimeInput(raw)
	if err != nil {
		t.Fatalf("解析本地时间失败: %v", err)
	}
	if got := parsed.In(time.Local).Format(timeLayout); got != raw {
		t.Fatalf("无时区时间不应被转换到其他小时: got=%s want=%s", got, raw)
	}
}

func TestStoredRequestLogTimestampUsesSQLiteUTC(t *testing.T) {
	const raw = "2026-07-30 00:30:00"
	got, hasTime := parseStoredRequestLogTimestamp(raw)
	if !hasTime {
		t.Fatalf("SQLite 完整时间戳应包含时分秒: %q", raw)
	}
	want, err := time.ParseInLocation(timeLayout, raw, time.UTC)
	if err != nil {
		t.Fatalf("构造 UTC 期望时间失败: %v", err)
	}
	if !got.Equal(want.In(time.Local)) {
		t.Fatalf("SQLite CURRENT_TIMESTAMP 应按 UTC 解析: got=%s want=%s", got, want.In(time.Local))
	}
	if gotDay, wantDay := dayFromTimestamp(raw), want.In(time.Local).Format("2006-01-02"); gotDay != wantDay {
		t.Fatalf("日报归属应使用本地日期: got=%s want=%s", gotDay, wantDay)
	}
}

func TestRequestLogQueryBoundaryUsesUTC(t *testing.T) {
	localStart := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.Local)
	if got, want := formatRequestLogQueryBoundary(localStart), localStart.UTC().Format(timeLayout); got != want {
		t.Fatalf("SQLite 查询边界应转换 UTC: got=%s want=%s", got, want)
	}
}

func TestLogServiceStatsUsesPersistedBillableInput(t *testing.T) {
	setupRenameTestEnv(t)
	db, err := xdb.DB("default")
	if err != nil {
		t.Fatalf("获取测试数据库失败: %v", err)
	}
	if err := ensureRequestLogTableWithDB(db); err != nil {
		t.Fatalf("升级测试 request_log 失败: %v", err)
	}

	now := time.Now().Format(timeLayout)
	if _, err := db.Exec(`
		INSERT INTO request_log (
			platform, model, provider, http_code,
			input_tokens, billable_input_tokens, output_tokens,
			cache_create_tokens, cache_read_tokens, reasoning_tokens,
			usage_accounting_version, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "codex", "gpt-5", "fixture", 200, 1000, 200, 100, 0, 800, 50, requestLogUsageAccountingVersion, now); err != nil {
		t.Fatalf("写入请求日志 fixture 失败: %v", err)
	}

	service := NewLogService()
	stats, err := service.StatsSince("codex")
	if err != nil {
		t.Fatalf("读取统计失败: %v", err)
	}
	if stats.InputTokens != 1000 || stats.BillableInputTokens != 200 || stats.CacheReadTokens != 800 || stats.CacheHitDenominatorTokens != 1000 {
		t.Fatalf("统计 Token 口径不一致: %+v", stats)
	}

	want := service.pricing.CalculateCost("gpt-5", modelpricing.UsageSnapshot{
		InputTokens:     200,
		OutputTokens:    100,
		CacheReadTokens: 800,
		ReasoningTokens: 50,
	})
	if stats.CostTotal != want.TotalCost {
		t.Fatalf("统计费用未使用持久化 billable input: got=%f want=%f", stats.CostTotal, want.TotalCost)
	}

	costSince, err := service.CostSince(now, "codex")
	if err != nil {
		t.Fatalf("读取区间费用失败: %v", err)
	}
	if costSince != want.TotalCost {
		t.Fatalf("托盘区间费用与主页统计不一致: got=%f want=%f", costSince, want.TotalCost)
	}
}

func TestLogServiceStatsAggregatesPlatformSpecificCacheDenominators(t *testing.T) {
	setupRenameTestEnv(t)
	db, err := xdb.DB("default")
	if err != nil {
		t.Fatalf("获取测试数据库失败: %v", err)
	}
	if err := ensureRequestLogTableWithDB(db); err != nil {
		t.Fatalf("升级测试 request_log 失败: %v", err)
	}

	now := time.Now().Format(timeLayout)
	_, err = db.Exec(`
		INSERT INTO request_log (
			platform, model, provider, http_code,
			input_tokens, billable_input_tokens, output_tokens,
			cache_create_tokens, cache_read_tokens, reasoning_tokens,
			usage_accounting_version, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
		         (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"codex", "gpt-5", "fixture-codex", 200, 1000, 200, 100, 50, 800, 0, requestLogUsageAccountingVersion, now,
		"claude", "claude-sonnet-4-5-20250929", "fixture-claude", 200, 100, 100, 50, 50, 800, 0, requestLogUsageAccountingVersion, now,
	)
	if err != nil {
		t.Fatalf("写入混合平台请求日志失败: %v", err)
	}

	stats, err := NewLogService().StatsSince("")
	if err != nil {
		t.Fatalf("读取混合平台统计失败: %v", err)
	}
	// Codex 分母为 1000，Claude 分母为 100+800+50=950，总分母应为 1950。
	if stats.CacheReadTokens != 1600 || stats.CacheHitDenominatorTokens != 1950 {
		t.Fatalf("混合平台缓存分母错误: %+v", stats)
	}
}
