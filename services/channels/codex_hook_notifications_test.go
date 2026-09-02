package channels

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"codeswitch/services"
)

func newCodexHookDeliveryFixture(t *testing.T, instance ChannelInstance) (*Store, *Manager, *AgentRuntime, *channelToolTestProvider) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertInstance(instance); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	provider := &channelToolTestProvider{}
	manager := NewManager(store, nil)
	manager.RegisterFactory(instance.Type, func(ChannelInstance, EventSink) (ChannelProvider, error) {
		return provider, nil
	})
	if err := manager.Start(context.Background(), instance.ID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	runtime := NewAgentRuntime(store, manager, nil, nil)
	t.Cleanup(func() {
		_ = manager.Stop(context.Background(), instance.ID)
		_ = store.Close()
	})
	return store, manager, runtime, provider
}

func TestDeliverCodexHookNotificationTargetsExactChannelAndDeduplicates(t *testing.T) {
	workspace := t.TempDir()
	projectID := workspace
	instance := ChannelInstance{
		ID:          "hook-channel-direct",
		Type:        "hook-test-direct",
		Name:        "Hook Direct",
		Enabled:     true,
		ProjectID:   &projectID,
		Config:      map[string]string{"broadcastChatId": "wrong-broadcast-chat"},
		Features:    defaultFeatures(),
		Permissions: defaultPermissions(),
	}
	store, _, runtime, provider := newCodexHookDeliveryFixture(t, instance)
	notification := services.CodexHookNotification{
		EventID:           "hook-direct-1",
		Event:             services.CodexHookNotificationWaitingApproval,
		HookEventName:     "PermissionRequest",
		ProjectID:         projectID,
		ProjectPath:       workspace,
		ProjectName:       "CodeSwitch",
		SessionID:         "thread-direct",
		SessionName:       "频道会话",
		ThreadID:          "thread-direct",
		TurnID:            "turn-direct",
		OccurredAt:        1710000000000,
		ToolName:          "shell",
		Reason:            "需要执行命令",
		Source:            services.AgentConversationSourceChannel,
		ChannelInstanceID: instance.ID,
		ChannelChatID:     "exact-chat",
	}
	if err := runtime.DeliverCodexHookNotification(context.Background(), notification); err != nil {
		t.Fatalf("direct Hook delivery failed: %v", err)
	}
	if err := runtime.DeliverCodexHookNotification(context.Background(), notification); err != nil {
		t.Fatalf("duplicate Hook delivery failed: %v", err)
	}

	provider.mu.Lock()
	sent := append([]string(nil), provider.sent...)
	provider.mu.Unlock()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "exact-chat:") || !strings.Contains(sent[0], "等待授权") || !strings.Contains(sent[0], "CodeSwitch") || !strings.Contains(sent[0], "频道会话") {
		t.Fatalf("direct provider messages = %#v", sent)
	}
	session, found, err := store.GetSession(instance.ID, "exact-chat")
	if err != nil || !found {
		t.Fatalf("Hook session = %#v, found=%t, err=%v", session, found, err)
	}
	messages, err := store.ListMessages(session.ID, 10)
	if err != nil || len(messages) != 1 || messages[0].ExternalID != notification.EventID {
		t.Fatalf("Hook history = %#v, err=%v", messages, err)
	}
}

func TestDeliverCodexHookNotificationBroadcastsExternalProjectHook(t *testing.T) {
	workspace := t.TempDir()
	projectID := workspace
	instance := ChannelInstance{
		ID:          "hook-channel-broadcast",
		Type:        "hook-test-broadcast",
		Name:        "Hook Broadcast",
		Enabled:     true,
		ProjectID:   &projectID,
		Config:      map[string]string{"broadcastChatId": "broadcast-chat"},
		Features:    defaultFeatures(),
		Permissions: defaultPermissions(),
	}
	_, _, runtime, provider := newCodexHookDeliveryFixture(t, instance)
	notification := services.CodexHookNotification{
		EventID:       "hook-broadcast-1",
		Event:         services.CodexHookNotificationSessionEnded,
		HookEventName: "SessionEnd",
		ProjectID:     projectID,
		ProjectPath:   workspace,
		ProjectName:   "Broadcast Project",
		SessionID:     "external-session",
		SessionName:   "外部 Codex",
		OccurredAt:    1710000000000,
		Managed:       false,
	}
	if err := runtime.DeliverCodexHookNotification(context.Background(), notification); err != nil {
		t.Fatalf("broadcast Hook delivery failed: %v", err)
	}
	provider.mu.Lock()
	sent := append([]string(nil), provider.sent...)
	provider.mu.Unlock()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "broadcast-chat:") || !strings.Contains(sent[0], "会话结束") {
		t.Fatalf("broadcast provider messages = %#v", sent)
	}
}

