package services

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newPetCodexCommandTestRuntime(t *testing.T, scenario string) (*PetCodexRuntime, *petCodexEventRecorder, string) {
	t.Helper()
	workspace := filepath.Clean(t.TempDir())
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions: &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(_ context.Context, _ string) (string, error) {
			return workspace, nil
		}),
		Emitter:         recorder,
		CommandFactory:  newCodexFixtureFactory(scenario, func() string { return scenario + "-thread" }),
		ResponseTimeout: 2 * time.Second,
	})
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, recorder, workspace
}

func agentCommandTestRequest(petID, persona string) AgentCommandRequest {
	return AgentCommandRequest{
		PetID:   petID,
		Persona: persona,
		Source:  AgentConversationSourceManager,
	}
}

func TestPetCodexRuntimeListsSkillsAndModelsFromJSONLFixture(t *testing.T) {
	runtime, _, workspace := newPetCodexCommandTestRuntime(t, "pet-capabilities")
	request := agentCommandTestRequest("capability-pet", "capability persona")

	// nil context 是 Wails/旧调用方仍可能传入的形态，runtime 必须归一化，
	// 不能在能力面板打开时因为 context nil 直接 panic。
	skills, err := runtime.ListSkills(nil, request)
	if err != nil {
		t.Fatalf("ListSkills() error = %v", err)
	}
	if skills.ProjectID != "" || skills.Workspace != workspace || len(skills.Skills) != 2 {
		t.Fatalf("skills result = %#v", skills)
	}
	if skills.Skills[0].Name != "fixture-skill" || !skills.Skills[0].Enabled {
		t.Fatalf("skills result did not preserve fixture skill: %#v", skills.Skills)
	}
	if skills.Skills[1].Name != "disabled-skill" || skills.Skills[1].Enabled {
		t.Fatalf("skills result did not preserve disabled skill: %#v", skills.Skills)
	}

	models, err := runtime.ListModels(nil, request)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if models.Workspace != workspace || len(models.Models) != 2 || models.NextCursor != "fixture-next" {
		t.Fatalf("models result = %#v", models)
	}
	if models.Models[0].ID != "fixture-default" || !models.Models[0].IsDefault || models.Models[0].DefaultReasoningEffort != "medium" {
		t.Fatalf("models result did not preserve default model metadata: %#v", models.Models)
	}
}

func TestPetCodexRuntimeExecutesCompactSteerAndInterruptCommands(t *testing.T) {
	runtime, recorder, _ := newPetCodexCommandTestRuntime(t, "pet-controls")
	request := agentCommandTestRequest("control-pet", "control persona")

	compact, err := runtime.ExecuteCommand(nil, AgentCommandRequest{
		PetID:   request.PetID,
		Persona: request.Persona,
		Command: "compact",
	})
	if err != nil {
		t.Fatalf("compact command error = %v", err)
	}
	if !compact.Accepted || compact.Command != "compact" || compact.ThreadID == "" {
		t.Fatalf("compact result = %#v", compact)
	}

	chat := petCodexRuntimeRequest(request.PetID, "control-chat", request.Persona)
	if _, err := runtime.StartChat(context.Background(), chat); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	started := recorder.waitFor(chat.RequestID, PetAIEventStarted)
	if started.Type != PetAIEventStarted {
		t.Fatalf("started event = %#v", started)
	}

	steer, err := runtime.ExecuteCommand(context.Background(), AgentCommandRequest{
		PetID:          request.PetID,
		Persona:        request.Persona,
		Command:        "turn/steer",
		ExpectedTurnID: "active-turn",
		Input:          "继续检查",
	})
	if err != nil {
		t.Fatalf("steer command error = %v", err)
	}
	if !steer.Accepted || steer.TurnID != "steered-turn" || steer.ThreadID != compact.ThreadID {
		t.Fatalf("steer result = %#v", steer)
	}

	interrupt, err := runtime.ExecuteCommand(context.Background(), AgentCommandRequest{
		PetID:          request.PetID,
		Persona:        request.Persona,
		Command:        "turn/interrupt",
		ExpectedTurnID: "steered-turn",
	})
	if err != nil {
		t.Fatalf("interrupt command error = %v", err)
	}
	if !interrupt.Accepted || interrupt.TurnID != "steered-turn" || interrupt.ThreadID != compact.ThreadID {
		t.Fatalf("interrupt result = %#v", interrupt)
	}
	if cancelled := recorder.waitFor(chat.RequestID, PetAIEventCancelled); cancelled.Type != PetAIEventCancelled {
		t.Fatalf("cancelled event = %#v", cancelled)
	}
}

