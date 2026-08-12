package services

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PetProviderErrorCode 是宠物 provider/model 引用解析失败时的稳定错误码。
// 调用方应按 Code 判断分支，不应依赖中文错误文本。
type PetProviderErrorCode string

const (
	PET_PROVIDER_NOT_FOUND     PetProviderErrorCode = "PET_PROVIDER_NOT_FOUND"
	PET_MODEL_NOT_CONFIGURED   PetProviderErrorCode = "PET_MODEL_NOT_CONFIGURED"
	PET_MODEL_UNSUPPORTED      PetProviderErrorCode = "PET_MODEL_UNSUPPORTED"
	PET_CAPABILITY_UNSUPPORTED PetProviderErrorCode = "PET_CAPABILITY_UNSUPPORTED"
	PET_UPSTREAM_ERROR         PetProviderErrorCode = "PET_UPSTREAM_ERROR"
	PET_PLATFORM_UNSUPPORTED   PetProviderErrorCode = "PET_PLATFORM_UNSUPPORTED"
	PET_REFERENCE_INVALID      PetProviderErrorCode = "PET_REFERENCE_INVALID"
	PET_SPEECH_NOT_CONFIGURED  PetProviderErrorCode = "PET_SPEECH_NOT_CONFIGURED"
)

// PetProviderError 是不携带 provider 配置内容的结构化错误。
// cause 只用于保留底层错误的 errors.Is/errors.As 能力，Error() 不会输出其文本，
// 以免底层读取错误意外把配置文件内容或敏感字段带到 UI/日志。
type PetProviderError struct {
	Code       PetProviderErrorCode
	Platform   string
	ProviderID string
	Model      string
	Capability PetCapability
	Reason     string
	cause      error
}

func (e *PetProviderError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Reason == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Reason)
}

