package services

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

type petCodexModelReaderStub struct {
	reference PetAgentModelReference
	err       error
}

func (s *petCodexModelReaderStub) LoadAgentModelReference(context.Context, string) (PetAgentModelReference, error) {
	if s == nil {
		return PetAgentModelReference{}, nil
	}
	return s.reference, s.err
}

func TestNormalizePetAgentModelReference(t *testing.T) {
	tests := []struct {
		name  string
		input PetAgentModelReference
		code  PetProviderErrorCode
		want  PetAgentModelReference
	}{
		{
			name: "normalizes codex model and effort",
			input: PetAgentModelReference{
				ProviderPlatform: " CODEX ",
				ModelID:          " gpt-5-codex ",
				ReasoningEffort:  PetReasoningEffort(" HIGH "),
			},
			want: PetAgentModelReference{
				ProviderPlatform: "codex",
				ModelID:          "gpt-5-codex",
				ReasoningEffort:  PetReasoningHigh,
			},
		},
		{
			name:  "rejects missing model",
			input: PetAgentModelReference{ProviderPlatform: "codex"},
			code:  PET_MODEL_NOT_CONFIGURED,
		},
		{
			name:  "rejects non codex platform",
			input: PetAgentModelReference{ProviderPlatform: "openai", ModelID: "gpt-4o"},
			code:  PET_PLATFORM_UNSUPPORTED,
		},
		{
			name: "rejects unsupported effort",
			input: PetAgentModelReference{
				ProviderPlatform: "codex",
				ModelID:          "gpt-5-codex",
				ReasoningEffort:  PetReasoningEffort("xhigh"),
			},
			code: PET_PROVIDER_CONFIG_INVALID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizePetAgentModelReference(tt.input)
			if tt.code != "" {
				if err == nil || PetProviderErrorCodeOf(err) != tt.code {
					t.Fatalf("error = %v, code = %q, want %q", err, PetProviderErrorCodeOf(err), tt.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizePetAgentModelReference() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("reference = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPetCodexRuntimeModelParamsUsePetAgentReference(t *testing.T) {
	reference := PetAgentModelReference{
		ProviderPlatform: "codex",
		ModelID:          "gpt-5-codex",
		ReasoningEffort:  PetReasoningHigh,
	}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{})
	workspace := t.TempDir()

	start := runtime.threadStartParamsWithModel(workspace, "persona", reference)
	if start["model"] != reference.ModelID {
		t.Fatalf("thread/start model = %#v", start["model"])
	}
	if _, ok := start["effort"]; ok {
		t.Fatal("thread/start must not receive turn-only effort")
	}

	resume := runtime.threadResumeParamsWithModel("thread-id", workspace, "persona", reference)
	if resume["model"] != reference.ModelID {
		t.Fatalf("thread/resume model = %#v", resume["model"])
	}

	turn := runtime.buildTurnStartParams(&petCodexActiveTurn{
		state:          &petCodexPetState{threadID: "thread-id", workspace: workspace},
		modelReference: reference,
		request: petCodexChatInput{
			RequestID: "request-id",
			UserText:  "hello",
		},
	})
	if turn["model"] != reference.ModelID || turn["effort"] != string(reference.ReasoningEffort) {
		t.Fatalf("turn/start model/effort = %#v/%#v", turn["model"], turn["effort"])
	}
	for _, key := range []string{"modelProvider", "model_provider", "approvalPolicy", "sandbox", "networkAccess"} {
		if _, ok := turn[key]; ok {
			t.Fatalf("turn/start unexpectedly overrides Codex default %q", key)
		}
	}
}

func TestPetCodexRuntimeModelReferenceErrorsAreStructured(t *testing.T) {
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		AgentModelReader: &petCodexModelReaderStub{
			reference: PetAgentModelReference{ProviderPlatform: "codex"},
		},
	})
	_, err := runtime.loadPetAgentModelReference(context.Background(), DefaultPetID)
	if err == nil || PetProviderErrorCodeOf(err) != PET_MODEL_NOT_CONFIGURED {
		t.Fatalf("missing model error = %v, want %s", err, PET_MODEL_NOT_CONFIGURED)
	}

	runtime = NewPetCodexRuntime(PetCodexRuntimeDependencies{
		AgentModelReader: &petCodexModelReaderStub{err: errors.New("database unavailable")},
	})
	_, err = runtime.loadPetAgentModelReference(context.Background(), DefaultPetID)
	if err == nil || PetAIErrorCodeOf(err) != string(PET_AI_DEPENDENCY_UNAVAILABLE) {
		t.Fatalf("reader error = %v, want %s", err, PET_AI_DEPENDENCY_UNAVAILABLE)
	}
}

func TestPetCodexRuntimeSwitchesModelOnExistingThread(t *testing.T) {
	workspace := t.TempDir()
	sessions := &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)}
	recorder := &petCodexEventRecorder{}
	reader := &petCodexModelReaderStub{reference: PetAgentModelReference{
		ProviderPlatform: "codex",
		ModelID:          "model-a",
	}}
	var processCount atomic.Int32
	factory := func(executable string, args ...string) *exec.Cmd {
		processNumber := processCount.Add(1)
		return newCodexFixtureCommand("pet-model-switch", fmt.Sprintf("new-process-thread-%d", processNumber))
	}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions:         sessions,
		AgentModelReader: reader,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return workspace, nil
		}),
		Emitter:         recorder,
		CommandFactory:  factory,
		ResponseTimeout: 2 * time.Second,
	})
	defer runtime.Close()

	first := petCodexRuntimeRequest("model-switch-pet", "model-switch-first", "model switch persona")
	if _, err := runtime.StartChat(context.Background(), first); err != nil {
		t.Fatalf("first StartChat() error = %v", err)
	}
	recorder.waitFor(first.RequestID, PetAIEventCompleted)
	sessions.mu.Lock()
	firstSession := sessions.sessions[first.PetID]
	sessions.mu.Unlock()

	reader.reference.ModelID = "model-b"
	second := petCodexRuntimeRequest("model-switch-pet", "model-switch-second", "model switch persona")
	if _, err := runtime.StartChat(context.Background(), second); err != nil {
		t.Fatalf("second StartChat() error = %v", err)
	}
	recorder.waitFor(second.RequestID, PetAIEventCompleted)

	sessions.mu.Lock()
	secondSession := sessions.sessions[second.PetID]
	sessions.mu.Unlock()
	if processCount.Load() != 2 {
		t.Fatalf("app-server process count = %d, want one process per model selection", processCount.Load())
	}
	if firstSession.ThreadID == "" || firstSession.ThreadID != secondSession.ThreadID {
		t.Fatalf("model switch created a new thread: first=%#v second=%#v", firstSession, secondSession)
	}
}
