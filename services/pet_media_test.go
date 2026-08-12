package services

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func opaqueRect(width, height int, bounds image.Rectangle, fill color.NRGBA) *image.NRGBA {
	imageValue := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			imageValue.SetNRGBA(x, y, fill)
		}
	}
	return imageValue
}

func TestValidatePetAssetPathRejectsTraversalAndUnsupportedExtensions(t *testing.T) {
	root := t.TempDir()
	valid, err := ValidatePetAssetPath(root, `sprites\idle.PNG`)
	if err != nil {
		t.Fatalf("valid pet asset path error = %v", err)
	}
	if filepath.Base(valid) != "idle.PNG" || !strings.HasPrefix(filepath.Clean(valid), filepath.Clean(root)) {
		t.Fatalf("valid pet asset path = %q", valid)
	}

	for _, test := range []struct {
		name string
		path string
		want error
	}{
		{name: "unix traversal", path: "../escape.png", want: ErrPetAssetPathTraversal},
		{name: "windows traversal", path: `..\escape.png`, want: ErrPetAssetPathTraversal},
		{name: "absolute outside", path: filepath.Join(filepath.Dir(root), "escape.png"), want: ErrPetAssetPathOutsideRoot},
		{name: "unsupported extension", path: "sprites/idle.exe", want: ErrPetAssetExtension},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidatePetAssetPath(root, test.path)
			if !errors.Is(err, test.want) {
				t.Fatalf("ValidatePetAssetPath(%q) error = %v, want errors.Is(%v)", test.path, err, test.want)
			}
		})
	}

	for _, extension := range []string{"png", "jpg", "jpeg", "webp", "bmp", "gif"} {
		if _, err := ValidatePetAssetPath(root, "assets/pet."+extension); err != nil {
			t.Fatalf("allowed extension %q error = %v", extension, err)
		}
	}
}

func TestValidatePetAssetPathRejectsSymlinkEscapeWhenSupported(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.png")
	if err := os.WriteFile(outsideFile, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.png")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlink unavailable on this platform: %v", err)
	}
	_, err := ValidatePetAssetPath(root, "link.png")
	if !errors.Is(err, ErrPetAssetPathTraversal) {
		t.Fatalf("symlink escape error = %v, want %v", err, ErrPetAssetPathTraversal)
	}
}

func TestApplyPetChromaKeyPreservesForegroundAlpha(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	source.SetNRGBA(0, 0, color.NRGBA{R: 0, G: 255, B: 0, A: 255})
	source.SetNRGBA(1, 0, color.NRGBA{R: 0, G: 205, B: 0, A: 128})
	source.SetNRGBA(2, 0, color.NRGBA{R: 220, G: 40, B: 30, A: 77})
	source.SetNRGBA(3, 0, color.NRGBA{R: 0, G: 255, B: 0, A: 0})

	got, err := ApplyPetChromaKey(source, PetChromaKeyOptions{
		KeyColor:  color.RGBA{R: 0, G: 255, B: 0, A: 255},
		Tolerance: 0,
		Softness:  100,
	})
	if err != nil {
		t.Fatalf("ApplyPetChromaKey() error = %v", err)
	}
	if alpha := got.NRGBAAt(0, 0).A; alpha != 0 {
		t.Fatalf("exact key alpha = %d, want 0", alpha)
	}
	partialAlpha := got.NRGBAAt(1, 0).A
	if partialAlpha < 55 || partialAlpha > 72 {
		t.Fatalf("soft key alpha = %d, want around 64", partialAlpha)
	}
	if alpha := got.NRGBAAt(2, 0).A; alpha != 77 {
		t.Fatalf("foreground alpha = %d, want 77", alpha)
	}
	if alpha := got.NRGBAAt(3, 0).A; alpha != 0 {
		t.Fatalf("transparent source alpha = %d, want 0", alpha)
	}
}

func TestNormalizePetSpriteReturnsStableBoundsAndPNG(t *testing.T) {
	source := image.NewNRGBA(image.Rect(10, 20, 16, 26))
	for y := 22; y < 25; y++ {
		for x := 11; x < 14; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}

	result, err := NormalizePetSprite(source, PetSpriteNormalizationOptions{
		TargetHeight:   6,
		AlphaThreshold: 1,
		PaddingX:       1,
		PaddingY:       1,
	})
	if err != nil {
		t.Fatalf("NormalizePetSprite() error = %v", err)
	}
	if got, want := result.SourceBounds, (PetSpriteBounds{X: 11, Y: 22, Width: 3, Height: 3}); got != want {
		t.Fatalf("source bounds = %#v, want %#v", got, want)
	}
	if got, want := result.SubjectBounds, (PetSpriteBounds{X: 1, Y: 1, Width: 6, Height: 6}); got != want {
		t.Fatalf("subject bounds = %#v, want %#v", got, want)
	}
	if result.OutputWidth != 8 || result.OutputHeight != 8 || len(result.PNG) == 0 {
		t.Fatalf("normalized output = %dx%d, png bytes = %d", result.OutputWidth, result.OutputHeight, len(result.PNG))
	}
	decoded, err := png.Decode(bytes.NewReader(result.PNG))
	if err != nil {
		t.Fatalf("decode normalized png error = %v", err)
	}
	if decoded.Bounds().Dx() != 8 || decoded.Bounds().Dy() != 8 {
		t.Fatalf("decoded normalized bounds = %v", decoded.Bounds())
	}
}

