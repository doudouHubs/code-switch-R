package channels

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeswitch/services"
)

type channelChatRuntimeStub struct {
	mu       sync.Mutex
	requests []services.PetChatRequest
	startErr error
	closed   bool
}

func (s *channelChatRuntimeStub) StartChat(_ context.Context, request services.PetChatRequest) (services.PetChatStartResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	err := s.startErr
	s.mu.Unlock()
	if err != nil {
		return services.PetChatStartResult{}, err
	}
	return services.PetChatStartResult{RequestID: request.RequestID}, nil
}

func (s *channelChatRuntimeStub) CancelChat(string) error { return nil }

func (s *channelChatRuntimeStub) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func newChannelRuntimeFixture(t *testing.T, chatRuntime services.PetChatRuntime) (*Store, *Manager, ChannelInstance) {
	t.Helper()
	workspace := t.TempDir()
	projectID := workspace
	instance := ChannelInstance{
		ID:          "channel-runtime",
		Type:        "test-channel",
		Name:        "Runtime Channel",
		Enabled:     true,
		ProjectID:   &projectID,
		Config:      map[string]string{"systemPrompt": "channel persona"},
		Features:    defaultFeatures(),
		Permissions: defaultPermissions(),
	}
	store, err := OpenStore(t.TempDir() + "/channels.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertInstance(instance); err != nil {
		store.Close()
		t.Fatalf("seed channel: %v", err)
	}
	provider := &channelToolTestProvider{}
	manager := NewManager(store, nil)
	manager.RegisterFactory(instance.Type, func(ChannelInstance, EventSink) (ChannelProvider, error) {
		return provider, nil
	})
	if err := manager.Start(context.Background(), instance.ID); err != nil {
		store.Close()
		t.Fatalf("start channel: %v", err)
	}
	return store, manager, instance
}

func TestAgentRuntimeRoutesIncomingMessageToSharedProjectCodexRuntime(t *testing.T) {
	chatRuntime := &channelChatRuntimeStub{}
	store, manager, instance := newChannelRuntimeFixture(t, chatRuntime)
	defer store.Close()
	defer manager.Stop(context.Background(), instance.ID)

	runtime := NewAgentRuntime(store, manager, nil, nil, AgentRuntimeOptions{ChatRuntime: chatRuntime})
	runtime.handleIncoming(ChannelMessage{
		InstanceID: instance.ID,
		ExternalID: "incoming-codex",
		Role:       "user",
		ChatID:     "chat-a",
		Content:    "通过 Agent 管家处理",
		Timestamp:  nowMillis(),
	})

	chatRuntime.mu.Lock()
	defer chatRuntime.mu.Unlock()
	if len(chatRuntime.requests) != 1 {
		t.Fatalf("Codex runtime requests = %#v", chatRuntime.requests)
	}
	request := chatRuntime.requests[0]
	if request.UserText != "通过 Agent 管家处理" || request.ProjectID != *instance.ProjectID || request.Persona != "" {
		t.Fatalf("Codex chat request = %#v", request)
	}
	if request.Provider.Platform != "" || request.Provider.ProviderID != "" || request.Provider.Model != "" {
		t.Fatalf("channel request unexpectedly contains provider override: %#v", request.Provider)
	}
	if len(request.History) != 0 {
		t.Fatalf("channel request unexpectedly injects database history: %#v", request.History)
	}
	wantProjectScope := services.PetCodexProjectToolScope(*instance.ProjectID)
	if request.ToolScope != wantProjectScope {
		t.Fatalf("channel tool scope = %q, want project scope %q", request.ToolScope, wantProjectScope)
	}
	wantExecutionScope := channelToolScope(instance.ID, sessionKey(instance.ID, "chat-a"), "chat-a")
	if request.ToolExecutionScope != wantExecutionScope {
		t.Fatalf("channel tool execution scope = %q, want %q", request.ToolExecutionScope, wantExecutionScope)
	}
}

