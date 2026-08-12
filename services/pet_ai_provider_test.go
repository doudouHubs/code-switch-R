package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type petAIProviderAdapterProviderStub struct {
	providers []Provider
	err       error
	calls     int
	onLoad    func(int)
}

func (s *petAIProviderAdapterProviderStub) LoadProviders(string) ([]Provider, error) {
	s.calls++
	if s.onLoad != nil {
		s.onLoad(s.calls)
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.providers, nil
}

type petAIProviderAdapterGeminiStub struct {
	providers []GeminiProvider
}

func (s *petAIProviderAdapterGeminiStub) GetProviders() []GeminiProvider {
	return s.providers
}

func TestPetAIProviderReaderNormalProviderProjectsMappingAndAuth(t *testing.T) {
	providerReader := &petAIProviderAdapterProviderStub{providers: []Provider{
		{
			ID:                   42,
			Name:                 "primary",
			APIURL:               "https://provider.test/root",
			APIKey:               "provider-secret",
			APIEndpoint:          "/v1/chat/completions",
			ConnectivityAuthType: "X-Pet-Token",
			UpstreamProtocol:     "openai_chat",
			SupportedModels:      map[string]bool{"upstream-model": true},
			ModelMapping:         map[string]string{"pet-model": "upstream-model"},
		},
	}}
	reader := NewPetAIProviderReader(providerReader, nil)

	config, err := reader.Read(context.Background(), PetProviderReference{
		Platform:   "codex",
		ProviderID: "primary",
		Model:      "pet-model",
		Capability: PetCapabilityChat,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if providerReader.calls != 2 {
		t.Fatalf("LoadProviders calls = %d, want resolver validation plus entity read", providerReader.calls)
	}
	if config.Platform != "codex" || config.ProviderID != "42" {
		t.Fatalf("identity projection = %+v", config)
	}
	if config.Model != "upstream-model" || config.EffectiveModel != "upstream-model" {
		t.Fatalf("model mapping projection = %+v", config)
	}
	if config.APIURL != "https://provider.test/root" || config.APIKey != "provider-secret" {
		t.Fatalf("endpoint/credential projection = %+v", config)
	}
	if config.APIEndpoint != "/v1/chat/completions" || config.Protocol != "openai" || config.UpstreamProtocol != "openai_chat" {
		t.Fatalf("protocol projection = %+v", config)
	}
	if config.AuthType != "custom" || config.AuthHeader != "X-Pet-Token" {
		t.Fatalf("auth projection = %+v", config)
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "provider-secret") || strings.Contains(string(encoded), "apiKey") {
		t.Fatalf("serialized config leaks credentials: %s", encoded)
	}
}

func TestPetAIProviderReaderDefaultProtocolFollowsPlatform(t *testing.T) {
	providerReader := &petAIProviderAdapterProviderStub{providers: []Provider{
		{
			ID:              7,
			Name:            "primary",
			APIURL:          "https://provider.test",
			APIKey:          "provider-secret",
			SupportedModels: map[string]bool{"model-a": true},
		},
	}}
	reader := NewPetAIProviderReader(providerReader, nil)

	cases := []struct {
		name     string
		platform string
		protocol string
	}{
		{name: "codex", platform: "codex", protocol: "openai"},
		{name: "claude", platform: "claude-code", protocol: "anthropic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := reader.Read(context.Background(), PetProviderReference{
				Platform:   tc.platform,
				ProviderID: "7",
				Model:      "model-a",
				Capability: PetCapabilityChat,
			})
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if config.Protocol != tc.protocol || config.UpstreamProtocol != tc.protocol {
				t.Fatalf("default protocol = %q/%q, want %q", config.Protocol, config.UpstreamProtocol, tc.protocol)
			}
		})
	}
}

