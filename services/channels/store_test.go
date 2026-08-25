package channels

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreConsolidatesBuiltinInstancesAndPreservesHistory(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()

	configPath := filepath.Join(t.TempDir(), "plugins.json")
	plugins := []map[string]any{
		{
			"id":      "old-feishu-1",
			"type":    "feishu-bot",
			"name":    "Old Feishu",
			"enabled": false,
			"config":  map[string]string{"appId": "short"},
		},
		{
			"id":       "old-feishu-2",
			"type":     "feishu-bot",
			"name":     "Configured Feishu",
			"enabled":  true,
			"config":   map[string]string{"appId": "cli_test", "appSecret": "secret"},
			"features": map[string]bool{"autoReply": true, "streamingReply": false, "autoStart": false},
		},
		{
			"id":     "unsupported",
			"type":   "custom-plugin",
			"config": map[string]string{"token": "ignored"},
		},
	}
	data, err := json.Marshal(plugins)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := store.ImportOpenCoworkOnce(configPath)
	if err != nil {
		t.Fatalf("ImportOpenCoworkOnce() error = %v", err)
	}
	if report.Imported != 2 || report.Templates != 1 || report.Skipped != 1 {
		t.Fatalf("unexpected import report: %#v", report)
	}
	report, err = store.ImportOpenCoworkOnce(configPath)
	if err != nil {
		t.Fatalf("second ImportOpenCoworkOnce() error = %v", err)
	}
	if report.AlreadyApplied != 1 {
		t.Fatalf("second import should be idempotent: %#v", report)
	}

	projectA := "project-a"
	projectB := "project-b"
	if err := store.UpsertInstance(ChannelInstance{
		ID: "legacy-feishu-a", Type: ChannelTypeFeishu, Name: "Old Feishu", Builtin: true,
		Config: map[string]string{"appId": "short"}, ProjectID: &projectA,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertInstance(ChannelInstance{
		ID: "legacy-feishu-b", Type: ChannelTypeFeishu, Name: "Configured Feishu", Builtin: true, Enabled: true,
		Config:   map[string]string{"appId": "cli_test", "appSecret": "secret"},
		Features: ChannelFeatures{AutoReply: true, StreamingReply: false, AutoStart: false}, ProjectID: &projectB,
	}); err != nil {
		t.Fatal(err)
	}
	historyFolder := t.TempDir()
	if err := store.UpsertSession(ChannelSession{InstanceID: "legacy-feishu-b", ChatID: "chat-b", ProjectID: projectB, WorkingFolder: historyFolder}); err != nil {
		t.Fatal(err)
	}
	history, found, err := store.GetSession("legacy-feishu-b", "chat-b")
	if err != nil || !found {
		t.Fatalf("seed legacy session = %#v, %v, %v", history, found, err)
	}
	if err := store.AppendMessage(ChannelMessage{InstanceID: "legacy-feishu-b", SessionID: history.ID, ExternalID: "legacy-message", Role: "user", ChatID: "chat-b", Content: "keep this history", Timestamp: 10}); err != nil {
		t.Fatal(err)
	}

	created, err := store.EnsureBuiltinInstances()
	if err != nil {
		t.Fatalf("EnsureBuiltinInstances() error = %v", err)
	}
	if created != len(BuiltinChannelTypes) {
		t.Fatalf("created = %d, want %d canonical instances", created, len(BuiltinChannelTypes))
	}
	created, err = store.EnsureBuiltinInstances()
	if err != nil {
		t.Fatalf("second EnsureBuiltinInstances() error = %v", err)
	}
	if created != 0 {
		t.Fatalf("second ensure created = %d, want 0", created)
	}

	instances, err := store.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != len(BuiltinChannelTypes)+2 {
		t.Fatalf("instance count = %d, want %d active plus archived history", len(instances), len(BuiltinChannelTypes)+2)
	}
	activeTypes := make(map[string]int)
	var canonicalFeishu ChannelInstance
	var archivedA, archivedB ChannelInstance
	for _, instance := range instances {
		if !instance.Archived {
			activeTypes[instance.Type]++
			if instance.ID == canonicalBuiltinInstanceID(ChannelTypeFeishu) {
				canonicalFeishu = instance
			}
		} else if instance.ID == "legacy-feishu-a" {
			archivedA = instance
		} else if instance.ID == "legacy-feishu-b" {
			archivedB = instance
		}
	}
	if len(activeTypes) != len(BuiltinChannelTypes) || activeTypes[ChannelTypeFeishu] != 1 {
		t.Fatalf("active builtin types = %#v", activeTypes)
	}
	if canonicalFeishu.ID == "" || canonicalFeishu.Archived || canonicalFeishu.Enabled || canonicalFeishu.ProjectID != nil || canonicalFeishu.Config["appId"] != "cli_test" || canonicalFeishu.Config["appSecret"] != "secret" {
		t.Fatalf("canonical Feishu did not use the most complete legacy config: %#v", canonicalFeishu)
	}
	if canonicalFeishu.Features.AutoStart {
		t.Fatalf("legacy feature flags were not preserved: %#v", canonicalFeishu.Features)
	}
	if !archivedA.Archived || !archivedB.Archived || archivedA.ProjectID == nil || *archivedA.ProjectID != projectA || archivedB.ProjectID == nil || *archivedB.ProjectID != projectB {
		t.Fatalf("legacy project ownership was not preserved: %#v %#v", archivedA, archivedB)
	}
	if sessions, err := store.ListSessions("legacy-feishu-b"); err != nil || len(sessions) != 1 {
		t.Fatalf("archived sessions = %#v, err=%v", sessions, err)
	}
	messages, err := store.ListMessages(history.ID, 20)
	if err != nil || len(messages) != 1 || messages[0].Content != "keep this history" {
		t.Fatalf("archived messages = %#v, err=%v", messages, err)
	}
	if err := store.AppendMessage(ChannelMessage{InstanceID: "legacy-feishu-b", SessionID: history.ID, ExternalID: "new-message", Role: "user", ChatID: "chat-b", Content: "must reject", Timestamp: 20}); err == nil {
		t.Fatal("archived message append should be rejected")
	}
	projectC := "project-c"
	if err := store.UpsertInstance(ChannelInstance{ID: "duplicate-feishu", Type: ChannelTypeFeishu, Name: "Duplicate", Builtin: true, ProjectID: &projectC}); err == nil {
		t.Fatal("active builtin type uniqueness should reject a second Feishu instance")
	}
}

func TestStoreEnsuresBuiltinInstancesWithoutProjects(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.EnsureBuiltinInstances()
	if err != nil {
		t.Fatalf("EnsureBuiltinInstances() error = %v", err)
	}
	if created != len(BuiltinChannelTypes) {
		t.Fatalf("created = %d, want %d", created, len(BuiltinChannelTypes))
	}
	instances, err := store.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != len(BuiltinChannelTypes) {
		t.Fatalf("instance count = %d, want %d", len(instances), len(BuiltinChannelTypes))
	}
	for _, instance := range instances {
		if instance.Archived || instance.ProjectID != nil || instance.Enabled {
			t.Fatalf("new builtin instance should be active, stopped and unbound: %#v", instance)
		}
	}
}

func TestOpenStoreMigratesLegacySchemaBeforeArchivedIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE channel_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE channel_instances (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		name TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 0,
		builtin INTEGER NOT NULL DEFAULT 0,
		config_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL,
		project_id TEXT,
		provider_id TEXT,
		model TEXT,
		tools_json TEXT NOT NULL DEFAULT '{}',
		features_json TEXT NOT NULL DEFAULT '{}',
		permissions_json TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'stopped',
		last_error TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() legacy migration error = %v", err)
	}
	defer store.Close()
	created, err := store.EnsureBuiltinInstances()
	if err != nil {
		t.Fatalf("EnsureBuiltinInstances() after legacy migration error = %v", err)
	}
	if created != len(BuiltinChannelTypes) {
		t.Fatalf("created = %d, want %d", created, len(BuiltinChannelTypes))
	}
}

func TestStoreRejectsEnabledUnboundChannelAndDeduplicatesMessages(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "channels.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	err = store.UpsertInstance(ChannelInstance{ID: "unbound", Type: ChannelTypeDiscord, Name: "Discord", Enabled: true})
	if err == nil {
		t.Fatal("enabled unbound channel should be rejected")
	}
	projectID := "project-a"
	if err := store.UpsertInstance(ChannelInstance{ID: "channel-a", Type: ChannelTypeDiscord, Name: "Discord", ProjectID: &projectID}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSession(ChannelSession{InstanceID: "channel-a", ChatID: "chat-a", ProjectID: projectID, WorkingFolder: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	session, ok, err := store.GetSession("channel-a", "chat-a")
	if err != nil || !ok {
		t.Fatalf("GetSession() = %#v, %v, %v", session, ok, err)
	}
	message := ChannelMessage{InstanceID: "channel-a", SessionID: session.ID, ExternalID: "external-1", Role: "user", ChatID: "chat-a", Content: "hello", Timestamp: 10}
	if err := store.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessage(message); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(session.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != "hello" {
		t.Fatalf("messages = %#v", messages)
	}
}
