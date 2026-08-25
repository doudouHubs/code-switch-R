package channels

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"codeswitch/services"
)

type channelToolTestProvider struct {
	mu      sync.Mutex
	sent    []string
	replied []string
	running bool
	groups  []ChannelGroup
	history []ChannelMessage
}

func (p *channelToolTestProvider) Start(context.Context) error {
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	return nil
}
func (p *channelToolTestProvider) Stop(context.Context) error {
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
	return nil
}
func (p *channelToolTestProvider) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}
func (p *channelToolTestProvider) SendMessage(_ context.Context, chatID, content string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, chatID+":"+content)
	return "provider-message-1", nil
}
func (p *channelToolTestProvider) ReplyMessage(_ context.Context, messageID, content string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replied = append(p.replied, messageID+":"+content)
	return "provider-reply-1", nil
}
func (p *channelToolTestProvider) GetGroupMessages(context.Context, string, int) ([]ChannelMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ChannelMessage(nil), p.history...), nil
}
func (p *channelToolTestProvider) ListGroups(context.Context) ([]ChannelGroup, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ChannelGroup(nil), p.groups...), nil
}
func (p *channelToolTestProvider) SendMedia(context.Context, string, ChannelMedia, string) (string, error) {
	return "provider-media-1", nil
}
func (p *channelToolTestProvider) SupportsStreaming() bool { return false }
func (p *channelToolTestProvider) SendStreamingMessage(context.Context, string, string, string) (StreamingHandle, error) {
	return nil, errors.New("streaming is not supported in test provider")
}

func newChannelToolFixture(t *testing.T) (*Store, *Manager, *channelAgentToolExecutor, ChannelInstance, ChannelSession, *channelToolTestProvider, *[]ChannelEvent) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "existing.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	projectID := "project-a"
	instance := ChannelInstance{
		ID:        "channel-a",
		Type:      "test-channel",
		Name:      "Test Channel",
		Enabled:   true,
		ProjectID: &projectID,
		Config:    map[string]string{},
		Tools:     map[string]bool{},
		Features:  defaultFeatures(),
		Permissions: ChannelPermissions{
			ReadablePathPrefixes: []string{},
		},
	}
	if err := store.UpsertInstance(instance); err != nil {
		t.Fatal(err)
	}
	session := ChannelSession{ID: "session-a", InstanceID: instance.ID, ChatID: "chat-a", ProjectID: projectID, WorkingFolder: workspace}
	if err := store.UpsertSession(session); err != nil {
		t.Fatal(err)
	}

	var events []ChannelEvent
	manager := NewManager(store, func(event ChannelEvent) { events = append(events, event) })
	provider := &channelToolTestProvider{}
	manager.RegisterFactory(instance.Type, func(ChannelInstance, EventSink) (ChannelProvider, error) { return provider, nil })
	if err := manager.Start(context.Background(), instance.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.StopAll(context.Background()) })

	executor, err := newChannelAgentToolExecutor(store, manager, func(event ChannelEvent) { events = append(events, event) }, instance, session.ID, session.ChatID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	return store, manager, executor, instance, session, provider, &events
}

func channelToolCall(id string, name services.PetAgentToolName, arguments string) services.PetAgentToolCall {
	return services.PetAgentToolCall{ID: id, Name: name, Arguments: json.RawMessage(arguments)}
}

func channelToolErrorCode(t *testing.T, result services.PetAgentToolResult) string {
	t.Helper()
	var payload services.PetAgentToolError
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("tool result is not an error payload: %q (%v)", result.Content, err)
	}
	return payload.Code
}

func TestChannelAgentToolsIsolateInstanceAndToolFlags(t *testing.T) {
	_, _, executor, instance, _, provider, _ := newChannelToolFixture(t)
	result, err := executor.Execute(context.Background(), channelToolCall("cross-instance", channelToolSendMessage, `{"plugin_id":"channel-b","chat_id":"chat-b","content":"nope"}`))
	if err != nil || !result.IsError || channelToolErrorCode(t, result) != services.PetAgentToolErrorExecution {
		t.Fatalf("cross-instance send result = %#v, err=%v", result, err)
	}
	provider.mu.Lock()
	if len(provider.sent) != 0 {
		t.Fatalf("cross-instance send reached provider: %#v", provider.sent)
	}
	provider.mu.Unlock()

	instance.Tools[string(channelToolRead)] = false
	result, err = executor.storeAndExecuteInstance(instance, channelToolCall("disabled-read", channelToolRead, `{"file_path":"existing.txt"}`))
	if err != nil || !result.IsError || channelToolErrorCode(t, result) != services.PetAgentToolErrorExecution {
		t.Fatalf("disabled tool result = %#v, err=%v", result, err)
	}
}

func (e *channelAgentToolExecutor) storeAndExecuteInstance(instance ChannelInstance, call services.PetAgentToolCall) (services.PetAgentToolResult, error) {
	if e.store != nil {
		if err := e.store.UpsertInstance(instance); err != nil {
			return services.PetAgentToolResult{}, err
		}
	}
	return e.Execute(context.Background(), call)
}