func TestPetAIProviderReaderGeminiUsesEnvConfigWithoutLeakingIt(t *testing.T) {
	geminiReader := &petAIProviderAdapterGeminiStub{providers: []GeminiProvider{
		{
			ID:                  "gemini-main",
			Name:                "Gemini Env",
			PartnerPromotionKey: "gemini-alias",
			EnvConfig: map[string]string{
				"GOOGLE_GEMINI_BASE_URL": "https://gemini.test/v1beta",
				"GEMINI_API_KEY":         "gemini-secret",
				"GEMINI_MODEL":           "gemini-2.5-pro",
			},
		},
	}}
	reader := NewPetAIProviderReader(nil, geminiReader)

	config, err := reader.Read(context.Background(), PetProviderReference{
		Platform:   "gemini",
		ProviderID: "gemini-alias",
		Model:      "gemini-2.5-pro",
		Capability: PetCapabilityChat,
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if config.ProviderID != "gemini-main" || config.Platform != "gemini" {
		t.Fatalf("Gemini identity projection = %+v", config)
	}
	if config.BaseURL != "https://gemini.test/v1beta" || config.APIKey != "gemini-secret" {
		t.Fatalf("Gemini EnvConfig projection = %+v", config)
	}
	if config.Model != "gemini-2.5-pro" || config.EffectiveModel != "gemini-2.5-pro" {
		t.Fatalf("Gemini model projection = %+v", config)
	}
	if config.Protocol != "gemini" || config.UpstreamProtocol != "gemini" || config.AuthType != "" {
		t.Fatalf("Gemini protocol/auth projection = %+v", config)
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "gemini-secret") || strings.Contains(string(encoded), "GEMINI_API_KEY") {
		t.Fatalf("serialized Gemini config leaks credentials: %s", encoded)
	}
}

func TestPetAIProviderReaderNotFoundDoesNotFallback(t *testing.T) {
	providerReader := &petAIProviderAdapterProviderStub{providers: []Provider{
		{ID: 7, Name: "available", SupportedModels: map[string]bool{"model-a": true}},
	}}
	reader := NewPetAIProviderReader(providerReader, nil)

	_, err := reader.Read(context.Background(), PetProviderReference{
		Platform:   "codex",
		ProviderID: "missing",
		Model:      "model-a",
		Capability: PetCapabilityChat,
	})
	if !IsPetProviderErrorCode(err, PET_PROVIDER_NOT_FOUND) {
		t.Fatalf("error code = %s, want %s; error=%v", PetProviderErrorCodeOf(err), PET_PROVIDER_NOT_FOUND, err)
	}
	if providerReader.calls != 1 {
		t.Fatalf("LoadProviders calls = %d, want no fallback read", providerReader.calls)
	}
}

func TestPetAIProviderReaderModelUnsupported(t *testing.T) {
	providerReader := &petAIProviderAdapterProviderStub{providers: []Provider{
		{ID: 7, Name: "primary", SupportedModels: map[string]bool{"model-a": true}},
	}}
	reader := NewPetAIProviderReader(providerReader, nil)

	_, err := reader.Read(context.Background(), PetProviderReference{
		Platform:   "codex",
		ProviderID: "7",
		Model:      "model-b",
		Capability: PetCapabilityChat,
	})
	if !IsPetProviderErrorCode(err, PET_MODEL_UNSUPPORTED) {
		t.Fatalf("error code = %s, want %s; error=%v", PetProviderErrorCodeOf(err), PET_MODEL_UNSUPPORTED, err)
	}
}

func TestPetAIProviderReaderCapabilityUnsupported(t *testing.T) {
	providerReader := &petAIProviderAdapterProviderStub{providers: []Provider{
		{ID: 7, Name: "primary", SupportedModels: map[string]bool{"text-model": true}},
	}}
	reader := NewPetAIProviderReader(providerReader, nil)

	_, err := reader.Read(context.Background(), PetProviderReference{
		Platform:   "codex",
		ProviderID: "7",
		Model:      "text-model",
		Capability: PetCapabilityTTS,
	})
	if !IsPetProviderErrorCode(err, PET_CAPABILITY_UNSUPPORTED) {
		t.Fatalf("error code = %s, want %s; error=%v", PetProviderErrorCodeOf(err), PET_CAPABILITY_UNSUPPORTED, err)
	}
}

func TestPetAIProviderReaderUsesExplicitSpeechCategoryForTranscription(t *testing.T) {
	providerReader := &petAIProviderAdapterProviderStub{providers: []Provider{
		{
			ID:              7,
			Name:            "speech-relay",
			APIURL:          "https://provider.test",
			SupportedModels: map[string]bool{"stt-custom": true},
			ModelCategories: map[string]string{"stt-custom": "speech"},
		},
	}}
	reader := NewPetAIProviderReader(providerReader, nil)

	config, err := reader.Read(context.Background(), PetProviderReference{
		Platform:   "codex",
		ProviderID: "7",
		Model:      "stt-custom",
		Capability: PetCapabilityTranscription,
	})
	if err != nil {
		t.Fatalf("显式 speech 类别不应被模型名启发式拒绝: %v", err)
	}
	if config.ModelCategory != "speech" || config.Resolution.ModelCategory != "speech" {
		t.Fatalf("speech category 未投影到运行时配置: %+v", config)
	}
}

func TestPetAIProviderReaderExplicitChatCategoryRejectsTranscription(t *testing.T) {
	providerReader := &petAIProviderAdapterProviderStub{providers: []Provider{
		{
			ID:              7,
			Name:            "chat-relay",
			SupportedModels: map[string]bool{"whisper-like-chat": true},
			ModelCategories: map[string]string{"whisper-like-chat": "chat"},
		},
	}}
	reader := NewPetAIProviderReader(providerReader, nil)

	_, err := reader.Read(context.Background(), PetProviderReference{
		Platform:   "codex",
		ProviderID: "7",
		Model:      "whisper-like-chat",
		Capability: PetCapabilityTranscription,
	})
	if !IsPetProviderErrorCode(err, PET_CAPABILITY_UNSUPPORTED) {
		t.Fatalf("显式 chat 类别错误码 = %s，期望 %s；error=%v", PetProviderErrorCodeOf(err), PET_CAPABILITY_UNSUPPORTED, err)
	}
}

func TestPetAIProviderReaderChecksCancellationAfterResolverRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	providerReader := &petAIProviderAdapterProviderStub{
		providers: []Provider{{ID: 7, Name: "primary", SupportedModels: map[string]bool{"model-a": true}}},
		onLoad: func(calls int) {
			if calls == 1 {
				cancel()
			}
		},
	}
	reader := NewPetAIProviderReader(providerReader, nil)

	_, err := reader.Read(ctx, PetProviderReference{
		Platform:   "codex",
		ProviderID: "7",
		Model:      "model-a",
		Capability: PetCapabilityChat,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if providerReader.calls != 1 {
		t.Fatalf("LoadProviders calls = %d, want no entity read after cancellation", providerReader.calls)
	}
}

func TestPetAIProviderReaderProviderErrorDoesNotExposeAPIKey(t *testing.T) {
	providerReader := &petAIProviderAdapterProviderStub{err: errors.New("read failed apiKey=provider-secret")}
	reader := NewPetAIProviderReader(providerReader, nil)

	_, err := reader.Read(context.Background(), PetProviderReference{
		Platform:   "codex",
		ProviderID: "7",
		Model:      "model-a",
		Capability: PetCapabilityChat,
	})
	if !IsPetProviderErrorCode(err, PET_UPSTREAM_ERROR) {
		t.Fatalf("error code = %s, want %s; error=%v", PetProviderErrorCodeOf(err), PET_UPSTREAM_ERROR, err)
	}
	if strings.Contains(err.Error(), "provider-secret") {
		t.Fatalf("error leaks API key: %v", err)
	}
}
