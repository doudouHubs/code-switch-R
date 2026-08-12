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

func TestPetAIWorkspaceResolverIgnoresForgedProjectFolder(t *testing.T) {
	dao := NewPetDAO(newPetMigrationTestDB(t))
	boundRoot := petWorkspaceRoot(t, "bound-root-secret")
	forgedRoot := petWorkspaceRoot(t, "forged-root-secret")
	petID := "workspace-pet"
	if err := savePetWorkspace(t, dao, petID, boundRoot); err != nil {
		t.Fatal(err)
	}

	var continuationToolContent string
	transport := petWorkspaceToolTransport(t, func(content string) {
		continuationToolContent = content
	})
	emitter := &petAITestEmitter{}
	service := newPetAIWorkspaceTestService(dao, transport, emitter)

	if _, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:     petID,
		RequestID: "workspace-forged-folder",
		Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:  "读取 marker.txt",
		// 这个值模拟前端篡改；工具根目录必须来自 petID 对应的持久化绑定。
		ProjectFolder: forgedRoot,
	}); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := emitter.waitFor(t, PetAIEventCompleted)
	if completed.Text != "workspace-ok" {
		t.Fatalf("completed text = %q", completed.Text)
	}
	if !strings.Contains(continuationToolContent, "bound-root-secret") || strings.Contains(continuationToolContent, "forged-root-secret") {
		t.Fatalf("tool result used forged workspace: %q", continuationToolContent)
	}
}

func TestPetAIWorkspaceResolverUnboundPetDoesNotEnableToolsFromRequest(t *testing.T) {
	dao := NewPetDAO(newPetMigrationTestDB(t))
	forgedRoot := petWorkspaceRoot(t, "forged-unbound-secret")
	reader := &petAITestProviderReader{config: petAITestConfig("openai", "openai", "gpt-pet")}
	emitter := &petAITestEmitter{}
	requestHadTools := false
	transport := petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		if _, ok := payload["tools"]; ok {
			requestHadTools = true
		}
		return petAITestResponse(http.StatusOK, "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"unbound-ok\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"), nil
	})
	service := newPetAIWorkspaceTestServiceWithReader(dao, reader, transport, emitter)

	if _, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:         "unbound-pet",
		RequestID:     "workspace-unbound",
		Provider:      petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:      "普通聊天",
		ProjectFolder: forgedRoot,
	}); err != nil {
		t.Fatalf("StartChat() error = %v", err)
	}
	completed := emitter.waitFor(t, PetAIEventCompleted)
	if completed.Text != "unbound-ok" || requestHadTools {
		t.Fatalf("unbound workspace enabled tools: text=%q tools=%v", completed.Text, requestHadTools)
	}
}

func TestPetAIWorkspaceResolverRejectsMissingBoundDirectory(t *testing.T) {
	dao := NewPetDAO(newPetMigrationTestDB(t))
	missingRoot := filepath.Join(t.TempDir(), "does-not-exist")
	if err := savePetWorkspace(t, dao, "missing-pet", missingRoot); err != nil {
		t.Fatal(err)
	}

	emitter := &petAITestEmitter{}
	service := newPetAIWorkspaceTestService(dao, petAITestRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("missing workspace must fail before HTTP")
		return nil, nil
	}), emitter)
	_, err := service.StartChat(context.Background(), PetChatRequest{
		PetID:         "missing-pet",
		RequestID:     "workspace-missing",
		Provider:      petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
		UserText:      "读取项目",
		ProjectFolder: missingRoot,
	})
	if err == nil {
		failed := emitter.waitFor(t, PetAIEventFailed)
		if failed.Error == nil || failed.Error.Code != string(PET_AI_INVALID_REQUEST) {
			t.Fatalf("missing workspace failed event = %#v", failed)
		}
		return
	}
	if petAIErrorCodeForWorkspaceTest(err) != string(PET_AI_INVALID_REQUEST) {
		t.Fatalf("missing workspace error = %v, want %s", err, PET_AI_INVALID_REQUEST)
	}
}

