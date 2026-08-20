package services

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

type recordingRequestLogSink struct {
	mu   sync.Mutex
	logs []ReqeustLog
}

func (s *recordingRequestLogSink) WriteRequestLog(_ context.Context, entry *ReqeustLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry != nil {
		s.logs = append(s.logs, *entry)
	}
	return nil
}

func (s *recordingRequestLogSink) last() (ReqeustLog, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.logs) == 0 {
		return ReqeustLog{}, false
	}
	return s.logs[len(s.logs)-1], true
}

func TestPetImageServiceLogsActualReturnedImageCount(t *testing.T) {
	sink := &recordingRequestLogSink{}
	service := NewPetImageServiceWithDependencies(PetImageDependencies{
		ProviderReader: &petImageTestProviderReader{config: petImageTestConfig()},
		Transport: petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return petImageTestResponse(http.StatusCreated, "application/json", petImageTestJSONResponse(petImageTestPNG(t))), nil
		}),
		RequestLogSink: sink,
	})

	request := petImageTestRequest()
	request.Count = 2
	result, err := service.GenerateImage(context.Background(), request)
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("returned images = %d, want 1", len(result.Images))
	}
	entry, ok := sink.last()
	if !ok {
		t.Fatal("image request was not logged")
	}
	if entry.RequestType != requestLogTypeImage || entry.ImageCount != 1 || entry.HttpCode != http.StatusCreated {
		t.Fatalf("image log = %+v", entry)
	}
	if entry.ImageWidth != 512 || entry.ImageHeight != 512 {
		t.Fatalf("image dimensions = %dx%d, want 512x512", entry.ImageWidth, entry.ImageHeight)
	}
	if entry.InputTokens != 0 || entry.OutputTokens != 0 {
		t.Fatalf("image log must not invent token usage: %+v", entry)
	}
}

func TestPetImageServiceLogsFailedImageRequestWithZeroImages(t *testing.T) {
	sink := &recordingRequestLogSink{}
	service := NewPetImageServiceWithDependencies(PetImageDependencies{
		ProviderReader: &petImageTestProviderReader{config: petImageTestConfig()},
		Transport: petImageTestRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return petImageTestResponse(http.StatusBadGateway, "application/json", `{"error":"upstream"}`), nil
		}),
		RequestLogSink: sink,
	})

	if _, err := service.GenerateImage(context.Background(), petImageTestRequest()); err == nil {
		t.Fatal("GenerateImage() should fail for non-2xx response")
	}
	entry, ok := sink.last()
	if !ok {
		t.Fatal("failed image request was not logged")
	}
	if entry.RequestType != requestLogTypeImage || entry.ImageCount != 0 || entry.HttpCode != http.StatusBadGateway {
		t.Fatalf("failed image log = %+v", entry)
	}
}
