package services

import (
	"encoding/json"
	"strings"
)

const requestLogUsageAccountingVersion = 1

// requestLogUsageSnapshot 保存上游响应已经确认的用量快照。它不保存完整响应，
// 既能让后续统计复现计费口径，也避免把请求内容或工具结果写入请求日志。
type requestLogUsageSnapshot struct {
	Version             int    `json:"version"`
	Platform            string `json:"platform"`
	InputTokens         int    `json:"input_tokens"`
	BillableInputTokens int    `json:"billable_input_tokens"`
	OutputTokens        int    `json:"output_tokens"`
	CacheCreateTokens   int    `json:"cache_create_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
	Ephemeral5mTokens   int    `json:"ephemeral_5m_tokens"`
	Ephemeral1hTokens   int    `json:"ephemeral_1h_tokens"`
	ReasoningTokens     int    `json:"reasoning_tokens"`
	ServiceTier         string `json:"service_tier"`
}

// finalizeRequestLogUsage 在请求结束后冻结已解析到的 Token 语义。
// input_tokens 保留供应商原始口径，billable_input_tokens 只表示应按普通输入价计费的部分；
// 两者分开保存，才能避免缓存命中同时被普通输入和缓存价重复计费。
func finalizeRequestLogUsage(logEntry *ReqeustLog) {
	if logEntry == nil {
		return
	}

	clampCacheEphemerals(logEntry)
	logEntry.BillableInputTokens = deriveBillableInputTokens(
		logEntry.Platform,
		logEntry.InputTokens,
		logEntry.CacheReadTokens,
		logEntry.CacheCreateTokens,
	)
	logEntry.UsageAccountingVersion = requestLogUsageAccountingVersion

	payload, err := json.Marshal(requestLogUsageSnapshot{
		Version:             requestLogUsageAccountingVersion,
		Platform:            logEntry.Platform,
		InputTokens:         logEntry.InputTokens,
		BillableInputTokens: logEntry.BillableInputTokens,
		OutputTokens:        logEntry.OutputTokens,
		CacheCreateTokens:   logEntry.CacheCreateTokens,
		CacheReadTokens:     logEntry.CacheReadTokens,
		Ephemeral5mTokens:   logEntry.Ephemeral5mTokens,
		Ephemeral1hTokens:   logEntry.Ephemeral1hTokens,
		ReasoningTokens:     logEntry.ReasoningTokens,
		ServiceTier:         logEntry.ServiceTier,
	})
	if err == nil {
		logEntry.UsageRawJSON = string(payload)
	}
}

// deriveBillableInputTokens 归一化不同协议对 input_tokens 的定义。
// OpenAI Responses 与 Gemini 的 input/prompt 计数会包含缓存读取；部分 OpenAI 兼容上游还会
// 传回已包含在 input 内的 cache_write_tokens。它们已单列按缓存价计费，必须从普通输入扣除。
// Anthropic 的 cache_read/cache_creation 则是独立字段，不能再从普通输入中扣一次。
func deriveBillableInputTokens(platform string, inputTokens int, cacheReadTokens int, cacheCreateTokens int) int {
	billable := maxTokenCount(inputTokens)
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "codex", "gemini":
		billable -= maxTokenCount(cacheReadTokens)
		billable -= maxTokenCount(cacheCreateTokens)
	}
	return maxTokenCount(billable)
}

// deriveCacheHitDenominatorTokens 统一缓存命中率的分母口径。
// Codex/Gemini 的 input_tokens 已包含缓存读取 Token；Anthropic 则把普通输入、缓存读取和缓存创建分开上报。
// 逐请求归一化后再聚合，才能避免默认“全部平台”统计把缓存 Token 重复加进分母。
func deriveCacheHitDenominatorTokens(platform string, inputTokens int, cacheReadTokens int, cacheCreateTokens int) int {
	input := maxTokenCount(inputTokens)
	cacheRead := maxTokenCount(cacheReadTokens)
	cacheCreate := maxTokenCount(cacheCreateTokens)

	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "codex", "gemini":
		// 异常旧数据可能出现 cached_tokens 大于 input_tokens；抬高分母可保证展示层不会超过 100%。
		if input < cacheRead {
			return cacheRead
		}
		return input
	case "claude":
		return input + cacheRead + cacheCreate
	default:
		// 未知协议按 Anthropic 的独立字段口径保守计算，避免把独立缓存字段漏出分母。
		return input + cacheRead + cacheCreate
	}
}

func maxTokenCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

// observedModelFromResponse 只有上游明确返回模型时才覆盖路由选择的模型名。
// 这保证模型映射、别名或供应商侧透明重定向后，费用按实际响应模型核算。
func observedModelFromResponse(logEntry *ReqeustLog, candidates ...string) {
	if logEntry == nil {
		return
	}
	for _, candidate := range candidates {
		if model := strings.TrimSpace(candidate); model != "" {
			logEntry.Model = model
			return
		}
	}
}
