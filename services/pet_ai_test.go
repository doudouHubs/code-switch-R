package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	modelpricing "codeswitch/resources/model-pricing"
)

type petAITestProviderReader struct {
	config PetAIProviderConfig
	err    error
}

type PetAIProviderReaderFunc func(context.Context, PetProviderReference) (PetAIProviderConfig, error)

func (f PetAIProviderReaderFunc) Read(ctx context.Context, reference PetProviderReference) (PetAIProviderConfig, error) {
	return f(ctx, reference)
}

func (r *petAITestProviderReader) Read(ctx context.Context, _ PetProviderReference) (PetAIProviderConfig, error) {
	if ctx != nil && ctx.Err() != nil {
		return PetAIProviderConfig{}, ctx.Err()
	}
	if r.err != nil {
		return PetAIProviderConfig{}, r.err
	}
	return r.config, nil
}

type petAITestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f petAITestRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type petAITestEmitter struct {
	mu     sync.Mutex
	events []PetAIEvent
	err    error
}

func (e *petAITestEmitter) Emit(event PetAIEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return e.err
}

func (e *petAITestEmitter) snapshot() []PetAIEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]PetAIEvent, len(e.events))
	copy(result, e.events)
	return result
}

func (e *petAITestEmitter) waitFor(t *testing.T, eventType PetAIEventType) PetAIEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, event := range e.snapshot() {
			if event.Type == eventType {
				return event
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("未收到 PetAI 事件 %q，已有事件=%#v", eventType, e.snapshot())
	return PetAIEvent{}
}

func petAITestResponse(status int, contentType, body string) *http.Response {
	response := &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
	if contentType != "" {
		response.Header.Set("Content-Type", contentType)
	}
	return response
}

func petAITestReference(platform, providerID, model string, capability PetCapability) PetProviderReference {
	return PetProviderReference{
		Platform:   platform,
		ProviderID: providerID,
		Model:      model,
		Capability: capability,
	}
}

func petAITestConfig(platform, protocol, model string) PetAIProviderConfig {
	return PetAIProviderConfig{
		Platform:   platform,
		ProviderID: "pet-provider",
		Model:      model,
		BaseURL:    "https://provider.test/v1",
		APIKey:     "pet-secret-key",
		Protocol:   protocol,
	}
}

func petAITestErrorCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，但返回 nil")
	}
	code := PetAIErrorCodeOf(err)
	if code == "" {
		t.Fatalf("错误缺少结构化 code: %T %v", err, err)
	}
	return code
}

func TestPetAIStartChatOpenAICompatibleSSEEmitsSafeLifecycle(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	emitter := &petAITestEmitter{}
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("OpenAI chat path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer pet-secret-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var payload struct {
			Model           string              `json:"model"`
			Messages        []map[string]string `json:"messages"`
			Stream          bool                `json:"stream"`
			ReasoningEffort string              `json:"reasoning_effort"`
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("读取 OpenAI body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析 OpenAI body: %v", err)
		}
		if payload.Model != "gpt-pet" || !payload.Stream || payload.ReasoningEffort != "low" {
			t.Fatalf("OpenAI body = %#v", payload)
		}
		if len(payload.Messages) != 4 || payload.Messages[0]["role"] != "system" || payload.Messages[1]["content"] != "旧消息" || payload.Messages[3]["content"] != "今天好吗" {
			t.Fatalf("OpenAI messages = %#v", payload.Messages)
		}
		return petAITestResponse(http.StatusOK, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{\"content\":\"，今天不错。\"}}]}\n\n"+
			"data: [DONE]\n\n"), nil
	})
	service := NewPetAIService(reader, transport, emitter)
	result, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:     "pet-1",
		RequestID: "chat-openai-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		Persona:   "你是一个安静的桌宠",
		UserText:  "今天好吗",
		History:   []PetAIMessage{{Role: "user", Content: "旧消息"}, {Role: "assistant", Content: "我在"}},
		Reasoning: "low",
	})
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	if result.RequestID != "chat-openai-1" {
		t.Fatalf("StartChat() result = %#v", result)
	}
	completed := emitter.waitFor(t, PetAIEventCompleted)
	if completed.Text != "你好，今天不错。" {
		t.Fatalf("completed text = %q", completed.Text)
	}
	events := emitter.snapshot()
	if len(events) < 4 || events[0].Type != PetAIEventStarted || events[len(events)-1].Type != PetAIEventCompleted {
		t.Fatalf("lifecycle events = %#v", events)
	}
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("event sequence at %d = %d, want %d", index, event.Sequence, index+1)
		}
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("序列化事件: %v", err)
	}
	if strings.Contains(string(encoded), "pet-secret-key") {
		t.Fatalf("事件泄露 API Key: %s", encoded)
	}
}

