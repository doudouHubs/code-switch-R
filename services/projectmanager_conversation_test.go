package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseProjectManagerConversationMessageSupportsLegacyAndResponseItems(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantOK      bool
		wantPayload string
		wantRole    string
		wantContent string
	}{
		{
			name:        "legacy user message",
			line:        `{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
			wantOK:      true,
			wantPayload: "user_message",
			wantRole:    "user",
			wantContent: "hello",
		},
		{
			name:        "legacy agent message",
			line:        `{"type":"event_msg","payload":{"type":"agent_message","message":"world"}}`,
			wantOK:      true,
			wantPayload: "agent_message",
			wantRole:    "agent",
			wantContent: "world",
		},
		{
			name:        "response item user message",
			line:        `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"again"}]}}`,
			wantOK:      true,
			wantPayload: "user_message",
			wantRole:    "user",
			wantContent: "hello\nagain",
		},
		{
			name:        "response item assistant message",
			line:        `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`,
			wantOK:      true,
			wantPayload: "agent_message",
			wantRole:    "agent",
			wantContent: "answer",
		},
		{
			name: "developer response item is not conversation",
			line: `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"internal"}]}}`,
		},
		{
			name: "completed item is not conversation",
			line: `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage"}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseProjectManagerConversationMessage(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parse ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.PayloadType != tt.wantPayload || got.Role != tt.wantRole || got.Content != tt.wantContent {
				t.Fatalf("message = %#v, want payload=%q role=%q content=%q", got, tt.wantPayload, tt.wantRole, tt.wantContent)
			}
		})
	}
}

func TestReadProjectManagerRolloutConversationItemsSupportsResponseItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-test.jsonl")
	lines := []string{
		`{"timestamp":"2026-08-20T01:00:00Z","type":"session_meta","payload":{"session_id":"sess-new","cwd":"C:/workspace"}}`,
		`{"timestamp":"2026-08-20T01:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-08-20T01:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"question"}]}}`,
		`{"timestamp":"2026-08-20T01:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage"}}}`,
		`{"timestamp":"2026-08-20T01:00:04Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`,
		`{"timestamp":"2026-08-20T01:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage"}}}`,
		`{"timestamp":"2026-08-20T01:00:06Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write rollout fixture: %v", err)
	}

	items, err := readProjectManagerRolloutConversationItems(path, "sess-new")
	if err != nil {
		t.Fatalf("read rollout conversation: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("conversation item count = %d, want 2", len(items))
	}
	if items[0].Role != "user" || items[0].Content != "question" {
		t.Fatalf("user item = %#v", items[0])
	}
	if items[1].Role != "agent" || items[1].Content != "answer" {
		t.Fatalf("agent item = %#v", items[1])
	}
	if items[0].TurnID != "turn-1" || items[1].TurnID != "turn-1" {
		t.Fatalf("turn ids = %q, %q, want turn-1", items[0].TurnID, items[1].TurnID)
	}
	if items[1].ReplyFor != items[0].ID {
		t.Fatalf("agent reply_for = %q, want %q", items[1].ReplyFor, items[0].ID)
	}
}

func TestReadProjectManagerRolloutConversationItemsPreservesLegacyMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-legacy.jsonl")
	lines := []string{
		`{"timestamp":"2026-08-20T01:00:00Z","type":"session_meta","payload":{"id":"sess-legacy","cwd":"C:/workspace"}}`,
		`{"timestamp":"2026-08-20T01:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-legacy"}}`,
		`{"timestamp":"2026-08-20T01:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"old question"}}`,
		`{"timestamp":"2026-08-20T01:00:03Z","type":"event_msg","payload":{"type":"agent_message","message":"old answer"}}`,
		`{"timestamp":"2026-08-20T01:00:04Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write legacy rollout fixture: %v", err)
	}

	items, err := readProjectManagerRolloutConversationItems(path, "sess-legacy")
	if err != nil {
		t.Fatalf("read legacy rollout conversation: %v", err)
	}
	if len(items) != 2 || items[0].Content != "old question" || items[1].Content != "old answer" {
		t.Fatalf("legacy items = %#v", items)
	}
}

func TestScanProjectManagerCodexSessionFileSupportsSessionIDAndResponseUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-session.jsonl")
	lines := []string{
		`{"timestamp":"2026-08-20T01:00:00Z","type":"session_meta","payload":{"session_id":"sess-meta","cwd":"C:/workspace"}}`,
		`{"timestamp":"2026-08-20T01:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"new question"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write session fixture: %v", err)
	}

	sessionID, cwd, projectPath, projectSource, _, latestUserMessage, _, _, err := scanProjectManagerCodexSessionFileDetails(path)
	if err != nil {
		t.Fatalf("scan session file: %v", err)
	}
	if sessionID != "sess-meta" || cwd != "C:/workspace" || projectPath != "C:/workspace" || projectSource != "cwd" {
		t.Fatalf("session metadata = id=%q cwd=%q project=%q source=%q", sessionID, cwd, projectPath, projectSource)
	}
	if latestUserMessage != "new question" {
		t.Fatalf("latest user message = %q, want new question", latestUserMessage)
	}
}
