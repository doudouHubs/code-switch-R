package services

import (
	"testing"
	"time"
)

func TestDecodeProjectManagerCodexHookPayloadSupportsSnakeAndCamelCase(t *testing.T) {
	payload, err := decodeProjectManagerCodexHookPayload([]byte(`{
		"hookEventName":"PermissionRequest",
		"sessionId":"session-camel",
		"project_id":"project-snake",
		"threadId":"thread-camel",
		"turn_id":"turn-snake",
		"tool":"shell",
		"workingDirectory":"C:\\workspace",
		"toolResponse":{"success":false,"error":"denied"}
	}`))
	if err != nil {
		t.Fatalf("decode Hook payload failed: %v", err)
	}
	if payload.HookEventName != "PermissionRequest" || payload.SessionID != "session-camel" || payload.ProjectID != "project-snake" || payload.ThreadID != "thread-camel" || payload.TurnID != "turn-snake" {
		t.Fatalf("decoded identifiers = %#v", payload)
	}
	if payload.ToolName != "shell" || payload.Cwd != `C:\workspace` || payload.ToolResponse == "" {
		t.Fatalf("decoded tool fields = %#v", payload)
	}
}

func TestCodexHookNotificationTypeCoversRequiredEvents(t *testing.T) {
	tests := []struct {
		name  string
		event projectManagerCodexHookEvent
		want  string
	}{
		{name: "approval", event: projectManagerCodexHookEvent{HookEventName: "PermissionRequest"}, want: CodexHookNotificationWaitingApproval},
		{name: "user input", event: projectManagerCodexHookEvent{HookEventName: "PreToolUse", ToolName: "request_user_input"}, want: CodexHookNotificationWaitingUserInput},
		{name: "system error", event: projectManagerCodexHookEvent{HookEventName: "PostToolUse", ToolResponse: `{"success":false}`}, want: CodexHookNotificationSystemError},
		{name: "session ended", event: projectManagerCodexHookEvent{HookEventName: "SessionEnd"}, want: CodexHookNotificationSessionEnded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := codexHookNotificationType(test.event)
			if !ok || got != test.want {
				t.Fatalf("notification type = %q, ok=%t, want %q", got, ok, test.want)
			}
		})
	}
}

func TestBuildCodexHookNotificationFiltersManagerAndUnmatchedManagedSources(t *testing.T) {
	service := newProjectManagerCodexStatusService()
	event := projectManagerCodexHookEvent{
		EventID:       "hook-filter",
		HookEventName: "PermissionRequest",
		SessionID:     "thread-filter",
		ProjectID:     "project-filter",
		ReceivedAt:    time.Now().UnixMilli(),
	}
	service.registerCodexHookSource(CodexHookSource{
		Source:    AgentConversationSourceManager,
		ProjectID: "project-filter",
		SessionID: "thread-filter",
		ThreadID:  "thread-filter",
	})
	if notification := service.buildCodexHookNotification(event); notification != nil {
		t.Fatalf("manager Hook should be filtered: %#v", notification)
	}

	event.EventID = "hook-unmatched"
	event.SessionID = "thread-unknown"
	event.Managed = true
	if notification := service.buildCodexHookNotification(event); notification != nil {
		t.Fatalf("unmatched managed Hook should be dropped: %#v", notification)
	}
}

func TestBuildCodexHookNotificationUsesLatestSourceForSharedThread(t *testing.T) {
	service := newProjectManagerCodexStatusService()
	base := CodexHookSource{
		ProjectID: "project-shared",
		SessionID: "thread-shared",
		ThreadID:  "thread-shared",
	}
	manager := base
	manager.Source = AgentConversationSourceManager
	service.registerCodexHookSource(manager)
	time.Sleep(time.Millisecond)
	channel := base
	channel.Source = AgentConversationSourceChannel
	channel.ChannelInstanceID = "channel-shared"
	channel.ChannelChatID = "chat-shared"
	service.registerCodexHookSource(channel)

	notification := service.buildCodexHookNotification(projectManagerCodexHookEvent{
		EventID:       "hook-shared",
		HookEventName: "SessionEnd",
		SessionID:     "thread-shared",
		ProjectID:     "project-shared",
		ReceivedAt:    time.Now().UnixMilli(),
	})
	if notification == nil || notification.Source != AgentConversationSourceChannel || notification.ChannelInstanceID != "channel-shared" || notification.ChannelChatID != "chat-shared" {
		t.Fatalf("latest shared source = %#v", notification)
	}
}

func TestBuildCodexHookNotificationIncludesSystemErrorText(t *testing.T) {
	service := newProjectManagerCodexStatusService()
	notification := service.buildCodexHookNotification(projectManagerCodexHookEvent{
		EventID:       "hook-error",
		HookEventName: "PostToolUse",
		SessionID:     "session-error",
		ProjectID:     "project-error",
		ToolResponse:  `{"success":false,"error":"permission denied"}`,
		ReceivedAt:    time.Now().UnixMilli(),
	})
	if notification == nil || notification.Event != CodexHookNotificationSystemError || notification.Error == "" {
		t.Fatalf("system error notification = %#v", notification)
	}
}