func TestAgentConversationHubPublishesReviewLifecycle(t *testing.T) {
	workspace := filepath.Clean(t.TempDir())
	projectID := "review-project"
	recorder := &petCodexEventRecorder{}
	sessions := &agentCodexSessionMemory{sessions: make(map[string]AgentCodexSession)}
	var hub *AgentConversationHub
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		AgentSessions: sessions,
		ProjectWorkspaceResolver: ProjectWorkspaceResolverFunc(func(_ context.Context, id string) (string, error) {
			if id != projectID {
				t.Fatalf("project resolver id = %q, want %q", id, projectID)
			}
			return workspace, nil
		}),
		Emitter: PetAIEventEmitterFunc(func(event PetAIEvent) error {
			if hub == nil {
				return nil
			}
			return hub.Emit(event)
		}),
		CommandFactory:  newCodexFixtureFactory("pet-review", func() string { return "review-thread" }),
		ResponseTimeout: 2 * time.Second,
	})
	hub = NewAgentConversationHub(runtime, AgentConversationHubOptions{
		Emitter: recorder,
		PersonaResolver: AgentConversationPersonaResolverFunc(func(_ context.Context, id, petID string) (string, error) {
			if id != projectID || petID != DefaultPetID {
				t.Fatalf("persona resolver args = project:%q pet:%q", id, petID)
			}
			return "canonical review persona", nil
		}),
	})
	t.Cleanup(func() { _ = hub.Close() })

	result, err := hub.ExecuteCommand(context.Background(), AgentCommandRequest{
		ProjectID: projectID,
		PetID:     DefaultPetID,
		Command:   "review",
		Args:      []string{"uncommitted"},
	})
	if err != nil {
		t.Fatalf("review command error = %v", err)
	}
	if !result.Accepted || result.RequestID == "" {
		t.Fatalf("review result = %#v", result)
	}
	started := recorder.waitFor(result.RequestID, PetAIEventStarted)
	if started.ProjectID != projectID || started.Source != AgentConversationSourceManager || started.PetID != DefaultPetID {
		t.Fatalf("public review started event = %#v", started)
	}
	completed := recorder.waitFor(result.RequestID, PetAIEventCompleted)
	if completed.Text != "Review 已完成" || completed.ProjectID != projectID || completed.Source != AgentConversationSourceManager {
		t.Fatalf("public review completed event = %#v", completed)
	}
}