func TestAgentRuntimeSendsFailureWhenCodexRuntimeCannotStart(t *testing.T) {
	chatRuntime := &channelChatRuntimeStub{startErr: errors.New("codex app-server unavailable")}
	store, manager, instance := newChannelRuntimeFixture(t, chatRuntime)
	defer store.Close()
	defer manager.Stop(context.Background(), instance.ID)

	runtime := NewAgentRuntime(store, manager, nil, nil, AgentRuntimeOptions{ChatRuntime: chatRuntime})
	runtime.handleIncoming(ChannelMessage{
		InstanceID: instance.ID,
		ExternalID: "incoming-failure",
		Role:       "user",
		ChatID:     "chat-a",
		Content:    "hello",
		Timestamp:  nowMillis(),
	})

	provider, err := manager.provider(instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	testProvider, ok := provider.(*channelToolTestProvider)
	if !ok {
		t.Fatalf("provider type = %T", provider)
	}
	testProvider.mu.Lock()
	defer testProvider.mu.Unlock()
	if len(testProvider.sent) != 1 || !strings.Contains(testProvider.sent[0], "上游错误") || strings.Contains(testProvider.sent[0], "默认模型") {
		t.Fatalf("failure message = %#v", testProvider.sent)
	}
}

func TestChannelFailureMessageUsesPetAIErrorCode(t *testing.T) {
	tests := []struct {
		name string
		code services.PetAIErrorCode
		want string
	}{
		{name: "dependency", code: services.PET_AI_DEPENDENCY_UNAVAILABLE, want: "Codex CLI 当前不可用"},
		{name: "timeout", code: services.PET_AI_TIMEOUT, want: "响应超时"},
		{name: "queue full", code: services.PET_AI_QUEUE_FULL, want: "消息队列已满"},
		{name: "invalid request", code: services.PET_AI_INVALID_REQUEST, want: "请求无效"},
		{name: "upstream", code: services.PET_AI_UPSTREAM_ERROR, want: "上游错误"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := channelFailureMessage(string(test.code))
			if !strings.Contains(message, test.want) {
				t.Fatalf("message = %q, want substring %q", message, test.want)
			}
			if strings.Contains(message, "未配置") || strings.Contains(message, "默认模型") {
				t.Fatalf("message contains misleading configuration hint: %q", message)
			}
		})
	}
	if message := channelFailureMessage(string(services.PET_AI_REQUEST_CANCELLED)); message != "" {
		t.Fatalf("cancelled message = %q, want empty", message)
	}
}

