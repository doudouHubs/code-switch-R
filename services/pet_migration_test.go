package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestPetMigrationDecodesBOMStringAndWrapperIdempotently(t *testing.T) {
	source := t.TempDir()
	petsDir := filepath.Join(source, "pets", "custom")
	if err := os.MkdirAll(petsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":    "Custom",
		"subject": "test pet",
		"modelId": "image-model",
		"atlas": map[string]any{
			"image": "atlas.png", "width": 10, "height": 20,
			"anchor": "bottom-center", "layout": "action-rows",
		},
	}
	manifestBytes, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(petsDir, "pet.json"), append([]byte{0xEF, 0xBB, 0xBF}, manifestBytes...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(petsDir, "atlas.png"), []byte("atlas"), 0o644); err != nil {
		t.Fatal(err)
	}
	expStore, _ := json.Marshal(map[string]any{
		"state": map[string]any{
			"totalExp":    12.5,
			"totalTokens": 99,
			"log": []any{map[string]any{
				"id": "exp-1", "at": 100, "model": "m", "tokens": 20, "premium": false, "exp": 0.02,
			}},
		},
		"version": 0,
	})
	settings := map[string]any{
		OpenCoworkPetStateKey: map[string]any{
			"state":   map[string]any{"name": "Mimi", "hunger": 42, "coins": 10.4},
			"version": 0,
		},
		OpenCoworkPetExperienceKey:  string(expStore),
		OpenCoworkPetCareKey:        map[string]any{"state": map[string]any{"autoCareEnabled": true, "autoCareThreshold": 47}},
		OpenCoworkPetAgentKey:       map[string]any{"state": map[string]any{"providerId": "provider-1", "modelId": "model-1"}},
		OpenCoworkPetDreamConfigKey: map[string]any{"state": map[string]any{"keywords": "月亮", "sleepTalkMinLength": 20}},
		OpenCoworkPetEnabledKey:     true,
		OpenCoworkPetSkinsKey:       map[string]any{"state": map[string]any{"activeSkinId": "custom"}},
	}
	rawSettings, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(source, "settings.json"), rawSettings, 0o644); err != nil {
		t.Fatal(err)
	}
	db := newPetMigrationTestDB(t)
	dao := NewPetDAO(db)
	migrator := NewPetMigrator(dao, source)
	migrator.now = func() int64 { return 1_700_000_000_000 }

	first, err := migrator.Migrate(context.Background())
	if err != nil {
		t.Fatalf("first migration error = %v", err)
	}
	if first.AlreadyApplied || first.Imported == 0 || first.Failed != 0 {
		t.Fatalf("first migration report = %#v", first)
	}
	snapshot, err := dao.LoadSnapshot(context.Background(), DefaultPetID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State == nil || snapshot.State.Name != "Mimi" || snapshot.State.Coins != 10 {
		t.Fatalf("state = %#v", snapshot.State)
	}
	if snapshot.Experience == nil || snapshot.Experience.TotalExp != 12.5 || len(snapshot.ExpLog) != 1 {
		t.Fatalf("experience/log = %#v/%#v", snapshot.Experience, snapshot.ExpLog)
	}
	if snapshot.Care == nil || snapshot.Care.AutoCareThreshold != 45 {
		t.Fatalf("care = %#v", snapshot.Care)
	}
	if snapshot.Agent == nil || snapshot.Agent.ProviderID == nil || *snapshot.Agent.ProviderID != "provider-1" {
		t.Fatalf("agent = %#v", snapshot.Agent)
	}
	if snapshot.Window == nil || !snapshot.Window.Enabled || snapshot.SkinSelection == nil || snapshot.SkinSelection.ActiveSkinID == nil {
		t.Fatalf("window/skin selection = %#v/%#v", snapshot.Window, snapshot.SkinSelection)
	}
	second, err := migrator.Migrate(context.Background())
	if err != nil {
		t.Fatalf("second migration error = %v", err)
	}
	if !second.AlreadyApplied {
		t.Fatalf("second migration report = %#v, want AlreadyApplied", second)
	}
}

