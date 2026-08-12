package services

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPetAIChatExecutesOpenAIToolAndContinuesWithNativeMessages(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("工具结果第一行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	emitter := &petAITestEmitter{}
	requestCount := 0
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		body, err := ioReadAllRequest(request)
		if err != nil {
			t.Fatalf("读取 OpenAI request: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析 OpenAI request: %v", err)
		}
		if payload["stream"] != false {
			t.Fatalf("工具请求必须是非流式: %#v", payload["stream"])
		}
		tools, ok := payload["tools"].([]any)
		if !ok || len(tools) != 4 {
			t.Fatalf("OpenAI tools = %#v", payload["tools"])
		}
		messages, ok := payload["messages"].([]any)
		if !ok {
			t.Fatalf("OpenAI messages = %#v", payload["messages"])
		}
		switch requestCount {
		case 1:
			if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" {
				t.Fatalf("首轮 messages = %#v", messages)
			}
			return petAITestResponse(http.StatusOK, "application/json", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-read","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"README.md\"}"}}]}}]}`), nil
		case 2:
			if len(messages) != 3 {
				t.Fatalf("续接 messages = %#v", messages)
			}
			assistant := messages[1].(map[string]any)
			if assistant["role"] != "assistant" {
				t.Fatalf("续接 assistant message = %#v", assistant)
			}
			assistantCalls := assistant["tool_calls"].([]any)
			assistantCall := assistantCalls[0].(map[string]any)
			if assistantCall["id"] != "call-read" || assistantCall["type"] != "function" {
				t.Fatalf("续接 assistant tool call = %#v", assistantCall)
			}
			toolMessage := messages[2].(map[string]any)
			if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call-read" || !strings.Contains(toolMessage["content"].(string), "工具结果第一行") {
				t.Fatalf("续接 tool result = %#v", toolMessage)
			}
			return petAITestResponse(http.StatusOK, "application/json", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"已读到工具结果"}}]}`), nil
		default:
			t.Fatalf("请求次数超出预期: %d", requestCount)
			return nil, nil
		}
	})
	service := NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader: reader,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return root, nil
		}),
		Transport: transport,
		Emitter:   emitter,
	})
	if _, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:         "pet-1",
		RequestID:     "tool-chat-1",
		Provider:      petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:      "读取 README",
		ProjectFolder: root,
	}); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := emitter.waitFor(t, PetAIEventCompleted)
	if completed.Text != "已读到工具结果" {
		t.Fatalf("completed text = %q", completed.Text)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
}

func TestPetAIToolResponsesNormalizeOpenAIAnthropicGemini(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		body     string
		wantText string
		wantID   string
	}{
		{
			name:     "openai",
			protocol: "openai",
			body:     `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"oa-1","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"README.md\"}"}}]}}]}`,
			wantID:   "oa-1",
		},
		{
			name:     "anthropic",
			protocol: "anthropic",
			body:     `{"content":[{"type":"text","text":"先查一下"},{"type":"tool_use","id":"an-1","name":"Read","input":{"file_path":"README.md"}}],"stop_reason":"tool_use"}`,
			wantText: "先查一下",
			wantID:   "an-1",
		},
		{
			name:     "gemini",
			protocol: "gemini",
			body:     `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"Read","args":{"file_path":"README.md"}}}]}}]}`,
			wantID:   "gemini_1",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			turn, err := parsePetAIAssistantTurn([]byte(testCase.body), testCase.protocol, http.StatusOK)
			if err != nil {
				t.Fatalf("parsePetAIAssistantTurn() error = %v", err)
			}
			if turn.Text != testCase.wantText || len(turn.ToolCalls) != 1 || turn.ToolCalls[0].ID != testCase.wantID || turn.ToolCalls[0].Name != PetAgentToolRead {
				t.Fatalf("normalized turn = %#v", turn)
			}
			var args map[string]string
			if err := json.Unmarshal(turn.ToolCalls[0].Arguments, &args); err != nil || args["file_path"] != "README.md" {
				t.Fatalf("normalized arguments = %s, error=%v", turn.ToolCalls[0].Arguments, err)
			}
		})
	}
}

func TestPetAIProjectFolderNormalizeRejectsUnsafeAndRelativePaths(t *testing.T) {
	valid := t.TempDir()
	base := PetChatRequest{
		PetID:     "pet-1",
		RequestID: "folder-1",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "查看项目",
	}
	service := &PetAIService{}
	input := base
	input.ProjectFolder = valid
	if normalized, err := service.normalizeChatRequest(input, PetCapabilityChat); err != nil || normalized.ProjectFolder != filepath.Clean(valid) {
		t.Fatalf("valid project folder = %#v, error=%v", normalized.ProjectFolder, err)
	}
	for _, folder := range []string{
		"relative/project",
		valid + "\nforged",
		valid + "\x00forged",
		strings.Repeat("x", PetAIMaxProjectFolderLength+1),
	} {
		input = base
		input.ProjectFolder = folder
		if _, err := service.normalizeChatRequest(input, PetCapabilityChat); PetAIErrorCodeOf(err) != string(PET_AI_INVALID_REQUEST) {
			t.Fatalf("unsafe project folder %q error = %v", folder, err)
		}
	}
}

func TestPetAIToolContinuationLimitProjectsSafeError(t *testing.T) {
	root := t.TempDir()
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	emitter := &petAITestEmitter{}
	requestCount := 0
	transport := petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return petAITestResponse(http.StatusOK, "application/json", `{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[{"id":"call-loop","type":"function","function":{"name":"LS","arguments":"{}"}}]}}]}`), nil
	})
	service := NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader: reader,
		WorkspaceResolver: PetWorkspaceResolverFunc(func(context.Context, string) (string, error) {
			return root, nil
		}),
		Transport: transport,
		Emitter:   emitter,
		Options:   PetAIOptions{MaxToolContinuationRounds: 1, MaxToolCalls: 1},
	})
	if _, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:         "pet-1",
		RequestID:     "tool-limit-1",
		Provider:      petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:      "持续调用工具",
		ProjectFolder: root,
	}); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	failed := emitter.waitFor(t, PetAIEventFailed)
	if failed.Error == nil || failed.Error.Code != string(PET_AI_RESPONSE_INVALID) {
		t.Fatalf("failed event = %#v", failed)
	}
	if requestCount != 2 {
		t.Fatalf("request count before safe stop = %d, want 2", requestCount)
	}
}

func ioReadAllRequest(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, os.ErrInvalid
	}
	return io.ReadAll(request.Body)
}