func TestChannelAgentToolsEnforceReadRootsAndWriteSnapshots(t *testing.T) {
	store, _, executor, instance, _, _, _ := newChannelToolFixture(t)
	workspace := executor.workspaceRoot

	result, err := executor.Execute(context.Background(), channelToolCall("write-before-read", channelToolWrite, `{"file_path":"existing.txt","content":"blocked"}`))
	if err != nil || !result.IsError || channelToolErrorCode(t, result) != services.PetAgentToolErrorExecution {
		t.Fatalf("write-before-read result = %#v, err=%v", result, err)
	}

	result, err = executor.Execute(context.Background(), channelToolCall("read-existing", channelToolRead, `{"file_path":"existing.txt"}`))
	if err != nil || result.IsError {
		t.Fatalf("read-existing result = %#v, err=%v", result, err)
	}
	result, err = executor.Execute(context.Background(), channelToolCall("write-after-read", channelToolWrite, `{"file_path":"existing.txt","content":"after-read"}`))
	if err != nil || result.IsError {
		t.Fatalf("write-after-read result = %#v, err=%v", result, err)
	}
	data, readErr := os.ReadFile(filepath.Join(workspace, "existing.txt"))
	if readErr != nil || string(data) != "after-read" {
		t.Fatalf("written file = %q, err=%v", data, readErr)
	}

	editPath := filepath.Join(workspace, "edit.txt")
	if err := os.WriteFile(editPath, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Execute(context.Background(), channelToolCall("read-edit", channelToolRead, `{"file_path":"edit.txt"}`))
	if err != nil || result.IsError {
		t.Fatalf("read-edit result = %#v, err=%v", result, err)
	}
	if err := os.WriteFile(editPath, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Execute(context.Background(), channelToolCall("edit-stale", channelToolEdit, `{"file_path":"edit.txt","old_string":"one","new_string":"two"}`))
	if err != nil || !result.IsError || channelToolErrorCode(t, result) != services.PetAgentToolErrorExecution {
		t.Fatalf("edit-stale result = %#v, err=%v", result, err)
	}

	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "readable.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Execute(context.Background(), channelToolCall("read-outside", channelToolRead, `{"file_path":"`+jsonPath(outsidePath)+`"}`))
	if err != nil || !result.IsError || channelToolErrorCode(t, result) != services.PetAgentToolErrorPathOutsideRoot {
		t.Fatalf("read outside result = %#v, err=%v", result, err)
	}
	instance.Permissions.ReadablePathPrefixes = []string{outsideDir}
	if err := store.UpsertInstance(instance); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Execute(context.Background(), channelToolCall("read-whitelisted", channelToolRead, `{"file_path":"`+jsonPath(outsidePath)+`"}`))
	if err != nil || result.IsError {
		t.Fatalf("read whitelisted result = %#v, err=%v", result, err)
	}
}

func TestChannelAgentToolsEnforceHomeAndShellPermissions(t *testing.T) {
	store, _, executor, instance, _, _, _ := newChannelToolFixture(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("home directory is unavailable: %v", err)
	}
	homeDir, err := os.MkdirTemp(home, "codeswitch-channel-home-")
	if err != nil {
		t.Skipf("cannot create temporary home fixture: %v", err)
	}
	defer os.RemoveAll(homeDir)
	homePath := filepath.Join(homeDir, "home.txt")
	if err := os.WriteFile(homePath, []byte("home"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), channelToolCall("home-denied", channelToolRead, `{"file_path":"`+jsonPath(homePath)+`"}`))
	if err != nil || !result.IsError || channelToolErrorCode(t, result) != services.PetAgentToolErrorPathOutsideRoot {
		t.Fatalf("home denied result = %#v, err=%v", result, err)
	}
	instance.Permissions.AllowReadHome = true
	if err := store.UpsertInstance(instance); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Execute(context.Background(), channelToolCall("home-allowed", channelToolRead, `{"file_path":"`+jsonPath(homePath)+`"}`))
	if err != nil || result.IsError {
		t.Fatalf("home allowed result = %#v, err=%v", result, err)
	}

	result, err = executor.Execute(context.Background(), channelToolCall("shell-denied", channelToolBash, `{"command":"echo denied"}`))
	if err != nil || !result.IsError || channelToolErrorCode(t, result) != services.PetAgentToolErrorExecution {
		t.Fatalf("shell denied result = %#v, err=%v", result, err)
	}
	instance.Permissions.AllowShell = true
	if err := store.UpsertInstance(instance); err != nil {
		t.Fatal(err)
	}
	result, err = executor.Execute(context.Background(), channelToolCall("shell-allowed", channelToolBash, `{"command":"echo allowed"}`))
	if err != nil || result.IsError || !strings.Contains(result.Content, "allowed") {
		t.Fatalf("shell allowed result = %#v, err=%v", result, err)
	}
}

func TestChannelPluginMessageToolPersistsAndPublishes(t *testing.T) {
	store, _, executor, instance, session, provider, events := newChannelToolFixture(t)
	result, err := executor.Execute(context.Background(), channelToolCall("send", channelToolSendMessage, `{"plugin_id":"channel-a","chat_id":"chat-a","content":"hello from tool"}`))
	if err != nil || result.IsError {
		t.Fatalf("plugin send result = %#v, err=%v", result, err)
	}
	provider.mu.Lock()
	if len(provider.sent) != 1 || provider.sent[0] != "chat-a:hello from tool" {
		t.Fatalf("provider sent = %#v", provider.sent)
	}
	provider.mu.Unlock()
	messages, err := store.ListMessages(session.ID, 20)
	if err != nil || len(messages) != 1 {
		t.Fatalf("persisted messages = %#v, err=%v", messages, err)
	}
	if messages[0].Role != "assistant" || messages[0].ExternalID != "provider-message-1" || messages[0].Content != "hello from tool" {
		t.Fatalf("persisted outbound message = %#v", messages[0])
	}
	foundEvent := false
	for _, event := range *events {
		if event.Type == "message" && event.InstanceID == instance.ID {
			foundEvent = true
			break
		}
	}
	if !foundEvent {
		t.Fatalf("outbound message event was not published: %#v", *events)
	}
}

func jsonPath(path string) string {
	data, _ := json.Marshal(path)
	return strings.Trim(string(data), `"`)
}
