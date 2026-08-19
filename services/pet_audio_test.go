package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

type petAudioTestEmitter struct {
	mu     sync.Mutex
	events []PetAudioEvent
}

func (e *petAudioTestEmitter) EmitAudio(event PetAudioEvent) error {
	e.mu.Lock()
	e.events = append(e.events, event)
	e.mu.Unlock()
	return nil
}

func (e *petAudioTestEmitter) snapshot() []PetAudioEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]PetAudioEvent(nil), e.events...)
}

func TestPetAudioOpenAIStreamEmitsOrderedPCMEvents(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-4o-audio-preview")}
	emitter := &petAudioTestEmitter{}
	first := base64.StdEncoding.EncodeToString([]byte{1, 2, 3})
	second := base64.StdEncoding.EncodeToString([]byte{4})
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Audio struct {
				Format string `json:"format"`
			} `json:"audio"`
			Stream bool `json:"stream"`
		}
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return nil, err
		}
		if !body.Stream || body.Audio.Format != "pcm16" {
			t.Fatalf("stream body = %#v", body)
		}
		return petAITestResponse(http.StatusOK, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"audio\":{\"data\":\""+first+"\"}}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{\"audio\":{\"data\":\""+second+"\"}}}]}\n\n"+
			"data: [DONE]\n\n"), nil
	})
	service := NewPetAIService(reader, transport, nil)
	err := service.StreamSpeech(context.Background(), PetSpeechRequest{
		PetID: "pet-1", RequestID: "audio-stream-1",
		Provider: petAITestReference("openai", "pet-provider", "gpt-4o-audio-preview", PetCapabilityTTS),
		Text:     "你好", VoiceMode: PetVoiceChat,
	}, emitter)
	if err != nil {
		t.Fatalf("StreamSpeech() error = %v", err)
	}
	events := emitter.snapshot()
	if len(events) != 4 || events[0].Type != PetAudioEventStarted || events[1].Type != PetAudioEventChunk || events[2].Type != PetAudioEventChunk || events[3].Type != PetAudioEventCompleted {
		t.Fatalf("audio events = %#v", events)
	}
	if got := append(events[1].Data, events[2].Data...); string(got) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("PCM chunks = %v", got)
	}
	for index, event := range events {
		if event.Sequence != int64(index+1) || event.RequestID != "audio-stream-1" {
			t.Fatalf("event[%d] = %#v", index, event)
		}
	}
}

func TestPetAudioStreamCancellationEmitsCancelled(t *testing.T) {
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-4o-audio-preview")}
	emitter := &petAudioTestEmitter{}
	started := make(chan struct{})
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	service := NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader: reader, Transport: transport, AudioEmitter: emitter,
		Options: PetAIOptions{Timeout: time.Second},
	})
	if _, err := service.StartSpeechStream(context.Background(), PetSpeechRequest{
		PetID: "pet-1", RequestID: "audio-cancel-1",
		Provider: petAITestReference("openai", "pet-provider", "gpt-4o-audio-preview", PetCapabilityTTS),
		Text:     "马上取消", VoiceMode: PetVoiceChat,
	}); err != nil {
		t.Fatalf("StartSpeechStream() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("audio stream did not start")
	}
	if err := service.CancelSpeech("audio-cancel-1"); err != nil {
		t.Fatalf("CancelSpeech() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, event := range emitter.snapshot() {
			if event.Type == PetAudioEventCancelled {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("cancelled event missing: %#v", emitter.snapshot())
}

func TestPetAudioCancelBeforeProviderResolutionStopsRegistrationWindow(t *testing.T) {
	readerEntered := make(chan struct{})
	reader := PetAIProviderReaderFunc(func(ctx context.Context, _ PetProviderReference) (PetAIProviderConfig, error) {
		close(readerEntered)
		<-ctx.Done()
		return PetAIProviderConfig{}, ctx.Err()
	})
	service := NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader: reader,
		Transport: petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("provider cancellation should happen before transport")
			return nil, nil
		}),
		AudioEmitter: &petAudioTestEmitter{},
		Options:      PetAIOptions{Timeout: time.Second},
	})
	result := make(chan error, 1)
	go func() {
		_, err := service.StartSpeechStream(context.Background(), PetSpeechRequest{
			PetID: "pet-1", RequestID: "audio-cancel-before-register",
			Provider: petAITestReference("openai", "pet-provider", "pet-model", PetCapabilityTTS),
			Text:     "provider 解析期间取消", VoiceMode: PetVoiceChat,
		})
		result <- err
	}()
	select {
	case <-readerEntered:
	case <-time.After(time.Second):
		t.Fatal("provider resolution did not start")
	}
	if err := service.CancelSpeech("audio-cancel-before-register"); err != nil {
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

func TestPetAudioQueueOrdersAndDrainsFiniteSentences(t *testing.T) {
	queue := NewPetAudioQueue(2)
	defer queue.Close()
	started := make(chan struct{})
	finished := make(chan struct{})
	if err := queue.Enqueue(context.Background(), PetAudioQueueItem{
		RequestID: "sentence-1",
		Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(finished)
			return ctx.Err()
		},
	}); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := queue.Enqueue(context.Background(), PetAudioQueueItem{
		RequestID: "sentence-2",
		Run:       func(context.Context) error { t.Fatal("drained sentence started"); return nil },
	}); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}
	if err := queue.Enqueue(context.Background(), PetAudioQueueItem{
		RequestID: "sentence-3",
		Run:       func(context.Context) error { return nil },
	}); PetAIErrorCodeOf(err) != string(PET_AI_AUDIO_QUEUE_FULL) {
		t.Fatalf("third enqueue error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first sentence did not start")
	}
	queue.Drain()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("active sentence was not cancelled by drain")
	}
	if queue.Cancel("sentence-2") {
		t.Fatal("drain should remove queued sentence")
	}
	if err := queue.Enqueue(context.Background(), PetAudioQueueItem{
		RequestID: "sentence-4",
		Run:       func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("enqueue after drain: %v", err)
	}
}

func TestPetAudioRejectsUnknownFormatAndUnalignedPCM(t *testing.T) {
	if _, err := ParsePetAudioMediaType("audio/midi"); PetAIErrorCodeOf(err) != string(PET_AI_MEDIA_TYPE_INVALID) {
		t.Fatalf("unknown MIME error = %v", err)
	}
	parser := NewPetPCM16Parser(4)
	if data, err := parser.Push([]byte{1}); err != nil || len(data) != 0 {
		t.Fatalf("first PCM push = %v, %v", data, err)
	}
	if err := parser.Flush(); PetAIErrorCodeOf(err) != string(PET_AI_RESPONSE_INVALID) {
		t.Fatalf("unaligned PCM flush = %v", err)
	}
	if _, err := DecodePetAudioBase64("data:audio/pcm;base64,AA==", 4); PetAIErrorCodeOf(err) != string(PET_AI_RESPONSE_INVALID) {
		t.Fatalf("data URL error = %v", err)
	}
}
