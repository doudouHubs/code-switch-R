package services

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type petCodexDynamicToolTestProvider struct {
	mu          sync.Mutex
	snapshot    PetCodexDynamicToolSnapshot
	executor    PetAgentToolRunner
	executorErr error
	scopes      []string
	workspaces  []string
}

func (p *petCodexDynamicToolTestProvider) Snapshot(scope string) (PetCodexDynamicToolSnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopes = append(p.scopes, scope)
	return p.snapshot, nil
}

func (p *petCodexDynamicToolTestProvider) NewExecutor(_ context.Context, scope, workspace string) (PetAgentToolRunner, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopes = append(p.scopes, scope)
	p.workspaces = append(p.workspaces, workspace)
	if p.executorErr != nil {
		return nil, p.executorErr
	}
	return p.executor, nil
}

type petCodexDynamicToolTestRunner struct {
	mu     sync.Mutex
	calls  []PetAgentToolCall
	result PetAgentToolResult
	err    error
}

func (r *petCodexDynamicToolTestRunner) Execute(_ context.Context, call PetAgentToolCall) (PetAgentToolResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	result := r.result
	err := r.err
	r.mu.Unlock()
	if result.ToolCallID == "" {
		result.ToolCallID = call.ID
	}
	if result.ToolName == "" {
		result.ToolName = string(call.Name)
	}
	return result, err
}

func petCodexDynamicToolTestDefinition(name PetAgentToolName) PetAgentToolDefinition {
	return PetAgentToolDefinition{
		Name:        name,
		Description: "A test-only dynamic tool.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string"},
			},
			"required": []string{"value"},
		},
	}
}

func petCodexDynamicToolCallMessage(threadID, turnID, callID string, toolName PetAgentToolName) CodexAppServerMessage {
	params, _ := json.Marshal(map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
		"callId":   callID,
		"name":     string(toolName),
		"arguments": map[string]any{
			"value": "hello",
		},
	})
	return CodexAppServerMessage{ID: json.RawMessage("901"), Method: "item/tool/call", Params: params}
}

func setupPetCodexDynamicToolRuntime(t *testing.T, runner PetAgentToolRunner) (*PetCodexRuntime, *petCodexDynamicToolTestProvider, *petCodexActiveTurn) {
	t.Helper()
	workspace := filepath.Clean(t.TempDir())
	provider := &petCodexDynamicToolTestProvider{
		snapshot: PetCodexDynamicToolSnapshot{
			Definitions: []PetAgentToolDefinition{petCodexDynamicToolTestDefinition("FixtureTool")},
			Fingerprint: "fixture-tools-v1",
		},
		executor: runner,
	}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{DynamicToolProvider: provider})
	state := runtime.stateForPet("dynamic-tool-pet")
	active := &petCodexActiveTurn{
		state: state,
		request: petCodexChatInput{
			ToolScope:          "fixture-scope",
			ToolExecutionScope: "fixture-scope",
		},
		turnID:    "fixture-turn",
		toolCalls: make(map[string]struct{}),
	}
	state.mu.Lock()
	state.threadID = "fixture-thread"
	state.workspace = workspace
	state.toolNames = petCodexToolNames(provider.snapshot.Definitions)
	state.active = active
	state.mu.Unlock()
	return runtime, provider, active
}