func (e *PetProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is 让 errors.Is 可以按错误码判断，而不是要求调用方匹配具体文案。
func (e *PetProviderError) Is(target error) bool {
	other, ok := target.(*PetProviderError)
	return ok && e != nil && other != nil && e.Code == other.Code
}

// PetProviderErrorCodeOf 返回链路中最外层的宠物引用错误码。
func PetProviderErrorCodeOf(err error) PetProviderErrorCode {
	var petErr *PetProviderError
	if errors.As(err, &petErr) && petErr != nil {
		return petErr.Code
	}
	return ""
}

// IsPetProviderErrorCode 判断错误是否属于指定的宠物引用错误码。
func IsPetProviderErrorCode(err error, code PetProviderErrorCode) bool {
	return PetProviderErrorCodeOf(err) == code
}

// PetProviderReader 是 ProviderService 的最小读取接口。
// 解析层只依赖 LoadProviders，便于主控注入真实服务或测试替身。
type PetProviderReader interface {
	LoadProviders(kind string) ([]Provider, error)
}

// PetGeminiReader 是 GeminiService 的最小读取接口。
// Gemini 配置是独立存储，不能通过普通 ProviderService 的 platform 分支猜测。
type PetGeminiReader interface {
	GetProviders() []GeminiProvider
}

// 使用编译期断言锁定窄接口与现有服务的兼容关系。
var _ PetProviderReader = (*ProviderService)(nil)
var _ PetGeminiReader = (*GeminiService)(nil)

// PetProviderResolution 是解析后的无凭据结果。
// Provider/GeminiProvider 结构体内部可能含 APIKey，故这里刻意只投影身份和模型信息，
// 让宠物状态、IPC payload 和后续编排层无法通过结果对象顺手复制密钥。
type PetProviderResolution struct {
	Reference      PetProviderReference `json:"reference"`
	Platform       string               `json:"platform"`
	ProviderID     string               `json:"providerId"`
	ProviderName   string               `json:"providerName,omitempty"`
	Model          string               `json:"model"`
	EffectiveModel string               `json:"effectiveModel"`
	Capability     PetCapability        `json:"capability"`
	ModelCategory  string               `json:"modelCategory,omitempty"`
	AutoFallback   bool                 `json:"autoFallback"`
}

// PetProviderResolver 只负责把宠物的引用解析成可供后续请求层使用的安全快照。
// 它不发起网络请求、不切换 provider，也不修改任何 provider owner 状态。
type PetProviderResolver struct {
	providerReader PetProviderReader
	geminiReader   PetGeminiReader
}

// NewPetProviderResolver 构造引用解析器。
// 两个参数允许为 nil；只有解析到对应 platform 时才会报告 PET_UPSTREAM_ERROR。
func NewPetProviderResolver(providerReader PetProviderReader, geminiReader PetGeminiReader) *PetProviderResolver {
	return &PetProviderResolver{
		providerReader: providerReader,
		geminiReader:   geminiReader,
	}
}

// Resolve 校验并规范化一个宠物 provider/model 引用。
// AutoFallback 只作为结果中的策略字段保留，解析过程永远只验证当前引用，不寻找替代 provider。
func (r *PetProviderResolver) Resolve(reference PetProviderReference) (*PetProviderResolution, error) {
	normalized, platform, err := normalizePetProviderReference(reference)
	if err != nil {
		return nil, err
	}

	if platform == "gemini" {
		return r.resolveGemini(normalized)
	}
	return r.resolveProvider(normalized, platform)
}

// ValidateReference 仅执行引用校验，不向调用方暴露任何 provider 配置实体。
func (r *PetProviderResolver) ValidateReference(reference PetProviderReference) error {
	_, err := r.Resolve(reference)
	return err
}

type petProviderPlatform struct {
	canonical string
	loadKind  string
}

func normalizePetProviderReference(reference PetProviderReference) (PetProviderReference, string, error) {
	normalized := reference
	normalized.Platform = strings.ToLower(strings.TrimSpace(reference.Platform))
	normalized.ProviderID = strings.TrimSpace(reference.ProviderID)
	normalized.Model = strings.TrimSpace(reference.Model)
	normalized.Capability = PetCapability(strings.ToLower(strings.TrimSpace(string(reference.Capability))))

	platform, err := normalizePetProviderPlatform(normalized.Platform)
	if err != nil {
		return normalized, "", newPetProviderError(
			PET_PLATFORM_UNSUPPORTED,
			normalized,
			"不支持的 provider platform",
			nil,
		)
	}
	if normalized.ProviderID == "" {
		return normalized, "", newPetProviderError(
			PET_PROVIDER_NOT_FOUND,
			normalized,
			"provider 引用不能为空",
			nil,
		)
	}
	if normalized.Model == "" {
		return normalized, "", newPetProviderError(
			PET_MODEL_NOT_CONFIGURED,
			normalized,
			"model 引用不能为空",
			nil,
		)
	}
	if !isPetCapability(normalized.Capability) {
		return normalized, "", newPetProviderError(
			PET_CAPABILITY_UNSUPPORTED,
			normalized,
			"不支持的宠物能力",
			nil,
		)
	}

	return normalized, platform.canonical, nil
}

func normalizePetProviderPlatform(platform string) (petProviderPlatform, error) {
	switch platform {
	case "claude", "claude-code", "claude_code":
		return petProviderPlatform{canonical: "claude", loadKind: "claude"}, nil
	case "codex":
		return petProviderPlatform{canonical: "codex", loadKind: "codex"}, nil
	case "gemini":
		return petProviderPlatform{canonical: "gemini", loadKind: "gemini"}, nil
	}

	if strings.HasPrefix(platform, "custom:") {
		toolID := strings.TrimSpace(strings.TrimPrefix(platform, "custom:"))
		if toolID != "" {
			kind := "custom:" + toolID
			return petProviderPlatform{canonical: kind, loadKind: kind}, nil
		}
	}
	return petProviderPlatform{}, fmt.Errorf("unsupported provider platform")
}

func (r *PetProviderResolver) resolveProvider(reference PetProviderReference, platform string) (*PetProviderResolution, error) {
	platformInfo, _ := normalizePetProviderPlatform(platform)
	if r == nil || r.providerReader == nil {
		return nil, newPetProviderError(
			PET_UPSTREAM_ERROR,
			reference,
			"ProviderService 读取接口不可用",
			nil,
		)
	}

	providers, err := r.providerReader.LoadProviders(platformInfo.loadKind)
	if err != nil {
		return nil, newPetProviderError(
			PET_UPSTREAM_ERROR,
			reference,
			"读取 provider 配置失败",
			err,
		)
	}

	provider, found := findProviderByReference(providers, reference.ProviderID)
	if !found {
		return nil, newPetProviderError(
			PET_PROVIDER_NOT_FOUND,
			reference,
			"未找到引用的 provider",
			nil,
		)
	}

	if !provider.IsModelSupported(reference.Model) {
		return nil, newPetProviderError(
			PET_MODEL_UNSUPPORTED,
			reference,
			"provider 不支持引用的 model",
			nil,
		)
	}

	effectiveModel := strings.TrimSpace(provider.GetEffectiveModel(reference.Model))
	if effectiveModel == "" {
		return nil, newPetProviderError(
			PET_MODEL_NOT_CONFIGURED,
			reference,
			"provider 未解析出有效 model",
			nil,
		)
	}
	modelCategory, categoryExplicit := provider.GetModelCategory(reference.Model, effectiveModel)
	if !petCapabilitySupportedForModel(reference.Capability, effectiveModel, modelCategory, categoryExplicit) {
		return nil, newPetProviderError(
			PET_CAPABILITY_UNSUPPORTED,
			reference,
			"model 不支持引用的宠物能力",
			nil,
		)
	}

	canonicalProviderID := strconv.FormatInt(provider.ID, 10)
	reference.ProviderID = canonicalProviderID
	return &PetProviderResolution{
		Reference:      reference,
		Platform:       platformInfo.canonical,
		ProviderID:     canonicalProviderID,
		ProviderName:   strings.TrimSpace(provider.Name),
		Model:          reference.Model,
		EffectiveModel: effectiveModel,
		Capability:     reference.Capability,
		ModelCategory:  modelCategory,
		AutoFallback:   reference.AutoFallback,
	}, nil
}

func (r *PetProviderResolver) resolveGemini(reference PetProviderReference) (*PetProviderResolution, error) {
	if r == nil || r.geminiReader == nil {
		return nil, newPetProviderError(
			PET_UPSTREAM_ERROR,
			reference,
			"GeminiService 读取接口不可用",
			nil,
		)
	}

	provider, found := findGeminiProviderByReference(r.geminiReader.GetProviders(), reference.ProviderID)
	if !found {
		return nil, newPetProviderError(
			PET_PROVIDER_NOT_FOUND,
			reference,
			"未找到引用的 Gemini provider",
			nil,
		)
	}

	configuredModel := configuredGeminiModel(provider)
	if configuredModel == "" {
		return nil, newPetProviderError(
			PET_MODEL_NOT_CONFIGURED,
			reference,
			"Gemini provider 未配置 model",
			nil,
		)
	}
	if configuredModel != reference.Model {
		return nil, newPetProviderError(
			PET_MODEL_UNSUPPORTED,
			reference,
			"Gemini provider 不支持引用的 model",
			nil,
		)
	}
	modelCategory := normalizeModelCategory(provider.ModelCategory)
	if !petCapabilitySupportedForModel(reference.Capability, configuredModel, modelCategory, modelCategory != "") {
		return nil, newPetProviderError(
			PET_CAPABILITY_UNSUPPORTED,
			reference,
			"model 不支持引用的宠物能力",
			nil,
		)
	}

	reference.ProviderID = strings.TrimSpace(provider.ID)
	return &PetProviderResolution{
		Reference:      reference,
		Platform:       "gemini",
		ProviderID:     reference.ProviderID,
		ProviderName:   strings.TrimSpace(provider.Name),
		Model:          reference.Model,
		EffectiveModel: configuredModel,
		Capability:     reference.Capability,
		ModelCategory:  modelCategory,
		AutoFallback:   reference.AutoFallback,
	}, nil
}

func newPetProviderError(code PetProviderErrorCode, reference PetProviderReference, reason string, cause error) *PetProviderError {
	return &PetProviderError{
		Code:       code,
		Platform:   strings.TrimSpace(reference.Platform),
		ProviderID: strings.TrimSpace(reference.ProviderID),
		Model:      strings.TrimSpace(reference.Model),
		Capability: reference.Capability,
		Reason:     reason,
		cause:      cause,
	}
}

func findProviderByReference(providers []Provider, referenceID string) (Provider, bool) {
	if isNumericProviderReference(referenceID) {
		id, err := strconv.ParseInt(referenceID, 10, 64)
		if err != nil {
			// 数值形式只允许按 Provider.ID 查找；解析失败时不能退回名称别名，
			// 否则名称恰好是数字的 provider 会被静默误选。
			return Provider{}, false
		}
		for _, provider := range providers {
			if provider.ID == id {
				return provider, true
			}
		}
		return Provider{}, false
	}

	var exact Provider
	exactCount := 0
	for _, provider := range providers {
		if strings.TrimSpace(provider.Name) == referenceID {
			exact = provider
			exactCount++
		}
	}
	if exactCount == 1 {
		return exact, true
	}
	if exactCount > 1 {
		return Provider{}, false
	}

	var folded Provider
	foldedCount := 0
	for _, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(provider.Name), referenceID) {
			folded = provider
			foldedCount++
		}
	}
	if foldedCount == 1 {
		return folded, true
	}
	return Provider{}, false
}

