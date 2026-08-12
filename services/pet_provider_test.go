package services

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type petProviderReaderStub struct {
	providers      map[string][]Provider
	err            error
	requestedKinds []string
}

func (s *petProviderReaderStub) LoadProviders(kind string) ([]Provider, error) {
	s.requestedKinds = append(s.requestedKinds, kind)
	if s.err != nil {
		return nil, s.err
	}
	return s.providers[kind], nil
}

type petGeminiReaderStub struct {
	providers []GeminiProvider
}

func (s *petGeminiReaderStub) GetProviders() []GeminiProvider {
	return s.providers
}

func petTestReference(platform, providerID, model string, capability PetCapability) PetProviderReference {
	return PetProviderReference{
		Platform:   platform,
		ProviderID: providerID,
		Model:      model,
		Capability: capability,
	}
}

func assertPetProviderErrorCode(t *testing.T, err error, expected PetProviderErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误码 %s，但没有返回错误", expected)
	}
	if !IsPetProviderErrorCode(err, expected) {
		t.Fatalf("错误码 = %s，期望 %s；error=%v", PetProviderErrorCodeOf(err), expected, err)
	}
	if !errors.Is(err, &PetProviderError{Code: expected}) {
		t.Fatalf("errors.Is 无法判断错误码 %s；error=%v", expected, err)
	}

	var petErr *PetProviderError
	if !errors.As(err, &petErr) || petErr.Code != expected {
		t.Fatalf("errors.As 未得到结构化错误 %s；error=%v", expected, err)
	}
}

func TestPetProviderResolverProviderNotFoundDoesNotFallbackNumericIDToAlias(t *testing.T) {
	reader := &petProviderReaderStub{
		providers: map[string][]Provider{
			"claude": {{ID: 7, Name: "7", SupportedModels: map[string]bool{"pet-chat": true}}},
		},
	}
	resolver := NewPetProviderResolver(reader, nil)

	err := resolver.ValidateReference(petTestReference("claude", "8", "pet-chat", PetCapabilityChat))
	assertPetProviderErrorCode(t, err, PET_PROVIDER_NOT_FOUND)
	if len(reader.requestedKinds) != 1 || reader.requestedKinds[0] != "claude" {
		t.Fatalf("ProviderService 读取 platform = %v，期望 [claude]", reader.requestedKinds)
	}
}

func TestPetProviderResolverModelErrors(t *testing.T) {
	reader := &petProviderReaderStub{
		providers: map[string][]Provider{
			"claude": {{ID: 7, Name: "primary", SupportedModels: map[string]bool{"model-a": true}}},
		},
	}
	resolver := NewPetProviderResolver(reader, nil)

	t.Run("model missing", func(t *testing.T) {
		err := resolver.ValidateReference(petTestReference("claude", "7", "", PetCapabilityChat))
		assertPetProviderErrorCode(t, err, PET_MODEL_NOT_CONFIGURED)
	})

	t.Run("model unsupported", func(t *testing.T) {
		err := resolver.ValidateReference(petTestReference("claude", "7", "model-b", PetCapabilityChat))
		assertPetProviderErrorCode(t, err, PET_MODEL_UNSUPPORTED)
	})
}

func TestPetProviderResolverCapabilityUnsupported(t *testing.T) {
	reader := &petProviderReaderStub{
		providers: map[string][]Provider{
			"codex": {{ID: 9, Name: "fast", SupportedModels: map[string]bool{"chat-model": true}}},
		},
	}
	resolver := NewPetProviderResolver(reader, nil)

	err := resolver.ValidateReference(petTestReference("codex", "fast", "chat-model", PetCapabilityTTS))
	assertPetProviderErrorCode(t, err, PET_CAPABILITY_UNSUPPORTED)
}

func TestPetProviderResolverExplicitModelCategoryOverridesNameHeuristic(t *testing.T) {
	reader := &petProviderReaderStub{providers: map[string][]Provider{
		"codex": {{
			ID:              9,
			Name:            "speech-relay",
			SupportedModels: map[string]bool{"custom-model": true},
			ModelCategories: map[string]string{"custom-model": "speech"},
		}},
	}}
	resolver := NewPetProviderResolver(reader, nil)

	resolution, err := resolver.Resolve(petTestReference("codex", "9", "custom-model", PetCapabilityTranscription))
	if err != nil {
		t.Fatalf("显式 speech 类别解析失败: %v", err)
	}
	if resolution.ModelCategory != "speech" {
		t.Fatalf("解析结果类别 = %q，期望 speech", resolution.ModelCategory)
	}
}

func TestPetProviderResolverUnmatchedModelCategoryKeepsLegacySpeechHeuristic(t *testing.T) {
	reader := &petProviderReaderStub{providers: map[string][]Provider{
		"codex": {{
			ID:              9,
			Name:            "legacy-speech",
			SupportedModels: map[string]bool{"whisper-custom": true, "chat-model": true},
			ModelCategories: map[string]string{"chat-model": "chat"},
		}},
	}}
	resolver := NewPetProviderResolver(reader, nil)

	resolution, err := resolver.Resolve(petTestReference("codex", "9", "whisper-custom", PetCapabilityTranscription))
	if err != nil {
		t.Fatalf("未命中类别的旧转写模型不应被拒绝: %v", err)
	}
	if resolution.ModelCategory != "" {
		t.Fatalf("未命中类别应保持空值，得到 %q", resolution.ModelCategory)
	}
}