func decodePetCodexDynamicToolResult(t *testing.T, response CodexAppServerServerRequestResponse) (bool, string) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("dynamic tool response error = %#v", response.Error)
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ContentItems []struct {
			Text string `json:"text"`
		} `json:"contentItems"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	text := ""
	if len(result.ContentItems) > 0 {
		text = result.ContentItems[0].Text
	}
	return result.Success, text
}

func TestPetCodexThreadStartParamsIncludesDynamicTools(t *testing.T) {
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{})
	definition := petCodexDynamicToolTestDefinition("FixtureTool")
	params := runtime.threadStartParams(t.TempDir(), "persona", PetCodexDynamicToolSnapshot{
		Definitions: []PetAgentToolDefinition{definition},
		Fingerprint: "fixture-tools-v1",
	})
	rawTools, ok := params["dynamicTools"].([]map[string]any)
	if !ok || len(rawTools) != 1 {
		t.Fatalf("dynamicTools = %#v", params["dynamicTools"])
	}
	if rawTools[0]["type"] != "function" || rawTools[0]["name"] != "FixtureTool" {
		t.Fatalf("dynamic tool definition = %#v", rawTools[0])
	}
	if _, ok := rawTools[0]["inputSchema"].(map[string]any); !ok {
		t.Fatalf("dynamic tool inputSchema = %#v", rawTools[0]["inputSchema"])
	}
}

func TestPetCodexServerRequestExecutesRegisteredDynamicTool(t *testing.T) {
	runner := &petCodexDynamicToolTestRunner{result: PetAgentToolResult{Content: "tool succeeded"}}
	runtime, provider, _ := setupPetCodexDynamicToolRuntime(t, runner)
	defer runtime.Close()

	response := runtime.handleCodexServerRequest(context.Background(), petCodexDynamicToolCallMessage("fixture-thread", "fixture-turn", "call-success", "FixtureTool"))
	success, content := decodePetCodexDynamicToolResult(t, response)
	if !success || content != "tool succeeded" {
		t.Fatalf("dynamic tool result = success:%t content:%q", success, content)
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || string(runner.calls[0].Arguments) != `{"value":"hello"}` {
		t.Fatalf("dynamic tool calls = %#v", runner.calls)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.scopes) != 1 || provider.scopes[0] != "fixture-scope" {
		t.Fatalf("dynamic tool scopes = %#v", provider.scopes)
	}
}

func TestPetCodexServerRequestReturnsFailureResultWhenDynamicToolExecutionFails(t *testing.T) {
	runner := &petCodexDynamicToolTestRunner{err: errors.New("fixture executor failed")}
	runtime, _, _ := setupPetCodexDynamicToolRuntime(t, runner)
	defer runtime.Close()

	response := runtime.handleCodexServerRequest(context.Background(), petCodexDynamicToolCallMessage("fixture-thread", "fixture-turn", "call-error", "FixtureTool"))
	success, content := decodePetCodexDynamicToolResult(t, response)
	if success || content != "fixture executor failed" {
		t.Fatalf("dynamic tool failure result = success:%t content:%q", success, content)
	}
}

func TestPetCodexServerRequestRejectsDynamicToolWithoutExecutionScope(t *testing.T) {
	runner := &petCodexDynamicToolTestRunner{result: PetAgentToolResult{Content: "should not run"}}
	runtime, provider, active := setupPetCodexDynamicToolRuntime(t, runner)
	defer runtime.Close()

	active.state.mu.Lock()
	active.request.ToolExecutionScope = ""
	active.state.mu.Unlock()
	response := runtime.handleCodexServerRequest(context.Background(), petCodexDynamicToolCallMessage("fixture-thread", "fixture-turn", "call-no-scope", "FixtureTool"))
	if response.Error == nil || response.Error.Code != petCodexRPCInvalidParams || !strings.Contains(response.Error.Message, "execution scope") {
		t.Fatalf("missing execution scope response = %#v", response)
	}
	runner.mu.Lock()
	if len(runner.calls) != 0 {
		runner.mu.Unlock()
		t.Fatalf("dynamic tool executed without execution scope: %#v", runner.calls)
	}
	runner.mu.Unlock()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.scopes) != 0 {
		t.Fatalf("executor was created without execution scope: %#v", provider.scopes)
	}
}

func TestPetCodexServerRequestRejectsUnregisteredDynamicTool(t *testing.T) {
	runner := &petCodexDynamicToolTestRunner{result: PetAgentToolResult{Content: "should not run"}}
	runtime, _, _ := setupPetCodexDynamicToolRuntime(t, runner)
	defer runtime.Close()

	response := runtime.handleCodexServerRequest(context.Background(), petCodexDynamicToolCallMessage("fixture-thread", "fixture-turn", "call-unknown", "UnknownTool"))
	if response.Error == nil || response.Error.Code != petCodexRPCInvalidParams {
		t.Fatalf("unregistered dynamic tool response = %#v", response)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 0 {
		t.Fatalf("unregistered dynamic tool was executed: %#v", runner.calls)
	}
}

func TestPetCodexRuntimeExecutesDynamicToolThroughAppServerFixture(t *testing.T) {
	workspace := filepath.Clean(t.TempDir())
	runner := &petCodexDynamicToolTestRunner{result: PetAgentToolResult{Content: "fixture tool ran"}}
	provider := &petCodexDynamicToolTestProvider{
		snapshot: PetCodexDynamicToolSnapshot{
			Definitions: []PetAgentToolDefinition{petCodexDynamicToolTestDefinition("FixtureTool")},
			Fingerprint: "fixture-tools-v1",
		},
		executor: runner,
	}
	recorder := &petCodexEventRecorder{}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions: &petCodexSessionMemory{sessions: make(map[string]PetCodexSession)},
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return workspace, nil
		}),
		Emitter:             recorder,
		DynamicToolProvider: provider,
		CommandFactory:      newCodexFixtureFactory("pet-dynamic-tool", func() string { return "dynamic-tool-thread" }),
		ResponseTimeout:     2 * time.Second,
	})
	defer runtime.Close()

	request := petCodexRuntimeRequest("dynamic-tool-pet", "dynamic-tool-request", "dynamic tool persona")
	request.ToolScope = "fixture-scope"
	request.ToolExecutionScope = "fixture-scope"
	if _, err := runtime.StartChat(context.Background(), request); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := recorder.waitFor(request.RequestID, PetAIEventCompleted)
	if completed.Text != "动态工具已执行" {
		t.Fatalf("completed text = %q", completed.Text)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || runner.calls[0].Name != "FixtureTool" {
		t.Fatalf("fixture dynamic tool calls = %#v", runner.calls)
	}
}

func TestPetCodexHistoryDoesNotReuseThreadWhenDynamicToolFingerprintChanges(t *testing.T) {
	workspace := filepath.Clean(t.TempDir())
	persona := "history dynamic tools"
	sessions := &petCodexSessionMemory{sessions: map[string]PetCodexSession{
		"history-tools-pet": {
			PetID:              "history-tools-pet",
			ThreadID:           "old-thread",
			Workspace:          workspace,
			PersonaFingerprint: petCodexPersonaFingerprint(persona),
			ToolFingerprint:    "old-tools-v1",
			ProtocolVersion:    PetCodexPlanProtocolVersion,
		},
	}}
	provider := &petCodexDynamicToolTestProvider{
		snapshot: PetCodexDynamicToolSnapshot{
			Definitions: []PetAgentToolDefinition{petCodexDynamicToolTestDefinition("FixtureTool")},
			Fingerprint: "current-tools-v2",
		},
	}
	runtime := NewPetCodexRuntime(PetCodexRuntimeDependencies{
		Sessions: sessions,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return workspace, nil
		}),
		DynamicToolProvider: provider,
	})
	defer runtime.Close()
	state := runtime.stateForPet("history-tools-pet")
	state.mu.Lock()
	state.toolScope = "fixture-scope"
	state.mu.Unlock()

	history, err := runtime.GetChatHistory(context.Background(), PetChatHistoryRequest{PetID: "history-tools-pet", Persona: persona})
	if err != nil {
		t.Fatalf("GetChatHistory() error = %v", err)
	}
	if history.ThreadID != "" || len(history.Messages) != 0 {
		t.Fatalf("history reused incompatible thread: %#v", history)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.scopes) != 1 || provider.scopes[0] != "fixture-scope" {
		t.Fatalf("history tool snapshot scopes = %#v", provider.scopes)
	}
}