func findGeminiProviderByReference(providers []GeminiProvider, referenceID string) (GeminiProvider, bool) {
	// Gemini ID 本身是字符串，先做精确 ID 匹配；这与普通 provider 的 int64 ID 解析路径明确分开。
	var exactID GeminiProvider
	exactIDCount := 0
	for _, provider := range providers {
		if strings.TrimSpace(provider.ID) == referenceID {
			exactID = provider
			exactIDCount++
		}
	}
	if exactIDCount == 1 {
		return exactID, true
	}
	if exactIDCount > 1 {
		return GeminiProvider{}, false
	}

	// 非数值引用才允许作为 Gemini 的显式字符串别名，避免数字 ID 误落到名称匹配。
	if isNumericProviderReference(referenceID) {
		return GeminiProvider{}, false
	}

	aliases := func(provider GeminiProvider) []string {
		return []string{
			strings.TrimSpace(provider.Name),
			strings.TrimSpace(provider.PartnerPromotionKey),
		}
	}
	var exactAlias GeminiProvider
	exactAliasCount := 0
	for _, provider := range providers {
		for _, alias := range aliases(provider) {
			if alias != "" && alias == referenceID {
				exactAlias = provider
				exactAliasCount++
				break
			}
		}
	}
	if exactAliasCount == 1 {
		return exactAlias, true
	}
	if exactAliasCount > 1 {
		return GeminiProvider{}, false
	}

	var foldedAlias GeminiProvider
	foldedAliasCount := 0
	for _, provider := range providers {
		for _, alias := range aliases(provider) {
			if alias != "" && strings.EqualFold(alias, referenceID) {
				foldedAlias = provider
				foldedAliasCount++
				break
			}
		}
	}
	if foldedAliasCount == 1 {
		return foldedAlias, true
	}
	return GeminiProvider{}, false
}

