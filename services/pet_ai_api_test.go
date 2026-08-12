package services

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestPetAIAPIUnavailableReturnsStructuredError(t *testing.T) {
	api := NewPetAIAPIService(nil)
	actions := map[string]func() error{
		"StartChat": func() error {
			_, err := api.StartChat(PetChatRequest{})
			return err
		},
		"CancelChat": func() error {
			return api.CancelChat("request-1")
		},
		"GenerateDreamText": func() error {
			_, err := api.GenerateDreamText(PetDreamTextRequest{})
			return err
		},
		"SynthesizeSpeech": func() error {
			_, err := api.SynthesizeSpeech(PetSpeechRequest{})
			return err
		},
	}

	for name, action := range actions {
		t.Run(name, func(t *testing.T) {
			err := action()
			if err == nil {
				t.Fatal("调用返回 nil error")
			}
			if got := PetAIErrorCodeOf(err); got != string(PET_AI_DEPENDENCY_UNAVAILABLE) {
				t.Fatalf("错误码 = %q，期望 %q", got, PET_AI_DEPENDENCY_UNAVAILABLE)
			}
			var petErr *PetAIError
			if !errors.As(err, &petErr) || petErr == nil {
				t.Fatalf("错误不是 PetAIError: %T", err)
			}
		})
	}

	var nilAPI *PetAIAPIService
	if _, err := nilAPI.GenerateDreamText(PetDreamTextRequest{}); PetAIErrorCodeOf(err) != string(PET_AI_DEPENDENCY_UNAVAILABLE) {
		t.Fatalf("nil API receiver 错误码 = %q，期望 %q", PetAIErrorCodeOf(err), PET_AI_DEPENDENCY_UNAVAILABLE)
	}
}

func TestPetAIAPIForwardsSuccessAndWrapsSpeechResult(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "pet-model")}
	reader.config.AudioMode = "speech"
	emitter := &petAITestEmitter{}
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/chat/completions":
			return petAITestResponse(http.StatusOK, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"桥接成功\"}}]}\n\n"+
				"data: [DONE]\n\n"), nil
		case "/v1/audio/speech":
			return petAITestResponse(http.StatusOK, "audio/mpeg", "speech-bytes"), nil
		default:
			t.Fatalf("未预期的请求路径: %s", request.URL.Path)
			return nil, nil
		}
	})
	api := NewPetAIAPIService(NewPetAIService(reader, transport, emitter))

	chat, err := api.StartChat(PetChatRequest{
		PetID:     "pet-1",
		RequestID: "api-chat-1",
		Provider:  petAITestReference("openai", "pet-provider", "pet-model", PetCapabilityChat),
		UserText:  "测试 bridge",
	})
	if err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	if chat.RequestID != "api-chat-1" {
		t.Fatalf("StartChat() result = %#v", chat)
	}
	if completed := emitter.waitFor(t, PetAIEventCompleted); completed.Text != "桥接成功" {
		t.Fatalf("completed text = %q", completed.Text)
	}

	dream, err := api.GenerateDreamText(PetDreamTextRequest{
		PetID:     "pet-1",
		RequestID: "api-dream-1",
		Provider:  petAITestReference("openai", "pet-provider", "pet-model", PetCapabilityChat),
		UserText:  "生成梦境",
	})
	if err != nil {
		t.Fatalf("GenerateDreamText() error = %v", err)
	}
	if dream != "桥接成功" {
		t.Fatalf("dream = %q", dream)
	}

	speech, err := api.SynthesizeSpeech(PetSpeechRequest{
		PetID:     "pet-1",
		RequestID: "api-speech-1",
		Provider:  petAITestReference("openai", "pet-provider", "pet-model", PetCapabilityTTS),
		Text:      "朗读这句话",
	})
	if err != nil {
		t.Fatalf("SynthesizeSpeech() error = %v", err)
	}
	if string(speech.Audio) != "speech-bytes" || speech.MediaType != "audio/mpeg" {
		t.Fatalf("speech result = %#v", speech)
	}

	encoded, err := json.Marshal(speech)
	if err != nil {
		t.Fatalf("序列化 PetSpeechResult: %v", err)
	}
	var payload struct {
		Audio     string `json:"audio"`
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("解析 PetSpeechResult JSON: %v", err)
	}
	if payload.Audio != base64.StdEncoding.EncodeToString([]byte("speech-bytes")) || payload.MediaType != "audio/mpeg" {
		t.Fatalf("PetSpeechResult JSON = %s", encoded)
	}
}

func TestPetAIAPICancelChatForwardsRequestIDAndIsIdempotent(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "pet-model")}
	emitter := &petAITestEmitter{}
	transportStarted := make(chan struct{})
	contextCancelled := make(chan struct{})
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(transportStarted)
		<-request.Context().Done()
		close(contextCancelled)
		return nil, request.Context().Err()
	})
	api := NewPetAIAPIService(NewPetAIService(reader, transport, emitter))

	if _, err := api.StartChat(PetChatRequest{
		PetID:     "pet-1",
		RequestID: "api-cancel-1",
		Provider:  petAITestReference("openai", "pet-provider", "pet-model", PetCapabilityChat),
		UserText:  "马上取消",
	}); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	select {
	case <-transportStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP transport 未启动")
	}

	if err := api.CancelChat("api-cancel-1"); err != nil {
		t.Fatalf("第一次 CancelChat() error = %v", err)
	}
	if err := api.CancelChat("api-cancel-1"); err != nil {
		t.Fatalf("第二次 CancelChat() error = %v", err)
	}
	if err := api.CancelChat("unknown-request"); err != nil {
		t.Fatalf("取消未知 requestID error = %v", err)
	}

	select {
	case <-contextCancelled:
	case <-time.After(time.Second):
		t.Fatal("HTTP context 未被取消")
	}
	if event := emitter.waitFor(t, PetAIEventCancelled); event.RequestID != "api-cancel-1" {
		t.Fatalf("取消事件 requestId = %q", event.RequestID)
	}
}
