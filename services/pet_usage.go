package services

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	modelpricing "codeswitch/resources/model-pricing"
)

const (
	// 经验规则与 OpenCowork 保持一致：每 1000 个 input+output token 产生 1 点经验。
	petUsageTokensPerExp = 1000
	// OpenCowork 使用每百万输入 token 的价格判断 premium，严格大于 2 才进入 premium。
	petPremiumInputPriceThreshold = 2
	petPremiumExpMultiplier       = 2
	petMaxInt64                   = int64(^uint64(0) >> 1)
)

// PetUsageEvent 是已有请求 usage 事实进入宠物账本前的最小载体。
// Provider 只用于确认请求身份完整；Model 和 Usage 必须来自同一次已完成请求，
// 不能在这里根据 provider 或模型名自行猜价格和 token。
type PetUsageEvent struct {
	ID       string
	Provider string
	Model    string
	At       int64
	Usage    modelpricing.UsageSnapshot
}

// ToPetUsageEvent 把 AI 流事件里的 wire payload 还原为账本输入。
// usage 事件由 provider 流程产生，主入口不应该自行解释 service tier 或复制
// cache 字段；统一在这里做一次投影，确保不同调用方不会形成第二套经验语义。
func (payload *PetStreamUsagePayload) ToPetUsageEvent() (PetUsageEvent, error) {
	if payload == nil {
		return PetUsageEvent{}, errors.New("request usage 载荷为空")
	}
	return PetUsageEvent{
		ID:       strings.TrimSpace(payload.ID),
		Provider: strings.TrimSpace(payload.Provider),
		Model:    strings.TrimSpace(payload.Model),
		At:       payload.At,
		Usage: modelpricing.UsageSnapshot{
			InputTokens:       payload.InputTokens,
			OutputTokens:      payload.OutputTokens,
			ReasoningTokens:   payload.ReasoningTokens,
			CacheCreateTokens: payload.CacheCreateTokens,
			CacheReadTokens:   payload.CacheReadTokens,
			CacheCreation: petCacheCreationDetail(
				payload.Ephemeral5mTokens,
				payload.Ephemeral1hTokens,
			),
			ServiceTier: modelpricing.NormalizeObservedServiceTier(payload.ServiceTier, nil),
		},
	}, nil
}

// AddExperienceFromUsage 将 canonical usage/pricing 转换为宠物经验日志。
// 经验账本的唯一写入口仍是 AddExperience，因此重复事件继续复用已有的日志 ID 幂等保护。
func (s *PetService) AddExperienceFromUsage(event PetUsageEvent) (PetExperience, error) {
	if hasInvalidPetUsage(event.Usage) {
		return PetExperience{}, errors.New("usage token 不能为负数")
	}

	// 没有 input/output token 时与 OpenCowork 的 accruePetExpFromUsage 一致：
	// 这是无可入账 usage，不要求 provider/model，也不能制造一条空经验日志。
	if event.Usage.InputTokens == 0 && event.Usage.OutputTokens == 0 {
		return s.GetExperience()
	}

	entry, err := buildPetExpEntryFromUsage(event)
	if err != nil {
		return PetExperience{}, err
	}
	return s.AddExperience(entry)
}

// AddExperienceFromRequestLog 接收目标项目已有 request_log 事实源。
// ReqeustLog.ID 是持久化请求的稳定身份，因此不能用当前时间或随机数替代幂等键。
func (s *PetService) AddExperienceFromRequestLog(logEntry ReqeustLog) (PetExperience, error) {
	if logEntry.ID <= 0 {
		return PetExperience{}, errors.New("request usage 缺少有效 id")
	}

	return s.AddExperienceFromUsage(PetUsageEvent{
		ID:       "request-log:" + strconv.FormatInt(logEntry.ID, 10),
		Provider: logEntry.Provider,
		Model:    logEntry.Model,
		At:       time.Now().UnixMilli(),
		Usage: modelpricing.UsageSnapshot{
			InputTokens:       logEntry.InputTokens,
			OutputTokens:      logEntry.OutputTokens,
			ReasoningTokens:   logEntry.ReasoningTokens,
			CacheCreateTokens: logEntry.CacheCreateTokens,
			CacheReadTokens:   logEntry.CacheReadTokens,
			CacheCreation: petCacheCreationDetail(
				logEntry.Ephemeral5mTokens,
				logEntry.Ephemeral1hTokens,
			),
			ServiceTier: modelpricing.NormalizeObservedServiceTier(logEntry.ServiceTier, nil),
		},
	})
}