func TestPetAIStartChatResponsesSSEUsesResponsesContract(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("codex", "responses", "gpt-5.6-luna")}
	emitter := &petAITestEmitter{}
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/responses" {
			t.Fatalf("Responses path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer pet-secret-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var payload struct {
			Model  string           `json:"model"`
			Input  []map[string]any `json:"input"`
			Stream bool             `json:"stream"`
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("读取 Responses body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析 Responses body: %v", err)
		}
		if payload.Model != "gpt-5.6-luna" || !payload.Stream || len(payload.Input) != 4 {
			t.Fatalf("Responses body = %#v", payload)
		}
		if payload.Input[0]["role"] != "system" || payload.Input[1]["role"] != "user" || payload.Input[3]["role"] != "user" {
			t.Fatalf("Responses input roles = %#v", payload.Input)
		}
		return petAITestResponse(http.StatusOK, "text/event-stream", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"你好\"}\n\n"+
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"，Codex。\"}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":18,\"output_tokens\":6,\"input_tokens_details\":{\"cached_tokens\":2},\"output_tokens_details\":{\"reasoning_tokens\":1}}}}\n\n"), nil
	})
	service := NewPetAIService(reader, transport, emitter)
	_, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:     "pet-codex",
		RequestID: "chat-responses-1",
		Provider:  petAITestReference("codex", "pet-provider", "gpt-5.6-luna", PetCapabilityChat),
		Persona:   "你是一个安静的桌宠",
		UserText:  "今天好吗",
		History:   []PetAIMessage{{Role: "user", Content: "旧消息"}, {Role: "assistant", Content: "我在"}},
	})
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := emitter.waitFor(t, PetAIEventCompleted)
	if completed.Text != "你好，Codex。" {
		t.Fatalf("completed text = %q", completed.Text)
	}
	usage := emitter.waitFor(t, PetAIEventUsage)
	if usage.Usage == nil || usage.Usage.InputTokens != 18 || usage.Usage.OutputTokens != 6 || usage.Usage.CacheReadTokens != 2 || usage.Usage.ReasoningTokens != 1 {
		t.Fatalf("Responses usage = %#v", usage.Usage)
	}
}

func TestPetAIResponsesToolContinuationUsesFunctionCallOutput(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("宠物项目\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := &petAITestProviderReader{config: petAITestConfig("codex", "responses", "gpt-5.6-luna")}
	emitter := &petAITestEmitter{}
	callCount := 0
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		callCount++
		if request.URL.Path != "/v1/responses" {
			t.Fatalf("Responses tool path = %q", request.URL.Path)
		}
		var payload struct {
			Input  []map[string]any `json:"input"`
			Tools  []map[string]any `json:"tools"`
			Stream bool             `json:"stream"`
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("读取 Responses tool body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析 Responses tool body: %v", err)
		}
		if payload.Stream || len(payload.Tools) != 4 {
			t.Fatalf("Responses tool options = %#v", payload)
		}
		if callCount == 1 {
			return petAITestResponse(http.StatusOK, "application/json", `{"output":[{"type":"function_call","call_id":"call-read-1","name":"Read","arguments":"{\"file_path\":\"README.md\"}"}]}`), nil
		}
		if callCount != 2 || len(payload.Input) < 3 || payload.Input[len(payload.Input)-2]["type"] != "function_call" || payload.Input[len(payload.Input)-1]["type"] != "function_call_output" || payload.Input[len(payload.Input)-1]["call_id"] != "call-read-1" {
			t.Fatalf("Responses continuation input = %#v", payload.Input)
		}
		return petAITestResponse(http.StatusOK, "application/json", `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"文件已读"}]}]}`), nil
	})
	service := NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader: reader,
		Transport:      transport,
		Emitter:        emitter,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return workspace, nil
		}),
	})
	_, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:     "pet-codex-tools",
		RequestID: "chat-responses-tools-1",
		Provider:  petAITestReference("codex", "pet-provider", "gpt-5.6-luna", PetCapabilityChat),
		UserText:  "读取 README",
	})
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := emitter.waitFor(t, PetAIEventCompleted)
	if completed.Text != "文件已读" || callCount != 2 {
		t.Fatalf("Responses tool completion = %#v, calls=%d", completed, callCount)
	}
}