func TestPetDAOLoadSettingsSnapshotSkipsHistoricalTables(t *testing.T) {
	db := newPetMigrationTestDB(t)
	dao := NewPetDAO(db)
	ctx := context.Background()
	imagePath := `C:\\Users\\X1\\AppData\\Local\\pet\\dream.png`

	if err := dao.SaveSnapshot(ctx, PetMigrationSnapshot{
		PetID:      DefaultPetID,
		State:      &PetState{PetID: DefaultPetID, Name: "Kapi"},
		Experience: &PetExperience{PetID: DefaultPetID, TotalTokens: 7},
		Window:     &PetWindowConfig{PetID: DefaultPetID, Enabled: true},
		Care:       &PetCareConfig{PetID: DefaultPetID},
		Agent:      &PetAgentConfig{PetID: DefaultPetID},
		DreamConfig: &PetDreamConfig{
			PetID: DefaultPetID,
		},
		ExpLog:      []PetExpLogEntry{{PetID: DefaultPetID, ID: "exp-1", Exp: 1, At: 1}},
		PlanRecords: []PetPlanRecord{{PetID: DefaultPetID, PlanID: "plan-1"}},
		Dreams:      []PetDreamHistoryRecord{{PetID: DefaultPetID, ID: "dream-1", ImagePath: &imagePath}},
		Memories:    []PetMemoryRecord{{PetID: DefaultPetID, ID: "memory-1", Text: "history"}},
	}); err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}

	light, err := dao.LoadSettingsSnapshot(ctx, DefaultPetID)
	if err != nil {
		t.Fatalf("LoadSettingsSnapshot() error = %v", err)
	}
	if light.State == nil || light.State.Name != "Kapi" || light.Experience == nil || light.Experience.TotalTokens != 7 {
		t.Fatalf("settings snapshot core = %#v/%#v, want persisted core", light.State, light.Experience)
	}
	if len(light.ExpLog) != 0 || len(light.PlanRecords) != 0 || len(light.Dreams) != 0 || len(light.Memories) != 0 {
		t.Fatalf("settings snapshot loaded historical data: exp=%d plans=%d dreams=%d memories=%d", len(light.ExpLog), len(light.PlanRecords), len(light.Dreams), len(light.Memories))
	}
}

func TestPetDAOLoadAgentPersonaIgnoresUnrelatedMalformedConfig(t *testing.T) {
	db := newPetMigrationTestDB(t)
	dao := NewPetDAO(db)
	ctx := context.Background()
	if err := dao.SaveSnapshot(ctx, PetMigrationSnapshot{
		PetID: DefaultPetID,
		State: &PetState{PetID: DefaultPetID, Name: "Mimi"},
		Agent: &PetAgentConfig{PetID: DefaultPetID, SystemPrompt: "只读取 canonical persona"},
	}); err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	// 梦境配置不属于人格投影；故意写入损坏 JSON，验证聊天启动不会被设置页其它表拖垮。
	if _, err := db.Exec(`INSERT INTO pet_dream_config (pet_id, config_json, updated_at) VALUES (?, ?, ?)`, DefaultPetID, "{broken", 1); err != nil {
		t.Fatalf("写入损坏梦境配置失败: %v", err)
	}

	persona, err := dao.LoadAgentPersona(ctx, DefaultPetID)
	if err != nil {
		t.Fatalf("LoadAgentPersona() error = %v", err)
	}
	if persona.SystemPrompt != "只读取 canonical persona" || persona.PetName != "Mimi" {
		t.Fatalf("persona = %#v", persona)
	}
}

func TestPetMigrationImportsDreamDBAndLegacyArchiveWithoutWritingSource(t *testing.T) {
	source := t.TempDir()
	archive := filepath.Join(source, petMigrationDreamsDir)
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyText := "一场旧梦。"
	legacyPath := filepath.Join(archive, "1700000000000-old.txt")
	if err := os.WriteFile(legacyPath, []byte(legacyText), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(source, petMigrationDreamDBFile)
	oldDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldDB.Exec("CREATE TABLE pet_dream_records (" +
		"id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, title TEXT NOT NULL DEFAULT '', " +
		"creative_prompt TEXT NOT NULL DEFAULT '', effective_prompt TEXT NOT NULL DEFAULT '', " +
		"keywords_json TEXT NOT NULL DEFAULT '[]', theme_id TEXT, theme_label TEXT, " +
		"dream_text TEXT NOT NULL, sleep_talk TEXT NOT NULL DEFAULT '', emotion TEXT, " +
		"self_appears INTEGER, image_file_name TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := oldDB.Exec("INSERT INTO pet_dream_records (" +
		"id, created_at, title, creative_prompt, effective_prompt, keywords_json, " +
		"theme_id, theme_label, dream_text, sleep_talk, emotion, self_appears, image_file_name" +
		") VALUES ('dream-1', 1700000000001, '', '', '', '[\"星星\"]', NULL, NULL, " +
		"'一场新梦。', '星星在说话', 'calm', 1, NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := oldDB.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	db := newPetMigrationTestDB(t)
	report, err := MigrateOpenCoworkPet(context.Background(), NewPetDAO(db), PetMigrationOptions{
		SourceRoot: source,
		Now:        func() int64 { return 1_700_000_000_000 },
	})
	if err != nil {
		t.Fatalf("migration error = %v", err)
	}
	if report.Imported < 2 {
		t.Fatalf("report = %#v, want DB and archive records", report)
	}
	snapshot, err := NewPetDAO(db).LoadSnapshot(context.Background(), DefaultPetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Dreams) != 2 {
		t.Fatalf("dream records = %#v", snapshot.Dreams)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("migration modified the source archive")
	}
}

func newPetMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "target.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := EnsurePetSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPetMigrationSourceFingerprintChangesWhenSettingsChanges(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("{\"a\":1}"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := fingerprintPetMigrationSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte("{\"a\":2}"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := fingerprintPetMigrationSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(first) == "" || first == second {
		t.Fatalf("fingerprints = %q/%q", first, second)
	}
}
