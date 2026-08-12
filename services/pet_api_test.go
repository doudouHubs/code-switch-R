package services

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPetServiceGetSnapshotReturnsStableJSONContract(t *testing.T) {
	service := NewPetService(&memoryPetRepository{})

	snapshot, err := service.GetSnapshot(DefaultPetID)
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if snapshot.State.PetID != DefaultPetID || snapshot.Experience.PetID != DefaultPetID {
		t.Fatalf("snapshot IDs = %#v, want %q", snapshot, DefaultPetID)
	}
	if snapshot.SkinSelection.PetID != DefaultPetID || snapshot.SkinSelection.ActiveSkinID != nil {
		t.Fatalf("default skin selection = %#v", snapshot.SkinSelection)
	}
	if snapshot.Skins == nil || len(snapshot.Skins) != len(builtinPetSkinIDs) {
		t.Fatalf("builtin skins contract = %#v, want %d bundled skins", snapshot.Skins, len(builtinPetSkinIDs))
	}
	for _, skinID := range builtinPetSkinIDs {
		found := false
		for _, skin := range snapshot.Skins {
			if skin.SkinID == skinID && skin.Builtin && len(skin.ManifestJSON) > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("builtin skin %q missing from snapshot: %#v", skinID, snapshot.Skins)
		}
	}
	if snapshot.Plans == nil || len(snapshot.Plans) != 0 {
		t.Fatalf("empty plans contract = %#v, want non-nil empty slice", snapshot.Plans)
	}
	if snapshot.Dreams == nil || len(snapshot.Dreams) != 0 {
		t.Fatalf("empty dreams contract = %#v, want non-nil empty slice", snapshot.Dreams)
	}
	if snapshot.Memories == nil || len(snapshot.Memories) != 0 {
		t.Fatalf("empty memories contract = %#v, want non-nil empty slice", snapshot.Memories)
	}
	if snapshot.Atlas == nil {
		t.Fatal("atlas = nil, want a bundled pet atlas asset")
	}
	if !strings.HasPrefix(snapshot.Atlas.Src, petAtlasDataURLPrefix) {
		srcPreview := snapshot.Atlas.Src
		if len(srcPreview) > 64 {
			srcPreview = srcPreview[:64] + "..."
		}
		t.Fatalf("atlas src = %q, want a controlled PNG data URL", srcPreview)
	}
	if len(snapshot.Atlas.Manifest) == 0 || !json.Valid(snapshot.Atlas.Manifest) {
		t.Fatalf("atlas manifest = %s, want valid JSON", snapshot.Atlas.Manifest)
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode snapshot JSON: %v", err)
	}
	wantFields := []string{"state", "experience", "window", "care", "agent", "dream", "plans", "dreams", "memories", "skinSelection", "skins", "atlas"}
	for _, field := range wantFields {
		if _, ok := payload[field]; !ok {
			t.Fatalf("snapshot JSON missing field %q: %s", field, raw)
		}
	}
	if string(payload["atlas"]) == "null" {
		t.Fatalf("atlas JSON = %s, want a resource asset", payload["atlas"])
	}
	if string(payload["skins"]) == "[]" {
		t.Fatalf("skins JSON = %s, want bundled skins", payload["skins"])
	}
	for _, field := range []string{"plans", "dreams", "memories"} {
		if string(payload[field]) != "[]" {
			t.Fatalf("%s JSON = %s, want []", field, payload[field])
		}
	}
}