func TestPetAIUsageParsingAcrossProviderProtocols(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		data     string
		want     modelpricing.UsageSnapshot
	}{
		{
			name:     "openai",
			protocol: "openai",
			data:     `{"usage":{"prompt_tokens":12,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}},"service_tier":"priority"}`,
			want: modelpricing.UsageSnapshot{
				InputTokens: 12, OutputTokens: 7, ReasoningTokens: 2, CacheReadTokens: 3,
				ServiceTier: modelpricing.ServiceTierPriority,
			},
		},
		{
			name:     "anthropic",
			protocol: "anthropic",
			data:     `{"type":"message_start","message":{"usage":{"input_tokens":21,"output_tokens":0,"cache_creation_input_tokens":5,"cache_read_input_tokens":8,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":1},"service_tier":"standard"}}}`,
			want: modelpricing.UsageSnapshot{
				InputTokens: 21, CacheCreateTokens: 5, CacheReadTokens: 8,
				CacheCreation: &modelpricing.CacheCreationDetail{Ephemeral5mTokens: 4, Ephemeral1hTokens: 1},
				ServiceTier:   modelpricing.ServiceTierStandard,
			},
		},
		{
			name:     "gemini",
			protocol: "gemini",
			data:     `{"usageMetadata":{"promptTokenCount":31,"candidatesTokenCount":9,"thoughtsTokenCount":2,"cachedContentTokenCount":6}}`,
			want:     modelpricing.UsageSnapshot{InputTokens: 31, OutputTokens: 9, ReasoningTokens: 2, CacheReadTokens: 6},
		},
		{
			name:     "responses nested",
			protocol: "responses",
			data:     `{"type":"response.completed","response":{"usage":{"input_tokens":41,"output_tokens":13,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":3}}}}`,
			want:     modelpricing.UsageSnapshot{InputTokens: 41, OutputTokens: 13, ReasoningTokens: 3, CacheReadTokens: 7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parsePetAIUsage(tt.data, tt.protocol)
			if !ok {
				t.Fatal("parsePetAIUsage() 未识别有效 usage")
			}
			if got.InputTokens != tt.want.InputTokens || got.OutputTokens != tt.want.OutputTokens ||
				got.ReasoningTokens != tt.want.ReasoningTokens || got.CacheCreateTokens != tt.want.CacheCreateTokens ||
				got.CacheReadTokens != tt.want.CacheReadTokens || got.ServiceTier != tt.want.ServiceTier {
				t.Fatalf("usage = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(got.CacheCreation, tt.want.CacheCreation) {
				t.Fatalf("cache creation = %#v, want %#v", got.CacheCreation, tt.want.CacheCreation)
			}
		})
	}

	if _, ok := parsePetAIUsage(`{"usage":{}}`, "openai"); ok {
		t.Fatal("空 usage 不应触发入账")
	}
	if _, ok := parsePetAIUsage(`{"choices":[{"delta":{"content":"无 usage"}}]}`, "openai"); ok {
		t.Fatal("缺失 usage 不应触发入账")
	}
}

func TestPetAIUsageMergeTakesMaximumAndDoesNotAccumulateDuplicates(t *testing.T) {
	first := modelpricing.UsageSnapshot{
		InputTokens: 12, OutputTokens: 3, CacheReadTokens: 4,
		CacheCreation: &modelpricing.CacheCreationDetail{Ephemeral5mTokens: 2},
	}
	second := modelpricing.UsageSnapshot{
		InputTokens: 12, OutputTokens: 9, CacheReadTokens: 4,
		CacheCreation: &modelpricing.CacheCreationDetail{Ephemeral5mTokens: 7, Ephemeral1hTokens: 1},
		ServiceTier:   modelpricing.ServiceTierPriority,
	}
	got := mergePetAIUsage(first, second)
	if got.InputTokens != 12 || got.OutputTokens != 9 || got.CacheReadTokens != 4 ||
		got.CacheCreation == nil || got.CacheCreation.Ephemeral5mTokens != 7 || got.CacheCreation.Ephemeral1hTokens != 1 ||
		got.ServiceTier != modelpricing.ServiceTierPriority {
		t.Fatalf("merged usage = %#v, want field-wise maximum", got)
	}
	if got.InputTokens == first.InputTokens+second.InputTokens {
		t.Fatal("usage 不应按重复 SSE 快照累加")
	}
}

func TestPetAIUsagePayloadUsesStableRequestIDAndEffectiveModel(t *testing.T) {
	payload := petAIUsagePayload("request-stable-1", petAIProviderRuntime{
		platform:   "openai",
		providerID: "provider-a",
		model:      "effective-model",
		reference:  PetProviderReference{Platform: "fallback-platform", ProviderID: "fallback-provider", Model: "requested-model"},
	}, modelpricing.UsageSnapshot{InputTokens: 5, OutputTokens: 2})
	if payload.ID != "request-stable-1" || payload.Provider != "openai/provider-a" || payload.Model != "effective-model" {
		t.Fatalf("usage payload identity = %#v", payload)
	}
	if payload.InputTokens != 5 || payload.OutputTokens != 2 || payload.At <= 0 {
		t.Fatalf("usage payload facts = %#v", payload)
	}
}

func TestPetAIStartChatEmitsUsageWithStableRequestID(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "effective-pet-model")}
	emitter := &petAITestEmitter{}
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &body); err != nil {
			return nil, err
		}
		if !body.StreamOptions.IncludeUsage {
			return nil, errors.New("stream_options.include_usage 未开启")
		}
		return petAITestResponse(http.StatusOK, "text/event-stream",
			"data: {\"choices\":[{\"delta\":{\"content\":\"完成\"}}]}\n\n"+
				"data: {\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":6}}\n\n"+
				"data: [DONE]\n\n"), nil
	})
	service := NewPetAIService(reader, transport, emitter)
	if _, err := service.StartChat(context.Background(), PetChatRequest{
		PetID: "pet-usage", RequestID: "request-stable-1",
		Provider: petAITestReference("openai", "provider-a", "requested-model", PetCapabilityChat),
		UserText: "usage",
	}); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	usageEvent := emitter.waitFor(t, PetAIEventUsage)
	if usageEvent.RequestID != "request-stable-1" || usageEvent.Usage == nil {
		t.Fatalf("usage event = %#v", usageEvent)
	}
	if usageEvent.Usage.ID != "request-stable-1" || usageEvent.Usage.InputTokens != 11 || usageEvent.Usage.OutputTokens != 6 || usageEvent.Usage.Model != "effective-pet-model" {
		t.Fatalf("usage payload = %#v", usageEvent.Usage)
	}
	if completed := emitter.waitFor(t, PetAIEventCompleted); completed.Text != "完成" {
		t.Fatalf("completed event = %#v", completed)
	}
}