func TestPetAIWorkspaceResolverIsolatesMultiplePets(t *testing.T) {
	dao := NewPetDAO(newPetMigrationTestDB(t))
	rootA := petWorkspaceRoot(t, "pet-a-secret")
	rootB := petWorkspaceRoot(t, "pet-b-secret")
	if err := savePetWorkspace(t, dao, "pet-a", rootA); err != nil {
		t.Fatal(err)
	}
	if err := savePetWorkspace(t, dao, "pet-b", rootB); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		petID string
		want  string
	}{
		{petID: "pet-a", want: "pet-a-secret"},
		{petID: "pet-b", want: "pet-b-secret"},
	} {
		t.Run(testCase.petID, func(t *testing.T) {
			var toolContent string
			emitter := &petAITestEmitter{}
			service := newPetAIWorkspaceTestService(dao, petWorkspaceToolTransport(t, func(content string) {
				toolContent = content
			}), emitter)
			if _, err := service.StartChat(context.Background(), PetChatRequest{
				PetID:     testCase.petID,
				RequestID: "workspace-isolation-" + testCase.petID,
				Provider:  petAITestReference("openai", "pet-provider", "gpt-pet", PetCapabilityChat),
				UserText:  "读取 marker.txt",
				// 两个宠物都收到同一个伪造路径也不能跨宠物读取绑定目录。
				ProjectFolder: filepath.Dir(rootA),
			}); err != nil {
				t.Fatalf("StartChat() error = %v", err)
			}
			if completed := emitter.waitFor(t, PetAIEventCompleted); completed.Text != "workspace-ok" {
				t.Fatalf("completed text = %q", completed.Text)
			}
			if !strings.Contains(toolContent, testCase.want) {
				t.Fatalf("pet %q resolved wrong workspace: %q", testCase.petID, toolContent)
			}
		})
	}
}

func newPetAIWorkspaceTestService(dao *PetDAO, transport PetAIHTTPTransport, emitter PetAIEventEmitter) *PetAIService {
	return newPetAIWorkspaceTestServiceWithReader(dao, &petAITestProviderReader{
		config: petAITestConfig("openai", "openai", "gpt-pet"),
	}, transport, emitter)
}

func newPetAIWorkspaceTestServiceWithReader(dao *PetDAO, reader PetAIProviderReader, transport PetAIHTTPTransport, emitter PetAIEventEmitter) *PetAIService {
	return NewPetAIServiceWithDependencies(PetAIDependencies{
		ProviderReader:    reader,
		Transport:         transport,
		Emitter:           emitter,
		WorkspaceResolver: petWorkspaceResolverForDAO(dao),
	})
}

func petWorkspaceResolverForDAO(dao *PetDAO) PetWorkspaceResolver {
	return PetWorkspaceResolverFunc(func(ctx context.Context, petID string) (string, error) {
		agent, err := dao.LoadAgent(ctx, petID)
		if err != nil {
			return "", err
		}
		if agent == nil || agent.ProjectFolder == nil {
			return "", nil
		}
		return strings.TrimSpace(*agent.ProjectFolder), nil
	})
}

func savePetWorkspace(t *testing.T, dao *PetDAO, petID, root string) error {
	t.Helper()
	return dao.SaveAgent(context.Background(), PetAgentConfig{
		PetID:         petID,
		ProjectFolder: petWorkspaceStringPtr(root),
	})
}

func petWorkspaceStringPtr(value string) *string {
	return &value
}

func petWorkspaceRoot(t *testing.T, marker string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func petWorkspaceToolTransport(t *testing.T, capture func(string)) PetAIHTTPTransport {
	t.Helper()
	requestCount := 0
	return petAITestRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		if requestCount == 1 {
			return petAITestResponse(http.StatusOK, "application/json", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"workspace-read","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"marker.txt\"}"}}]}}]}`), nil
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) < 3 {
			t.Fatalf("continuation messages = %#v", payload["messages"])
		}
		toolMessage, ok := messages[len(messages)-1].(map[string]any)
		if !ok || toolMessage["role"] != "tool" {
			t.Fatalf("continuation tool message = %#v", messages[len(messages)-1])
		}
		if capture != nil {
			capture(toolMessage["content"].(string))
		}
		return petAITestResponse(http.StatusOK, "application/json", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"workspace-ok"}}]}`), nil
	})
}

func petAIErrorCodeForWorkspaceTest(err error) string {
	if err == nil {
		return ""
	}
	return PetAIErrorCodeOf(err)
}
