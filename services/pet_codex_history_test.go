package services

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePetCodexHistoryResponseExtractsVisibleMessages(t *testing.T) {
	workspace := filepath.Clean(filepath.Join(t.TempDir(), "workspace"))
	raw, err := json.Marshal(map[string]any{
		"thread": map[string]any{
			"id":  "thread-history",
			"cwd": workspace,
			"turns": []any{
				map[string]any{
					"id":        "turn-1",
					"status":    "completed",
					"createdAt": "2026-08-24T08:00:00Z",
					"items": []any{
						map[string]any{
							"type": "userMessage",
							"id":   "user-1",
							"content": []any{
								map[string]any{"type": "text", "text": "你好，宠物"},
							},
						},
						map[string]any{"type": "commandExecution", "id": "tool-1", "command": "不应显示"},
						map[string]any{"type": "agentMessage", "id": "assistant-1", "text": "你好，主人"},
					},
				},
			},
		},
		"cwd":            workspace,
		"approvalPolicy": "on-request",
		"sandbox":        map[string]any{"type": "readOnly"},
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := parsePetCodexHistoryResponse(raw, workspace, "thread-history")
	if err != nil {
		t.Fatalf("parse history error = %v", err)
	}
	if len(history.Messages) != 2 {
		t.Fatalf("visible messages = %#v", history.Messages)
	}
	if history.Messages[0].Role != "user" || history.Messages[0].Content != "你好，宠物" {
		t.Fatalf("user message = %#v", history.Messages[0])
	}
	if history.Messages[1].Role != "assistant" || history.Messages[1].Content != "你好，主人" {
		t.Fatalf("assistant message = %#v", history.Messages[1])
	}
	if history.Messages[0].CreatedAt != 1787558400000 {
		t.Fatalf("createdAt = %d", history.Messages[0].CreatedAt)
	}
}

func TestPetCodexHistoryRuntimeReadsPersistedThread(t *testing.T) {
	workspace := t.TempDir()
	persona := "历史 persona"
	sessions := &petCodexSessionMemory{sessions: map[string]PetCodexSession{
		"history-pet": {
			PetID:              "history-pet",
			ThreadID:           "history-thread",
			Workspace:          workspace,
			PersonaFingerprint: petCodexPersonaFingerprint(persona),
			ProtocolVersion:    PetCodexPlanProtocolVersion,
			UpdatedAt:          time.Now().UnixMilli(),
		},
	}}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions: sessions,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return workspace, nil
		}),
		CommandFactory:  newCodexFixtureFactory("pet-history", func() string { return "history-thread" }),
		ResponseTimeout: 2 * time.Second,
	})
	defer runtime.Close()

	history, err := runtime.GetChatHistory(context.Background(), PetChatHistoryRequest{
		PetID:   "history-pet",
		Persona: persona,
	})
	if err != nil {
		t.Fatalf("GetChatHistory() error = %v", err)
	}
	if history.ThreadID != "history-thread" || len(history.Messages) != 2 {
		t.Fatalf("history = %#v", history)
	}
	if history.Messages[1].Content != "历史回答" {
		t.Fatalf("assistant history = %#v", history.Messages[1])
	}
}

func TestParsePetCodexHistoryResponseRejectsDifferentWorkspace(t *testing.T) {
	workspace := filepath.Clean(filepath.Join(t.TempDir(), "workspace"))
	other := filepath.Clean(filepath.Join(t.TempDir(), "other"))
	raw, err := json.Marshal(map[string]any{
		"thread": map[string]any{"id": "thread-history", "cwd": other, "turns": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsePetCodexHistoryResponse(raw, workspace, "thread-history"); err == nil {
		t.Fatal("history outside workspace should be rejected")
	}
}

func TestNormalizePetCodexTimestampSupportsCommonUnits(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		want  int64
	}{
		{name: "seconds", value: 1787558400, want: 1787558400000},
		{name: "milliseconds", value: 1787558400000, want: 1787558400000},
		{name: "microseconds", value: 1787558400000000, want: 1787558400000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizePetCodexTimestamp(test.value); got != test.want {
				t.Fatalf("normalizePetCodexTimestamp(%d) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}