func TestPetAIGenerateDreamTextUsesAnthropicMessagesSSE(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("claude", "anthropic", "claude-pet")}
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/messages" {
			t.Fatalf("Anthropic path = %q", request.URL.Path)
		}
		if request.Header.Get("x-api-key") != "pet-secret-key" || request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("Anthropic auth headers = %#v", request.Header)
		}
		var payload struct {
			Model    string              `json:"model"`
			System   string              `json:"system"`
			Messages []map[string]string `json:"messages"`
			Stream   bool                `json:"stream"`
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("读取 Anthropic body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析 Anthropic body: %v", err)
		}
		if payload.Model != "claude-pet" || payload.System != "梦里的桌宠" || !payload.Stream || len(payload.Messages) != 1 {
			t.Fatalf("Anthropic body = %#v", payload)
		}
		return petAITestResponse(http.StatusOK, "text/event-stream", "event: content_block_delta\n"+
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"月光落在窗台。\"}}\n\n"+
			"event: message_stop\n"+
			"data: {\"type\":\"message_stop\"}\n\n"), nil
	})
	service := NewPetAIService(reader, transport, nil)
	dream, err := service.GenerateDreamText(context.Background(), PetDreamTextRequest{
		PetID:     "pet-1",
		RequestID: "dream-1",
		Provider:  petAITestReference("claude", "pet-provider", "claude-pet", PetCapabilityChat),
		Persona:   "梦里的桌宠",
		UserText:  "请生成一段梦境",
	})
	if err != nil {
		t.Fatalf("GenerateDreamText() error = %v", err)
	}
	if dream != "月光落在窗台。" {
		t.Fatalf("dream = %q", dream)
	}
}

func TestPetAIGenerateDreamTextUsesGeminiGenerateContent(t *testing.T) {
	config := petAITestConfig("gemini", "gemini", "gemini-2.5-flash")
	config.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	reader := &petAITestProviderReader{config: config}
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1beta/models/gemini-2.5-flash:generateContent" {
			t.Fatalf("Gemini path = %q", request.URL.Path)
		}
		if request.Header.Get("x-goog-api-key") != "pet-secret-key" {
			t.Fatalf("Gemini auth header = %q", request.Header.Get("x-goog-api-key"))
		}
		var payload map[string]any
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("读取 Gemini body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析 Gemini body: %v", err)
		}
		if _, ok := payload["contents"]; !ok {
			t.Fatalf("Gemini body 缺少 contents: %s", body)
		}
		if _, ok := payload["systemInstruction"]; !ok {
			t.Fatalf("Gemini body 缺少 systemInstruction: %s", body)
		}
		return petAITestResponse(http.StatusOK, "application/json", `{"candidates":[{"content":{"parts":[{"text":"星星在打盹。"}]}}]}`), nil
	})
	service := NewPetAIService(reader, transport, nil)
	dream, err := service.GenerateDreamText(context.Background(), PetDreamTextRequest{
		PetID:     "pet-1",
		RequestID: "dream-gemini-1",
		Provider:  petAITestReference("gemini", "pet-provider", "gemini-2.5-flash", PetCapabilityChat),
		Persona:   "一只会做梦的宠物",
		UserText:  "请生成梦境",
		Reasoning: "minimal",
	})
	if err != nil {
		t.Fatalf("Gemini GenerateDreamText() error = %v", err)
	}
	if dream != "星星在打盹。" {
		t.Fatalf("Gemini dream = %q", dream)
	}
}

