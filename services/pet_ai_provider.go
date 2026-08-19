package services

import (
	"context"
	"errors"
	"strings"
)

// PetAIProviderReaderAdapter 将 ProviderService/GeminiService 的配置投影为
// PetAIService 能直接消费的临时运行时配置。reader 本身只保存窄接口和解析器，
// 不缓存 provider 实体，避免凭据随着服务生命周期滞留在内存状态中。
type PetAIProviderReaderAdapter struct {
	providerReader PetProviderReader
	geminiReader   PetGeminiReader
	resolver       *PetProviderResolver
}

var _ PetAIProviderReader = (*PetAIProviderReaderAdapter)(nil)

// NewPetAIProviderReader 创建真实 provider 配置 reader。
// 参数使用现有服务实现的窄接口，既保持对 ProviderService/GeminiService 的依赖，
// 也让读取顺序、错误码和取消边界可以在不触碰真实文件的测试中被精确验证。
func NewPetAIProviderReader(
	providerService PetProviderReader,
	geminiService PetGeminiReader,
) *PetAIProviderReaderAdapter {
	return &PetAIProviderReaderAdapter{
		providerReader: providerService,
		geminiReader:   geminiService,
		resolver:       NewPetProviderResolver(providerService, geminiService),
	}
}

// Read 先用 PetProviderResolver 验证引用，再按解析后的规范 ID 读取实体配置。
// AutoFallback 只作为引用快照的一部分返回，整个过程不会寻找或切换其他 provider。
func (r *PetAIProviderReaderAdapter) Read(
	ctx context.Context,
	reference PetProviderReference,
) (PetAIProviderConfig, error) {
	if ctx == nil {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_UPSTREAM_ERROR,
			reference,
			"provider 配置读取上下文不可用",
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return PetAIProviderConfig{}, err
	}
	if r == nil {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_UPSTREAM_ERROR,
			reference,
			"provider 配置读取不可用",
			nil,
		)
	}

	resolver := r.resolver
	if resolver == nil {
		resolver = NewPetProviderResolver(r.providerReader, r.geminiReader)
	}
	resolution, err := resolver.Resolve(reference)
	// resolver 底层读取不接受 context，所以无论成功还是失败都必须在返回前
	// 再检查一次；取消优先于读取错误，避免取消后的凭据继续进入返回值。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PetAIProviderConfig{}, ctxErr
	}
	if err != nil {
		return PetAIProviderConfig{}, err
	}
	if resolution == nil {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_PROVIDER_CONFIG_INVALID,
			reference,
			"provider 解析结果为空",
			nil,
		)
	}

	if resolution.Platform == "gemini" {
		return r.readGemini(ctx, resolution)
	}
	return r.readProvider(ctx, resolution)
}

