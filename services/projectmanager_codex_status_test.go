package services

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setProjectManagerCodexTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func readProjectManagerCodexHookRoot(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode hooks file: %v", err)
	}
	return root
}

func projectManagerCodexManagedHandlerCount(t *testing.T, root map[string]any, eventName string) int {
	t.Helper()
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks must be an object")
	}
	groups, _ := hooks[eventName].([]any)
	count := 0
	for _, rawGroup := range groups {
		group, _ := rawGroup.(map[string]any)
		handlers, _ := group["hooks"].([]any)
		for _, rawHandler := range handlers {
			handler, _ := rawHandler.(map[string]any)
			command, _ := handler["command"].(string)
			if strings.Contains(command, projectManagerCodexHookCommandMarker) {
				count++
			}
		}
	}
	return count
}

func TestMergeProjectManagerCodexHooksPreservesUserHooksAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	original := `{
  "description": "user managed",
  "custom": {"keep": true},
  "hooks": {
    "SessionStart": [{"matcher":"resume","hooks":[{"type":"command","command":"user-hook"}]}],
    "SubagentStart": [{"hooks":[{"type":"command","command":"old.exe --codex-hook-event"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	command := `C:\Users\TestUser\.code-switch\project-manager-codex-hook\CodeSwitch.codex-hook.cmd --codex-hook-event`
	if err := mergeProjectManagerCodexHooks(path, command, false); err != nil {
		t.Fatalf("merge hooks: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := mergeProjectManagerCodexHooks(path, command, false); err != nil {
		t.Fatalf("merge hooks again: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("idempotent hook installation changed the file")
	}

	root := readProjectManagerCodexHookRoot(t, path)
	if root["description"] != "user managed" {
		t.Fatalf("description was overwritten: %#v", root["description"])
	}
	custom, _ := root["custom"].(map[string]any)
	if custom["keep"] != true {
		t.Fatalf("custom root data was not preserved: %#v", root["custom"])
	}
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest", "PreToolUse", "PostToolUse"} {
		if count := projectManagerCodexManagedHandlerCount(t, root, eventName); count != 1 {
			t.Fatalf("managed handler count for %s = %d, want 1", eventName, count)
		}
	}
	if count := projectManagerCodexManagedHandlerCount(t, root, "SubagentStart"); count != 0 {
		t.Fatalf("unsupported SubagentStart handler count = %d, want 0", count)
	}

	hooks := root["hooks"].(map[string]any)
	sessionGroups := hooks["SessionStart"].([]any)
	if len(sessionGroups) != 2 {
		t.Fatalf("SessionStart groups = %d, want preserved user group plus managed group", len(sessionGroups))
	}
	preToolGroups := hooks["PreToolUse"].([]any)
	managedPreToolGroup := preToolGroups[len(preToolGroups)-1].(map[string]any)
	if managedPreToolGroup["matcher"] != "^request_user_input$" {
		t.Fatalf("PreToolUse matcher = %#v", managedPreToolGroup["matcher"])
	}
	managedSessionGroup := sessionGroups[len(sessionGroups)-1].(map[string]any)
	managedSessionHandlers := managedSessionGroup["hooks"].([]any)
	managedSessionHandler := managedSessionHandlers[0].(map[string]any)
	if installedCommand := managedSessionHandler["command"]; installedCommand != command {
		t.Fatalf("installed hook command = %#v, want %q", installedCommand, command)
	}
}

func TestMergeProjectManagerCodexHooksAddsAgentEventsOnlyWhenSupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	command := `C:\CodeSwitch.codex-hook.cmd --codex-hook-event`
	if err := mergeProjectManagerCodexHooks(path, command, false); err != nil {
		t.Fatal(err)
	}
	root := readProjectManagerCodexHookRoot(t, path)
	if count := projectManagerCodexManagedHandlerCount(t, root, "SubagentStart"); count != 0 {
		t.Fatalf("legacy SubagentStart count = %d", count)
	}

	if err := mergeProjectManagerCodexHooks(path, command, true); err != nil {
		t.Fatal(err)
	}
	root = readProjectManagerCodexHookRoot(t, path)
	for _, eventName := range []string{"SubagentStart", "SubagentStop"} {
		if count := projectManagerCodexManagedHandlerCount(t, root, eventName); count != 1 {
			t.Fatalf("managed handler count for %s = %d, want 1", eventName, count)
		}
	}
}

func TestMergeProjectManagerCodexHooksFailsWithoutOverwritingInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	original := []byte(`{"hooks":`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeProjectManagerCodexHooks(path, `C:\CodeSwitch.codex-hook.cmd --codex-hook-event`, false); err == nil {
		t.Fatal("expected invalid hooks config to fail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("invalid hooks config was overwritten")
	}
}

func TestUpdateProjectManagerCodexFeatureText(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		preferredKey string
		want         string
	}{
		{
			name:         "enable legacy key in place",
			input:        "model = \"gpt\"\n\n[features]\ncodex_hooks = false\nfoo = true\n",
			preferredKey: "codex_hooks",
			want:         "model = \"gpt\"\n\n[features]\ncodex_hooks = true\nfoo = true\n",
		},
		{
			name:         "append stable key with CRLF",
			input:        "model = \"gpt\"\r\n",
			preferredKey: "hooks",
			want:         "model = \"gpt\"\r\n\r\n[features]\r\nhooks = true",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := updateProjectManagerCodexFeatureText(test.input, test.preferredKey); got != test.want {
				t.Fatalf("updated config:\n%q\nwant:\n%q", got, test.want)
			}
		})
	}
}

func TestEnableProjectManagerCodexHooksFeatureFailsWithoutOverwritingInvalidTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("[features\ncodex_hooks = false\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := enableProjectManagerCodexHooksFeature(path, projectManagerCodexVersion{Minor: 122}); err == nil {
		t.Fatal("expected invalid TOML to fail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("invalid TOML was overwritten")
	}
}

func TestEnableProjectManagerCodexHooksFeatureUsesVersionedKey(t *testing.T) {
	tests := []struct {
		name    string
		version projectManagerCodexVersion
		wantKey string
	}{
		{name: "legacy 0.122", version: projectManagerCodexVersion{Minor: 122}, wantKey: "codex_hooks = true"},
		{name: "stable 0.131", version: projectManagerCodexVersion{Minor: 131}, wantKey: "hooks = true"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := enableProjectManagerCodexHooksFeature(path, test.version); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), test.wantKey) {
				t.Fatalf("config = %q, want %q", data, test.wantKey)
			}
		})
	}
}

func TestRunProjectManagerCodexHookReceiverWritesSanitizedEvent(t *testing.T) {
	setProjectManagerCodexTestHome(t)
	payload := `{
  "hook_event_name":"PreToolUse",
  "session_id":"session-1",
  "turn_id":"turn-1",
  "tool_name":"request_user_input",
  "cwd":"F:/work/demo/",
  "transcript_path":{"value":"F:/codex/session.jsonl"},
  "tool_input":{"secret":"must-not-be-persisted"}
}`
	if err := RunProjectManagerCodexHookReceiver(strings.NewReader(payload)); err != nil {
		t.Fatalf("receive hook: %v", err)
	}
	eventRoot, err := projectManagerCodexEventRootPath()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(eventRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("event files = %d, want 1", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(eventRoot, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("must-not-be-persisted")) {
		t.Fatal("hook event persisted full tool payload")
	}
	var event projectManagerCodexHookEvent
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if event.HookEventName != "PreToolUse" || event.SessionID != "session-1" || event.TurnID != "turn-1" {
		t.Fatalf("unexpected event identity: %#v", event)
	}
	if event.ToolName != "request_user_input" {
		t.Fatalf("tool name = %q", event.ToolName)
	}
	if event.Cwd != normalizeProjectManagerProjectPath("F:/work/demo/") {
		t.Fatalf("cwd = %q", event.Cwd)
	}
	if event.TranscriptPath != "F:/codex/session.jsonl" {
		t.Fatalf("transcript path = %q", event.TranscriptPath)
	}
}

func TestRunProjectManagerCodexHookReceiverMarksPlanImplementationPending(t *testing.T) {
	setProjectManagerCodexTestHome(t)
	payload := `{
  "hook_event_name":"Stop",
  "session_id":"session-1",
  "turn_id":"turn-1",
  "cwd":"F:/work/demo/",
  "permission_mode":"plan",
  "last_assistant_message":"<proposed_plan>must-not-be-persisted</proposed_plan>"
}`
	if err := RunProjectManagerCodexHookReceiver(strings.NewReader(payload)); err != nil {
		t.Fatalf("receive hook: %v", err)
	}
	eventRoot, err := projectManagerCodexEventRootPath()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(eventRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("event files = %d, want 1", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(eventRoot, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("must-not-be-persisted")) {
		t.Fatal("hook event persisted last assistant message")
	}
	var event projectManagerCodexHookEvent
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if !event.PlanImplementationPending {
		t.Fatalf("plan implementation pending = %t, want true", event.PlanImplementationPending)
	}
}

func TestProjectManagerCodexStatusStateMachine(t *testing.T) {
	service := newProjectManagerCodexStatusService()
	service.monitor.AgentHooksSupported = true
	event := func(name string, sequence int64) projectManagerCodexHookEvent {
		return projectManagerCodexHookEvent{
			EventID:          name,
			HookEventName:    name,
			SessionID:        "session-1",
			TurnID:           "turn-1",
			Cwd:              `F:\work\demo`,
			ReceivedAt:       sequence,
			ReceivedUnixNano: sequence,
		}
	}

	if !service.applyEvent(event("SessionStart", 1)) {
		t.Fatal("SessionStart was not applied")
	}
	if status := service.sessions["session-1"]; status.State != CodexRuntimeIdle {
		t.Fatalf("SessionStart state = %s", status.State)
	}
	service.applyEvent(event("UserPromptSubmit", 2))
	if status := service.sessions["session-1"]; status.State != CodexRuntimeActive || status.TurnStatus != "in_progress" {
		t.Fatalf("UserPromptSubmit status = %#v", status)
	}
	service.applyEvent(event("PermissionRequest", 3))
	if status := service.sessions["session-1"]; status.State != CodexRuntimeWaitingApproval {
		t.Fatalf("PermissionRequest state = %s", status.State)
	}
	preTool := event("PreToolUse", 4)
	preTool.ToolName = "request_user_input"
	service.applyEvent(preTool)
	if status := service.sessions["session-1"]; status.State != CodexRuntimeWaitingUserInput {
		t.Fatalf("PreToolUse state = %s", status.State)
	}
	service.applyEvent(event("PostToolUse", 5))
	if status := service.sessions["session-1"]; status.State != CodexRuntimeActive {
		t.Fatalf("PostToolUse state = %s", status.State)
	}
	firstAgent := event("SubagentStart", 6)
	firstAgent.AgentID = "agent-1"
	service.applyEvent(firstAgent)
	secondAgent := event("SubagentStart", 7)
	secondAgent.AgentID = "agent-2"
	service.applyEvent(secondAgent)
	if status := service.sessions["session-1"]; status.ActiveAgents != 2 || status.State != CodexRuntimeActive {
		t.Fatalf("SubagentStart status = %#v", status)
	}
	stopAgent := event("SubagentStop", 8)
	stopAgent.AgentID = "agent-1"
	service.applyEvent(stopAgent)
	if status := service.sessions["session-1"]; status.ActiveAgents != 1 {
		t.Fatalf("SubagentStop active agents = %d", status.ActiveAgents)
	}
	service.applyEvent(event("Stop", 9))
	status := service.sessions["session-1"]
	if status.State != CodexRuntimeIdle || status.TurnStatus != "completed" || status.ActiveAgents != 0 {
		t.Fatalf("Stop status = %#v", status)
	}
	if service.applyEvent(event("UserPromptSubmit", 2)) {
		t.Fatal("stale event was applied")
	}
}

func TestProjectManagerCodexStatusStateMachineKeepsPlanImplementationPending(t *testing.T) {
	service := newProjectManagerCodexStatusService()
	service.applyEvent(projectManagerCodexHookEvent{
		EventID:          "prompt",
		HookEventName:    "UserPromptSubmit",
		SessionID:        "session-1",
		TurnID:           "turn-1",
		ReceivedAt:       1,
		ReceivedUnixNano: 1,
	})
	service.applyEvent(projectManagerCodexHookEvent{
		EventID:                   "stop",
		HookEventName:             "Stop",
		SessionID:                 "session-1",
		TurnID:                    "turn-1",
		PlanImplementationPending: true,
		ReceivedAt:                2,
		ReceivedUnixNano:          2,
	})
	status := service.sessions["session-1"]
	if status.State != CodexRuntimeWaitingUserInput || status.TurnStatus != "completed" {
		t.Fatalf("plan implementation status = %#v", status)
	}
}

func TestAggregateProjectManagerCodexProjectsUsesPriorityRepresentative(t *testing.T) {
	path := `F:\work\demo`
	projects := aggregateProjectManagerCodexProjects([]CodexSessionRuntimeStatus{
		{SessionID: "idle-newest", ProjectPath: path, State: CodexRuntimeIdle, UpdatedAt: 300},
		{SessionID: "error-representative", ProjectPath: strings.ToLower(path), State: CodexRuntimeSystemError, UpdatedAt: 100},
		{SessionID: "active", ProjectPath: path, State: CodexRuntimeActive, UpdatedAt: 200},
		{SessionID: "waiting", ProjectPath: path, State: CodexRuntimeWaitingApproval, UpdatedAt: 250},
	})
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(projects))
	}
	project := projects[0]
	if project.State != CodexRuntimeSystemError || project.LatestSessionID != "error-representative" {
		t.Fatalf("aggregate representative = %#v", project)
	}
	if project.UpdatedAt != 300 || project.ActiveSessions != 1 || project.WaitingSessions != 1 || project.ErrorSessions != 1 {
		t.Fatalf("aggregate counters = %#v", project)
	}
}

func TestProjectManagerCodexLoadStateInvalidatesUnverifiableProcessState(t *testing.T) {
	setProjectManagerCodexTestHome(t)
	service := newProjectManagerCodexStatusService()
	path, err := service.statePath()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := CodexRuntimeStatusSnapshot{
		Sessions: []CodexSessionRuntimeStatus{{
			SessionID:    "session-1",
			State:        CodexRuntimeActive,
			TurnStatus:   "in_progress",
			ActiveAgents: 2,
		}},
	}
	if err := AtomicWriteJSON(path, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := service.loadState(); err != nil {
		t.Fatal(err)
	}
	status := service.sessions["session-1"]
	if status.State != CodexRuntimeNotLoaded || status.TurnStatus != "interrupted" || status.ActiveAgents != 0 {
		t.Fatalf("loaded status = %#v", status)
	}
}

func TestReconcileProjectManagerCodexTranscriptDetectsPendingUserInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	pending := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"type":"event_msg","payload":{"type":"request_user_input","turn_id":"turn-1","call_id":"call-1"}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(pending), 0o600); err != nil {
		t.Fatal(err)
	}
	status := &CodexSessionRuntimeStatus{
		TurnID:         "turn-1",
		State:          CodexRuntimeActive,
		TurnStatus:     "in_progress",
		TranscriptPath: path,
	}
	if !reconcileProjectManagerCodexTranscript(status) {
		t.Fatal("pending request_user_input did not change status")
	}
	if status.State != CodexRuntimeWaitingUserInput {
		t.Fatalf("pending state = %s", status.State)
	}

	resolvedPath := filepath.Join(t.TempDir(), "resolved.jsonl")
	resolved := pending + "\n" + `{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"ok"}}`
	if err := os.WriteFile(resolvedPath, []byte(resolved), 0o600); err != nil {
		t.Fatal(err)
	}
	resolvedStatus := &CodexSessionRuntimeStatus{
		TurnID:         "turn-1",
		State:          CodexRuntimeWaitingUserInput,
		TurnStatus:     "in_progress",
		TranscriptPath: resolvedPath,
	}
	if !reconcileProjectManagerCodexTranscript(resolvedStatus) {
		t.Fatal("resolved request_user_input did not change status")
	}
	if resolvedStatus.State != CodexRuntimeActive {
		t.Fatalf("resolved state = %s", resolvedStatus.State)
	}
}

func TestReconcileProjectManagerCodexTranscriptPreservesPlanImplementationPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	completed := `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1"}}`
	if err := os.WriteFile(path, []byte(completed), 0o600); err != nil {
		t.Fatal(err)
	}
	status := &CodexSessionRuntimeStatus{
		TurnID:         "turn-1",
		State:          CodexRuntimeWaitingUserInput,
		TurnStatus:     "completed",
		TranscriptPath: path,
	}
	if reconcileProjectManagerCodexTranscript(status) {
		t.Fatal("task_complete overwrote plan implementation pending state")
	}
	if status.State != CodexRuntimeWaitingUserInput || status.TurnStatus != "completed" {
		t.Fatalf("plan implementation status = %#v", status)
	}
}
