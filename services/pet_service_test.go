package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	modelpricing "codeswitch/resources/model-pricing"
)

type memoryPetRepository struct {
	mu sync.Mutex

	snapshot  PetMigrationSnapshot
	snapshots map[string]PetMigrationSnapshot
	loadErr   error
	saveErr   error
	appendErr error
	listErr   error

	saveCount    int
	appendCount  int
	appended     []PetExpLogEntry
	lastPage     int
	lastPageSize int
}

func (r *memoryPetRepository) LoadSnapshot(_ context.Context, petID string) (PetMigrationSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return PetMigrationSnapshot{}, r.loadErr
	}
	if r.snapshots != nil {
		return clonePetSnapshotForTest(r.snapshots[petID]), nil
	}
	return clonePetSnapshotForTest(r.snapshot), nil
}

func (r *memoryPetRepository) SaveSnapshot(_ context.Context, snapshot PetMigrationSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	if r.snapshots != nil {
		r.snapshots[snapshot.PetID] = clonePetSnapshotForTest(snapshot)
	} else {
		r.snapshot = clonePetSnapshotForTest(snapshot)
	}
	r.saveCount++
	return nil
}

func (r *memoryPetRepository) AppendExpLog(_ context.Context, entry PetExpLogEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.appendErr != nil {
		return r.appendErr
	}
	r.appended = append(r.appended, entry)
	r.appendCount++
	return nil
}

func (r *memoryPetRepository) ListExpLog(_ context.Context, _ string, page, pageSize int) (PetExpLogPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return PetExpLogPage{}, r.listErr
	}
	r.lastPage = page
	r.lastPageSize = pageSize
	return PetExpLogPage{
		Entries:  append([]PetExpLogEntry(nil), r.snapshot.ExpLog...),
		Page:     page,
		PageSize: pageSize,
		Total:    len(r.snapshot.ExpLog),
	}, nil
}

func (r *memoryPetRepository) setSnapshot(snapshot PetMigrationSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot = clonePetSnapshotForTest(snapshot)
}

func (r *memoryPetRepository) getSnapshot() PetMigrationSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return clonePetSnapshotForTest(r.snapshot)
}

func (r *memoryPetRepository) getSnapshotForPet(petID string) PetMigrationSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshots != nil {
		return clonePetSnapshotForTest(r.snapshots[petID])
	}
	return clonePetSnapshotForTest(r.snapshot)
}

func clonePetSnapshotForTest(snapshot PetMigrationSnapshot) PetMigrationSnapshot {
	clone := snapshot
	if snapshot.State != nil {
		state := *snapshot.State
		if snapshot.State.AwayTask != nil {
			awayTask := *snapshot.State.AwayTask
			state.AwayTask = &awayTask
		}
		clone.State = &state
	}
	if snapshot.Experience != nil {
		experience := *snapshot.Experience
		clone.Experience = &experience
	}
	if snapshot.Care != nil {
		care := *snapshot.Care
		clone.Care = &care
	}
	if snapshot.Agent != nil {
		agent := *snapshot.Agent
		clone.Agent = &agent
	}
	if snapshot.DreamConfig != nil {
		dream := *snapshot.DreamConfig
		clone.DreamConfig = &dream
	}
	if snapshot.Window != nil {
		window := *snapshot.Window
		clone.Window = &window
	}
	clone.ExpLog = append([]PetExpLogEntry(nil), snapshot.ExpLog...)
	clone.PlanRecords = append([]PetPlanRecord(nil), snapshot.PlanRecords...)
	clone.Skins = append([]PetSkinRecord(nil), snapshot.Skins...)
	clone.Dreams = append([]PetDreamHistoryRecord(nil), snapshot.Dreams...)
	clone.Memories = append([]PetMemoryRecord(nil), snapshot.Memories...)
	return clone
}