func TestPetProviderResolverOverlappingModelCategoriesAreDeterministic(t *testing.T) {
	provider := Provider{ModelCategories: map[string]string{
		"gpt-*":  "chat",
		"gpt-4*": "speech",
	}}
	for i := 0; i < 20; i++ {
		category, explicit := provider.GetModelCategory("gpt-4o-transcribe", "gpt-4o-transcribe")
		if !explicit || category != "speech" {
			t.Fatalf("重叠类别匹配 = %q/%v，期望 speech/true", category, explicit)
		}
	}
}

func TestPetProviderResolverInvalidModelCategoryIsRejectedAtSaveValidation(t *testing.T) {
	provider := Provider{ModelCategories: map[string]string{"model": "audio"}}
	if errors := provider.ValidateConfiguration(); len(errors) == 0 {
		t.Fatal("非法模型类别应被 provider 配置校验拒绝")
	}
}

func TestPetProviderResolverValidReferencesKeepCredentialsOutOfResolution(t *testing.T) {
	reader := &petProviderReaderStub{
		providers: map[string][]Provider{
			"claude": {
				{
					ID:   42,
					Name: "primary",
					// 读取实体可以含密钥，但宠物引用和解析结果不能复制它。
					APIKey: "provider-secret",
					SupportedModels: map[string]bool{
						"pet-chat":      true,
						"provider-chat": true,
					},
					ModelMapping: map[string]string{"pet-alias": "provider-chat"},
				},
			},
		},
	}
	resolver := NewPetProviderResolver(reader, nil)

	numericResolution, err := resolver.Resolve(PetProviderReference{
		Platform:     "claude-code",
		ProviderID:   "42",
		Model:        "pet-chat",
		Capability:   PetCapabilityChat,
		AutoFallback: true,
	})
	if err != nil {
		t.Fatalf("数值 provider 引用解析失败: %v", err)
	}
	if numericResolution.ProviderID != "42" || numericResolution.Platform != "claude" {
		t.Fatalf("数值引用未规范化: %+v", numericResolution)
	}
	if !numericResolution.AutoFallback || numericResolution.EffectiveModel != "pet-chat" {
		t.Fatalf("解析结果丢失策略或有效模型: %+v", numericResolution)
	}

	aliasResolution, err := resolver.Resolve(petTestReference("claude", "primary", "pet-alias", PetCapabilityChat))
	if err != nil {
		t.Fatalf("字符串别名引用解析失败: %v", err)
	}
	if aliasResolution.ProviderID != "42" || aliasResolution.EffectiveModel != "provider-chat" {
		t.Fatalf("别名引用未解析到规范 ID/映射模型: %+v", aliasResolution)
	}
	if err := resolver.ValidateReference(petTestReference("claude", "primary", "pet-chat", PetCapabilityChat)); err != nil {
		t.Fatalf("有效引用校验失败: %v", err)
	}

	encoded, err := json.Marshal(numericResolution)
	if err != nil {
		t.Fatalf("序列化无凭据解析结果失败: %v", err)
	}
	if strings.Contains(string(encoded), "provider-secret") || strings.Contains(string(encoded), "apiKey") {
		t.Fatalf("解析结果泄漏 provider 凭据: %s", encoded)
	}
}

func TestPetProviderResolverGeminiIndependentConfiguration(t *testing.T) {
	geminiReader := &petGeminiReaderStub{
		providers: []GeminiProvider{
			{
				ID:                  "gemini-main",
				Name:                "Google Official",
				PartnerPromotionKey: "google-official",
				Model:               "gemini-2.5-pro",
				APIKey:              "gemini-secret",
			},
		},
	}
	resolver := NewPetProviderResolver(nil, geminiReader)

	resolution, err := resolver.Resolve(petTestReference("gemini", "google-official", "gemini-2.5-pro", PetCapabilityChat))
	if err != nil {
		t.Fatalf("Gemini 独立配置解析失败: %v", err)
	}
	if resolution.ProviderID != "gemini-main" || resolution.Platform != "gemini" {
		t.Fatalf("Gemini 引用未保留独立字符串 ID: %+v", resolution)
	}
	if resolution.EffectiveModel != "gemini-2.5-pro" {
		t.Fatalf("Gemini 有效模型错误: %+v", resolution)
	}

	noModelResolver := NewPetProviderResolver(nil, &petGeminiReaderStub{
		providers: []GeminiProvider{{ID: "gemini-empty", Name: "Empty"}},
	})
	err = noModelResolver.ValidateReference(petTestReference("gemini", "gemini-empty", "gemini-2.5-pro", PetCapabilityChat))
	assertPetProviderErrorCode(t, err, PET_MODEL_NOT_CONFIGURED)
}

func TestPetProviderResolverUpstreamErrorKeepsCauseButNotCauseTextInPublicError(t *testing.T) {
	underlying := errors.New("provider file read failed")
	resolver := NewPetProviderResolver(&petProviderReaderStub{err: underlying}, nil)

	err := resolver.ValidateReference(petTestReference("claude", "7", "model-a", PetCapabilityChat))
	assertPetProviderErrorCode(t, err, PET_UPSTREAM_ERROR)
	if !errors.Is(err, underlying) {
		t.Fatalf("底层读取错误未通过 Unwrap 保留: %v", err)
	}
	if strings.Contains(err.Error(), underlying.Error()) {
		t.Fatalf("公开错误文本不应拼接底层错误细节: %v", err)
	}
}