func TestPetAIImageBodiesUseProviderSpecificShapes(t *testing.T) {
	image := PetAIImage{Data: base64.StdEncoding.EncodeToString([]byte("image-bytes")), MediaType: "image/png"}
	input := petAIChatInput{
		Persona:  "一只会看图的桌宠",
		UserText: "这张图里有什么？",
		Images:   []PetAIImage{image},
		History: []PetAIMessage{{
			Role:    "user",
			Content: "上一张图",
			Images:  []PetAIImage{image},
		}},
	}
	provider := petAIProviderRuntime{model: "vision-pet"}

	openAIBody, err := buildOpenAIChatBody(provider, input)
	if err != nil {
		t.Fatalf("buildOpenAIChatBody() error = %v", err)
	}
	var openAI map[string]any
	if err := json.Unmarshal(openAIBody, &openAI); err != nil {
		t.Fatalf("解析 OpenAI body: %v", err)
	}
	openAIMessages, ok := openAI["messages"].([]any)
	if !ok || len(openAIMessages) != 3 {
		t.Fatalf("OpenAI messages = %#v", openAI["messages"])
	}
	openAIUser := openAIMessages[2].(map[string]any)
	openAIContent, ok := openAIUser["content"].([]any)
	if !ok || len(openAIContent) != 2 {
		t.Fatalf("OpenAI image content = %#v", openAIUser["content"])
	}
	openAIImage := openAIContent[1].(map[string]any)
	openAIImageURL := openAIImage["image_url"].(map[string]any)["url"].(string)
	if openAIImage["type"] != "image_url" || openAIImageURL != "data:image/png;base64,"+image.Data {
		t.Fatalf("OpenAI image part = %#v", openAIImage)
	}

	anthropicBody, err := buildAnthropicMessagesBody(provider, input)
	if err != nil {
		t.Fatalf("buildAnthropicMessagesBody() error = %v", err)
	}
	var anthropic map[string]any
	if err := json.Unmarshal(anthropicBody, &anthropic); err != nil {
		t.Fatalf("解析 Anthropic body: %v", err)
	}
	anthropicMessages := anthropic["messages"].([]any)
	anthropicUser := anthropicMessages[1].(map[string]any)
	anthropicContent := anthropicUser["content"].([]any)
	anthropicImage := anthropicContent[1].(map[string]any)
	anthropicSource := anthropicImage["source"].(map[string]any)
	if anthropicImage["type"] != "image" || anthropicSource["type"] != "base64" || anthropicSource["media_type"] != "image/png" || anthropicSource["data"] != image.Data {
		t.Fatalf("Anthropic image part = %#v", anthropicImage)
	}

	geminiBody, err := buildGeminiGenerateContentBody(provider, input)
	if err != nil {
		t.Fatalf("buildGeminiGenerateContentBody() error = %v", err)
	}
	var gemini map[string]any
	if err := json.Unmarshal(geminiBody, &gemini); err != nil {
		t.Fatalf("解析 Gemini body: %v", err)
	}
	geminiContents := gemini["contents"].([]any)
	geminiUser := geminiContents[1].(map[string]any)
	geminiParts := geminiUser["parts"].([]any)
	geminiImage := geminiParts[1].(map[string]any)["inline_data"].(map[string]any)
	if geminiImage["mime_type"] != "image/png" || geminiImage["data"] != image.Data {
		t.Fatalf("Gemini image part = %#v", geminiImage)
	}
}

func TestPetAINormalizeImagesRejectsUnsafeInputAndAllowsImageOnly(t *testing.T) {
	validReference := petAITestReference("openai", "pet-provider", "vision-pet", PetCapabilityChat)
	validImage := PetAIImage{
		Data:      base64.StdEncoding.EncodeToString([]byte("image-bytes")),
		MediaType: "image/jpeg",
	}
	cases := []struct {
		name string
		want PetAIErrorCode
		edit func(*PetChatRequest)
	}{
		{
			name: "invalid media type",
			want: PET_AI_MEDIA_TYPE_INVALID,
			edit: func(request *PetChatRequest) { request.Images[0].MediaType = "text/plain" },
		},
		{
			name: "invalid base64",
			want: PET_AI_INVALID_REQUEST,
			edit: func(request *PetChatRequest) { request.Images[0].Data = "not-base64" },
		},
		{
			name: "data url is not accepted",
			want: PET_AI_INVALID_REQUEST,
			edit: func(request *PetChatRequest) { request.Images[0].Data = "data:image/jpeg;base64," + validImage.Data },
		},
		{
			name: "image is too large",
			want: PET_AI_REQUEST_TOO_LARGE,
			edit: func(request *PetChatRequest) {
				request.Images[0].Data = base64.StdEncoding.EncodeToString(make([]byte, PetAIMaxImageBytes+1))
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := PetChatRequest{
				PetID:     "pet-1",
				RequestID: "image-validation-1",
				Provider:  validReference,
				Images:    []PetAIImage{validImage},
				UserText:  "请看看",
			}
			testCase.edit(&request)
			service := &PetAIService{}
			if _, err := service.normalizeChatRequest(request, PetCapabilityChat); petAITestErrorCode(t, err) != string(testCase.want) {
				t.Fatalf("normalize image error = %v", err)
			}
		})
	}

	imageOnly := PetChatRequest{
		PetID:     "pet-1",
		RequestID: "image-only-1",
		Provider:  validReference,
		Images:    []PetAIImage{validImage},
	}
	service := &PetAIService{}
	input, err := service.normalizeChatRequest(imageOnly, PetCapabilityChat)
	if err != nil || len(input.Images) != 1 {
		t.Fatalf("image-only request = %#v, error=%v", input, err)
	}
}