func TestAgentRuntimeReportsPersonaFailureFromSharedHub(t *testing.T) {
	chatRuntime := &channelChatRuntimeStub{}
	store, manager, instance := newChannelRuntimeFixture(t, chatRuntime)
	defer store.Close()
	defer manager.Stop(context.Background(), instance.ID)

	var runtime *AgentRuntime
	hub := services.NewAgentConversationHub(chatRuntime, services.AgentConversationHubOptions{
		PersonaResolver: services.AgentConversationPersonaResolverFunc(func(context.Context, string, string) (string, error) {
			return "", errors.New("persona storage is temporarily unavailable")
		}),
		Emitter: services.PetAIEventEmitterFunc(func(event services.PetAIEvent) error {
			if runtime == nil {
				return nil
			}
			return runtime.Emit(event)
		}),
	})
	defer hub.Close()
	runtime = NewAgentRuntime(store, manager, nil, nil, AgentRuntimeOptions{
		ChatRuntime:       hub,
		SharedChatRuntime: true,
	})
	runtime.handleIncoming(ChannelMessage{
		InstanceID: instance.ID,
		ExternalID: "incoming-persona-failure",
		Role:       "user",
		ChatID:     "chat-a",
		Content:    "hello",
		Timestamp:  nowMillis(),
	})

	provider, err := manager.provider(instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	testProvider, ok := provider.(*channelToolTestProvider)
	if !ok {
		t.Fatalf("provider type = %T", provider)
	}
	deadline := time.After(2 * time.Second)
	for {
		testProvider.mu.Lock()
		messages := append([]string(nil), testProvider.sent...)
		testProvider.mu.Unlock()
		if len(messages) > 0 {
			if len(messages) != 1 || !strings.Contains(messages[0], "Codex CLI 当前不可用") || strings.Contains(messages[0], "默认模型") || strings.Contains(messages[0], "未配置") {
				t.Fatalf("persona failure message = %#v", messages)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("等待共享 Hub 人格失败回传超时")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestAgentRuntimeCloseDelegatesToDedicatedCodexRuntime(t *testing.T) {
	chatRuntime := &channelChatRuntimeStub{}
	runtime := NewAgentRuntime(nil, nil, nil, nil, AgentRuntimeOptions{ChatRuntime: chatRuntime})
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	chatRuntime.mu.Lock()
	defer chatRuntime.mu.Unlock()
	if !chatRuntime.closed {
		t.Fatal("Close() did not close the dedicated Codex runtime")
	}
}

func TestAgentRuntimeDoesNotCloseSharedCodexRuntime(t *testing.T) {
	chatRuntime := &channelChatRuntimeStub{}
	runtime := NewAgentRuntime(nil, nil, nil, nil, AgentRuntimeOptions{
		ChatRuntime:       chatRuntime,
		SharedChatRuntime: true,
	})
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	chatRuntime.mu.Lock()
	defer chatRuntime.mu.Unlock()
	if chatRuntime.closed {
		t.Fatal("shared Codex runtime must be closed by the application owner")
	}
}

func TestAgentRuntimeBroadcastProjectDeliversOriginalAndConfiguredTargetOnce(t *testing.T) {
	workspace := t.TempDir()
	projectID := workspace
	store, err := OpenStore(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sourceProvider := &channelToolTestProvider{}
	targetProvider := &channelToolTestProvider{}
	source := ChannelInstance{
		ID:          "channel-broadcast-source",
		Type:        "test-broadcast-source",
		Name:        "Broadcast Source",
		Enabled:     true,
		ProjectID:   &projectID,
		Features:    ChannelFeatures{AutoReply: true, AutoStart: false},
		Permissions: defaultPermissions(),
		Config:      map[string]string{},
	}
	target := ChannelInstance{
		ID:          "channel-broadcast-target",
		Type:        "test-broadcast-target",
		Name:        "Broadcast Target",
		Enabled:     true,
		ProjectID:   &projectID,
		Features:    ChannelFeatures{AutoReply: true, AutoStart: false},
		Permissions: defaultPermissions(),
		Config: map[string]string{
			"broadcastChatId": "broadcast-chat",
		},
	}
	if err := store.UpsertInstance(source); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertInstance(target); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, nil)
	manager.RegisterFactory(source.Type, func(ChannelInstance, EventSink) (ChannelProvider, error) {
		return sourceProvider, nil
	})
	manager.RegisterFactory(target.Type, func(ChannelInstance, EventSink) (ChannelProvider, error) {
		return targetProvider, nil
	})
	if err := manager.Start(context.Background(), source.ID); err != nil {
		t.Fatalf("启动源频道失败: %v", err)
	}
	if err := manager.Start(context.Background(), target.ID); err != nil {
		t.Fatalf("启动目标频道失败: %v", err)
	}
	defer manager.Stop(context.Background(), source.ID)
	defer manager.Stop(context.Background(), target.ID)

	chatRuntime := &channelChatRuntimeStub{}
	runtime := NewAgentRuntime(store, manager, nil, nil, AgentRuntimeOptions{ChatRuntime: chatRuntime})
	runtime.handleIncoming(ChannelMessage{
		InstanceID: source.ID,
		ExternalID: "source-message",
		Role:       "user",
		ChatID:     "source-chat",
		Content:    "source question",
		Timestamp:  nowMillis(),
	})
	chatRuntime.mu.Lock()
	if len(chatRuntime.requests) != 1 {
		chatRuntime.mu.Unlock()
		t.Fatalf("shared runtime requests = %#v", chatRuntime.requests)
	}
	requestID := chatRuntime.requests[0].RequestID
	chatRuntime.mu.Unlock()
	results := runtime.BroadcastProject(context.Background(), projectID, "agent answer", requestID)
	if len(results) != 2 {
		t.Fatalf("broadcast results = %#v, want original plus one configured target", results)
	}

	sourceProvider.mu.Lock()
	if len(sourceProvider.replied) != 1 || sourceProvider.replied[0] != "source-message:agent answer" || len(sourceProvider.sent) != 0 {
		t.Fatalf("source provider delivery = replied:%#v sent:%#v", sourceProvider.replied, sourceProvider.sent)
	}
	sourceProvider.mu.Unlock()
	targetProvider.mu.Lock()
	defer targetProvider.mu.Unlock()
	if len(targetProvider.sent) != 1 || targetProvider.sent[0] != "broadcast-chat:agent answer" || len(targetProvider.replied) != 0 {
		t.Fatalf("target provider delivery = sent:%#v replied:%#v", targetProvider.sent, targetProvider.replied)
	}
}
