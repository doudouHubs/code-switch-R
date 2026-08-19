package services

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type petSpeechSelectionStub struct {
	selection PetSpeechProviderSelection
	err       error
}

func petSpeechStringPointer(value string) *string {
	return &value
}

func (s *petSpeechSelectionStub) GetSpeechProviderSelection() (PetSpeechProviderSelection, error) {
	if s.err != nil {
		return PetSpeechProviderSelection{}, s.err
	}
	return s.selection, nil
}

func TestPetAITranscribeAudioBuildsOpenAICompatibleMultipartRequest(t *testing.T) {
	config := petAITestConfig("openai", "openai", "gpt-4o-mini-transcribe")
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("transcription path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer pet-secret-key" {
			t.Fatalf("transcription auth = %q", request.Header.Get("Authorization"))
		}
		reader, err := request.MultipartReader()
		if err != nil {
			t.Fatalf("create multipart reader: %v", err)
		}
		fields := map[string]string{}
		var uploaded []byte
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				t.Fatalf("read multipart part: %v", nextErr)
			}
			data, readErr := io.ReadAll(part)
			if readErr != nil {
				t.Fatalf("read multipart data: %v", readErr)
			}
			if part.FormName() == "file" {
				uploaded = data
				if part.FileName() != "voice-input.webm" || part.Header.Get("Content-Type") != "audio/webm" {
					t.Fatalf("file part = filename %q, content type %q", part.FileName(), part.Header.Get("Content-Type"))
				}
			} else {
				fields[part.FormName()] = string(data)
			}
		}
		if string(uploaded) != "webm-audio" || fields["model"] != "gpt-4o-mini-transcribe" || fields["response_format"] != "json" {
			t.Fatalf("multipart fields = %#v, file = %q", fields, uploaded)
		}
		return petAITestResponse(http.StatusOK, "application/json", `{"text":"  你好，桌宠  "}`), nil
	})

	service := NewPetAIService(&petAITestProviderReader{config: config}, transport, nil)
	result, err := service.TranscribeAudio(context.Background(), PetTranscriptionRequest{
		PetID:     "pet-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-4o-mini-transcribe", PetCapabilityChat),
		Data:      base64.StdEncoding.EncodeToString([]byte("webm-audio")),
		MediaType: "audio/webm;codecs=opus",
		FileName:  `C:\\tmp\\voice-input.webm`,
	})
	if err != nil {
		t.Fatalf("TranscribeAudio() error = %v", err)
	}
	if result.Text != "你好，桌宠" {
		t.Fatalf("transcription text = %q", result.Text)
	}
}

func TestPetAITranscribeAudioPreservesCallerCancellation(t *testing.T) {
	transportStarted := make(chan struct{})
	transportCancelled := make(chan struct{})
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(transportStarted)
		<-request.Context().Done()
		close(transportCancelled)
		return nil, request.Context().Err()
	})
	api := NewPetAIAPIService(NewPetAIService(
		&petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-transcribe")},
		transport,
		nil,
	))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := api.transcribeAudio(ctx, PetTranscriptionRequest{
			PetID:     "pet-1",
			Provider:  petAITestReference("openai", "pet-provider", "gpt-transcribe", PetCapabilityTranscription),
			Data:      base64.StdEncoding.EncodeToString([]byte("webm-audio")),
			MediaType: "audio/webm", FileName: "voice-input.webm",
		})
		result <- err
	}()
	select {
	case <-transportStarted:
	case <-time.After(time.Second):
		t.Fatal("transcription transport did not start")
	}
	cancel()
	select {
	case <-transportCancelled:
	case <-time.After(time.Second):
		t.Fatal("transcription transport did not observe cancellation")
	}
	select {
	case err := <-result:
		if got := petAITestErrorCode(t, err); got != string(PET_AI_REQUEST_CANCELLED) {
			t.Fatalf("transcription cancel error code = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("transcription cancellation did not finish")
	}
}

func TestPetAITranscribeAudioUsesApplicationSpeechSelection(t *testing.T) {
	config := petAITestConfig("openai", "openai", "gpt-4o-mini-transcribe")
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		reader, err := request.MultipartReader()
		if err != nil {
			t.Fatalf("create multipart reader: %v", err)
		}
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				t.Fatalf("read multipart part: %v", nextErr)
			}
			if part.FormName() == "model" {
				data, readErr := io.ReadAll(part)
				if readErr != nil {
					t.Fatalf("read model part: %v", readErr)
				}
				if string(data) != "gpt-4o-mini-transcribe" {
					t.Fatalf("model = %q, want application speech model", data)
				}
			}
		}
		if request.URL.Path != "/v1/audio/transcriptions" {
			t.Fatalf("transcription path = %q", request.URL.Path)
		}
		return petAITestResponse(http.StatusOK, "application/json", `{"text":"应用级语音"}`), nil
	})
	selection := &petSpeechSelectionStub{selection: PetSpeechProviderSelection{
		Platform:   petSpeechStringPointer("openai"),
		ProviderID: petSpeechStringPointer("configured-speech-provider"),
		ModelID:    "gpt-4o-mini-transcribe",
	}}
	service := NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader:        &petAITestProviderReader{config: config},
		SpeechSelectionReader: selection,
		Transport:             transport,
	})

	result, err := service.TranscribeAudio(context.Background(), PetTranscriptionRequest{
		PetID: "pet-1",
		// 请求中的引用故意使用聊天模型；配置 reader 存在时必须忽略它。
		Provider:  petAITestReference("codex", "chat-provider", "chat-model", PetCapabilityChat),
		Data:      base64.StdEncoding.EncodeToString([]byte("webm-audio")),
		MediaType: "audio/webm",
	})
	if err != nil {
		t.Fatalf("TranscribeAudio() error = %v", err)
	}
	if result.Text != "应用级语音" {
		t.Fatalf("transcription text = %q", result.Text)
	}
}

