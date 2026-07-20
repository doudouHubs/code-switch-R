package services

import (
	"testing"

	modelpricing "codeswitch/resources/model-pricing"
)

// TestBuildPricingUsageCodexSeparatesCachedInput 验证 Codex 的缓存命中只按缓存价计费,
// 同时保证计价转换不会污染用于统计展示和历史审计的原始快照。
func TestBuildPricingUsageCodexSeparatesCachedInput(t *testing.T) {
	raw := modelpricing.UsageSnapshot{
		InputTokens:     1000,
		OutputTokens:    100,
		CacheReadTokens: 800,
	}

	got := buildPricingUsage(" codex ", raw)
	if got.InputTokens != 200 {
		t.Fatalf("Codex 普通计价输入应为 200,实际 %d", got.InputTokens)
	}
	if got.CacheReadTokens != 800 {
		t.Fatalf("Codex 缓存计价输入应保持 800,实际 %d", got.CacheReadTokens)
	}
	if raw.InputTokens != 1000 {
		t.Fatalf("原始输入 token 不应被计价转换修改,实际 %d", raw.InputTokens)
	}
}

// TestBuildPricingUsageCodexClampsInvalidInput 验证旧数据或异常响应不会产生负数计价输入。
func TestBuildPricingUsageCodexClampsInvalidInput(t *testing.T) {
	got := buildPricingUsage("CODEX", modelpricing.UsageSnapshot{
		InputTokens:     100,
		CacheReadTokens: 150,
	})
	if got.InputTokens != 0 {
		t.Fatalf("cache_read 大于 input 时普通计价输入应为 0,实际 %d", got.InputTokens)
	}
}

// TestBuildPricingUsageLeavesOtherPlatformsUnchanged 锁定非 Codex 平台的既有 token 语义。
func TestBuildPricingUsageLeavesOtherPlatformsUnchanged(t *testing.T) {
	raw := modelpricing.UsageSnapshot{
		InputTokens:     1000,
		OutputTokens:    100,
		CacheReadTokens: 800,
	}
	for _, platform := range []string{"claude", "gemini", ""} {
		if got := buildPricingUsage(platform, raw); got != raw {
			t.Fatalf("platform=%q 不应改变计价快照: got=%+v want=%+v", platform, got, raw)
		}
	}
}

// TestDecorateCostUsesSharedCodexPricingUsage 验证日志详情和聚合统计共享同一计价入口,
// 防止首页费用修正后日志详情仍沿用旧口径。
func TestDecorateCostUsesSharedCodexPricingUsage(t *testing.T) {
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
		t.Fatalf("日志详情应命中非零定价,实际 %+v", entry)
	}
	if entry.TotalCost != want.TotalCost || entry.InputCost != want.InputCost || entry.CacheReadCost != want.CacheReadCost {
		t.Fatalf("日志详情与聚合计价入口不一致: entry=%+v want=%+v", entry, want)
	}
}
