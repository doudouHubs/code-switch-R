package services

import (
	"context"
	"strings"
	"testing"
	"time"
)

type memoryPetMemoryRepository struct {
	records map[string][]PetMemoryRecord
}

func newMemoryPetMemoryRepository() *memoryPetMemoryRepository {
	return &memoryPetMemoryRepository{records: make(map[string][]PetMemoryRecord)}
}

func (r *memoryPetMemoryRepository) ListMemories(_ context.Context, petID string) ([]PetMemoryRecord, error) {
	return clonePetMemoryRecords(r.records[petID]), nil
}

func (r *memoryPetMemoryRepository) UpsertMemory(_ context.Context, record PetMemoryRecord) error {
	items := r.records[record.PetID]
	for index := range items {
		if items[index].ID == record.ID {
			items[index] = record
			r.records[record.PetID] = items
			return nil
		}
	}
	r.records[record.PetID] = append(items, record)
	return nil
}

func (r *memoryPetMemoryRepository) DeleteMemory(_ context.Context, petID, id string) error {
	items := r.records[petID]
	filtered := items[:0]
	for _, item := range items {
		if item.ID != id {
			filtered = append(filtered, item)
		}
	}
	r.records[petID] = filtered
	return nil
}

func TestPetMemoryServiceAppendDeduplicatesAndBuildsPrompt(t *testing.T) {
	repository := newMemoryPetMemoryRepository()
	service := NewPetMemoryServiceForPet(repository, "default")
	service.now = func() time.Time { return time.Date(2026, 8, 11, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60)) }

	records, err := service.Append([]string{"  喜欢泡温泉。 ", "喜欢泡温泉。", "主人喜欢黑咖啡"})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(records) != 2 || records[0].Date != "2026-08-11" {
		t.Fatalf("records = %#v", records)
	}
	prompt := BuildPetMemorySection(records)
	if prompt == "" || !containsAll(prompt, "喜欢泡温泉。", "主人喜欢黑咖啡", "记住") {
		t.Fatalf("memory prompt = %q", prompt)
	}
}

func TestPetMemoryDirectivesAreHiddenAndNormalized(t *testing.T) {
	text := "好的。[[记住: 主人喜欢黑咖啡]]\n[[remember: 早上提醒休息]]"
	got := ExtractPetMemoryDirectives(text)
	if len(got) != 2 || got[0] != "主人喜欢黑咖啡" || got[1] != "早上提醒休息" {
		t.Fatalf("directives = %#v", got)
	}
	if visible := StripPetMemoryDirectives(text); visible != "好的。" {
		t.Fatalf("visible = %q", visible)
	}
	if visible := StripPetMemoryDirectives("正在输出 [[记住: 主人喜欢"); visible != "正在输出" {
		t.Fatalf("unfinished visible = %q", visible)
	}
}

func TestPetMemoryServiceRemoveAndClearArePetScoped(t *testing.T) {
	repository := newMemoryPetMemoryRepository()
	service := NewPetMemoryServiceForPet(repository, "pet-a")
	records, err := service.Append([]string{"a", "b"})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := service.Remove(records[0].ID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	other := NewPetMemoryServiceForPet(repository, "pet-b")
	if _, err := other.Append([]string{"other"}); err != nil {
		t.Fatalf("other Append() error = %v", err)
	}
	if err := service.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	if len(repository.records["pet-a"]) != 0 || len(repository.records["pet-b"]) != 1 {
		t.Fatalf("records = %#v", repository.records)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