func TestPetAITranscribeAudioRejectsMissingApplicationSpeechSelection(t *testing.T) {
	transportCalls := 0
	service := NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader:        &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-4o-mini-transcribe")},
		SpeechSelectionReader: &petSpeechSelectionStub{},
		Transport: petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls++
			return nil, nil
		}),
	})

	_, err := service.TranscribeAudio(context.Background(), PetTranscriptionRequest{
		PetID:     "pet-1",
		Provider:  petAITestReference("openai", "chat-provider", "gpt-4o-mini-transcribe", PetCapabilityChat),
		Data:      base64.StdEncoding.EncodeToString([]byte("webm-audio")),
		MediaType: "audio/webm",
	})
	if got := PetProviderErrorCodeOf(err); got != PET_SPEECH_NOT_CONFIGURED {
		t.Fatalf("error code = %q, want %q; err=%v", got, PET_SPEECH_NOT_CONFIGURED, err)
	}
	if transportCalls != 0 {
		t.Fatalf("missing speech selection reached transport %d times", transportCalls)
	}
}

func TestPetAITranscribeAudioRejectsInvalidOrUnsupportedRequests(t *testing.T) {
	api := NewPetAIService(&petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-4o-mini")}, petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid transcription request should not reach transport")
		return nil, nil
	}), nil)
	for _, test := range []struct {
		name string
		data string
		mime string
	}{
		{name: "data url", data: "data:audio/webm;base64,abc", mime: "audio/webm"},
		{name: "invalid base64", data: "not-base64", mime: "audio/webm"},
		{name: "non audio", data: base64.StdEncoding.EncodeToString([]byte("bytes")), mime: "image/png"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := api.TranscribeAudio(context.Background(), PetTranscriptionRequest{
				PetID:    "pet-1",
				Provider: petAITestReference("openai", "pet-provider", "gpt-4o-mini", PetCapabilityChat),
				Data:     test.data, MediaType: test.mime,
			})
			if err == nil || !strings.Contains(PetAIErrorCodeOf(err), string(PET_AI_INVALID_REQUEST)) && !strings.Contains(PetAIErrorCodeOf(err), string(PET_AI_MEDIA_TYPE_INVALID)) {
				t.Fatalf("error = %v, code = %q", err, PetAIErrorCodeOf(err))
			}
		})
	}

	config := petAITestConfig("gemini", "gemini", "gemini-2.5-flash")
	unsupported := NewPetAIService(&petAITestProviderReader{config: config}, petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported protocol should not reach transport")
		return nil, nil
	}), nil)
	_, err := unsupported.TranscribeAudio(context.Background(), PetTranscriptionRequest{
		PetID: "pet-1", Provider: petAITestReference("gemini", "pet-provider", "gemini-2.5-flash", PetCapabilityChat),
		Data: base64.StdEncoding.EncodeToString([]byte("audio")), MediaType: "audio/webm",
	})
	if got := petAITestErrorCode(t, err); got != string(PET_CAPABILITY_UNSUPPORTED) {
		t.Fatalf("unsupported protocol code = %q", got)
	}
}