func TestPetAISynthesizeSpeechUsesOpenAISpeechEndpoint(t *testing.T) {
	config := petAITestConfig("openai", "openai", "tts-1")
	config.AudioMode = "speech"
	reader := &petAITestProviderReader{config: config}
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/audio/speech" {
			t.Fatalf("speech path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer pet-secret-key" {
			t.Fatalf("speech auth header = %q", request.Header.Get("Authorization"))
		}
		var payload map[string]string
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("读取 speech body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析 speech body: %v", err)
		}
		if payload["model"] != "tts-1" || payload["input"] != "你好，桌宠" || payload["voice"] != "alloy" || payload["instructions"] != "温柔地朗读" {
			t.Fatalf("speech body = %#v", payload)
		}
		return petAITestResponse(http.StatusOK, "audio/mpeg", "mp3-bytes"), nil
	})
	service := NewPetAIService(reader, transport, nil)
	audio, mediaType, err := service.SynthesizeSpeech(context.Background(), PetSpeechRequest{
		PetID:       "pet-1",
		RequestID:   "tts-1",
		Provider:    petAITestReference("openai", "pet-provider", "tts-1", PetCapabilityTTS),
		Text:        "你好，桌宠",
		Voice:       "alloy",
		Instruction: "温柔地朗读",
	})
	if err != nil {
		t.Fatalf("SynthesizeSpeech() error = %v", err)
	}
	if string(audio) != "mp3-bytes" || mediaType != "audio/mpeg" {
		t.Fatalf("speech result = %q, %q", audio, mediaType)
	}
}

func TestPetAISpeechExplicitSpeechRejectsChatAudioProvider(t *testing.T) {
	config := petAITestConfig("openai", "openai", "gpt-4o-audio-preview")
	config.AudioMode = "chat"
	reader := &petAITestProviderReader{config: config}
	called := false
	transport := petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return petAITestResponse(http.StatusOK, "audio/raw", "not-pcm-contract"), nil
	})
	service := NewPetAIService(reader, transport, nil)
	_, _, err := service.SynthesizeSpeech(context.Background(), PetSpeechRequest{
		PetID:     "pet-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-4o-audio-preview", PetCapabilityTTS),
		Text:      "不要把 chat 音频猜成 speech",
		VoiceMode: PetVoiceSpeech,
	})
	if got := petAITestErrorCode(t, err); got != string(PET_CAPABILITY_UNSUPPORTED) {
		t.Fatalf("chat audio error code = %q", got)
	}
	if called {
		t.Fatal("显式 speech 遇到 chat provider 不应发起 HTTP 请求")
	}
}

func TestPetAISynthesizeSpeechAutoUsesNonStreamingChatAudio(t *testing.T) {
	config := petAITestConfig("openai", "openai", "gpt-4o-audio-preview")
	reader := &petAITestProviderReader{config: config}
	encodedAudio := base64.StdEncoding.EncodeToString([]byte("wav-bytes"))
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("chat audio path = %q", request.URL.Path)
		}
		if request.Header.Get("Accept") != "application/json" {
			t.Fatalf("chat audio accept = %q", request.Header.Get("Accept"))
		}
		var payload struct {
			Model      string   `json:"model"`
			Modalities []string `json:"modalities"`
			Messages   []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Audio struct {
				Format string `json:"format"`
				Voice  string `json:"voice"`
			} `json:"audio"`
			Stream bool `json:"stream"`
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("读取 chat audio body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析 chat audio body: %v", err)
		}
		if payload.Model != "gpt-4o-audio-preview" || payload.Stream || len(payload.Modalities) != 2 || payload.Modalities[0] != "text" || payload.Modalities[1] != "audio" {
			t.Fatalf("chat audio body = %#v", payload)
		}
		if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || !strings.Contains(payload.Messages[0].Content, "请温柔朗读") || !strings.Contains(payload.Messages[0].Content, "你好，桌宠") {
			t.Fatalf("chat audio messages = %#v", payload.Messages)
		}
		if payload.Audio.Format != "wav" || payload.Audio.Voice != "alloy" {
			t.Fatalf("chat audio options = %#v", payload.Audio)
		}
		return petAITestResponse(http.StatusOK, "application/json", `{"choices":[{"message":{"audio":{"data":"`+encodedAudio+`","format":"wav"}}}]}`), nil
	})

	service := NewPetAIService(reader, transport, nil)
	audio, mediaType, err := service.SynthesizeSpeech(context.Background(), PetSpeechRequest{
		PetID:       "pet-1",
		RequestID:   "chat-audio-1",
		Provider:    petAITestReference("openai", "pet-provider", "gpt-4o-audio-preview", PetCapabilityTTS),
		Text:        "你好，桌宠",
		Voice:       "alloy",
		Instruction: "请温柔朗读",
		VoiceMode:   PetVoiceAuto,
	})
	if err != nil {
		t.Fatalf("SynthesizeSpeech(chat audio) error = %v", err)
	}
	if string(audio) != "wav-bytes" || mediaType != "audio/wav" {
		t.Fatalf("chat audio result = %q, %q", audio, mediaType)
	}
}