func TestPetSnapshotExposesPersistedRecordsWithoutSensitivePaths(t *testing.T) {
	imagePath := `C:\\Users\\X1\\AppData\\Local\\pet\\dream.png`
	repository := &memoryPetRepository{
		snapshot: PetMigrationSnapshot{
			PetID:      DefaultPetID,
			State:      &PetState{PetID: DefaultPetID, Name: "Kapi"},
			Experience: &PetExperience{PetID: DefaultPetID},
			Care:       &PetCareConfig{PetID: DefaultPetID},
			Agent:      &PetAgentConfig{PetID: DefaultPetID, ProjectFolder: &imagePath},
			DreamConfig: &PetDreamConfig{
				PetID: DefaultPetID,
			},
			Window: &PetWindowConfig{PetID: DefaultPetID, Enabled: true},
			PlanRecords: []PetPlanRecord{{
				PetID:  DefaultPetID,
				PlanID: "plan-1",
				Title:  "每日照护",
				Script: PetPlanScript{Version: 1},
			}},
			Dreams: []PetDreamHistoryRecord{{
				PetID:     DefaultPetID,
				ID:        "dream-1",
				Title:     "星星",
				Keywords:  nil,
				ImagePath: &imagePath,
				Dream:     "在海边散步",
			}},
			Memories: []PetMemoryRecord{{
				PetID: DefaultPetID,
				ID:    "memory-1",
				Text:  "用户喜欢海边",
			}},
		},
	}

	snapshot, err := NewPetService(repository).GetSnapshot(DefaultPetID)
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if len(snapshot.Plans) != 1 || snapshot.Plans[0].PlanID != "plan-1" {
		t.Fatalf("plans = %#v, want persisted plan", snapshot.Plans)
	}
	if snapshot.Plans[0].Script.Steps == nil {
		t.Fatal("plan script steps = nil, want []")
	}
	if len(snapshot.Dreams) != 1 || snapshot.Dreams[0].ID != "dream-1" {
		t.Fatalf("dreams = %#v, want persisted dream", snapshot.Dreams)
	}
	if snapshot.Dreams[0].Keywords == nil || snapshot.Dreams[0].ImagePath != nil {
		t.Fatalf("dream security/array contract = %#v, want imagePath nil and keywords []", snapshot.Dreams[0])
	}
	if len(snapshot.Memories) != 1 || snapshot.Memories[0].ID != "memory-1" {
		t.Fatalf("memories = %#v, want persisted memory", snapshot.Memories)
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	encodedPath, err := json.Marshal(imagePath)
	if err != nil {
		t.Fatalf("marshal image path: %v", err)
	}
	if strings.Contains(string(raw), string(encodedPath)) || strings.Contains(string(raw), "apiKey") {
		t.Fatalf("snapshot JSON contains forbidden sensitive data: %s", raw)
	}
}

func TestPetServicePerformActionSeparatesBusinessAndPersistenceErrors(t *testing.T) {
	t.Run("business failure is result", func(t *testing.T) {
		repository := &memoryPetRepository{
			snapshot: PetMigrationSnapshot{
				State: &PetState{Hunger: 100},
			},
		}
		service := NewPetService(repository)

		result, err := service.PerformAction(DefaultPetID, PetActionFeed)
		if err != nil {
			t.Fatalf("PerformAction() error = %v, want nil for business failure", err)
		}
		if result.OK || result.Reason != PetActionFailureFull {
			t.Fatalf("PerformAction() result = %#v, want full failure", result)
		}
	})

	t.Run("persistence failure is error", func(t *testing.T) {
		saveErr := errors.New("save failed")
		service := NewPetService(&memoryPetRepository{saveErr: saveErr})

		result, err := service.PerformAction(DefaultPetID, PetActionFeed)
		if !errors.Is(err, saveErr) {
			t.Fatalf("PerformAction() error = %v, want wrapped save error", err)
		}
		if result.OK {
			t.Fatalf("PerformAction() result = %#v, persistence failure must not report success", result)
		}
	})

	t.Run("unknown action is parameter error", func(t *testing.T) {
		service := NewPetService(&memoryPetRepository{})

		result, err := service.PerformAction(DefaultPetID, PetAction("dance"))
		if err == nil || !strings.Contains(err.Error(), "不支持") {
			t.Fatalf("PerformAction() error = %v, want unsupported action error", err)
		}
		if result != (PetActionResult{}) {
			t.Fatalf("PerformAction() result = %#v, want zero result on parameter error", result)
		}
	})
}

func TestPetServiceExplicitPetLifecycleCountersPersist(t *testing.T) {
	repository := &memoryPetRepository{}
	service := NewPetService(repository)
	dayOne := time.Date(2026, 8, 12, 10, 0, 0, 0, time.Local).UnixMilli()

	snapshot, err := service.RecordProactive("pet-1", dayOne)
	if err != nil {
		t.Fatalf("RecordProactive() error = %v", err)
	}
	if snapshot.State.ProactiveCount != 1 || snapshot.State.LastProactiveAt != dayOne {
		t.Fatalf("first proactive snapshot = %#v", snapshot.State)
	}

	snapshot, err = service.RecordProactive("pet-1", dayOne+time.Hour.Milliseconds())
	if err != nil {
		t.Fatalf("second RecordProactive() error = %v", err)
	}
	if snapshot.State.ProactiveCount != 2 {
		t.Fatalf("same-day proactive count = %d, want 2", snapshot.State.ProactiveCount)
	}

	bonus, err := service.ClaimDailyBonusForPet("pet-1", dayOne)
	if err != nil {
		t.Fatalf("ClaimDailyBonusForPet() error = %v", err)
	}
	if bonus.Bonus != PetDailyBonusCoins || bonus.Snapshot.State.LastDailyBonusDate != PetLocalDateKey(dayOne) {
		t.Fatalf("daily bonus result = %#v", bonus)
	}
	repeat, err := service.ClaimDailyBonusForPet("pet-1", dayOne+time.Hour.Milliseconds())
	if err != nil {
		t.Fatalf("repeated ClaimDailyBonusForPet() error = %v", err)
	}
	if repeat.Bonus != 0 || repeat.Snapshot.State.Coins != bonus.Snapshot.State.Coins {
		t.Fatalf("repeated daily bonus = %#v, want no second reward", repeat)
	}

	snapshot, err = service.MarkMilestoneForPet("pet-1", 7)
	if err != nil {
		t.Fatalf("MarkMilestoneForPet() error = %v", err)
	}
	if snapshot.State.LastMilestoneDays != 7 {
		t.Fatalf("milestone = %d, want 7", snapshot.State.LastMilestoneDays)
	}
	snapshot, err = service.MarkMilestoneForPet("pet-1", 3)
	if err != nil {
		t.Fatalf("older MarkMilestoneForPet() error = %v", err)
	}
	if snapshot.State.LastMilestoneDays != 7 {
		t.Fatalf("milestone regressed to %d", snapshot.State.LastMilestoneDays)
	}
}

func TestPetServiceSaveSettingsMergesAndValidatesPetID(t *testing.T) {
	providerID := "provider-before"
	repository := &memoryPetRepository{
		snapshot: PetMigrationSnapshot{
			State: &PetState{Name: "kept-state", Hunger: 42},
			Agent: &PetAgentConfig{ProviderID: &providerID, ProactiveFreq: PetProactiveHigh},
		},
	}
	service := NewPetService(repository)

	var settings PetSettingsInput
	if err := json.Unmarshal([]byte(`{
		"care": {"petId": "pet-1", "autoCareEnabled": true, "autoCareThreshold": 37},
		"agent": {"providerId": "provider-after", "apiKey": "must-not-be-persisted"}
	}`), &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}

	snapshot, err := service.SaveSettings(" pet-1 ", settings)
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	if snapshot.State.Name != "kept-state" || snapshot.State.Hunger != 42 {
		t.Fatalf("state was not preserved: %#v", snapshot.State)
	}
	if !snapshot.Care.AutoCareEnabled || snapshot.Care.AutoCareThreshold != 35 || snapshot.Care.PetID != "pet-1" {
		t.Fatalf("merged care = %#v", snapshot.Care)
	}
	if snapshot.Agent.ProviderID == nil || *snapshot.Agent.ProviderID != "provider-after" {
		t.Fatalf("merged agent = %#v", snapshot.Agent)
	}
	encoded, err := json.Marshal(snapshot.Agent)
	if err != nil {
		t.Fatalf("marshal agent: %v", err)
	}
	if strings.Contains(string(encoded), "apiKey") {
		t.Fatalf("agent JSON contains forbidden API key field: %s", encoded)
	}

	beforeSaveCount := repository.saveCount
	settings.Window = &PetWindowConfig{PetID: "other-pet", Enabled: true}
	if _, err := service.SaveSettings("pet-1", settings); err == nil {
		t.Fatal("SaveSettings() with mismatched nested petId returned nil error")
	}
	if repository.saveCount != beforeSaveCount {
		t.Fatalf("invalid petId changed persistence count: got %d, want %d", repository.saveCount, beforeSaveCount)
	}
}