func TestDeliverCodexHookNotificationUsesLatestProjectSessionWithoutBroadcastConfig(t *testing.T) {
	workspace := t.TempDir()
	projectID := workspace
	instance := ChannelInstance{
		ID:          "hook-channel-session-fallback",
		Type:        "hook-test-session-fallback",
		Name:        "Hook Session Fallback",
		Enabled:     true,
		ProjectID:   &projectID,
		Config:      map[string]string{},
		Features:    defaultFeatures(),
		Permissions: defaultPermissions(),
	}
	store, _, runtime, provider := newCodexHookDeliveryFixture(t, instance)
	if err := store.UpsertSession(ChannelSession{
		InstanceID:    instance.ID,
		ChatID:        "known-chat",
		ProjectID:     projectID,
		WorkingFolder: workspace,
		UpdatedAt:     20,
	}); err != nil {
		t.Fatal(err)
	}
	session, found, err := store.GetSession(instance.ID, "known-chat")
	if err != nil || !found {
		t.Fatalf("session fallback session = %#v, found=%t, err=%v", session, found, err)
	}
	if err := store.AppendMessage(ChannelMessage{
		InstanceID: instance.ID,
		SessionID:  session.ID,
		ExternalID: "inbound-context-1",
		Role:       "user",
		ChatID:     "known-chat",
		Content:    "previous message",
		Timestamp:  10,
		Raw:        `{"context_token":"persisted-context"}`,
	}); err != nil {
		t.Fatal(err)
	}

	notification := services.CodexHookNotification{
		EventID:       "hook-session-fallback-1",
		Event:         services.CodexHookNotificationSystemError,
		HookEventName: "PostToolUse",
		ProjectID:     projectID,
		ProjectPath:   workspace,
		ProjectName:   "Fallback Project",
		SessionID:     "external-session",
		SessionName:   "外部 Codex",
		OccurredAt:    1710000000000,
		Error:         "command failed",
	}
	if err := runtime.DeliverCodexHookNotification(context.Background(), notification); err != nil {
		t.Fatalf("session fallback Hook delivery failed: %v", err)
	}

	provider.mu.Lock()
	sent := append([]string(nil), provider.sent...)
	provider.mu.Unlock()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "known-chat:") || !strings.Contains(sent[0], "系统错误") {
		t.Fatalf("session fallback provider messages = %#v", sent)
	}
	provider.mu.Lock()
	restored := append([]ChannelMessage(nil), provider.restored...)
	provider.mu.Unlock()
	if len(restored) != 1 || restored[0].ExternalID != "inbound-context-1" {
		t.Fatalf("restored provider context messages = %#v", restored)
	}
}

func TestDeliverCodexHookNotificationDropsManagerAndUnmatchedManagedHook(t *testing.T) {
	workspace := t.TempDir()
	projectID := workspace
	instance := ChannelInstance{
		ID:          "hook-channel-filter",
		Type:        "hook-test-filter",
		Name:        "Hook Filter",
		Enabled:     true,
		ProjectID:   &projectID,
		Config:      map[string]string{"broadcastChatId": "broadcast-chat"},
		Features:    defaultFeatures(),
		Permissions: defaultPermissions(),
	}
	_, _, runtime, provider := newCodexHookDeliveryFixture(t, instance)
	base := services.CodexHookNotification{
		EventID:       "hook-filter-manager",
		Event:         services.CodexHookNotificationWaitingApproval,
		HookEventName: "PermissionRequest",
		ProjectID:     projectID,
		ProjectPath:   workspace,
		Source:        services.AgentConversationSourceManager,
	}
	if err := runtime.DeliverCodexHookNotification(context.Background(), base); err != nil {
		t.Fatalf("manager Hook should be ignored: %v", err)
	}
	base.EventID = "hook-filter-managed-unknown"
	base.Source = ""
	base.Managed = true
	if err := runtime.DeliverCodexHookNotification(context.Background(), base); err != nil {
		t.Fatalf("unmatched managed Hook should be ignored: %v", err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.sent) != 0 {
		t.Fatalf("filtered Hook provider messages = %#v", provider.sent)
	}
}

func TestFormatCodexHookNotificationIncludesRequiredFields(t *testing.T) {
	text := formatCodexHookNotification(services.CodexHookNotification{
		Event:         services.CodexHookNotificationSystemError,
		HookEventName: "PostToolUse",
		ProjectName:   "Demo",
		SessionName:   "Terminal",
		OccurredAt:    1710000000000,
		Error:         "permission denied",
	})
	for _, want := range []string{"【Codex Hook｜系统错误】", "项目：Demo", "会话：Terminal", "事件：系统错误 · PostToolUse", "时间：", "错误：permission denied"} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted Hook notification %q missing %q", text, want)
		}
	}
}

