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

func TestPetServiceGetRuntimeSnapshotExcludesHeavyPayloads(t *testing.T) {
	imagePath := `C:\\Users\\X1\\AppData\\Local\\pet\\dream.png`
	repository := &memoryPetRepository{
		snapshot: PetMigrationSnapshot{
			PetID:      DefaultPetID,
			State:      &PetState{PetID: DefaultPetID, Name: "Kapi"},
			Experience: &PetExperience{PetID: DefaultPetID, TotalExp: 12},
			Window:     &PetWindowConfig{PetID: DefaultPetID, Enabled: true},
			Care:       &PetCareConfig{PetID: DefaultPetID},
			Agent:      &PetAgentConfig{PetID: DefaultPetID, ProjectFolder: &imagePath},
			DreamConfig: &PetDreamConfig{
				PetID: DefaultPetID,
			},
			PlanRecords: []PetPlanRecord{{PetID: DefaultPetID, PlanID: "plan-1"}},
			Dreams:      []PetDreamHistoryRecord{{PetID: DefaultPetID, ID: "dream-1", ImagePath: &imagePath}},
			Memories:    []PetMemoryRecord{{PetID: DefaultPetID, ID: "memory-1", Text: "keep me out of runtime"}},
		},
	}

	runtimeSnapshot, err := NewPetService(repository).GetRuntimeSnapshot(DefaultPetID)
	if err != nil {
		t.Fatalf("GetRuntimeSnapshot() error = %v", err)
	}
	if runtimeSnapshot.State.Name != "Kapi" || runtimeSnapshot.Experience.TotalExp != 12 {
		t.Fatalf("runtime snapshot state = %#v, want hydrated runtime fields", runtimeSnapshot)
	}
	if runtimeSnapshot.Agent.ProjectFolder != nil {
		t.Fatalf("runtime snapshot leaked project folder: %#v", runtimeSnapshot.Agent.ProjectFolder)
	}

	raw, err := json.Marshal(runtimeSnapshot)
	if err != nil {
		t.Fatalf("marshal runtime snapshot: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode runtime snapshot JSON: %v", err)
	}
	for _, field := range []string{"state", "experience", "window", "care", "agent", "dream", "skinSelection"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("runtime snapshot JSON missing field %q: %s", field, raw)
		}
	}
	for _, field := range []string{"plans", "dreams", "memories", "skins", "atlas"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("runtime snapshot JSON contains heavy field %q: %s", field, raw)
		}
	}
	if strings.Contains(string(raw), "dream.png") || strings.Contains(string(raw), "keep me out of runtime") {
		t.Fatalf("runtime snapshot JSON contains persisted history/path data: %s", raw)
	}
}

func TestPetServiceGetSettingsSnapshotExcludesHeavyPayloads(t *testing.T) {
	imagePath := `C:\\Users\\X1\\AppData\\Local\\pet\\dream.png`
	repository := &memoryPetRepository{
		snapshot: PetMigrationSnapshot{
			State:       &PetState{PetID: DefaultPetID, Name: "Kapi"},
			Experience:  &PetExperience{PetID: DefaultPetID, TotalExp: 12},
			Window:      &PetWindowConfig{PetID: DefaultPetID, Enabled: true},
			Care:        &PetCareConfig{PetID: DefaultPetID},
			Agent:       &PetAgentConfig{PetID: DefaultPetID, ProjectFolder: &imagePath},
			DreamConfig: &PetDreamConfig{PetID: DefaultPetID},
			PlanRecords: []PetPlanRecord{{PetID: DefaultPetID, PlanID: "plan-1"}},
			Dreams:      []PetDreamHistoryRecord{{PetID: DefaultPetID, ID: "dream-1"}},
			Memories:    []PetMemoryRecord{{PetID: DefaultPetID, ID: "memory-1"}},
		},
	}

	snapshot, err := NewPetService(repository).GetSettingsSnapshot(DefaultPetID)
	if err != nil {
		t.Fatalf("GetSettingsSnapshot() error = %v", err)
	}
	if snapshot.State.Name != "Kapi" || snapshot.Agent.ProjectFolder != nil {
		t.Fatalf("settings snapshot core = %#v, want normalized core without project path", snapshot)
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal settings snapshot: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode settings snapshot JSON: %v", err)
	}
	for _, field := range []string{"plans", "dreams", "memories", "atlas"} {
		if _, ok := payload[field]; ok {
			t.Fatalf("settings snapshot contains heavy field %q: %s", field, raw)
		}
	}
}

