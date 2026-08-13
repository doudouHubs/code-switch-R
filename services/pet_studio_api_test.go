package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

type petStudioTestStore struct {
	records        map[string]PetSkinRecord
	selection      map[string]PetSkinSelection
	listErr        error
	upsertErr      error
	deleteErr      error
	selectionErr   error
	deleteCalls    int
	upsertCalls    int
	selectionCalls int
}

func (s *petStudioTestStore) UpsertSkin(_ context.Context, record PetSkinRecord) error {
	s.upsertCalls++
	if s.upsertErr != nil {
		return s.upsertErr
	}
	if s.records == nil {
		s.records = make(map[string]PetSkinRecord)
	}
	s.records[petStudioTestStoreKey(record.PetID, record.SkinID)] = record
	return nil
}

func (s *petStudioTestStore) DeleteSkin(_ context.Context, petID, skinID string) error {
	s.deleteCalls++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.records, petStudioTestStoreKey(petID, skinID))
	return nil
}

func (s *petStudioTestStore) ListSkins(_ context.Context, petID string) ([]PetSkinRecord, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	result := make([]PetSkinRecord, 0)
	for _, record := range s.records {
		if record.PetID == petID {
			result = append(result, record)
		}
	}
	return result, nil
}

func (s *petStudioTestStore) SaveSkinSelection(_ context.Context, selection PetSkinSelection) error {
	s.selectionCalls++
	if s.selectionErr != nil {
		return s.selectionErr
	}
	if s.selection == nil {
		s.selection = make(map[string]PetSkinSelection)
	}
	s.selection[selection.PetID] = selection
	return nil
}

func petStudioTestStoreKey(petID, skinID string) string {
	return petID + "\x00" + skinID
}