func TestAgentConversationHubSharesConcurrentProjectRequestsAndThread(t *testing.T) {
	workspace := filepath.Clean(t.TempDir())
	projectID := "concurrent-project"
	sessions := &agentCodexSessionMemory{sessions: make(map[string]AgentCodexSession)}
	recorder := &petCodexEventRecorder{}
	var processCount atomic.Int32
	var hub *AgentConversationHub
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		AgentSessions: sessions,
		ProjectWorkspaceResolver: ProjectWorkspaceResolverFunc(func(_ context.Context, id string) (string, error) {
			return workspace, nil
		}),
		Emitter: PetAIEventEmitterFunc(func(event PetAIEvent) error {
			if hub == nil {
				return nil
			}
			return hub.Emit(event)
		}),
		CommandFactory: func(executable string, args ...string) *exec.Cmd {
			processCount.Add(1)
			return newCodexFixtureCommand("pet-shared-concurrent", "concurrent-thread")
		},
		ResponseTimeout: 2 * time.Second,
	})
	hub = NewAgentConversationHub(runtime, AgentConversationHubOptions{
		Emitter: recorder,
		PersonaResolver: AgentConversationPersonaResolverFunc(func(context.Context, string, string) (string, error) {
			return "canonical concurrent persona", nil
		}),
	})
	t.Cleanup(func() { _ = hub.Close() })

	first := AgentConversationRequest{
		ProjectID: projectID,
		PetID:     DefaultPetID,
		RequestID: "concurrent-manager-request",
		UserText:  "第一条",
	}
	second := AgentConversationRequest{
		ProjectID:         projectID,
		PetID:             DefaultPetID,
		RequestID:         "concurrent-channel-request",
		Source:            AgentConversationSourceChannel,
		ChannelInstanceID: "channel-instance",
		ChannelChatID:     "channel-chat",
		UserText:          "第二条",
	}
	if _, err := hub.StartConversation(context.Background(), first); err != nil {
		t.Fatalf("first conversation error = %v", err)
	}
	recorder.waitFor(first.RequestID, PetAIEventStarted)
	if !waitForPetCodexActiveTurn(runtime, projectID, DefaultPetID, 5*time.Second) {
		t.Fatalf("first Codex turn was not established before queuing the second request")
	}
	if _, err := hub.StartConversation(context.Background(), second); err != nil {
		t.Fatalf("second conversation error = %v", err)
	}
	queued := recorder.waitFor(second.RequestID, PetAIEventQueued)
	if queued.Source != AgentConversationSourceChannel || queued.ProjectID != projectID {
		t.Fatalf("queued channel event = %#v", queued)
	}

	if err := hub.CancelChat(first.RequestID); err != nil {
		t.Fatalf("cancel first conversation error = %v", err)
	}
	recorder.waitFor(first.RequestID, PetAIEventCancelled)
	startedSecond := recorder.waitFor(second.RequestID, PetAIEventStarted)
	if startedSecond.Source != AgentConversationSourceChannel || startedSecond.ProjectID != projectID {
		t.Fatalf("started channel event = %#v", startedSecond)
	}
	if completed, ok := waitPetCodexEvent(recorder, second.RequestID, PetAIEventCompleted, 5*time.Second); !ok {
		recorder.mu.Lock()
		events := append([]PetAIEvent(nil), recorder.events...)
		recorder.mu.Unlock()
		t.Fatalf("second completed event missing; events = %#v", events)
	} else if completed.Type != PetAIEventCompleted {
		t.Fatalf("second completed event = %#v", completed)
	}
	if processCount.Load() != 1 {
		t.Fatalf("Codex process count = %d, want 1", processCount.Load())
	}
	session, err := sessions.LoadAgentCodexSession(context.Background(), projectID)
	if err != nil || session == nil || session.ThreadID != "concurrent-thread" {
		t.Fatalf("shared project session = %#v, err=%v", session, err)
	}
}