func TestPetServiceDefaultsAndActionPersistFullSnapshot(t *testing.T) {
	repository := &memoryPetRepository{
		snapshot: PetMigrationSnapshot{
			PlanRecords: []PetPlanRecord{{}},
			Memories:    []PetMemoryRecord{{ID: "memory-1"}},
		},
	}
	service := NewPetService(repository)

	result, err := service.Feed()
	if err != nil {
		t.Fatalf("Feed() error = %v", err)
	}
	if !result.OK {
		t.Fatalf("Feed() result = %#v, want success", result)
	}

	snapshot := repository.getSnapshot()
	if snapshot.State == nil || snapshot.State.Hunger != 100 || snapshot.State.Coins != 110 {
		t.Fatalf("saved state = %#v, want default state after feeding", snapshot.State)
	}
	if snapshot.Experience == nil || snapshot.Care == nil || snapshot.Agent == nil || snapshot.DreamConfig == nil || snapshot.Window == nil {
		t.Fatalf("default sub-snapshots were not materialized: %#v", snapshot)
	}
	if !snapshot.Window.Enabled {
		t.Fatalf("default window config = %#v, want enabled by default", snapshot.Window)
	}
	if snapshot.Care.AutoCareThreshold != PetAutoCareDefaultThreshold {
		t.Fatalf("default care threshold = %d, want %d", snapshot.Care.AutoCareThreshold, PetAutoCareDefaultThreshold)
	}
	if !snapshot.DreamConfig.DreamEnabled || snapshot.DreamConfig.SleepTalkMinLength != PetDreamDefaultSleepTalkLength {
		t.Fatalf("default dream config = %#v", snapshot.DreamConfig)
	}
	if len(snapshot.PlanRecords) != 1 || len(snapshot.Memories) != 1 {
		t.Fatalf("unrelated snapshot fields were lost: %#v", snapshot)
	}
}

func TestPetServicePropagatesRepositoryErrors(t *testing.T) {
	loadErr := errors.New("load failed")
	service := NewPetService(&memoryPetRepository{loadErr: loadErr})
	if _, err := service.GetState(); !errors.Is(err, loadErr) {
		t.Fatalf("GetState() error = %v, want wrapped load error", err)
	}

	saveErr := errors.New("save failed")
	saveRepository := &memoryPetRepository{}
	saveRepository.saveErr = saveErr
	service = NewPetService(saveRepository)
	result, err := service.Feed()
	if !errors.Is(err, saveErr) {
		t.Fatalf("Feed() error = %v, want wrapped save error", err)
	}
	if result.OK {
		t.Fatalf("Feed() result = %#v, persistence failure must not report success", result)
	}

	appendErr := errors.New("append failed")
	appendRepository := &memoryPetRepository{appendErr: appendErr}
	service = NewPetService(appendRepository)
	if _, err := service.AddExperience(PetExpLogEntry{ID: "exp-1", Exp: 1}); !errors.Is(err, appendErr) {
		t.Fatalf("AddExperience() error = %v, want wrapped append error", err)
	}

	listErr := errors.New("list failed")
	listRepository := &memoryPetRepository{listErr: listErr}
	service = NewPetService(listRepository)
	if _, err := service.ListExpLog(0, 0); !errors.Is(err, listErr) {
		t.Fatalf("ListExpLog() error = %v, want wrapped list error", err)
	}
}

func TestPetServicePettedSupportsLegacyAndExplicitPetIDRouting(t *testing.T) {
	defaultState := DefaultPetStateAt(time.Now().UnixMilli())
	defaultState.Mood = 10
	otherState := DefaultPetStateAt(time.Now().UnixMilli())
	otherState.Mood = 20
	repository := &memoryPetRepository{
		snapshots: map[string]PetMigrationSnapshot{
			DefaultPetID: {State: &defaultState},
			"pet-2":      {State: &otherState},
		},
	}
	service := NewPetService(repository)

	// 内部旧调用者仍可直接走 helper；Wails 对外入口必须使用固定 petId 参数。
	if err := service.petted(); err != nil {
		t.Fatalf("petted() error = %v", err)
	}
	// 显式 ID 必须沿用 API 层的路由，不能把 pet-2 的动作写回 default 分区。
	if err := service.Petted(" pet-2 "); err != nil {
		t.Fatalf("Petted(pet-2) error = %v", err)
	}

	defaultSnapshot := repository.getSnapshotForPet(DefaultPetID)
	otherSnapshot := repository.getSnapshotForPet("pet-2")
	if defaultSnapshot.State == nil || defaultSnapshot.State.Mood != 13 {
		t.Fatalf("default pet after legacy Petted = %#v, want mood 13", defaultSnapshot.State)
	}
	if otherSnapshot.State == nil || otherSnapshot.State.Mood != 23 {
		t.Fatalf("explicit pet after Petted = %#v, want mood 23", otherSnapshot.State)
	}
	if defaultSnapshot.State.PetID != DefaultPetID || otherSnapshot.State.PetID != "pet-2" {
		t.Fatalf("pet IDs after Petted = default:%q other:%q", defaultSnapshot.State.PetID, otherSnapshot.State.PetID)
	}

	// PerformAction 是现有内部无参调用点的真实入口，必须继续复用同一业务实现。
	result, err := service.PerformAction("pet-2", PetAction("petted"))
	if err != nil || !result.OK {
		t.Fatalf("PerformAction(petted) result=%#v error=%v, want success", result, err)
	}
	if snapshot := repository.getSnapshotForPet("pet-2"); snapshot.State.Mood != 26 {
		t.Fatalf("pet-2 after PerformAction(petted) = %#v, want mood 26", snapshot.State)
	}
}

