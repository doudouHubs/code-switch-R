package channels

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreConsolidatesBuiltinInstancesAndPurgesRetiredHistory(t *testing.T) {
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
	if err := store.AppendMessage(ChannelMessage{ID: "legacy-message", InstanceID: "legacy-feishu-b", SessionID: history.ID, ExternalID: "legacy-message", Role: "user", ChatID: "chat-b", Content: "retired history", Timestamp: 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMedia("legacy-message", "legacy-feishu-b", ChannelMedia{Kind: "image", MediaType: "image/png", FileName: "legacy.png", Data: []byte("legacy")}); err != nil {
		t.Fatal(err)
	}

	created, err := store.EnsureBuiltinInstances()
	if err != nil {
		t.Fatalf("EnsureBuiltinInstances() error = %v", err)
	}
	if created != len(BuiltinChannelTypes) {
		t.Fatalf("created = %d, want %d canonical instances", created, len(BuiltinChannelTypes))
	}
	instances, err := store.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != len(BuiltinChannelTypes) {
		t.Fatalf("instance count = %d, want only canonical active instances", len(instances))
	}
	activeTypes := make(map[string]int)
	var canonicalFeishu ChannelInstance
	for _, instance := range instances {
		activeTypes[instance.Type]++
		if instance.ID == canonicalBuiltinInstanceID(ChannelTypeFeishu) {
			canonicalFeishu = instance
		}
	}
	if len(activeTypes) != len(BuiltinChannelTypes) || activeTypes[ChannelTypeFeishu] != 1 {
		t.Fatalf("active builtin types = %#v", activeTypes)
	}
	if canonicalFeishu.ID == "" || canonicalFeishu.Enabled || canonicalFeishu.ProjectID != nil || canonicalFeishu.Config["appId"] != "cli_test" || canonicalFeishu.Config["appSecret"] != "secret" {
		t.Fatalf("canonical Feishu did not use the most complete legacy config: %#v", canonicalFeishu)
	}
	if canonicalFeishu.Features.AutoStart {
		t.Fatalf("legacy feature flags were not preserved: %#v", canonicalFeishu.Features)
	}
	if _, found, err := store.GetInstance("legacy-feishu-a"); err != nil || found {
		t.Fatalf("legacy-feishu-a should be deleted: found=%t err=%v", found, err)
	}
	if _, found, err := store.GetInstance("legacy-feishu-b"); err != nil || found {
		t.Fatalf("legacy-feishu-b should be deleted: found=%t err=%v", found, err)
	}
	if sessions, err := store.ListSessions("legacy-feishu-b"); err != nil || len(sessions) != 0 {
		t.Fatalf("deleted duplicate sessions = %#v, err=%v", sessions, err)
	}
	if count := countChannelRows(t, store, `SELECT COUNT(*) FROM channel_messages WHERE instance_id=?`, "legacy-feishu-b"); count != 0 {
		t.Fatalf("deleted duplicate messages = %d, want 0", count)
	}
	if count := countChannelRows(t, store, `SELECT COUNT(*) FROM channel_media WHERE instance_id=?`, "legacy-feishu-b"); count != 0 {
		t.Fatalf("deleted duplicate media = %d, want 0", count)
	}

	activeProject := "active-project"
	activeFolder := t.TempDir()
	if err := store.UpsertSession(ChannelSession{InstanceID: canonicalFeishu.ID, ChatID: "active-chat", ProjectID: activeProject, WorkingFolder: activeFolder}); err != nil {
		t.Fatal(err)
	}
	activeSession, found, err := store.GetSession(canonicalFeishu.ID, "active-chat")
	if err != nil || !found {
		t.Fatalf("seed active session = %#v, %v, %v", activeSession, found, err)
	}
	if err := store.AppendMessage(ChannelMessage{ID: "active-message", InstanceID: canonicalFeishu.ID, SessionID: activeSession.ID, ExternalID: "active-message", Role: "user", ChatID: "active-chat", Content: "keep active history", Timestamp: 20}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMedia("active-message", canonicalFeishu.ID, ChannelMedia{Kind: "image", MediaType: "image/png", FileName: "active.png", Data: []byte("active")}); err != nil {
		t.Fatal(err)
	}

	// 用原始 SQL 构造旧版本留下的归档实例，验证启动清理不仅隐藏它，
	// 还会通过外键级联删除其 session、message 和 media。
	archivedNow := nowMillis()
	if _, err := store.db.Exec(`INSERT INTO channel_instances(id,type,name,archived,created_at,updated_at) VALUES(?,?,?,?,?,?)`, "archived-channel", "legacy", "Legacy history", 1, archivedNow, archivedNow); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSession(ChannelSession{InstanceID: "archived-channel", ChatID: "archived-chat", ProjectID: "archived-project", WorkingFolder: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	archivedSession, found, err := store.GetSession("archived-channel", "archived-chat")
	if err != nil || !found {
		t.Fatalf("seed archived session = %#v, %v, %v", archivedSession, found, err)
	}
	if err := store.AppendMessage(ChannelMessage{ID: "archived-message", InstanceID: "archived-channel", SessionID: archivedSession.ID, ExternalID: "archived-message", Role: "user", ChatID: "archived-chat", Content: "delete this history", Timestamp: 30}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMedia("archived-message", "archived-channel", ChannelMedia{Kind: "image", MediaType: "image/png", FileName: "archived.png", Data: []byte("archived")}); err != nil {
		t.Fatal(err)
	}

	created, err = store.EnsureBuiltinInstances()
	if err != nil {
		t.Fatalf("repeat EnsureBuiltinInstances() error = %v", err)
	}
	if created != 0 {
		t.Fatalf("repeat ensure created = %d, want 0", created)
	}
	if count := countChannelRows(t, store, `SELECT COUNT(*) FROM channel_instances WHERE archived=1`); count != 0 {
		t.Fatalf("archived instances = %d, want 0", count)
	}
	if _, found, err := store.GetInstance("archived-channel"); err != nil || found {
		t.Fatalf("archived instance should be deleted: found=%t err=%v", found, err)
	}
	if count := countChannelRows(t, store, `SELECT COUNT(*) FROM channel_sessions WHERE instance_id=?`, "archived-channel"); count != 0 {
		t.Fatalf("archived sessions = %d, want 0", count)
	}
	if count := countChannelRows(t, store, `SELECT COUNT(*) FROM channel_messages WHERE instance_id=?`, "archived-channel"); count != 0 {
		t.Fatalf("archived messages = %d, want 0", count)
	}
	if count := countChannelRows(t, store, `SELECT COUNT(*) FROM channel_media WHERE instance_id=?`, "archived-channel"); count != 0 {
		t.Fatalf("archived media = %d, want 0", count)
	}
	activeMessages, err := store.ListMessages(activeSession.ID, 20)
	if err != nil || len(activeMessages) != 1 || activeMessages[0].Content != "keep active history" {
		t.Fatalf("active messages = %#v, err=%v", activeMessages, err)
	}
	if count := countChannelRows(t, store, `SELECT COUNT(*) FROM channel_media WHERE instance_id=?`, canonicalFeishu.ID); count != 1 {
		t.Fatalf("active media = %d, want 1", count)
	}
	if count := countChannelRows(t, store, `SELECT COUNT(*) FROM channel_import_templates WHERE type=?`, ChannelTypeFeishu); count != 1 {
		t.Fatalf("import template count = %d, want 1", count)
	}
	projectC := "project-c"
	if err := store.UpsertInstance(ChannelInstance{ID: "duplicate-feishu", Type: ChannelTypeFeishu, Name: "Duplicate", Builtin: true, ProjectID: &projectC}); err == nil {
		t.Fatal("active builtin type uniqueness should reject a second Feishu instance")
	}
}

func countChannelRows(t *testing.T, store *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("count channel rows: %v", err)
	}
	return count
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
		if instance.ProjectID != nil || instance.Enabled {
			t.Fatalf("new builtin instance should be active, stopped and unbound: %#v", instance)
		}
	}
}

func TestOpenStoreMigratesLegacySchemaWithArchivedCompatibilityColumn(t *testing.T) {
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