func buildPetExpEntryFromUsage(event PetUsageEvent) (PetExpLogEntry, error) {
	id := strings.TrimSpace(event.ID)
	if id == "" {
		return PetExpLogEntry{}, errors.New("request usage 缺少幂等 id")
	}
	if strings.TrimSpace(event.Provider) == "" {
		return PetExpLogEntry{}, errors.New("request usage 缺少 provider")
	}
	model := strings.TrimSpace(event.Model)
	if model == "" {
		return PetExpLogEntry{}, errors.New("request usage 缺少 model")
	}

	pricing, err := modelpricing.DefaultService()
	if err != nil {
		return PetExpLogEntry{}, fmt.Errorf("读取 canonical model pricing 失败: %w", err)
	}
	premium, err := petUsagePremium(pricing, model, event.Usage.ServiceTier)
	if err != nil {
		return PetExpLogEntry{}, err
	}

	inputTokens := int64(event.Usage.InputTokens)
	outputTokens := int64(event.Usage.OutputTokens)
	if inputTokens > petMaxInt64-outputTokens {
		return PetExpLogEntry{}, errors.New("request usage token 总数溢出")
	}
	tokens := inputTokens + outputTokens
	if tokens <= 0 {
		return PetExpLogEntry{}, errors.New("request usage token 总数必须大于 0")
	}
	exp := float64(tokens) / petUsageTokensPerExp
	if premium {
		exp *= petPremiumExpMultiplier
	}
	exp = roundPetExp(exp)
	if exp <= 0 {
		return PetExpLogEntry{}, errors.New("request usage 换算后的经验为 0")
	}
	if math.IsNaN(exp) || math.IsInf(exp, 0) {
		return PetExpLogEntry{}, errors.New("request usage 经验不是有限数字")
	}

	at := event.At
	if at <= 0 {
		at = time.Now().UnixMilli()
	}
	return PetExpLogEntry{
		ID:      id,
		At:      at,
		Model:   model,
		Tokens:  tokens,
		Premium: premium,
		Exp:     exp,
	}, nil
}

func petUsagePremium(pricing *modelpricing.Service, model string, tier modelpricing.ServiceTier) (bool, error) {
	// CalculateCost 是目标项目唯一的价格解析入口；用一个普通输入 token 探测
	// canonical 输入单价，避免把价格常量复制到宠物模块，也避免把 output/cache 成本混入 premium 判断。
	probe := pricing.CalculateCost(model, modelpricing.UsageSnapshot{
		InputTokens: 1,
		ServiceTier: tier,
	})
	if !probe.HasPricing {
		return false, fmt.Errorf("模型 %q 缺少 canonical pricing", model)
	}
	inputPricePerMillion := probe.InputCost * 1_000_000
	if math.IsNaN(inputPricePerMillion) || math.IsInf(inputPricePerMillion, 0) || inputPricePerMillion < 0 {
		return false, fmt.Errorf("模型 %q 的 canonical input price 无效", model)
	}
	return inputPricePerMillion > petPremiumInputPriceThreshold, nil
}

func petCacheCreationDetail(fiveMinute, oneHour int) *modelpricing.CacheCreationDetail {
	if fiveMinute <= 0 && oneHour <= 0 {
		return nil
	}
	return &modelpricing.CacheCreationDetail{
		Ephemeral5mTokens: fiveMinute,
		Ephemeral1hTokens: oneHour,
	}
}

func hasInvalidPetUsage(usage modelpricing.UsageSnapshot) bool {
	return usage.InputTokens < 0 ||
		usage.OutputTokens < 0 ||
		usage.ReasoningTokens < 0 ||
		usage.CacheCreateTokens < 0 ||
		usage.CacheReadTokens < 0 ||
		(usage.CacheCreation != nil &&
			(usage.CacheCreation.Ephemeral5mTokens < 0 || usage.CacheCreation.Ephemeral1hTokens < 0))
}
