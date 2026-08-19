package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func petAgentTestExecutor(t *testing.T) (*PetAgentToolExecutor, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("宠物项目\n第二行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "nested", "pet.go"), []byte("package pet\nfunc ReadOnly() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor, err := NewPetAgentToolExecutor(root)
	if err != nil {
		t.Fatal(err)
	}
	return executor, root
}

func petAgentCall(id string, name PetAgentToolName, args string) PetAgentToolCall {
	return PetAgentToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func petAgentResultErrorCode(t *testing.T, result PetAgentToolResult) string {
	t.Helper()
	if !result.IsError {
		t.Fatalf("expected tool error, result=%#v", result)
	}
	var payload PetAgentToolError
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("tool error is not structured JSON: %v; content=%q", err, result.Content)
	}
	return payload.Code
}

func TestPetAgentToolsDefinitionsAreStableAndReadOnly(t *testing.T) {
	definitions := PetAgentToolDefinitions()
	if len(definitions) != 4 {
		t.Fatalf("definition count = %d, want 4", len(definitions))
	}
	want := []PetAgentToolName{PetAgentToolRead, PetAgentToolLS, PetAgentToolGlob, PetAgentToolGrep}
	for index, definition := range definitions {
		if definition.Name != want[index] {
			t.Fatalf("definition[%d].Name = %q, want %q", index, definition.Name, want[index])
		}
		if definition.InputSchema["type"] != "object" || definition.InputSchema["additionalProperties"] != false {
			t.Fatalf("definition[%d] schema is not a closed object: %#v", index, definition.InputSchema)
		}
	}
	if _, err := json.Marshal(definitions); err != nil {
		t.Fatalf("definitions must be JSON serializable: %v", err)
	}
}

func TestPetAgentToolsReadLSGlobGrep(t *testing.T) {
	executor, root := petAgentTestExecutor(t)

	read, err := executor.Execute(context.Background(), petAgentCall("read-1", PetAgentToolRead, `{"file_path":"README.md","offset":2,"limit":1}`))
	if err != nil || read.IsError || !strings.Contains(read.Content, "2\t第二行") {
		t.Fatalf("Read result=%#v, err=%v", read, err)
	}

	ls, err := executor.Execute(context.Background(), petAgentCall("ls-1", PetAgentToolLS, `{"path":"src"}`))
	if err != nil || ls.IsError || !strings.Contains(ls.Content, "main.go") || !strings.Contains(ls.Content, "nested") {
		t.Fatalf("LS result=%#v, err=%v", ls, err)
	}

	glob, err := executor.Execute(context.Background(), petAgentCall("glob-1", PetAgentToolGlob, `{"pattern":"**/*.go"}`))
	if err != nil || glob.IsError || !strings.Contains(glob.Content, "src/main.go") || !strings.Contains(glob.Content, "src/nested/pet.go") {
		t.Fatalf("Glob result=%#v, err=%v", glob, err)
	}

	grep, err := executor.Execute(context.Background(), petAgentCall("grep-1", PetAgentToolGrep, `{"pattern":"ReadOnly","path":"src","output_mode":"matches"}`))
	if err != nil || grep.IsError || !strings.Contains(grep.Content, "src/nested/pet.go:2:func ReadOnly() {}") {
		t.Fatalf("Grep result=%#v, err=%v", grep, err)
	}

	files, err := executor.Execute(context.Background(), petAgentCall("grep-2", PetAgentToolGrep, `{"pattern":"package","path":"src","glob":"**/*.go","output_mode":"files_with_matches"}`))
	if err != nil || files.IsError || !strings.Contains(files.Content, "src/main.go") {
		t.Fatalf("Grep files_with_matches result=%#v, err=%v", files, err)
	}

	without, err := executor.Execute(context.Background(), petAgentCall("grep-3", PetAgentToolGrep, `{"pattern":"does-not-exist","path":"src","output_mode":"files_without_matches"}`))
	if err != nil || without.IsError || !strings.Contains(without.Content, "src/main.go") {
		t.Fatalf("Grep files_without_matches result=%#v, err=%v", without, err)
	}

	if filepath.IsAbs(root) == false {
		t.Fatalf("test root must be absolute: %q", root)
	}
}

func TestPetAgentToolsRejectPathTraversalAndSymlinkEscape(t *testing.T) {
	executor, root := petAgentTestExecutor(t)
	outside := filepath.Join(filepath.Dir(root), "pet-agent-outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, path := range []string{"../pet-agent-outside.txt", outside} {
		result, err := executor.Execute(context.Background(), petAgentCall("escape", PetAgentToolRead, `{"file_path":"`+strings.ReplaceAll(path, `\`, `\\`)+`"}`))
		if err != nil || petAgentResultErrorCode(t, result) != PetAgentToolErrorPathOutsideRoot {
			t.Fatalf("path %q result=%#v, err=%v", path, result, err)
		}
	}

	link := filepath.Join(root, "escape-link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unavailable on this host: %v", err)
	}
	result, err := executor.Execute(context.Background(), petAgentCall("symlink", PetAgentToolRead, `{"file_path":"escape-link.txt"}`))
	if err != nil || petAgentResultErrorCode(t, result) != PetAgentToolErrorPathOutsideRoot {
		t.Fatalf("symlink result=%#v, err=%v", result, err)
	}
}

func TestPetAgentToolsRejectLimitsAndInvalidArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	limits := DefaultPetAgentToolLimits()
	limits.MaxFileBytes = 4
	limits.MaxResultBytes = 12
	limits.MaxResults = 1
	executor, err := NewPetAgentToolExecutorWithLimits(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	large, err := executor.Execute(context.Background(), petAgentCall("large", PetAgentToolRead, `{"file_path":"large.txt"}`))
	if err != nil || petAgentResultErrorCode(t, large) != PetAgentToolErrorLimitExceeded {
		t.Fatalf("large file result=%#v, err=%v", large, err)
	}
	invalid, err := executor.Execute(context.Background(), petAgentCall("bad", PetAgentToolGrep, `{"pattern":"x","command":"cat"}`))
	if err != nil || petAgentResultErrorCode(t, invalid) != PetAgentToolErrorInvalidArguments {
		t.Fatalf("invalid args result=%#v, err=%v", invalid, err)
	}
	canceled, err := executor.Execute(context.Background(), petAgentCall("cancel", PetAgentToolLS, `{}`))
	if err != nil || canceled.IsError {
		// This sanity call proves an empty LS argument object is valid; cancellation
		// itself is checked below with a canceled context.
		t.Fatalf("empty LS result=%#v, err=%v", canceled, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = executor.Execute(ctx, petAgentCall("cancel-2", PetAgentToolLS, `{}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled execution error=%v, want context.Canceled", err)
	}
}

func TestPetAgentToolsContinuationMapsNativeProtocols(t *testing.T) {
	assistant := PetAgentAssistantTurn{
		Text: "先查文件",
		ToolCalls: []PetAgentToolCall{
			petAgentCall("call-1", PetAgentToolRead, `{"file_path":"README.md"}`),
		},
	}
	results := []PetAgentToolResult{{ToolCallID: "call-1", ToolName: "Read", Content: "1\t内容"}}
	for _, protocol := range []PetAgentToolProtocol{PetAgentProtocolOpenAI, PetAgentProtocolResponses, PetAgentProtocolAnthropic, PetAgentProtocolGemini} {
		request, err := BuildPetAgentContinuationRequest(protocol, assistant, results)
		if err != nil {
			t.Fatalf("protocol %q error=%v", protocol, err)
		}
		var messages []map[string]any
		if err := json.Unmarshal(request.NativeMessages, &messages); err != nil {
			t.Fatalf("protocol %q native messages invalid: %v", protocol, err)
		}
		wantMessages := 2
		if protocol == PetAgentProtocolResponses {
			wantMessages = 3
		}
		if len(messages) != wantMessages {
			t.Fatalf("protocol %q message count=%d, want %d", protocol, len(messages), wantMessages)
		}
		switch protocol {
		case PetAgentProtocolOpenAI:
			if messages[0]["role"] != "assistant" || messages[0]["tool_calls"] == nil || messages[1]["role"] != "tool" || messages[1]["tool_call_id"] != "call-1" {
				t.Fatalf("OpenAI mapping=%#v", messages)
			}
		case PetAgentProtocolResponses:
			if messages[0]["type"] != "message" || messages[1]["type"] != "function_call" || messages[1]["call_id"] != "call-1" || messages[2]["type"] != "function_call_output" || messages[2]["call_id"] != "call-1" {
				t.Fatalf("Responses mapping=%#v", messages)
			}
		case PetAgentProtocolAnthropic:
			if messages[0]["role"] != "assistant" || messages[1]["role"] != "user" {
				t.Fatalf("Anthropic mapping=%#v", messages)
			}
			blocks := messages[1]["content"].([]any)
			if blocks[0].(map[string]any)["type"] != "tool_result" {
				t.Fatalf("Anthropic tool result=%#v", blocks)
			}
		case PetAgentProtocolGemini:
			if messages[0]["role"] != "model" || messages[1]["role"] != "user" {
				t.Fatalf("Gemini mapping=%#v", messages)
			}
			parts := messages[1]["parts"].([]any)
			if parts[0].(map[string]any)["functionResponse"] == nil {
				t.Fatalf("Gemini function response=%#v", parts)
			}
		}
	}
}

func TestPetAgentToolsCoordinatorExecutesContinuation(t *testing.T) {
	executor, _ := petAgentTestExecutor(t)
	continuationCalls := 0
	coordinator := NewPetAgentToolLoopCoordinator(executor, func(ctx context.Context, request PetAgentContinuationRequest) (PetAgentAssistantTurn, error) {
		continuationCalls++
		if err := ctx.Err(); err != nil {
			return PetAgentAssistantTurn{}, err
		}
		if request.Protocol != PetAgentProtocolOpenAI || len(request.ToolResults) != 1 || request.ToolResults[0].IsError {
			t.Fatalf("continuation request=%#v", request)
		}
		return PetAgentAssistantTurn{Text: "读取完成"}, nil
	}, PetAgentToolLoopOptions{Protocol: PetAgentProtocolOpenAI, MaxRounds: 2, MaxToolCalls: 2})

	result, err := coordinator.Run(context.Background(), PetAgentAssistantTurn{ToolCalls: []PetAgentToolCall{
		petAgentCall("loop-1", PetAgentToolRead, `{"file_path":"README.md","limit":1}`),
	}})
	if err != nil || result.Final.Text != "读取完成" || result.Rounds != 1 || result.ToolCallCount != 1 || continuationCalls != 1 {
		t.Fatalf("coordinator result=%#v, err=%v, continuationCalls=%d", result, err, continuationCalls)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = coordinator.Run(canceled, PetAgentAssistantTurn{ToolCalls: []PetAgentToolCall{
		petAgentCall("loop-cancel", PetAgentToolRead, `{"file_path":"README.md"}`),
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("coordinator cancellation error=%v", err)
	}
}

func TestPetAgentToolsCoordinatorHonorsLoopLimits(t *testing.T) {
	executor, _ := petAgentTestExecutor(t)
	coordinator := NewPetAgentToolLoopCoordinator(executor, func(context.Context, PetAgentContinuationRequest) (PetAgentAssistantTurn, error) {
		return PetAgentAssistantTurn{ToolCalls: []PetAgentToolCall{petAgentCall("again", PetAgentToolLS, `{}`)}}, nil
	}, PetAgentToolLoopOptions{Protocol: PetAgentProtocolOpenAI, MaxRounds: 1, MaxToolCalls: 1})
	_, err := coordinator.Run(context.Background(), PetAgentAssistantTurn{ToolCalls: []PetAgentToolCall{
		petAgentCall("first", PetAgentToolLS, `{}`),
	}})
	if !errors.Is(err, ErrPetAgentToolLoopMaxRounds) {
		t.Fatalf("max rounds error=%v", err)
	}
}
