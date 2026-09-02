package services

import (
	"context"
	"strings"
)

// PetAgentModelReference 是宠物 Agent 运行时需要的最小模型引用。
// provider ID 和其它 Agent 设置仍不属于 Codex app-server 的请求覆盖项；这里
// 只传递模型选择和可选 reasoning，认证、provider、权限等继续由 Codex CLI 读取。
type PetAgentModelReference struct {
	ProviderPlatform string
	ModelID          string
	ReasoningEffort  PetReasoningEffort
}

// PetAgentModelReader 是 Project Manager 读取宠物 Agent 模型的窄接口。
// 调用方每次启动任务时读取，确保设置页刚保存的新模型不会被进程内缓存遮住。
type PetAgentModelReader interface {
	LoadAgentModelReference(context.Context, string) (PetAgentModelReference, error)
}

func normalizePetAgentModelReference(reference PetAgentModelReference) (PetAgentModelReference, error) {
	reference.ProviderPlatform = strings.ToLower(strings.TrimSpace(reference.ProviderPlatform))
	reference.ModelID = strings.TrimSpace(reference.ModelID)
	reference.ReasoningEffort = PetReasoningEffort(strings.ToLower(strings.TrimSpace(string(reference.ReasoningEffort))))
	providerReference := PetProviderReference{
		Platform:   reference.ProviderPlatform,
		Model:      reference.ModelID,
		Capability: PetCapabilityChat,
	}
	if reference.ModelID == "" {
		return PetAgentModelReference{}, newPetProviderError(
			PET_MODEL_NOT_CONFIGURED,
			providerReference,
			"宠物 Agent 未配置 model",
			nil,
		)
	}
	if reference.ProviderPlatform != "codex" {
		return PetAgentModelReference{}, newPetProviderError(
			PET_PLATFORM_UNSUPPORTED,
			providerReference,
			"宠物 Agent 主聊天只支持 codex platform",
			nil,
		)
	}
	if runeLen(reference.ModelID) > PetAIMaxModelLength || hasLineBreak(reference.ModelID) || strings.IndexByte(reference.ModelID, 0) >= 0 || strings.ContainsRune(reference.ModelID, '*') {
		return PetAgentModelReference{}, newPetProviderError(
			PET_PROVIDER_CONFIG_INVALID,
			providerReference,
			"宠物 Agent model 无效",
			nil,
		)
	}
	switch reference.ReasoningEffort {
	case "", PetReasoningNone, PetReasoningMinimal, PetReasoningLow, PetReasoningMedium, PetReasoningHigh:
		return reference, nil
	default:
		return PetAgentModelReference{}, newPetProviderError(
			PET_PROVIDER_CONFIG_INVALID,
			providerReference,
			"宠物 Agent reasoning 无效",
			nil,
		)
	}
}

func (r *PetCodexRuntime) loadPetAgentModelReference(ctx context.Context, petID string) (PetAgentModelReference, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PetAgentModelReference{}, newPetAIError(PET_AI_REQUEST_CANCELLED, 0, err)
	}
	if r == nil || r.agentModelReader == nil {
		// 纯协议 fixture 和不持有宠物数据库的嵌入方仍可验证 Codex 默认行为；
		// 正式应用入口会注入 PetDAO，此兼容分支不会产生模型回退。
		return PetAgentModelReference{}, nil
	}
	reference, err := r.agentModelReader.LoadAgentModelReference(ctx, strings.TrimSpace(petID))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PetAgentModelReference{}, newPetAIError(PET_AI_REQUEST_CANCELLED, 0, ctxErr)
		}
		return PetAgentModelReference{}, newPetAIError(PET_AI_DEPENDENCY_UNAVAILABLE, 0, err)
	}
	if err := ctx.Err(); err != nil {
		return PetAgentModelReference{}, newPetAIError(PET_AI_REQUEST_CANCELLED, 0, err)
	}
	return normalizePetAgentModelReference(reference)
}