func TestPetCodexRuntimeResolvesInteractiveServerRequests(t *testing.T) {
	cases := []struct {
		name       string
		kind       PetAIInteractionKind
		request    ResolveInteractionRequest
		assertView func(*testing.T, PetAIInteraction)
	}{
		{
			name:    "approval",
			kind:    PetAIInteractionApproval,
			request: ResolveInteractionRequest{Decision: "acceptForSession"},
			assertView: func(t *testing.T, interaction PetAIInteraction) {
				if interaction.Command != "git status" || len(interaction.AvailableDecisions) != 3 {
					t.Fatalf("approval interaction = %#v", interaction)
				}
			},
		},
		{
			name: "permission",
			kind: PetAIInteractionPermission,
			request: ResolveInteractionRequest{
				Decision:    "accept",
				Scope:       "session",
				Permissions: map[string]any{},
			},
			assertView: func(t *testing.T, interaction PetAIInteraction) {
				if interaction.RawSchema == nil || interaction.CWD != "C:\\fixture" {
					t.Fatalf("permission interaction = %#v", interaction)
				}
			},
		},
		{
			name:    "user input",
			kind:    PetAIInteractionUserInput,
			request: ResolveInteractionRequest{Answers: map[string][]string{"question-1": []string{"answer"}}},
			assertView: func(t *testing.T, interaction PetAIInteraction) {
				if len(interaction.Questions) != 1 || interaction.Questions[0].ID != "question-1" {
					t.Fatalf("user input interaction = %#v", interaction)
				}
			},
		},
		{
			name: "MCP form",
			kind: PetAIInteractionMCPForm,
			request: ResolveInteractionRequest{
				Action:  "accept",
				Content: map[string]any{"token": "secret"},
			},
			assertView: func(t *testing.T, interaction PetAIInteraction) {
				if interaction.ServerName != "fixture-mcp" || interaction.RawSchema == nil {
					t.Fatalf("MCP interaction = %#v", interaction)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime, recorder, _ := newPetCodexCommandTestRuntime(t, "pet-interaction-"+interactionFixtureName(testCase.kind))
			request := petCodexRuntimeRequest("interaction-"+interactionFixtureName(testCase.kind), "interaction-request", "interaction persona")
			if _, err := runtime.StartChat(context.Background(), request); err != nil {
				t.Fatalf("StartChat() error = %v", err)
			}
			interactionEvent := recorder.waitFor(request.RequestID, PetAIEventInteraction)
			if interactionEvent.Interaction == nil || interactionEvent.Interaction.Kind != testCase.kind {
				t.Fatalf("interaction event = %#v", interactionEvent)
			}
			testCase.assertView(t, *interactionEvent.Interaction)
			testCase.request.InteractionID = interactionEvent.Interaction.ID
			if err := runtime.ResolveInteraction(context.Background(), testCase.request); err != nil {
				t.Fatalf("ResolveInteraction() error = %v", err)
			}
			completed := recorder.waitFor(request.RequestID, PetAIEventCompleted)
			if completed.Text != "交互已确认" {
				t.Fatalf("completed event = %#v", completed)
			}
			if err := runtime.ResolveInteraction(context.Background(), testCase.request); PetAIErrorCodeOf(err) != string(PET_AI_INVALID_REQUEST) {
				t.Fatalf("duplicate ResolveInteraction() error = %v", err)
			}
		})
	}
}

func interactionFixtureName(kind PetAIInteractionKind) string {
	switch kind {
	case PetAIInteractionApproval:
		return "approval"
	case PetAIInteractionPermission:
		return "permission"
	case PetAIInteractionUserInput:
		return "user-input"
	case PetAIInteractionMCPForm:
		return "mcp"
	default:
		return "unknown"
	}
}

func waitPetCodexEvent(recorder *petCodexEventRecorder, requestID string, eventType PetAIEventType, timeout time.Duration) (PetAIEvent, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		recorder.mu.Lock()
		for _, event := range recorder.events {
			if event.RequestID == requestID && event.Type == eventType {
				recorder.mu.Unlock()
				return event, true
			}
		}
		recorder.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	return PetAIEvent{}, false
}

func waitForPetCodexActiveTurn(runtime *PetCodexRuntime, projectID, petID string, timeout time.Duration) bool {
	if runtime == nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state := runtime.stateForConversation(projectID, petID)
		state.mu.Lock()
		ready := state.client != nil && state.active != nil && strings.TrimSpace(state.threadID) != "" && strings.TrimSpace(state.active.turnID) != ""
		state.mu.Unlock()
		if ready {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}
