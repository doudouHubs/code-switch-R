package services

import (
	"context"
	"testing"
	"time"
)

func TestProjectManagerCodexStatusPublishesOnlyVisibleTransitions(t *testing.T) {
	service := newProjectManagerCodexStatusService()
	event := projectManagerCodexHookEvent{
		SessionID:        "session-visible-state",
		Cwd:              `F:\workspace`,
		ReceivedAt:       1,
		ReceivedUnixNano: 1,
	}

	apply := func(name string, nano int64) bool {
		event.HookEventName = name
		event.ReceivedUnixNano = nano
		event.ReceivedAt = nano
		return service.applyEvent(event)
	}

	if !apply("SessionStart", 1) {
		t.Fatal("SessionStart must publish a newly observed session")
	}
	if apply("PostToolUse", 2) {
		t.Fatal("metadata-only PostToolUse must not publish a snapshot")
	}
	if !apply("UserPromptSubmit", 3) {
		t.Fatal("UserPromptSubmit must publish the idle-to-active transition")
	}
	if apply("PostToolUse", 4) {
		t.Fatal("repeated active PostToolUse must not publish a snapshot")
	}
	if !apply("PermissionRequest", 5) {
		t.Fatal("PermissionRequest must publish the waiting transition")
	}
	if apply("PermissionRequest", 6) {
		t.Fatal("repeated PermissionRequest must not publish a snapshot")
	}
	if !apply("Stop", 7) {
		t.Fatal("Stop must publish the completed transition")
	}
	if apply("Stop", 8) {
		t.Fatal("repeated Stop must not publish a snapshot")
	}
}

func TestProjectManagerCodexStatusDispatchesEligibleHookToSink(t *testing.T) {
	service := newProjectManagerCodexStatusService()
	received := make(chan CodexHookNotification, 1)
	service.notificationSink = func(context.Context, CodexHookNotification) error {
		received <- CodexHookNotification{Event: CodexHookNotificationSessionEnded}
		return nil
	}
	service.dispatchCodexHookNotification(projectManagerCodexHookEvent{
		EventID:       "dispatch-session-end",
		HookEventName: "SessionEnd",
		SessionID:     "external-session",
		ProjectID:     `F:\workspace`,
		Cwd:           `F:\workspace`,
		ReceivedAt:    time.Now().UnixMilli(),
	})

	select {
	case notification := <-received:
		if notification.Event != CodexHookNotificationSessionEnded {
			t.Fatalf("dispatched notification = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("eligible Hook notification was not dispatched to sink")
	}
}
