package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestBuildProjectManagerConversationPrunePlanBeforeUsesUserTimestamp(t *testing.T) {
	cutoffAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).UnixMilli()
	items := []SessionConversationItem{
		{ID: "user-old", Role: "user", Timestamp: cutoffAt - 1},
		{ID: "agent-old", Role: "agent", Timestamp: cutoffAt - 1, ReplyFor: "user-old"},
		{ID: "user-boundary", Role: "user", Timestamp: cutoffAt},
		{ID: "agent-boundary", Role: "agent", Timestamp: cutoffAt, ReplyFor: "user-boundary"},
		{ID: "user-new", Role: "user", Timestamp: cutoffAt + 1},
		{ID: "agent-new", Role: "agent", Timestamp: cutoffAt + 1, ReplyFor: "user-new"},
		{ID: "user-unknown", Role: "user", Timestamp: 0},
		{ID: "agent-unknown", Role: "agent", Timestamp: 0, ReplyFor: "user-unknown"},
	}

	plan, matched, err := buildProjectManagerConversationPrunePlanBefore("session-1", items, cutoffAt)
	if err != nil {
		t.Fatalf("build time prune plan: %v", err)
	}
	if !matched {
		t.Fatal("time prune plan did not match the old turn")
	}

	if len(plan.TargetUserIDs) != 1 || len(plan.TargetAgentIDs) != 1 || len(plan.TargetIDs) != 2 {
		t.Fatalf("target counts = users:%d agents:%d items:%d, want 1/1/2", len(plan.TargetUserIDs), len(plan.TargetAgentIDs), len(plan.TargetIDs))
	}
	for _, id := range []string{"user-old", "agent-old"} {
		if _, ok := plan.TargetIDs[id]; !ok {
			t.Fatalf("old conversation item %q was not selected", id)
		}
	}
	for _, id := range []string{"user-boundary", "agent-boundary", "user-new", "agent-new", "user-unknown", "agent-unknown"} {
		if _, ok := plan.TargetIDs[id]; ok {
			t.Fatalf("conversation item %q should be retained", id)
		}
	}
}

func TestBuildProjectManagerConversationPrunePlanBeforeReportsNoMatch(t *testing.T) {
	cutoffAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).UnixMilli()
	items := []SessionConversationItem{{
		ID:        "user-recent",
		Role:      "user",
		Timestamp: cutoffAt + 1,
	}}

	plan, matched, err := buildProjectManagerConversationPrunePlanBefore("session-1", items, cutoffAt)
	if err != nil {
		t.Fatalf("build no-match time prune plan: %v", err)
	}
	if matched {
		t.Fatal("recent-only conversation unexpectedly matched")
	}
	if len(plan.TargetIDs) != 0 || len(plan.TargetUserIDs) != 0 || len(plan.TargetAgentIDs) != 0 {
		t.Fatalf("no-match plan contains targets: %#v", plan)
	}
}

func TestProjectManagerSessionDeleteRangeDuration(t *testing.T) {
	tests := []struct {
		rangeKey string
		want     time.Duration
	}{
		{rangeKey: projectManagerSessionDeleteRangeOneWeek, want: 7 * 24 * time.Hour},
		{rangeKey: projectManagerSessionDeleteRangeThreeWeeks, want: 21 * 24 * time.Hour},
		{rangeKey: projectManagerSessionDeleteRangeOneMonth, want: 30 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.rangeKey, func(t *testing.T) {
			got, err := projectManagerSessionDeleteRangeDuration(tt.rangeKey)
			if err != nil {
				t.Fatalf("resolve duration: %v", err)
			}
			if got != tt.want {
				t.Fatalf("duration = %s, want %s", got, tt.want)
			}
		})
	}

	if _, err := projectManagerSessionDeleteRangeDuration(projectManagerSessionDeleteRangeAll); err == nil {
		t.Fatal("all range should not be accepted by the detail prune endpoint")
	}
}

func TestPruneSessionConversationsByRangeContinuesAfterSessionFailure(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	sessionID := "11111111-1111-4111-8111-111111111111"
	projectPath := filepath.Join(tmpHome, "workspace")
	sessionPath := filepath.Join(tmpHome, ".codex", "sessions", "2026", "08", "31", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}

	now := time.Now()
	oldAt := now.Add(-8 * 24 * time.Hour)
	recentAt := now.Add(-24 * time.Hour)
	lines := []string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":%q,"payload":{"id":%q,"cwd":%q}}`, oldAt.Format(time.RFC3339Nano), sessionID, projectPath),
		fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"user_message","message":"old question"}}`, oldAt.Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"agent_message","message":"old answer"}}`, oldAt.Add(time.Second).Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"user_message","message":"recent question"}}`, recentAt.Format(time.RFC3339Nano)),
		fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"agent_message","message":"recent answer"}}`, recentAt.Add(time.Second).Format(time.RFC3339Nano)),
	}
	if err := os.WriteFile(sessionPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("write session fixture: %v", err)
	}

	service := NewProjectManagerService()
	result, err := service.PruneSessionConversationsByRange(
		[]string{"missing-session", sessionID},
		projectManagerSessionDeleteRangeOneWeek,
	)
	if err != nil {
		t.Fatalf("batch time prune: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("batch result count = %d, want 2", len(result.Results))
	}
	if result.Results[0].Error == "" {
		t.Fatal("missing session did not report an item error")
	}
	if result.Results[1].Error != "" || result.Results[1].DeletedTurns != 1 || result.Results[1].DeletedItems != 2 {
		t.Fatalf("valid session result = %#v, want one turn and two items", result.Results[1])
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read pruned session: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "old question") || strings.Contains(content, "old answer") {
		t.Fatalf("old conversation remains after prune: %s", content)
	}
	if !strings.Contains(content, "recent question") || !strings.Contains(content, "recent answer") {
		t.Fatalf("recent conversation was removed: %s", content)
	}

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("load snapshot after prune: %v", err)
	}
	found := false
	for _, session := range snapshot.Sessions {
		if session.ID == sessionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("time pruning removed the session from the snapshot")
	}
}