func (r *PetAIProviderReaderAdapter) readProvider(
	ctx context.Context,
	resolution *PetProviderResolution,
) (PetAIProviderConfig, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PetAIProviderConfig{}, ctxErr
	}
	if r.providerReader == nil {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_UPSTREAM_ERROR,
			resolution.Reference,
			"ProviderService 读取接口不可用",
			nil,
		)
	}

	providers, err := r.providerReader.LoadProviders(resolution.Platform)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PetAIProviderConfig{}, ctxErr
	}
	if err != nil {
		return PetAIProviderConfig{}, petAIProviderReadError(
			resolution.Reference,
			"读取 provider 配置失败",
			err,
		)
	}

	provider, found := findProviderByReference(providers, resolution.ProviderID)
	if !found {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_PROVIDER_NOT_FOUND,
			resolution.Reference,
			"未找到引用的 provider",
			nil,
		)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PetAIProviderConfig{}, ctxErr
	}

	// 二次读取可能遇到外部配置变更，因此继续以这次命中的实体完成模型映射和
	// capability 校验，避免把第一次解析的有效模型错误地套在新配置上。
	if !provider.IsModelSupported(resolution.Reference.Model) {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_MODEL_UNSUPPORTED,
			resolution.Reference,
			"provider 不支持引用的 model",
			nil,
		)
	}
	effectiveModel := strings.TrimSpace(provider.GetEffectiveModel(resolution.Reference.Model))
	if effectiveModel == "" {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_MODEL_NOT_CONFIGURED,
			resolution.Reference,
			"provider 未解析出有效 model",
			nil,
		)
	}
	modelCategory, categoryExplicit := provider.GetModelCategory(resolution.Reference.Model, effectiveModel)
	if !petCapabilitySupportedForModel(resolution.Reference.Capability, effectiveModel, modelCategory, categoryExplicit) {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_CAPABILITY_UNSUPPORTED,
			resolution.Reference,
			"model 不支持引用的宠物能力",
			nil,
		)
	}
	resolution.EffectiveModel = effectiveModel
	resolution.ModelCategory = modelCategory

	protocol, upstreamProtocol, err := normalizePetAIProviderProtocol(
		resolution.Platform,
		provider.UpstreamProtocol,
		provider.APIEndpoint,
	)
	if err != nil {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_PROVIDER_PROTOCOL_INVALID,
			resolution.Reference,
			"provider protocol 不支持",
			nil,
		)
	}
	authType, authHeader, err := normalizePetAIProviderAuth(provider.ConnectivityAuthType)
	if err != nil {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_PROVIDER_CONFIG_INVALID,
			resolution.Reference,
			"provider 认证 Header 不安全",
			nil,
		)
	}

	return PetAIProviderConfig{
		Resolution:       *resolution,
		Platform:         resolution.Platform,
		ProviderID:       resolution.ProviderID,
		Model:            effectiveModel,
		EffectiveModel:   effectiveModel,
		ModelCategory:    modelCategory,
		APIURL:           strings.TrimSpace(provider.APIURL),
		APIKey:           strings.TrimSpace(provider.APIKey),
		APIEndpoint:      strings.TrimSpace(provider.APIEndpoint),
		Protocol:         protocol,
		UpstreamProtocol: upstreamProtocol,
		AuthType:         authType,
		AuthHeader:       authHeader,
	}, nil
}

func (r *PetAIProviderReaderAdapter) readGemini(
	ctx context.Context,
	resolution *PetProviderResolution,
) (PetAIProviderConfig, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PetAIProviderConfig{}, ctxErr
	}
	if r.geminiReader == nil {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_UPSTREAM_ERROR,
			resolution.Reference,
			"GeminiService 读取接口不可用",
			nil,
		)
	}

	providers := r.geminiReader.GetProviders()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PetAIProviderConfig{}, ctxErr
	}
	provider, found := findGeminiProviderByReference(providers, resolution.ProviderID)
	if !found {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_PROVIDER_NOT_FOUND,
			resolution.Reference,
			"未找到引用的 Gemini provider",
			nil,
		)
	}

	// GeminiService 允许顶级字段覆盖 EnvConfig；这里沿用同一优先级，且只把
	// BaseURL/APIKey/Model 投影到临时返回值，不把包含密钥的 EnvConfig 原样暴露。
	baseURL := geminiProviderEnvValue(provider, "GOOGLE_GEMINI_BASE_URL", provider.BaseURL)
	apiKey := geminiProviderEnvValue(provider, "GEMINI_API_KEY", provider.APIKey)
	effectiveModel := configuredGeminiModel(provider)
	if effectiveModel == "" {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_MODEL_NOT_CONFIGURED,
			resolution.Reference,
			"Gemini provider 未配置 model",
			nil,
		)
	}
	if effectiveModel != resolution.Reference.Model {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_MODEL_UNSUPPORTED,
			resolution.Reference,
			"Gemini provider 不支持引用的 model",
			nil,
		)
	}
	modelCategory := normalizeModelCategory(provider.ModelCategory)
	if !petCapabilitySupportedForModel(resolution.Reference.Capability, effectiveModel, modelCategory, modelCategory != "") {
		return PetAIProviderConfig{}, newPetProviderError(
			PET_CAPABILITY_UNSUPPORTED,
			resolution.Reference,
			"model 不支持引用的宠物能力",
			nil,
		)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return PetAIProviderConfig{}, ctxErr
	}
	resolution.EffectiveModel = effectiveModel
	resolution.ModelCategory = modelCategory

	return PetAIProviderConfig{
		Resolution:       *resolution,
		Platform:         "gemini",
		ProviderID:       resolution.ProviderID,
		Model:            effectiveModel,
		EffectiveModel:   effectiveModel,
		ModelCategory:    modelCategory,
		BaseURL:          baseURL,
		APIKey:           apiKey,
		Protocol:         "gemini",
		UpstreamProtocol: "gemini",
	}, nil
}