func TestPetAISpeechAutoHonorsExplicitProviderAudioMode(t *testing.T) {
	config := petAITestConfig("openai", "openai", "mimo-v2.5-tts")
	config.AudioMode = "speech"
	reader := &petAITestProviderReader{config: config}
	calledPath := ""
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calledPath = request.URL.Path
		return petAITestResponse(http.StatusOK, "audio/mpeg", "speech-bytes"), nil
	})
	service := NewPetAIService(reader, transport, nil)
	audio, mediaType, err := service.SynthesizeSpeech(context.Background(), PetSpeechRequest{
		PetID:     "pet-1",
		Provider:  petAITestReference("openai", "pet-provider", "mimo-v2.5-tts", PetCapabilityTTS),
		Text:      "明确走 speech",
		VoiceMode: PetVoiceAuto,
	})
	if err != nil {
		t.Fatalf("SynthesizeSpeech(explicit speech) error = %v", err)
	}
	if calledPath != "/v1/audio/speech" || string(audio) != "speech-bytes" || mediaType != "audio/mpeg" {
		t.Fatalf("explicit provider mode result = %q, %q, path=%q", audio, mediaType, calledPath)
	}
}

func TestPetChatAudioResponseRejectsInvalidBase64FormatAndSize(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString([]byte("audio"))
	tests := []struct {
		name      string
		data      string
		format    string
		maxBytes  int64
		wantError PetAIErrorCode
	}{
		{name: "invalid base64", data: "not-base64", format: "wav", maxBytes: 64, wantError: PET_AI_RESPONSE_INVALID},
		{name: "data url", data: "data:audio/wav;base64," + valid, format: "wav", maxBytes: 64, wantError: PET_AI_RESPONSE_INVALID},
		{name: "unknown format", data: valid, format: "midi", maxBytes: 64, wantError: PET_AI_MEDIA_TYPE_INVALID},
		{name: "too large", data: valid, format: "wav", maxBytes: 2, wantError: PET_AI_RESPONSE_TOO_LARGE},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := `{"choices":[{"message":{"audio":{"data":"` + test.data + `","format":"` + test.format + `"}}}]}`
			_, _, err := parsePetChatAudioResponse([]byte(body), test.maxBytes, http.StatusOK)
			if got := petAITestErrorCode(t, err); got != string(test.wantError) {
				t.Fatalf("error code = %q, want %q", got, test.wantError)
			}
		})
	}
}

func TestPetAISynthesizeChatAudioCanBeCancelled(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "mimo-v2.5-tts")}
	started := make(chan struct{})
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	service := NewPetAIServiceWithOptions(reader, transport, nil, PetAIOptions{Timeout: 2 * time.Second})
	result := make(chan error, 1)
	go func() {
		_, _, err := service.SynthesizeSpeech(context.Background(), PetSpeechRequest{
			PetID:     "pet-1",
			RequestID: "chat-audio-cancel",
			Provider:  petAITestReference("openai", "pet-provider", "mimo-v2.5-tts", PetCapabilityTTS),
			Text:      "马上取消",
			VoiceMode: PetVoiceChat,
		})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("chat audio request did not start")
	}
	if err := service.CancelChat("chat-audio-cancel"); err != nil {
		t.Fatalf("CancelChat() error = %v", err)
	}
	select {
	case err := <-result:
		if got := petAITestErrorCode(t, err); got != string(PET_AI_REQUEST_CANCELLED) {
			t.Fatalf("cancel error code = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("chat audio cancellation did not finish")
	}
}

func TestPetAISynthesizeSpeechCancelBeforeProviderResolution(t *testing.T) {
	readerEntered := make(chan struct{})
	reader := PetAIProviderReaderFunc(func(ctx context.Context, _ PetProviderReference) (PetAIProviderConfig, error) {
		close(readerEntered)
		<-ctx.Done()
		return PetAIProviderConfig{}, ctx.Err()
	})
	service := NewPetAIServiceWithOptions(reader, petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("provider cancellation should happen before transport")
		return nil, nil
	}), nil, PetAIOptions{Timeout: time.Second})
	result := make(chan error, 1)
	go func() {
		_, _, err := service.SynthesizeSpeech(context.Background(), PetSpeechRequest{
			PetID: "pet-1", RequestID: "sync-cancel-before-register",
			Provider: petAITestReference("openai", "pet-provider", "pet-model", PetCapabilityTTS),
			Text:     "provider 解析期间取消", VoiceMode: PetVoiceSpeech,
		})
		result <- err
	}()
	select {
	case <-readerEntered:
	case <-time.After(time.Second):
		t.Fatal("provider resolution did not start")
	}
	if err := service.CancelSpeech("sync-cancel-before-register"); err != nil {
		t.Fatalf("CancelSpeech() error = %v", err)
	}
	select {
	case err := <-result:
		if got := petAITestErrorCode(t, err); got != string(PET_AI_REQUEST_CANCELLED) {
			t.Fatalf("cancel-before-register error code = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("provider resolution did not observe cancellation")
	}
}

func TestPetAICancelChatCancelsHTTPContextAndIsIdempotent(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	emitter := &petAITestEmitter{}
	transportStarted := make(chan struct{})
	contextCancelled := make(chan struct{})
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(transportStarted)
		<-request.Context().Done()
		close(contextCancelled)
		return nil, request.Context().Err()
	})
	service := NewPetAIServiceWithOptions(reader, transport, emitter, PetAIOptions{Timeout: 2 * time.Second})
	if _, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:     "pet-1",
		RequestID: "cancel-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "马上取消",
	}); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	select {
	case <-transportStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP transport 未启动")
	}
	if err := service.CancelChat("cancel-1"); err != nil {
		t.Fatalf("第一次 CancelChat() error = %v", err)
	}
	if err := service.CancelChat("cancel-1"); err != nil {
		t.Fatalf("第二次 CancelChat() error = %v", err)
	}
	select {
	case <-contextCancelled:
	case <-time.After(time.Second):
		t.Fatal("HTTP context 未被取消")
	}
	emitter.waitFor(t, PetAIEventCancelled)
}