func TestPetStudioSaveSkinRejectsUnsafeIDDataURLAndOversizedAtlas(t *testing.T) {
	valid := petStudioTestRequest(t)
	tests := []struct {
		name   string
		mutate func(*PetStudioSaveSkinRequest)
		want   string
	}{
		{name: "path traversal id", mutate: func(request *PetStudioSaveSkinRequest) { request.SkinID = "../escape" }, want: "skinId"},
		{name: "absolute id", mutate: func(request *PetStudioSaveSkinRequest) { request.SkinID = `C:\\outside` }, want: "skinId"},
		{name: "data url", mutate: func(request *PetStudioSaveSkinRequest) { request.Atlas = "data:image/png;base64," + request.Atlas }, want: "bare base64"},
		{name: "oversized atlas", mutate: func(request *PetStudioSaveSkinRequest) {
			request.Atlas = strings.Repeat("A", base64.StdEncoding.EncodedLen(int(petAtlasMaxImageBytes))+1)
		}, want: "过大"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &petStudioTestStore{}
			service := NewPetStudioAPIService(store, PetStudioAPIOptions{Root: t.TempDir()})
			request := valid
			test.mutate(&request)
			if _, err := service.SaveSkin(DefaultPetID, request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SaveSkin() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestPetStudioSaveSkinRequiresIdleAndMatchingAtlasMetadata(t *testing.T) {
	atlas := petStudioTestPNG(t, 2, 2)
	tests := []struct {
		name     string
		manifest json.RawMessage
		want     string
	}{
		{
			name:     "missing idle",
			manifest: petStudioTestManifest(t, 2, 2, map[string]PetAtlasAnimation{"walk": petStudioTestAnimation()}),
			want:     "idle",
		},
		{
			name:     "dimension mismatch",
			manifest: petStudioTestManifest(t, 3, 2, map[string]PetAtlasAnimation{"idle": petStudioTestAnimation()}),
			want:     "尺寸",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewPetStudioAPIService(&petStudioTestStore{}, PetStudioAPIOptions{Root: t.TempDir()})
			request := PetStudioSaveSkinRequest{
				SkinID:       "studio-test",
				Name:         "Studio test",
				Atlas:        base64.StdEncoding.EncodeToString(atlas),
				ManifestJSON: test.manifest,
			}
			if _, err := service.SaveSkin(DefaultPetID, request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SaveSkin() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestPetStudioSaveSkinAtomicallyPersistsAndBinds(t *testing.T) {
	root := t.TempDir()
	store := &petStudioTestStore{}
	service := NewPetStudioAPIService(store, PetStudioAPIOptions{Root: root})
	request := petStudioTestRequest(t)
	request.Bind = true

	result, err := service.SaveSkin(DefaultPetID, request)
	if err != nil {
		t.Fatalf("SaveSkin() error = %v", err)
	}
	if result.Path != "" || result.AtlasPath != "" {
		t.Fatalf("SaveSkin() returned local paths: %#v", result)
	}
	if result.SkinID != request.SkinID || result.Atlas.Width != 2 || result.Atlas.Height != 2 {
		t.Fatalf("SaveSkin() result = %#v", result)
	}
	record, ok := store.records[petStudioTestStoreKey(DefaultPetID, request.SkinID)]
	if !ok || record.Path == "" || record.AtlasPath == "" {
		t.Fatalf("stored record = %#v, want internal paths", record)
	}
	selection, ok := store.selection[DefaultPetID]
	if !ok || selection.ActiveSkinID == nil || *selection.ActiveSkinID != request.SkinID {
		t.Fatalf("selection = %#v, want bound skin", selection)
	}
	if _, err := os.Stat(filepath.Join(root, request.SkinID, "atlas.png")); err != nil {
		t.Fatalf("atlas.png is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, request.SkinID, "pet.json")); err != nil {
		t.Fatalf("pet.json is missing: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != request.SkinID {
		t.Fatalf("root entries = %#v, want only committed skin directory", entries)
	}
	if store.upsertCalls != 1 || store.selectionCalls != 1 {
		t.Fatalf("store calls = upsert:%d selection:%d", store.upsertCalls, store.selectionCalls)
	}
}

func TestPetStudioReadSkinDefaultAndCustomArePathSafeAndPetScoped(t *testing.T) {
	atlas := petStudioTestPNG(t, 2, 2)
	manifest := petStudioTestBuiltinManifest(t)
	previousSource := currentPetAssetSource()
	t.Cleanup(func() { SetPetAssetSource(previousSource) })
	SetPetAssetSource(fstest.MapFS{
		"resources/pets/capybara/pet.json":  {Data: manifest, Mode: 0o600},
		"resources/pets/capybara/atlas.png": {Data: atlas, Mode: 0o600},
	})

	root := t.TempDir()
	store := &petStudioTestStore{}
	service := NewPetStudioAPIService(store, PetStudioAPIOptions{Root: root})

	defaultResult, err := service.ReadSkin("pet-1", "")
	if err != nil {
		t.Fatalf("ReadSkin(default) error = %v", err)
	}
	if defaultResult.Skin.SkinID != "capybara" || defaultResult.Skin.PetID != "pet-1" || !defaultResult.Skin.Builtin {
		t.Fatalf("ReadSkin(default) skin = %#v", defaultResult.Skin)
	}
	if defaultResult.Atlas.Src == "" || !strings.HasPrefix(defaultResult.Atlas.Src, "data:image/png;base64,") || len(defaultResult.Atlas.Manifest) == 0 {
		t.Fatalf("ReadSkin(default) atlas = %#v", defaultResult.Atlas)
	}
	if defaultResult.Skin.Path != "" || defaultResult.Skin.AtlasPath != "" {
		t.Fatalf("ReadSkin(default) leaked paths: %#v", defaultResult.Skin)
	}

	request := petStudioTestRequest(t)
	request.SkinID = "custom-one"
	request.Name = "Custom one"
	if _, err := service.SaveSkin("pet-1", request); err != nil {
		t.Fatalf("SaveSkin(custom) error = %v", err)
	}
	customResult, err := service.ReadSkin("pet-1", request.SkinID)
	if err != nil {
		t.Fatalf("ReadSkin(custom) error = %v", err)
	}
	if customResult.Skin.PetID != "pet-1" || customResult.Skin.SkinID != request.SkinID || customResult.Skin.Builtin {
		t.Fatalf("ReadSkin(custom) skin = %#v", customResult.Skin)
	}
	if customResult.Skin.Path != "" || customResult.Skin.AtlasPath != "" {
		t.Fatalf("ReadSkin(custom) leaked paths: %#v", customResult.Skin)
	}
	if customResult.Atlas.Src == "" || len(customResult.Atlas.Manifest) == 0 {
		t.Fatalf("ReadSkin(custom) atlas = %#v", customResult.Atlas)
	}

	if _, err := service.ReadSkin("pet-2", request.SkinID); err == nil || !strings.Contains(err.Error(), "当前宠物不存在") {
		t.Fatalf("ReadSkin(other pet) error = %v, want pet isolation", err)
	}
}

func TestPetStudioSaveSkinRollsBackFilesWhenDatabaseFails(t *testing.T) {
	root := t.TempDir()
	store := &petStudioTestStore{upsertErr: os.ErrPermission}
	service := NewPetStudioAPIService(store, PetStudioAPIOptions{Root: root})
	if _, err := service.SaveSkin(DefaultPetID, petStudioTestRequest(t)); err == nil {
		t.Fatal("SaveSkin() succeeded despite database failure")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("root entries after rollback = %#v, want empty", entries)
	}
}

func TestPetStudioBindFailureRollsBackRecordAndFiles(t *testing.T) {
	root := t.TempDir()
	store := &petStudioTestStore{selectionErr: os.ErrPermission}
	service := NewPetStudioAPIService(store, PetStudioAPIOptions{Root: root})
	request := petStudioTestRequest(t)
	request.Bind = true
	if _, err := service.SaveSkin(DefaultPetID, request); err == nil {
		t.Fatal("SaveSkin() succeeded despite binding failure")
	}
	if len(store.records) != 0 {
		t.Fatalf("records after binding rollback = %#v, want empty", store.records)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("root entries after binding rollback = %#v, want empty", entries)
	}
}

func TestPetStudioDeleteRejectsBuiltinRootEscapeAndSymlink(t *testing.T) {
	t.Run("builtin", func(t *testing.T) {
		service := NewPetStudioAPIService(&petStudioTestStore{}, PetStudioAPIOptions{Root: t.TempDir()})
		if err := service.DeleteSkin(DefaultPetID, "capybara"); err == nil || !strings.Contains(err.Error(), "内置") {
			t.Fatalf("DeleteSkin() error = %v, want builtin rejection", err)
		}
	})

	t.Run("database path escape", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		store := &petStudioTestStore{records: map[string]PetSkinRecord{
			petStudioTestStoreKey(DefaultPetID, "custom"): {
				PetID: DefaultPetID, SkinID: "custom", Path: outside,
			},
		}}
		service := NewPetStudioAPIService(store, PetStudioAPIOptions{Root: root})
		if err := service.DeleteSkin(DefaultPetID, "custom"); err == nil || !strings.Contains(err.Error(), "受控 root") {
			t.Fatalf("DeleteSkin() error = %v, want root escape rejection", err)
		}
		if store.deleteCalls != 0 {
			t.Fatal("DeleteSkin() touched DAO after root escape")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows symlink tests require developer mode or elevated privilege")
		}
		root := t.TempDir()
		outside := t.TempDir()
		target := filepath.Join(root, "custom")
		if err := os.Symlink(outside, target); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		store := &petStudioTestStore{records: map[string]PetSkinRecord{
			petStudioTestStoreKey(DefaultPetID, "custom"): {
				PetID: DefaultPetID, SkinID: "custom", Path: target,
			},
		}}
		service := NewPetStudioAPIService(store, PetStudioAPIOptions{Root: root})
		if err := service.DeleteSkin(DefaultPetID, "custom"); err == nil {
			t.Fatal("DeleteSkin() followed symlink")
		}
		if store.deleteCalls != 0 {
			t.Fatal("DeleteSkin() touched DAO for symlink target")
		}
	})
}

func TestPetStudioListSkinsClearsPathsAndSortsStable(t *testing.T) {
	old := int64(10)
	newer := int64(20)
	store := &petStudioTestStore{records: map[string]PetSkinRecord{
		petStudioTestStoreKey(DefaultPetID, "old"): {
			PetID: DefaultPetID, SkinID: "old", UpdatedAt: &old, Path: `C:\\private\\old`, AtlasPath: `C:\\private\\old\\atlas.png`,
		},
		petStudioTestStoreKey(DefaultPetID, "new"): {
			PetID: DefaultPetID, SkinID: "new", UpdatedAt: &newer, Path: `/private/new`, AtlasPath: `/private/new/atlas.png`,
		},
	}}
	service := NewPetStudioAPIService(store, PetStudioAPIOptions{Root: t.TempDir()})
	result, err := service.ListSkins(DefaultPetID)
	if err != nil {
		t.Fatalf("ListSkins() error = %v", err)
	}
	if len(result) != 2 || result[0].SkinID != "new" {
		t.Fatalf("ListSkins() result = %#v, want updated-desc order", result)
	}
	for _, record := range result {
		if record.Path != "" || record.AtlasPath != "" {
			t.Fatalf("ListSkins() leaked path: %#v", record)
		}
	}
}

func TestPetStudioGetRootReturnsOnlyConfiguredRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pets")
	service := NewPetStudioAPIService(&petStudioTestStore{}, PetStudioAPIOptions{Root: root})

	got, err := service.GetRoot()
	if err != nil {
		t.Fatalf("GetRoot() error = %v", err)
	}
	if got != filepath.Clean(root) {
		t.Fatalf("GetRoot() = %q, want %q", got, filepath.Clean(root))
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("GetRoot() created root directory or returned unexpected error: %v", err)
	}
}

func petStudioTestRequest(t *testing.T) PetStudioSaveSkinRequest {
	t.Helper()
	atlas := petStudioTestPNG(t, 2, 2)
	return PetStudioSaveSkinRequest{
		SkinID:       "studio-test",
		Name:         "Studio test",
		Atlas:        base64.StdEncoding.EncodeToString(atlas),
		ManifestJSON: petStudioTestManifest(t, 2, 2, map[string]PetAtlasAnimation{"idle": petStudioTestAnimation()}),
	}
}

func petStudioTestAnimation() PetAtlasAnimation {
	return PetAtlasAnimation{
		Loop:   true,
		Frames: []PetAtlasFrame{{X: 0, Y: 0, Width: 2, Height: 2, DurationMS: PetAtlasDefaultDuration}},
	}
}

func petStudioTestManifest(t *testing.T, width, height int, animations map[string]PetAtlasAnimation) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(PetAtlasManifest{
		Name:         "Studio test",
		AtlasVersion: PetAtlasVersion,
		Atlas: PetAtlasMetadata{
			AtlasVersion: PetAtlasVersion,
			Image:        "atlas.png",
			Width:        width,
			Height:       height,
			Anchor:       "bottom-center",
			Layout:       "action-rows",
		},
		Animations: animations,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func petStudioTestBuiltinManifest(t *testing.T) []byte {
	t.Helper()
	manifest, err := json.Marshal(PetAtlasManifest{
		Name:         "Builtin capybara",
		Subject:      "builtin subject",
		ModelID:      "builtin-model",
		AtlasVersion: PetAtlasVersion,
		Atlas: PetAtlasMetadata{
			AtlasVersion: PetAtlasVersion,
			Image:        "atlas.png",
			Width:        2,
			Height:       2,
			Anchor:       "bottom-center",
			Layout:       "action-rows",
		},
		Animations: map[string]PetAtlasAnimation{"idle": petStudioTestAnimation()},
	})
	if err != nil {
		t.Fatalf("marshal builtin manifest: %v", err)
	}
	return manifest
}

func petStudioTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	imageData := image.NewNRGBA(image.Rect(0, 0, width, height))
	imageData.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageData); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
