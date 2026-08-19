package services

import (
	"testing"

	modelpricing "codeswitch/resources/model-pricing"
)

// TestBuildPricingUsageSeparatesCachedInput 锁定跨协议的普通输入计价口径。
func TestBuildPricingUsageSeparatesCachedInput(t *testing.T) {
	raw := modelpricing.UsageSnapshot{
		InputTokens:     1000,
		OutputTokens:    100,
		CacheReadTokens: 800,
	}

	if got := buildPricingUsage(" codex ", raw); got.InputTokens != 200 {
		t.Fatalf("Codex 普通计价输入应为 200,实际 %d", got.InputTokens)
	}
	if got := buildPricingUsage("gemini", raw); got.InputTokens != 200 {
		t.Fatalf("Gemini 普通计价输入应为 200,实际 %d", got.InputTokens)
	}
	if got := buildPricingUsage("claude", raw); got.InputTokens != 1000 {
		t.Fatalf("Claude 独立缓存字段不应从普通输入扣除,实际 %d", got.InputTokens)
	}
	if raw.InputTokens != 1000 {
		t.Fatalf("原始展示输入 token 不应被计价转换修改,实际 %d", raw.InputTokens)
	}
}

// TestBuildPricingUsageClampsInvalidInput 防止异常旧数据产生负数计价输入。
func TestBuildPricingUsageClampsInvalidInput(t *testing.T) {
	got := buildPricingUsage("CODEX", modelpricing.UsageSnapshot{
		InputTokens:     100,
		CacheReadTokens: 150,
	})
	if got.InputTokens != 0 {
		t.Fatalf("cache_read 大于 input 时普通计价输入应为 0,实际 %d", got.InputTokens)
	}
}

// TestDecorateCostUsesSharedPricingUsage 防止日志明细与聚合统计各算一套费用口径。
func TestDecorateCostUsesSharedPricingUsage(t *testing.T) {
	service := NewLogService()
	if service.pricing == nil {
		t.Fatal("pricing service 初始化失败")
	}
	entry := &ReqeustLog{
		Platform:        "codex",
		Model:           "gpt-5.6-sol",
		InputTokens:     1000,
		OutputTokens:    100,
		CacheReadTokens: 800,
	}

	service.decorateCost(entry)
	raw := modelpricing.UsageSnapshot{
		InputTokens:     entry.InputTokens,
		OutputTokens:    entry.OutputTokens,
		CacheReadTokens: entry.CacheReadTokens,
	}
	want := service.calculateCost(entry.Platform, entry.Model, raw)
	if !entry.HasPricing || entry.TotalCost <= 0 {
		t.Fatalf("日志明细应命中非零定价,实际 %+v", entry)
	}
	if entry.TotalCost != want.TotalCost || entry.InputCost != want.InputCost || entry.CacheReadCost != want.CacheReadCost {
		t.Fatalf("日志明细与聚合计价入口不一致: entry=%+v want=%+v", entry, want)
	}
}
