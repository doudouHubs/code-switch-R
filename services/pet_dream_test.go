package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakePetDreamRepository struct {
	mu sync.Mutex

	records   []PetDreamHistoryRecord
	snapshot  PetMigrationSnapshot
	loadErr   error
	saveErr   error
	listErr   error
	deleteErr error

	savedRecords []PetDreamHistoryRecord
	lastPetID    string
	lastDeleteID string
	listCount    int
	saveCount    int
	deleteCount  int
}

func (r *fakePetDreamRepository) UpsertDreamHistory(_ context.Context, record PetDreamHistoryRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	record = clonePetDreamHistoryRecord(record)
	r.savedRecords = append(r.savedRecords, record)
	for i := range r.records {
		if r.records[i].PetID == record.PetID && r.records[i].ID == record.ID {
			r.records[i] = record
			r.saveCount++
			return nil
		}
	}
	r.records = append(r.records, record)
	r.saveCount++
	return nil
}

func (r *fakePetDreamRepository) ListDreamHistory(_ context.Context, petID string) ([]PetDreamHistoryRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	r.lastPetID = petID
	r.listCount++
	result := make([]PetDreamHistoryRecord, 0, len(r.records))
	for _, record := range r.records {
		if record.PetID == "" || record.PetID == petID {
			result = append(result, clonePetDreamHistoryRecord(record))
		}
	}
	return result, nil
}

func (r *fakePetDreamRepository) DeleteDreamHistory(_ context.Context, petID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.lastPetID = petID
	r.lastDeleteID = id
	r.deleteCount++
	for i := range r.records {
		if r.records[i].PetID == petID && r.records[i].ID == id {
			r.records = append(r.records[:i], r.records[i+1:]...)
			break
		}
	}
	return nil
}

func (r *fakePetDreamRepository) LoadSnapshot(_ context.Context, _ string) (PetMigrationSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return PetMigrationSnapshot{}, r.loadErr
	}
	return clonePetDreamSnapshot(r.snapshot), nil
}

func (r *fakePetDreamRepository) SaveSnapshot(_ context.Context, snapshot PetMigrationSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	r.snapshot = clonePetDreamSnapshot(snapshot)
	r.saveCount++
	return nil
}

func clonePetDreamHistoryRecord(record PetDreamHistoryRecord) PetDreamHistoryRecord {
	clone := record
	clone.Keywords = append([]string(nil), record.Keywords...)
	if record.ThemeID != nil {
		value := *record.ThemeID
		clone.ThemeID = &value
	}
	if record.ThemeLabel != nil {
		value := *record.ThemeLabel
		clone.ThemeLabel = &value
	}
	if record.Emotion != nil {
		value := *record.Emotion
		clone.Emotion = &value
	}
	if record.SelfAppears != nil {
		value := *record.SelfAppears
		clone.SelfAppears = &value
	}
	if record.ImagePath != nil {
		value := *record.ImagePath
		clone.ImagePath = &value
	}
	return clone
}

func clonePetDreamSnapshot(snapshot PetMigrationSnapshot) PetMigrationSnapshot {
	clone := snapshot
	if snapshot.State != nil {
		state := *snapshot.State
		if snapshot.State.AwayTask != nil {
			awayTask := *snapshot.State.AwayTask
			state.AwayTask = &awayTask
		}
		clone.State = &state
	}
	clone.Dreams = make([]PetDreamHistoryRecord, 0, len(snapshot.Dreams))
	for _, record := range snapshot.Dreams {
		clone.Dreams = append(clone.Dreams, clonePetDreamHistoryRecord(record))
	}
	return clone
}

func TestPetDreamSaveHistoryNormalizesFields(t *testing.T) {
	repository := &fakePetDreamRepository{}
	service := NewPetDreamService(repository)
	service.now = func() int64 { return 1700000000000 }
	longDream := strings.Repeat("梦", PetDreamHistoryTitleMaxLength+4) + "。后续"
	invalidEmotion := PetDreamEmotion("unknown")
	selfAppears := true

	err := service.SaveHistory(PetDreamHistoryRecord{
		ID:          "  dream-1  ",
		CreatedAt:   0,
		Title:       strings.Repeat("标题", PetDreamHistoryTitleMaxLength),
		Dream:       "  " + longDream + "  ",
		SleepTalk:   "  月亮在说话  ",
		Keywords:    []string{" 月亮 ", "", "月亮", " 星星 "},
		Emotion:     &invalidEmotion,
		SelfAppears: &selfAppears,
	})
	if err != nil {
		t.Fatalf("SaveHistory() error = %v", err)
	}

	repository.mu.Lock()
	saved := clonePetDreamHistoryRecord(repository.savedRecords[0])
	repository.mu.Unlock()
	if saved.ID != "dream-1" || saved.PetID != DefaultPetID {
		t.Fatalf("saved identity = %#v", saved)
	}
	if saved.CreatedAt != 1700000000000 || saved.Dream != longDream || saved.SleepTalk != "月亮在说话" {
		t.Fatalf("saved normalized text/time = %#v", saved)
	}
	if got := len([]rune(saved.Title)); got != PetDreamHistoryTitleMaxLength {
		t.Fatalf("title rune length = %d, want %d", got, PetDreamHistoryTitleMaxLength)
	}
	if strings.Join(saved.Keywords, ",") != "月亮,星星" {
		t.Fatalf("keywords = %#v, want cleaned stable unique values", saved.Keywords)
	}
	if saved.Emotion != nil || saved.SelfAppears == nil || !*saved.SelfAppears {
		t.Fatalf("emotion/selfAppears = %#v/%#v", saved.Emotion, saved.SelfAppears)
	}
}