func TestFormatCodexHookNotificationUsesCompactFixedFields(t *testing.T) {
	text := formatCodexHookNotification(services.CodexHookNotification{
		Event:         services.CodexHookNotificationWaitingUserInput,
		HookEventName: "PreToolUse",
		ProjectName:   "Demo",
		ProjectPath:   `C:\Work\Demo`,
		SessionName:   "Terminal",
		SessionID:     "session-id",
		ThreadID:      "thread-id",
		TurnID:        "turn-id",
		OccurredAt:    1710000000000,
		ToolName:      "request_user_input",
		ToolInput:     `{"questions":[{"question":"选择格式","options":[{"label":"紧凑"},{"label":"调试"}]}]}`,
	})

	for _, want := range []string{
		"【Codex Hook｜等待用户输入】",
		"项目：Demo",
		"【Codex Hook｜等待用户输入】 项目：Demo",
		"会话：Terminal",
		"事件：等待用户输入 · PreToolUse",
		"工具：request_user_input",
		"问题：选择格式（选项：紧凑、调试）",
		"项目：Demo\n\n会话：Terminal",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compact Hook notification %q missing %q", text, want)
		}
	}
	for _, unwanted := range []string{"项目路径：", "消息事件：", "Session ID：", "Thread ID：", "Turn ID：", "参数：", `"questions"`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("compact Hook notification %q contains unwanted field %q", text, unwanted)
		}
	}
}

func TestFormatCodexHookNotificationCompactsEachEventDetail(t *testing.T) {
	cases := []struct {
		name         string
		notification services.CodexHookNotification
		want         string
		unwanted     string
	}{
		{
			name: "waiting approval",
			notification: services.CodexHookNotification{
				Event:         services.CodexHookNotificationWaitingApproval,
				HookEventName: "PermissionRequest",
				Reason:        "需要执行命令",
				ToolInput:     `{"command":"ignored because reason is present"}`,
			},
			want:     "请求：需要执行命令",
			unwanted: "参数：",
		},
		{
			name: "system error",
			notification: services.CodexHookNotification{
				Event:         services.CodexHookNotificationSystemError,
				HookEventName: "PostToolUse",
				ToolInput:     `{"command":"rg --hidden"}`,
				Error:         strings.Repeat("error output ", 40),
			},
			want:     "错误：error output error output",
			unwanted: "工具响应：",
		},
		{
			name: "session ended",
			notification: services.CodexHookNotification{
				Event:         services.CodexHookNotificationSessionEnded,
				HookEventName: "SessionEnd",
				Reason:        "任务完成",
			},
			want: "原因：任务完成",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			text := formatCodexHookNotification(testCase.notification)
			if !strings.Contains(text, testCase.want) {
				t.Fatalf("formatted Hook notification %q missing %q", text, testCase.want)
			}
			if testCase.unwanted != "" && strings.Contains(text, testCase.unwanted) {
				t.Fatalf("formatted Hook notification %q contains %q", text, testCase.unwanted)
			}
			if len([]rune(text)) > 600 {
				t.Fatalf("formatted Hook notification is too long: %d runes", len([]rune(text)))
			}
		})
	}
}

func TestCompactCodexHookInputExtractsCommandWithoutRawJSON(t *testing.T) {
	got := compactCodexHookInput(`{"command":"go test ./services/channels"}`)
	if got != "go test ./services/channels" {
		t.Fatalf("compact Hook command = %q", got)
	}
}