func TestPetAIRejectsConcurrentRequestIDAndResponseBoundary(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	emitter := &petAITestEmitter{}
	started := make(chan struct{})
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	service := NewPetAIService(reader, transport, emitter)
	request := PetChatRequest{
		PetID:     "pet-1",
		RequestID: "same-id",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "并发测试",
	}
	if _, err := service.StartChat(context.Background(), request); err != nil {
		t.Fatalf("第一次 StartChat() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("第一次请求未进入 transport")
	}
	if _, err := service.StartChat(context.Background(), request); petAITestErrorCode(t, err) != string(PET_AI_REQUEST_IN_FLIGHT) {
		t.Fatalf("重复 requestId error = %v", err)
	}
	_ = service.CancelChat("same-id")

	limitedConfig := petAITestConfig("openai", "openai", "gpt-pet")
	limitedReader := &petAITestProviderReader{config: limitedConfig}
	largeTransport := petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return petAITestResponse(http.StatusOK, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"123456789\"}}]}\n\n"), nil
	})
	limited := NewPetAIServiceWithOptions(limitedReader, largeTransport, nil, PetAIOptions{MaxResponseBytes: 8})
	_, err := limited.GenerateDreamText(context.Background(), PetDreamTextRequest{
		PetID:     "pet-1",
		RequestID: "limit-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "响应上限",
	})
	if got := petAITestErrorCode(t, err); got != string(PET_AI_RESPONSE_TOO_LARGE) {
		t.Fatalf("response limit code = %q, error=%v", got, err)
	}
}

func TestPetAIUpstreamFailureEventDoesNotExposeAPIKey(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	emitter := &petAITestEmitter{}
	transport := petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return petAITestResponse(http.StatusBadGateway, "application/json", `{"error":"apiKey=pet-secret-key"}`), nil
	})
	service := NewPetAIService(reader, transport, emitter)
	if _, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:     "pet-1",
		RequestID: "failure-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "故障测试",
	}); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	failed := emitter.waitFor(t, PetAIEventFailed)
	if failed.Error == nil || failed.Error.Code != string(PET_AI_UPSTREAM_ERROR) {
		t.Fatalf("failed event = %#v", failed)
	}
	encoded, err := json.Marshal(emitter.snapshot())
	if err != nil {
		t.Fatalf("序列化失败事件: %v", err)
	}
	if strings.Contains(string(encoded), "pet-secret-key") {
		t.Fatalf("失败事件泄露 API Key: %s", encoded)
	}
}

func TestPetAIProviderReaderErrorIsStructuredWithoutUnderlyingText(t *testing.T) {
	reader := &petAITestProviderReader{err: errors.New("provider apiKey=pet-secret-key read failed")}
	service := NewPetAIService(reader, petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("provider read 失败时不应发起 HTTP")
		return nil, nil
	}), nil)
	_, err := service.GenerateDreamText(context.Background(), PetDreamTextRequest{
		PetID:     "pet-1",
		RequestID: "reader-error-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "读取配置",
	})
	if got := petAITestErrorCode(t, err); got != string(PET_UPSTREAM_ERROR) {
		t.Fatalf("provider reader error code = %q", got)
	}
	if strings.Contains(err.Error(), "pet-secret-key") {
		t.Fatalf("provider reader error 泄露底层文本: %v", err)
	}
}

func TestPetAIMalformedSSEFailsClosed(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	transport := petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return petAITestResponse(http.StatusOK, "text/event-stream", "data: definitely-not-json\n\n"), nil
	})
	service := NewPetAIService(reader, transport, nil)
	_, err := service.GenerateDreamText(context.Background(), PetDreamTextRequest{
		PetID:     "pet-1",
		RequestID: "bad-sse-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "坏 SSE",
	})
	if got := petAITestErrorCode(t, err); got != string(PET_AI_SSE_INVALID) {
		t.Fatalf("malformed SSE code = %q, error=%v", got, err)
	}
}

func TestPetAIProviderReadAndHTTPTimeoutAreStructured(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	service := NewPetAIServiceWithOptions(reader, transport, nil, PetAIOptions{Timeout: 20 * time.Millisecond})
	_, err := service.GenerateDreamText(context.Background(), PetDreamTextRequest{
		PetID:     "pet-1",
		RequestID: "timeout-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "超时测试",
	})
	if got := petAITestErrorCode(t, err); got != string(PET_AI_TIMEOUT) {
		t.Fatalf("timeout code = %q, error=%v", got, err)
	}
}