func petAIProviderReadError(
	reference PetProviderReference,
	reason string,
	cause error,
) error {
	if code := PetProviderErrorCodeOf(cause); code != "" {
		// 只投影稳定错误码，避免窄接口实现把底层错误文案（可能含密钥）带出。
		return newPetProviderError(code, reference, "provider 引用解析失败", nil)
	}
	return newPetProviderError(PET_UPSTREAM_ERROR, reference, reason, cause)
}

func normalizePetAIProviderProtocol(
	platform string,
	configuredProtocol string,
	apiEndpoint string,
) (string, string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	raw := strings.ToLower(strings.TrimSpace(configuredProtocol))
	defaultProtocol := "openai"
	if platform == "claude" {
		defaultProtocol = "anthropic"
	} else if platform == "codex" {
		// Codex provider 的平台契约是 Responses API；只有显式配置 Chat
		// 或 endpoint 已明确指向 Chat 时，才兼容旧的 OpenAI Chat 代理。
		defaultProtocol = "responses"
	}

	var protocol string
	switch raw {
	case "":
		protocol = defaultProtocol
	case "auto":
		// auto 只根据当前 provider 的 endpoint 判断，判断失败时回到该 platform
		// 的协议默认值，不会寻找另一个 provider 或修改当前 provider 配置。
		endpoint := strings.ToLower(strings.TrimSpace(apiEndpoint))
		if strings.Contains(endpoint, "/chat/completions") {
			protocol = "openai"
		} else if strings.Contains(endpoint, "/messages") {
			protocol = "anthropic"
		} else if strings.Contains(endpoint, "/responses") {
			protocol = "responses"
		} else {
			protocol = defaultProtocol
		}
	case "openai", "openai-chat", "openai_chat", "openai-compatible", "openai_compatible":
		protocol = "openai"
	case "responses", "openai-responses", "openai_responses", "codex":
		protocol = "responses"
	case "anthropic", "anthropic-messages", "messages":
		protocol = "anthropic"
	default:
		return "", "", errors.New("unsupported provider protocol")
	}

	upstreamProtocol := strings.TrimSpace(configuredProtocol)
	if upstreamProtocol == "" {
		upstreamProtocol = protocol
	}
	return protocol, upstreamProtocol, nil
}

func normalizePetAIProviderAuth(configuredAuth string) (string, string, error) {
	trimmed := strings.TrimSpace(configuredAuth)
	switch strings.ToLower(trimmed) {
	case "":
		// 交给 pet_ai.go 按协议选择默认 Header：Anthropic 使用 x-api-key，
		// Gemini 使用 x-goog-api-key，OpenAI 使用 Authorization Bearer。
		return "", "", nil
	case "bearer":
		return "bearer", "", nil
	case "x-api-key", "x_api_key":
		return "x-api-key", "", nil
	case "api-key", "api_key":
		return "api-key", "", nil
	case "custom":
		return "custom", "Authorization", nil
	default:
		if hasLineBreak(trimmed) {
			return "", "", errors.New("unsafe provider auth header")
		}
		return "custom", trimmed, nil
	}
}

func geminiProviderEnvValue(provider GeminiProvider, key, topLevel string) string {
	if value := strings.TrimSpace(topLevel); value != "" {
		return value
	}
	if provider.EnvConfig == nil {
		return ""
	}
	return strings.TrimSpace(provider.EnvConfig[key])
}