func TestPetServiceGetAtlasKeepsResourceOutOfRuntimeSnapshot(t *testing.T) {
	service := NewPetService(&memoryPetRepository{})

	runtimeSnapshot, err := service.GetRuntimeSnapshot(DefaultPetID)
	if err != nil {
		t.Fatalf("GetRuntimeSnapshot() error = %v", err)
	}
	atlas, err := service.GetAtlas(DefaultPetID)
	if err != nil {
		t.Fatalf("GetAtlas() error = %v", err)
	}
	if atlas == nil || !strings.HasPrefix(atlas.Src, petAtlasDataURLPrefix) {
		t.Fatalf("GetAtlas() = %#v, want controlled PNG data URL", atlas)
	}

	runtimeJSON, err := json.Marshal(runtimeSnapshot)
	if err != nil {
		t.Fatalf("marshal runtime snapshot: %v", err)
	}
	if strings.Contains(string(runtimeJSON), "data:image/png") {
		t.Fatalf("runtime snapshot unexpectedly contains atlas data: %s", runtimeJSON)
	}
}

func TestPetServiceLightweightActionsReturnStateWithoutFullSnapshot(t *testing.T) {
	const now = int64(1_750_000_000_000)
	service := NewPetService(&memoryPetRepository{})

	petted, err := service.PettedForPet(DefaultPetID)
	if err != nil {
		t.Fatalf("PettedForPet() error = %v", err)
	}
	if !petted.OK || petted.State == nil || petted.State.Mood <= 70 {
		t.Fatalf("PettedForPet() = %#v, want success with updated state", petted)
	}

	state, err := service.RecordProactiveState(DefaultPetID, now)
	if err != nil {
		t.Fatalf("RecordProactiveState() error = %v", err)
	}
	if state.ProactiveCount != 1 || state.LastProactiveAt != now {
		t.Fatalf("RecordProactiveState() = %#v, want lightweight counter update", state)
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
		if result.State == nil || result.State.Hunger != 100 {
			t.Fatalf("business failure state = %#v, want current lightweight state", result.State)
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

func TestPetServiceEndWorkEarlyForPetContract(t *testing.T) {
	const (
		petID = "pet-early-end"
		now   = int64(1_700_000_000_000)
	)

	tests := []struct {
		name            string
		kind            PetAwayKind
		endsAt          int64
		wantOK          bool
		wantReason      PetActionFailureReason
		wantTaskAfter   bool
		wantCoinsAfter  int64
		wantGrowthAfter float64
		wantSaveCount   int
	}{
		{
			name:            "unfinished work can end early",
			kind:            PetAwayWork,
			endsAt:          now + time.Minute.Milliseconds(),
			wantOK:          true,
			wantTaskAfter:   false,
			wantCoinsAfter:  37,
			wantGrowthAfter: 11,
			wantSaveCount:   1,
		},
		{
			name:            "study cannot end early",
			kind:            PetAwayStudy,
			endsAt:          now + time.Minute.Milliseconds(),
			wantReason:      PetActionFailureBusy,
			wantTaskAfter:   true,
			wantCoinsAfter:  37,
			wantGrowthAfter: 11,
			wantSaveCount:   0,
		},
		{
			name:            "expired work is handled by normal settlement",
			kind:            PetAwayWork,
			endsAt:          now,
			wantReason:      PetActionFailureBusy,
			wantTaskAfter:   true,
			wantCoinsAfter:  37,
			wantGrowthAfter: 11,
			wantSaveCount:   0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &memoryPetRepository{
				snapshot: PetMigrationSnapshot{
					PetID: petID,
					State: &PetState{
						PetID:  petID,
						Coins:  37,
						Growth: 11,
						Hunger: 80,
						AwayTask: &PetAwayTask{
							Kind:      test.kind,
							StartedAt: now - time.Minute.Milliseconds(),
							EndsAt:    test.endsAt,
						},
					},
				},
			}
			service := NewPetServiceForPet(repository, petID)

			result, err := service.EndWorkEarlyForPet(petID, now)
			if err != nil {
				t.Fatalf("EndWorkEarlyForPet() error = %v", err)
			}
			if result.OK != test.wantOK || result.Reason != test.wantReason {
				t.Fatalf("EndWorkEarlyForPet() result = %#v, want ok=%v reason=%q", result, test.wantOK, test.wantReason)
			}

			persisted := repository.getSnapshot()
			if persisted.State == nil {
				t.Fatal("persisted state = nil")
			}
			if (persisted.State.AwayTask != nil) != test.wantTaskAfter {
				t.Fatalf("persisted away task = %#v, want present=%v", persisted.State.AwayTask, test.wantTaskAfter)
			}
			if persisted.State.Coins != test.wantCoinsAfter || persisted.State.Growth != test.wantGrowthAfter {
				t.Fatalf("persisted rewards = coins:%d growth:%f, want coins:%d growth:%f", persisted.State.Coins, persisted.State.Growth, test.wantCoinsAfter, test.wantGrowthAfter)
			}
			if repository.saveCount != test.wantSaveCount {
				t.Fatalf("save count = %d, want %d", repository.saveCount, test.wantSaveCount)
			}
		})
	}
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
			State:  &PetState{Name: "kept-state", Hunger: 42},
			Window: &PetWindowConfig{PetID: "pet-1", Enabled: true},
			Agent:  &PetAgentConfig{ProviderID: &providerID, ProactiveFreq: PetProactiveHigh},
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
	if !snapshot.Window.Enabled {
		t.Fatalf("window.enabled was cleared by a partial settings update: %#v", snapshot.Window)
	}

	// 空设置提交代表“没有更新任何分组”，不能把已有的 window.enabled 覆盖成 Go 零值 false。
	beforeEmptySaveCount := repository.saveCount
	emptySnapshot, err := service.SaveSettings("pet-1", PetSettingsInput{})
	if err != nil {
		t.Fatalf("SaveSettings() with empty settings error = %v", err)
	}
	if !emptySnapshot.Window.Enabled {
		t.Fatalf("empty SaveSettings() cleared window.enabled: %#v", emptySnapshot.Window)
	}
	if repository.saveCount != beforeEmptySaveCount+1 {
		t.Fatalf("empty settings save count = %d, want %d", repository.saveCount, beforeEmptySaveCount+1)
	}
	persisted := repository.getSnapshot()
	if persisted.Window == nil || !persisted.Window.Enabled {
		t.Fatalf("empty settings did not preserve persisted window.enabled: %#v", persisted.Window)
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

func TestPetServiceUpdateNameTrimsAndPreservesSnapshot(t *testing.T) {
	repository := &memoryPetRepository{
		snapshot: PetMigrationSnapshot{
			State:      &PetState{PetID: "pet-1", Name: "Before", Hunger: 42, Coins: 17},
			Experience: &PetExperience{PetID: "pet-1", TotalExp: 12},
		},
	}
	service := NewPetServiceForPet(repository, "pet-1")

	snapshot, err := service.UpdateName("pet-1", "  新名字  ")
	if err != nil {
		t.Fatalf("UpdateName() error = %v", err)
	}
	if snapshot.State.Name != "新名字" || snapshot.State.Hunger != 42 || snapshot.State.Coins != 17 {
		t.Fatalf("updated snapshot = %#v, want name-only update", snapshot.State)
	}
	if got := repository.saveCount; got != 1 {
		t.Fatalf("save count = %d, want 1", got)
	}
}

func TestPetServiceUpdateNameRejectsInvalidNameWithoutSaving(t *testing.T) {
	repository := &memoryPetRepository{snapshot: PetMigrationSnapshot{State: &PetState{Name: "Before"}}}
	service := NewPetService(repository)

	invalidNames := []string{"", "   ", "123456789012345678901"}
	for _, name := range invalidNames {
		if _, err := service.UpdateName(DefaultPetID, name); err == nil {
			t.Fatalf("UpdateName(%q) returned nil error", name)
		}
	}
	if got := repository.saveCount; got != 0 {
		t.Fatalf("save count = %d, want 0", got)
	}
}