func TestPetServicePettedRejectsInvalidIDsAndPropagatesRepositoryErrors(t *testing.T) {
	service := NewPetService(&memoryPetRepository{})
	if err := service.Petted(""); err == nil || !strings.Contains(err.Error(), "petId 不能为空") {
		t.Fatalf("Petted(empty petId) error = %v, want explicit petId validation error", err)
	}
	loadErr := errors.New("load failed")
	service = NewPetService(&memoryPetRepository{loadErr: loadErr})
	if err := service.Petted("pet-2"); !errors.Is(err, loadErr) {
		t.Fatalf("Petted() load error = %v, want wrapped repository error", err)
	}

	saveErr := errors.New("save failed")
	service = NewPetService(&memoryPetRepository{saveErr: saveErr})
	if err := service.Petted("pet-2"); !errors.Is(err, saveErr) {
		t.Fatalf("Petted() save error = %v, want wrapped repository error", err)
	}
}

func TestPetServiceExperienceLedgerRetroCoinsAndIdempotency(t *testing.T) {
	repository := &memoryPetRepository{}
	service := NewPetService(repository)

	first, err := service.AddExperience(PetExpLogEntry{ID: "exp-1", At: 100, Model: "model-a", Tokens: 10, Exp: 500})
	if err != nil {
		t.Fatalf("first AddExperience() error = %v", err)
	}
	if first.TotalExp != 500 || first.TotalTokens != 10 {
		t.Fatalf("first experience = %#v", first)
	}
	snapshot := repository.getSnapshot()
	if snapshot.State.Coins != 320 || snapshot.State.CoinCreditedExp != 500 {
		t.Fatalf("retroactive coin credit state = %#v", snapshot.State)
	}
	if len(snapshot.ExpLog) != 1 || repository.appendCount != 1 {
		t.Fatalf("first experience log = %#v appendCount=%d", snapshot.ExpLog, repository.appendCount)
	}

	duplicate, err := service.AddExperience(PetExpLogEntry{ID: "exp-1", Tokens: 999, Exp: 999})
	if err != nil {
		t.Fatalf("duplicate AddExperience() error = %v", err)
	}
	if duplicate != first || repository.appendCount != 1 {
		t.Fatalf("duplicate experience was applied: got=%#v appendCount=%d", duplicate, repository.appendCount)
	}

	second, err := service.AddExperience(PetExpLogEntry{ID: "exp-2", Tokens: 5, Exp: 50})
	if err != nil {
		t.Fatalf("second AddExperience() error = %v", err)
	}
	if second.TotalExp != 550 || second.TotalTokens != 15 {
		t.Fatalf("second experience = %#v", second)
	}
	snapshot = repository.getSnapshot()
	if snapshot.State.Coins != 370 || snapshot.State.CoinCreditedExp != 550 {
		t.Fatalf("incremental coin credit state = %#v", snapshot.State)
	}
	if len(snapshot.ExpLog) != 2 || snapshot.ExpLog[0].ID != "exp-2" {
		t.Fatalf("experience log ordering = %#v", snapshot.ExpLog)
	}
}

func TestPetServiceAddExperienceFromUsageUsesCanonicalPricing(t *testing.T) {
	repository := &memoryPetRepository{}
	service := NewPetService(repository)

	// claude-sonnet-4-5-20250929 在目标 canonical pricing 中输入价为 3 USD/M，
	// 与 OpenCowork 的 >2 premium 规则一致，1000 token 应得到 2 点经验。
	experience, err := service.AddExperienceFromUsage(PetUsageEvent{
		ID:       "usage-premium-1",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5-20250929",
		At:       100,
		Usage: modelpricing.UsageSnapshot{
			InputTokens:  750,
			OutputTokens: 250,
		},
	})
	if err != nil {
		t.Fatalf("AddExperienceFromUsage() error = %v", err)
	}
	if experience.TotalExp != 2 || experience.TotalTokens != 1000 {
		t.Fatalf("experience = %#v, want premium 1000-token accounting", experience)
	}

	snapshot := repository.getSnapshot()
	if len(snapshot.ExpLog) != 1 || !snapshot.ExpLog[0].Premium || snapshot.ExpLog[0].Model != "claude-sonnet-4-5-20250929" {
		t.Fatalf("canonical usage log = %#v", snapshot.ExpLog)
	}
}

