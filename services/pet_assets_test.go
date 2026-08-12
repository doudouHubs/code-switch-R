package services

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSetPetAssetSourceLoadsAtlasAndDoesNotFallbackToDisk(t *testing.T) {
	atlas := testPetAtlasPNG(t, 2, 3)
	manifest := testPetAtlasManifest(t, 2, 3, "atlas.png")

	SetPetAssetSource(testPetAssetMap(manifest, atlas))
	t.Cleanup(func() { SetPetAssetSource(nil) })

	asset, err := loadBuiltinPetAtlas("capybara")
	if err != nil {
		t.Fatalf("loadBuiltinPetAtlas() error = %v", err)
	}
	if asset == nil || !strings.HasPrefix(asset.Src, petAtlasDataURLPrefix) {
		t.Fatalf("asset = %#v, want PNG data URL", asset)
	}
	if !json.Valid(asset.Manifest) {
		t.Fatalf("asset manifest = %s, want valid JSON", asset.Manifest)
	}

	// 注入源一旦存在就是打包态事实来源；资源缺失时不能偷偷读取当前工作区的同名目录。
	SetPetAssetSource(fstest.MapFS{
		"resources":      &fstest.MapFile{Mode: fs.ModeDir},
		"resources/pets": &fstest.MapFile{Mode: fs.ModeDir},
	})
	if _, err := loadBuiltinPetAtlas("capybara"); err == nil {
		t.Fatal("loadBuiltinPetAtlas() succeeded with missing injected resource")
	}
}

func TestInjectedBuiltinSourceDoesNotFallbackToPersistedDiskPath(t *testing.T) {
	diskRoot := t.TempDir()
	atlas := testPetAtlasPNG(t, 2, 3)
	manifest := testPetAtlasManifest(t, 2, 3, "atlas.png")
	if err := os.WriteFile(filepath.Join(diskRoot, "pet.json"), manifest, 0o600); err != nil {
		t.Fatalf("write persisted manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(diskRoot, "atlas.png"), atlas, 0o600); err != nil {
		t.Fatalf("write persisted atlas: %v", err)
	}

	SetPetAssetSource(fstest.MapFS{
		"resources":      &fstest.MapFile{Mode: fs.ModeDir},
		"resources/pets": &fstest.MapFile{Mode: fs.ModeDir},
	})
	t.Cleanup(func() { SetPetAssetSource(nil) })

	_, err := loadPetSkinAtlas(PetSkinRecord{
		SkinID:    "capybara",
		Builtin:   true,
		Path:      diskRoot,
		AtlasPath: filepath.Join(diskRoot, "atlas.png"),
	})
	if err == nil {
		t.Fatal("loadPetSkinAtlas() succeeded through persisted disk fallback")
	}
}

func TestInjectedPetAssetSourceFailsClosedForUnsafeAndInvalidAssets(t *testing.T) {
	validAtlas := testPetAtlasPNG(t, 2, 3)
	validManifest := testPetAtlasManifest(t, 2, 3, "atlas.png")

	tests := []struct {
		name   string
		skinID string
		fsys   fstest.MapFS
	}{
		{
			name:   "unknown skin",
			skinID: "mystery",
			fsys:   testPetAssetMap(validManifest, validAtlas),
		},
		{
			name:   "path traversal skin",
			skinID: "../capybara",
			fsys:   testPetAssetMap(validManifest, validAtlas),
		},
		{
			name:   "symlink manifest",
			skinID: "capybara",
			fsys: fstest.MapFS{
				"resources":                        &fstest.MapFile{Mode: fs.ModeDir},
				"resources/pets":                   &fstest.MapFile{Mode: fs.ModeDir},
				"resources/pets/capybara":          &fstest.MapFile{Mode: fs.ModeDir},
				"resources/pets/capybara/pet.json": &fstest.MapFile{Mode: fs.ModeSymlink},
			},
		},
		{
			name:   "manifest png dimension mismatch",
			skinID: "capybara",
			fsys:   testPetAssetMap(testPetAtlasManifest(t, 4, 3, "atlas.png"), validAtlas),
		},
		{
			name:   "oversized manifest",
			skinID: "capybara",
			fsys:   testPetAssetMap(bytes.Repeat([]byte{'x'}, petAtlasMaxManifestBytes+1), validAtlas),
		},
		{
			name:   "oversized atlas",
			skinID: "capybara",
			fsys:   testPetAssetMap(validManifest, bytes.Repeat([]byte{'x'}, petAtlasMaxImageBytes+1)),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			SetPetAssetSource(test.fsys)
			if _, err := loadBuiltinPetAtlas(test.skinID); err == nil {
				t.Fatal("loadBuiltinPetAtlas() succeeded, want fail-closed error")
			}
		})
	}
	SetPetAssetSource(nil)
}

func testPetAssetMap(manifest, atlas []byte) fstest.MapFS {
	return fstest.MapFS{
		"resources":                         &fstest.MapFile{Mode: fs.ModeDir},
		"resources/pets":                    &fstest.MapFile{Mode: fs.ModeDir},
		"resources/pets/capybara":           &fstest.MapFile{Mode: fs.ModeDir},
		"resources/pets/capybara/pet.json":  &fstest.MapFile{Data: manifest},
		"resources/pets/capybara/atlas.png": &fstest.MapFile{Data: atlas},
	}
}

func testPetAtlasManifest(t *testing.T, width, height int, imageName string) []byte {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"atlasVersion": PetAtlasVersion,
		"atlas": map[string]any{
			"image":  imageName,
			"width":  width,
			"height": height,
		},
	})
	if err != nil {
		t.Fatalf("marshal test manifest: %v", err)
	}
	return manifest
}

func testPetAtlasPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatalf("encode test atlas: %v", err)
	}
	return output.Bytes()
}