func TestPetDreamSaveHistoryRequiresDream(t *testing.T) {
	service := NewPetDreamService(&fakePetDreamRepository{})
	_, err := NormalizePetDreamHistoryRecord(PetDreamHistoryRecord{Dream: " \t"}, DefaultPetID)
	if err == nil {
		t.Fatal("NormalizePetDreamHistoryRecord() error = nil, want validation error")
	}
	var dreamErr *PetDreamError
	if !errors.As(err, &dreamErr) || dreamErr.Code != PetDreamErrorInvalidDream {
		t.Fatalf("error = %#v, want structured invalid dream error", err)
	}
	if err := service.SaveHistory(PetDreamHistoryRecord{}); PetDreamErrorCodeOf(err) != PetDreamErrorInvalidDream {
		t.Fatalf("SaveHistory() error code = %q, want %q", PetDreamErrorCodeOf(err), PetDreamErrorInvalidDream)
	}
}

func TestPetDreamListHistoryPageSortsAndBounds(t *testing.T) {
	repository := &fakePetDreamRepository{records: []PetDreamHistoryRecord{
		{PetID: DefaultPetID, ID: "a", CreatedAt: 100},
		{PetID: DefaultPetID, ID: "c", CreatedAt: 200},
		{PetID: DefaultPetID, ID: "b", CreatedAt: 200},
	}}
	service := NewPetDreamService(repository)

	page, err := service.ListHistoryPage(0, 2)
	if err != nil {
		t.Fatalf("ListHistoryPage() error = %v", err)
	}
	if page.Page != 1 || page.PageSize != 2 || page.Total != 3 || page.TotalPages != 2 || !page.HasNext || page.HasPrevious {
		t.Fatalf("page metadata = %#v", page)
	}
	if got := []string{page.Records[0].ID, page.Records[1].ID}; strings.Join(got, ",") != "c,b" {
		t.Fatalf("page records = %#v, want c,b", got)
	}

	page, err = service.ListHistoryPage(2, 999)
	if err != nil {
		t.Fatalf("ListHistoryPage() max page size error = %v", err)
	}
	if page.PageSize != PetDreamHistoryMaxPageSize || len(page.Records) != 0 || !page.HasPrevious || page.HasNext {
		t.Fatalf("bounded page = %#v", page)
	}

	page, err = service.ListHistoryPage(1, 0)
	if err != nil || page.PageSize != PetDreamHistoryPageSize {
		t.Fatalf("default page size = %d, err = %v", page.PageSize, err)
	}
}

func TestPetDreamImagePathMustStayInArchive(t *testing.T) {
	archiveDir := t.TempDir()
	validPath := filepath.Join(archiveDir, "night.PNG")
	got, err := NormalizePetDreamImagePath(validPath, archiveDir)
	if err != nil || got != "night.PNG" {
		t.Fatalf("valid archive image = %q, err = %v", got, err)
	}

	for _, value := range []string{
		filepath.Join(archiveDir, "nested", "night.png"),
		filepath.Join(archiveDir, "..", "outside.png"),
		"../outside.png",
		"night.txt",
	} {
		if _, err := NormalizePetDreamImagePath(value, archiveDir); PetDreamErrorCodeOf(err) != PetDreamErrorInvalidImage {
			t.Fatalf("image path %q error code = %q, want %q", value, PetDreamErrorCodeOf(err), PetDreamErrorInvalidImage)
		}
	}
}

func TestPetDreamApplyEmotionOnlyPersistsWhileSleeping(t *testing.T) {
	repository := &fakePetDreamRepository{snapshot: PetMigrationSnapshot{
		State: &PetState{PetID: DefaultPetID, Mood: 50, Sleeping: true},
	}}
	service := NewPetDreamService(repository)

	if err := service.ApplyEmotion(PetDreamAfraid); err != nil {
		t.Fatalf("sleeping ApplyEmotion() error = %v", err)
	}
	repository.mu.Lock()
	sleepingMood := repository.snapshot.State.Mood
	sleepingSaves := repository.saveCount
	repository.mu.Unlock()
	if sleepingMood != 47 || sleepingSaves != 1 {
		t.Fatalf("sleeping mood/save count = %v/%d, want 47/1", sleepingMood, sleepingSaves)
	}

	repository.mu.Lock()
	repository.snapshot.State.Sleeping = false
	repository.snapshot.State.Mood = 50
	repository.mu.Unlock()
	if err := service.ApplyEmotion(PetDreamPleasant); err != nil {
		t.Fatalf("awake ApplyEmotion() error = %v", err)
	}
	repository.mu.Lock()
	awakeMood := repository.snapshot.State.Mood
	awakeSaves := repository.saveCount
	repository.mu.Unlock()
	if awakeMood != 50 || awakeSaves != 1 {
		t.Fatalf("awake mood/save count = %v/%d, wake path must not mutate or persist", awakeMood, awakeSaves)
	}
}