func TestPetStreamUsagePayloadToPetUsageEventPreservesCanonicalFields(t *testing.T) {
	payload := &PetStreamUsagePayload{
		ID:                "chat:usage-1",
		At:                123,
		Provider:          "openai/openai-main",
		Model:             "gpt-5",
		InputTokens:       100,
		OutputTokens:      25,
		ReasoningTokens:   7,
		CacheCreateTokens: 11,
		CacheReadTokens:   13,
		Ephemeral5mTokens: 17,
		Ephemeral1hTokens: 19,
		ServiceTier:       "PRIORITY",
	}
	event, err := payload.ToPetUsageEvent()
	if err != nil {
		t.Fatalf("ToPetUsageEvent() error = %v", err)
	}
	if event.ID != payload.ID || event.Provider != payload.Provider || event.Model != payload.Model || event.At != payload.At {
		t.Fatalf("event identity = %#v", event)
	}
	if event.Usage.InputTokens != 100 || event.Usage.OutputTokens != 25 || event.Usage.ReasoningTokens != 7 ||
		event.Usage.CacheCreateTokens != 11 || event.Usage.CacheReadTokens != 13 || event.Usage.ServiceTier != modelpricing.ServiceTierPriority {
		t.Fatalf("event usage = %#v", event.Usage)
	}
	if event.Usage.CacheCreation == nil || event.Usage.CacheCreation.Ephemeral5mTokens != 17 || event.Usage.CacheCreation.Ephemeral1hTokens != 19 {
		t.Fatalf("event cache creation = %#v", event.Usage.CacheCreation)
	}
	if _, err := (*PetStreamUsagePayload)(nil).ToPetUsageEvent(); err == nil {
		t.Fatal("nil payload should fail")
	}
}

func TestPetServiceAddExperienceFromRequestLogUsesStableEventID(t *testing.T) {
	repository := &memoryPetRepository{}
	service := NewPetService(repository)
	request := ReqeustLog{
		ID:           42,
		Provider:     "openai",
		Model:        "gpt-5",
		InputTokens:  900,
		OutputTokens: 100,
	}

	first, err := service.AddExperienceFromRequestLog(request)
	if err != nil {
		t.Fatalf("first AddExperienceFromRequestLog() error = %v", err)
	}
	request.InputTokens = 9999
	request.OutputTokens = 9999
	second, err := service.AddExperienceFromRequestLog(request)
	if err != nil {
		t.Fatalf("duplicate AddExperienceFromRequestLog() error = %v", err)
	}
	if second != first || repository.appendCount != 1 || len(repository.getSnapshot().ExpLog) != 1 {
		t.Fatalf("request usage was applied more than once: first=%#v second=%#v appendCount=%d", first, second, repository.appendCount)
	}
}

func TestPetServiceAddExperienceFromUsageNoUsageDoesNotWrite(t *testing.T) {
	repository := &memoryPetRepository{}
	service := NewPetService(repository)

	before, err := service.GetExperience()
	if err != nil {
		t.Fatalf("GetExperience() error = %v", err)
	}
	after, err := service.AddExperienceFromUsage(PetUsageEvent{
		Usage: modelpricing.UsageSnapshot{},
	})
	if err != nil {
		t.Fatalf("empty usage should be a no-op, got %v", err)
	}
	if after != before || repository.appendCount != 0 || repository.saveCount != 0 {
		t.Fatalf("empty usage changed ledger: before=%#v after=%#v append=%d save=%d", before, after, repository.appendCount, repository.saveCount)
	}
}

