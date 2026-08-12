package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPetDreamAPIServicePersistsHistoryThroughSharedRules(t *testing.T) {
	repository := &fakePetDreamRepository{}
	service := NewPetDreamAPIService(repository)
	service.archiveRoot = t.TempDir()

	record := PetDreamHistoryRecord{Dream: "  温泉边的星星。  ", ImagePath: stringPointer("dream.png")}
	if err := service.SaveHistory("pet-a", record); err != nil {
		t.Fatalf("SaveHistory() error = %v", err)
	}
	page, err := service.ListHistory("pet-a", 1, 20)
	if err != nil {
		t.Fatalf("ListHistory() error = %v", err)
	}
	if page.Total != 1 || page.Records[0].Dream != "温泉边的星星。" || page.Records[0].PetID != "pet-a" {
		t.Fatalf("page = %#v", page)
	}
	if repository.lastPetID != "pet-a" {
		t.Fatalf("repository pet ID = %q", repository.lastPetID)
	}
}

func TestPetDreamAPIServiceImageRoundTripUsesDataURL(t *testing.T) {
	repository := &fakePetDreamRepository{}
	service := NewPetDreamAPIService(repository)
	service.archiveRoot = t.TempDir()

	name, err := service.StoreImage("pet-a", "image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("StoreImage() error = %v", err)
	}
	if name == "" || filepath.Base(name) != name {
		t.Fatalf("image name = %q", name)
	}
	dataURL, err := service.ReadImage("pet-a", name)
	if err != nil {
		t.Fatalf("ReadImage() error = %v", err)
	}
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("data URL = %q", dataURL)
	}

	if _, err := service.ReadImage("pet-a", filepath.Join("..", name)); err == nil {
		t.Fatal("path traversal should fail")
	}
	if _, err := os.Stat(filepath.Join(service.archiveRoot, name)); err != nil {
		t.Fatalf("stored image missing: %v", err)
	}
}