func TestNormalizePetSpriteRejectsTransparentSource(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	if _, err := FindPetSpriteSubjectBounds(source); err == nil || !strings.Contains(err.Error(), "visible pixels") {
		t.Fatalf("FindPetSpriteSubjectBounds() error = %v, want no-subject error", err)
	}
	if _, err := NormalizePetSprite(source, PetSpriteNormalizationOptions{TargetHeight: 8}); err == nil || !strings.Contains(err.Error(), "visible pixels") {
		t.Fatalf("NormalizePetSprite() error = %v, want no-subject error", err)
	}
}

func TestPackPetAtlasGeneratesDeterministicActionRowsAndFrameBounds(t *testing.T) {
	idleFirst := opaqueRect(4, 4, image.Rect(1, 1, 3, 4), color.NRGBA{R: 255, A: 255})
	idleSecond := opaqueRect(2, 3, image.Rect(0, 0, 2, 2), color.NRGBA{G: 255, A: 255})
	walk := opaqueRect(3, 2, image.Rect(1, 0, 2, 2), color.NRGBA{B: 255, A: 255})

	result, err := PackPetAtlas([]PetAtlasAction{
		{ID: "walk", Frames: []image.Image{walk}, Loop: true},
		{ID: "idle", Frames: []image.Image{idleFirst, idleSecond}, Loop: true},
	}, PetAtlasPackOptions{Name: "test-pet"})
	if err != nil {
		t.Fatalf("PackPetAtlas() error = %v", err)
	}
	if result.Manifest.Atlas.Anchor != "bottom-center" || result.Manifest.Atlas.Layout != "action-rows" {
		t.Fatalf("atlas metadata = %#v", result.Manifest.Atlas)
	}
	idle := result.Manifest.Animations["idle"]
	walkAnimation := result.Manifest.Animations["walk"]
	if len(idle.Frames) != 2 || len(walkAnimation.Frames) != 1 {
		t.Fatalf("animation frame counts = idle:%d walk:%d", len(idle.Frames), len(walkAnimation.Frames))
	}
	if got, want := idle.Frames[0], (PetAtlasFrame{X: 2, Y: 2, Width: 4, Height: 4, DurationMS: PetAtlasDefaultDuration, SubjectBounds: PetAtlasBounds{X: 1, Y: 1, Width: 2, Height: 3}}); got != want {
		t.Fatalf("idle first frame = %#v, want %#v", got, want)
	}
	if got := idle.Frames[1]; got.X != 10 || got.Y != 3 || got.Width != 2 || got.Height != 3 {
		t.Fatalf("idle second frame = %#v", got)
	}
	if got := walkAnimation.Frames[0]; got.X != 2 || got.Y != 10 || got.Width != 3 || got.Height != 2 {
		t.Fatalf("walk frame = %#v", got)
	}
	if result.Manifest.Atlas.Width != 14 || result.Manifest.Atlas.Height != 14 || len(result.PNG) == 0 {
		t.Fatalf("atlas size/png = %dx%d/%d", result.Manifest.Atlas.Width, result.Manifest.Atlas.Height, len(result.PNG))
	}
}

func TestPackPetAtlasRejectsDuplicateEmptyAndOversizedActions(t *testing.T) {
	frame := opaqueRect(2, 2, image.Rect(0, 0, 2, 2), color.NRGBA{A: 255})
	tests := []struct {
		name    string
		actions []PetAtlasAction
		options PetAtlasPackOptions
		want    string
	}{
		{
			name:    "duplicate action",
			actions: []PetAtlasAction{{ID: "idle", Frames: []image.Image{frame}}, {ID: "idle", Frames: []image.Image{frame}}},
			want:    "duplicate action",
		},
		{
			name:    "empty action",
			actions: []PetAtlasAction{{ID: "idle"}},
			want:    "contains no frames",
		},
		{
			name:    "oversized frame",
			actions: []PetAtlasAction{{ID: "idle", Frames: []image.Image{frame}}},
			options: PetAtlasPackOptions{MaxWidth: 5, MaxHeight: 5},
			want:    "exceeds texture",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := PackPetAtlas(test.actions, test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PackPetAtlas() error = %v, want message containing %q", err, test.want)
			}
		})
	}
}