func TestPetServiceAddExperienceFromUsageRejectsMissingProviderOrModel(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
	}{
		{name: "provider", provider: "", model: "gpt-5"},
		{name: "model", provider: "openai", model: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository := &memoryPetRepository{}
			service := NewPetService(repository)
			_, err := service.AddExperienceFromUsage(PetUsageEvent{
				ID:       "missing-" + tt.name,
				Provider: tt.provider,
				Model:    tt.model,
				Usage: modelpricing.UsageSnapshot{
					InputTokens: 1000,
				},
			})
			if err == nil || !strings.Contains(err.Error(), "缺少 "+tt.name) {
				t.Fatalf("error = %v, want missing %s validation", err, tt.name)
			}
			if repository.appendCount != 0 || repository.saveCount != 0 {
				t.Fatalf("invalid usage wrote ledger: append=%d save=%d", repository.appendCount, repository.saveCount)
			}
		})
	}
}

func TestPetServiceAddExperienceFromUsageRejectsUnknownCanonicalModel(t *testing.T) {
	repository := &memoryPetRepository{}
	service := NewPetService(repository)

	_, err := service.AddExperienceFromUsage(PetUsageEvent{
		ID:       "unknown-model-1",
		Provider: "provider",
		Model:    "model-that-is-not-in-canonical-pricing",
		Usage: modelpricing.UsageSnapshot{
			InputTokens: 1000,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "缺少 canonical pricing") {
		t.Fatalf("error = %v, want canonical pricing failure", err)
	}
	if repository.appendCount != 0 || repository.saveCount != 0 {
		t.Fatalf("unknown model wrote ledger: append=%d save=%d", repository.appendCount, repository.saveCount)
	}
}

func TestPetServiceConcurrentExperienceDoesNotOverwriteLedger(t *testing.T) {
	repository := &memoryPetRepository{}
	service := NewPetService(repository)

	const count = 32
	var waitGroup sync.WaitGroup
	errorsCh := make(chan error, count)
	for i := 0; i < count; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			_, err := service.AddExperience(PetExpLogEntry{
				ID:     "concurrent-" + string(rune('a'+index)),
				Tokens: 100,
				Exp:    1,
			})
			if err != nil {
				errorsCh <- err
			}
		}(i)
	}
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent AddExperience() error = %v", err)
	}

	snapshot := repository.getSnapshot()
	if snapshot.Experience.TotalExp != count || snapshot.Experience.TotalTokens != count*100 {
		t.Fatalf("concurrent experience = %#v", snapshot.Experience)
	}
	if snapshot.State.Coins != 120+count || snapshot.State.CoinCreditedExp != count {
		t.Fatalf("concurrent coin credit = %#v", snapshot.State)
	}
	if len(snapshot.ExpLog) != count || repository.appendCount != count {
		t.Fatalf("concurrent logs len=%d appendCount=%d", len(snapshot.ExpLog), repository.appendCount)
	}
}

func TestPetServiceResolveAwayTaskIsIdempotent(t *testing.T) {
	start := time.Date(2026, 8, 11, 9, 0, 0, 0, time.Local).UnixMilli()
	state := DefaultPetStateAt(start)
	state.AwayTask = &PetAwayTask{
		Kind:      PetAwayStudy,
		StartedAt: start,
		EndsAt:    start + PetStudyDuration.Milliseconds(),
	}
	repository := &memoryPetRepository{snapshot: PetMigrationSnapshot{State: &state}}
	service := NewPetService(repository)

	reward, err := service.ResolveAwayTask(state.AwayTask.EndsAt)
	if err != nil {
		t.Fatalf("ResolveAwayTask() error = %v", err)
	}
	if reward == nil || reward.Kind != PetAwayStudy || reward.Growth != PetStudyRewardGrowth {
		t.Fatalf("first away reward = %#v", reward)
	}
	snapshot := repository.getSnapshot()
	if snapshot.State.AwayTask != nil || snapshot.State.Growth != PetStudyRewardGrowth {
		t.Fatalf("resolved state = %#v", snapshot.State)
	}
	saves := repository.saveCount

	reward, err = service.ResolveAwayTask(state.AwayTask.EndsAt + time.Hour.Milliseconds())
	if err != nil {
		t.Fatalf("duplicate ResolveAwayTask() error = %v", err)
	}
	if reward != nil || repository.saveCount != saves {
		t.Fatalf("duplicate away resolution reward=%#v saveCount=%d want=%d", reward, repository.saveCount, saves)
	}
}