func configuredGeminiModel(provider GeminiProvider) string {
	if model := strings.TrimSpace(provider.Model); model != "" {
		return model
	}
	// GeminiService 支持把模型放在 EnvConfig；读取这个已有配置字段不等于切换 provider，
	// 但能避免把一个明确配置过的独立 Gemini provider 误判成未配置。
	if provider.EnvConfig != nil {
		return strings.TrimSpace(provider.EnvConfig["GEMINI_MODEL"])
	}
	return ""
}

func isPetCapability(capability PetCapability) bool {
	switch capability {
	case PetCapabilityChat, PetCapabilityTTS, PetCapabilityImage, PetCapabilityTranscription:
		return true
	default:
		return false
	}
}

func petCapabilitySupported(capability PetCapability, model string) bool {
	switch capability {
	case PetCapabilityChat:
		return true
	case PetCapabilityTTS:
		return modelContainsAny(model, "tts", "text-to-speech", "speech", "audio", "voice")
	case PetCapabilityImage:
		return modelContainsAny(
			model,
			"image",
			"vision",
			"visual",
			"multimodal",
			"dall-e",
			"imagen",
			"flux",
			"-vl",
			"_vl",
			"vl-",
		)
	case PetCapabilityTranscription:
		// 目标 Provider 结构暂未携带源项目的 model category；这里至少要求
		// 模型名明确声明转写能力，避免把普通 chat/TTS 模型送到上传端点。
		return modelContainsAny(model, "transcrib", "speech-to-text", "speech_to_text", "stt", "whisper")
	default:
		return false
	}
}

// petCapabilitySupportedForModel 优先使用源项目同等的显式类别；旧 provider 没有
// modelCategories 时才走兼容启发式，避免迁移后的历史配置被无故判为不可用。
func petCapabilitySupportedForModel(capability PetCapability, model, category string, explicit bool) bool {
	if explicit {
		return petCapabilitySupportedByCategory(capability, category)
	}
	return petCapabilitySupported(capability, model)
}

func petCapabilitySupportedByCategory(capability PetCapability, category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "chat":
		return capability == PetCapabilityChat
	case "speech":
		return capability == PetCapabilityTTS || capability == PetCapabilityTranscription
	case "image":
		return capability == PetCapabilityImage
	default:
		return false
	}
}

func modelContainsAny(model string, markers ...string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, marker := range markers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func isNumericProviderReference(referenceID string) bool {
	if referenceID == "" {
		return false
	}
	start := 0
	if referenceID[0] == '+' || referenceID[0] == '-' {
		start = 1
	}
	if start == len(referenceID) {
		return false
	}
	for _, char := range referenceID[start:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