func TestPetServiceNormalizesTimeAndConfigs(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.Local).UnixMilli()
	future := DefaultPetStateAt(now + time.Hour.Milliseconds())
	future.LastTickAt = now + 24*time.Hour.Milliseconds()
	future.Sleeping = false
	future.SleepEndsAt = now + time.Hour.Milliseconds()
	repository := &memoryPetRepository{snapshot: PetMigrationSnapshot{State: &future}}
	service := NewPetService(repository)

	state, err := service.Tick(now)
	if err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if state.LastTickAt != now || state.SleepEndsAt != 0 || state.PetID != DefaultPetID {
		t.Fatalf("normalized abnormal state = %#v", state)
	}

	care, err := service.GetCareConfig()
	if err != nil || care.AutoCareThreshold != PetAutoCareDefaultThreshold {
		t.Fatalf("default care config = %#v error=%v", care, err)
	}
	dream, err := service.GetDreamConfig()
	if err != nil || !dream.DreamEnabled || dream.SleepTalkMinLength != PetDreamDefaultSleepTalkLength {
		t.Fatalf("default dream config = %#v error=%v", dream, err)
	}

	if err := service.SaveCareConfig(PetCareConfig{AutoCareEnabled: true, AutoCareThreshold: 999}); err != nil {
		t.Fatalf("SaveCareConfig() error = %v", err)
	}
	if err := service.SaveDreamConfig(PetDreamConfig{DreamEnabled: true, SleepTalkMinLength: 999, BubbleMinDurationSeconds: 1}); err != nil {
		t.Fatalf("SaveDreamConfig() error = %v", err)
	}
	snapshot := repository.getSnapshot()
	if !snapshot.Care.AutoCareEnabled || snapshot.Care.AutoCareThreshold != PetAutoCareMaxThreshold {
		t.Fatalf("normalized saved care config = %#v", snapshot.Care)
	}
	if snapshot.DreamConfig.SleepTalkMinLength != PetDreamMaxSleepTalkLength || snapshot.DreamConfig.BubbleMinDurationSeconds != PetDreamMinBubbleDurationSeconds {
		t.Fatalf("normalized saved dream config = %#v", snapshot.DreamConfig)
	}
}

func TestPetServiceNormalizesDreamImageReferenceAsAnAtomicTuple(t *testing.T) {
	providerPlatform := "  openai  "
	providerID := "  image-provider  "
	modelID := "  image-model  "
	repository := &memoryPetRepository{}
	service := NewPetService(repository)

	if err := service.SaveDreamConfig(PetDreamConfig{
		DreamEnabled:          true,
		ImageProviderPlatform: &providerPlatform,
		ImageProviderID:       &providerID,
		ImageModelID:          &modelID,
	}); err != nil {
		t.Fatalf("SaveDreamConfig() with image reference error = %v", err)
	}
	snapshot := repository.getSnapshot()
	if snapshot.DreamConfig.ImageProviderPlatform == nil || *snapshot.DreamConfig.ImageProviderPlatform != "openai" ||
		snapshot.DreamConfig.ImageProviderID == nil || *snapshot.DreamConfig.ImageProviderID != "image-provider" ||
		snapshot.DreamConfig.ImageModelID == nil || *snapshot.DreamConfig.ImageModelID != "image-model" {
		t.Fatalf("normalized image reference = %#v", snapshot.DreamConfig)
	}

	missingModel := ""
	if err := service.SaveDreamConfig(PetDreamConfig{
		DreamEnabled:          true,
		ImageProviderPlatform: &providerPlatform,
		ImageProviderID:       &providerID,
		ImageModelID:          &missingModel,
	}); err != nil {
		t.Fatalf("SaveDreamConfig() with incomplete image reference error = %v", err)
	}
	snapshot = repository.getSnapshot()
	if snapshot.DreamConfig.ImageProviderPlatform != nil || snapshot.DreamConfig.ImageProviderID != nil || snapshot.DreamConfig.ImageModelID != nil {
		t.Fatalf("incomplete image reference was not cleared = %#v", snapshot.DreamConfig)
	}
}

func TestPetServiceListExpLogNormalizesPaging(t *testing.T) {
	repository := &memoryPetRepository{}
	service := NewPetService(repository)

	if _, err := service.ListExpLog(0, 0); err != nil {
		t.Fatalf("ListExpLog() error = %v", err)
	}
	if repository.lastPage != petLogDefaultPage || repository.lastPageSize != petLogDefaultPageSize {
		t.Fatalf("paging = page %d size %d", repository.lastPage, repository.lastPageSize)
	}
}
