package services

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func encodePetMediaTestPNG(t *testing.T, source image.Image) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, source); err != nil {
		t.Fatalf("encode test png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func decodePetMediaTestPNG(t *testing.T, encoded string) image.Image {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode result base64: %v", err)
	}
	result, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode result png: %v", err)
	}
	return result
}

func TestPetMediaAPIApplyChromaKeyReturnsPNGWithTransparentKey(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	source.SetNRGBA(0, 0, color.NRGBA{G: 255, A: 255})
	source.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 255})

	result, err := NewPetMediaAPIService().ApplyChromaKey(PetChromaKeyRequest{
		Data:      encodePetMediaTestPNG(t, source),
		KeyColor:  "#00ff00",
		Tolerance: 0,
	})
	if err != nil {
		t.Fatalf("ApplyChromaKey() error = %v", err)
	}
	if result.MediaType != "image/png" || result.Width != 2 || result.Height != 1 || result.Data == "" {
		t.Fatalf("ApplyChromaKey() result = %#v", result)
	}
	decoded := decodePetMediaTestPNG(t, result.Data)
	if got := color.NRGBAModel.Convert(decoded.At(0, 0)).(color.NRGBA).A; got != 0 {
		t.Fatalf("key pixel alpha = %d, want 0", got)
	}
	if got := color.NRGBAModel.Convert(decoded.At(1, 0)).(color.NRGBA).A; got != 255 {
		t.Fatalf("foreground alpha = %d, want 255", got)
	}
}

func TestPetMediaAPINormalizeSpriteReturnsBoundsAndPNG(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 6, 6))
	for y := 2; y < 5; y++ {
		for x := 1; x < 4; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 40, G: 80, B: 120, A: 255})
		}
	}

	result, err := NewPetMediaAPIService().NormalizeSprite(PetSpriteNormalizationAPIRequest{
		Data:           encodePetMediaTestPNG(t, source),
		TargetHeight:   6,
		AlphaThreshold: 1,
		PaddingX:       1,
		PaddingY:       1,
	})
	if err != nil {
		t.Fatalf("NormalizeSprite() error = %v", err)
	}
	if result.MediaType != "image/png" || result.OutputWidth <= 0 || result.OutputHeight <= 0 {
		t.Fatalf("NormalizeSprite() result = %#v", result)
	}
	if result.SourceBounds != (PetSpriteBounds{X: 1, Y: 2, Width: 3, Height: 3}) {
		t.Fatalf("source bounds = %#v", result.SourceBounds)
	}
	decoded := decodePetMediaTestPNG(t, result.Data)
	if decoded.Bounds().Dx() != result.OutputWidth || decoded.Bounds().Dy() != result.OutputHeight {
		t.Fatalf("normalized dimensions = %dx%d, result = %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy(), result.OutputWidth, result.OutputHeight)
	}
}

func TestPetMediaAPIPackAtlasRequiresIdleAndReturnsManifest(t *testing.T) {
	frame := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	frame.SetNRGBA(0, 0, color.NRGBA{B: 255, A: 255})
	encoded := encodePetMediaTestPNG(t, frame)

	result, err := NewPetMediaAPIService().PackAtlas(PetAtlasPackAPIRequest{
		Name: "api-test",
		Actions: []PetAtlasPackAPIAction{
			{ID: "idle", Frames: []string{encoded}, Loop: true},
			{ID: "walk", Frames: []string{encoded}, Loop: true},
		},
	})
	if err != nil {
		t.Fatalf("PackAtlas() error = %v", err)
	}
	if result.MediaType != "image/png" || result.Data == "" || result.Manifest.Name != "api-test" {
		t.Fatalf("PackAtlas() result = %#v", result)
	}
	if _, ok := result.Manifest.Animations["idle"]; !ok {
		t.Fatalf("PackAtlas() manifest has no idle animation: %#v", result.Manifest.Animations)
	}
	decoded := decodePetMediaTestPNG(t, result.Data)
	if decoded.Bounds().Dx() != result.Atlas.Width || decoded.Bounds().Dy() != result.Atlas.Height {
		t.Fatalf("atlas dimensions = %dx%d, metadata = %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy(), result.Atlas.Width, result.Atlas.Height)
	}
}

func TestPetMediaAPIPackAtlasPreservesMetadataAndBehaviors(t *testing.T) {
	frame := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	frame.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	encoded := encodePetMediaTestPNG(t, frame)
	metadata := PetAtlasManifestMetadata{
		Subject:                    "studio subject",
		ModelID:                    "studio-model",
		CreatedAt:                  101,
		UpdatedAt:                  202,
		Builtin:                    true,
		AssetVersion:               3,
		SpriteNormalizationVersion: 4,
	}
	behaviors := map[string]PetAtlasBehavior{
		"greeting": {Label: "问候", Actions: []string{"idle", "walk"}},
	}

	result, err := NewPetMediaAPIService().PackAtlas(PetAtlasPackAPIRequest{
		Name:      "studio-pet",
		Metadata:  &metadata,
		Behaviors: behaviors,
		Actions:   []PetAtlasPackAPIAction{{ID: "idle", Frames: []string{encoded}, Loop: true}, {ID: "walk", Frames: []string{encoded}, Loop: true}},
	})
	if err != nil {
		t.Fatalf("PackAtlas() error = %v", err)
	}
	manifest := result.Manifest
	if manifest.Subject != metadata.Subject || manifest.ModelID != metadata.ModelID ||
		manifest.CreatedAt != metadata.CreatedAt || manifest.UpdatedAt != metadata.UpdatedAt ||
		manifest.Builtin != metadata.Builtin || manifest.AssetVersion != metadata.AssetVersion ||
		manifest.SpriteNormalizationVersion != metadata.SpriteNormalizationVersion {
		t.Fatalf("PackAtlas() metadata = %#v, want %#v", manifest, metadata)
	}
	if got := manifest.Behaviors["greeting"]; got.Label != "问候" || len(got.Actions) != 2 || got.Actions[0] != "idle" || got.Actions[1] != "walk" {
		t.Fatalf("PackAtlas() behaviors = %#v", manifest.Behaviors)
	}
}

func TestPetMediaAPISplitActionSheetUsesStableGridAndKeepsAllFrames(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 10, 6))
	for y := 0; y < source.Bounds().Dy(); y++ {
		for x := 0; x < source.Bounds().Dx(); x++ {
			// 每列使用不同颜色，便于确认裁切没有交换顺序或丢失边界像素。
			source.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 20), G: uint8(y * 30), A: 255})
		}
	}

	result, err := NewPetMediaAPIService().SplitActionSheet(PetActionSheetSplitRequest{
		Data:       encodePetMediaTestPNG(t, source),
		FrameCount: 5,
	})
	if err != nil {
		t.Fatalf("SplitActionSheet() error = %v", err)
	}
	if result.Layout != (PetActionSheetLayout{Columns: 3, Rows: 2, FrameCount: 5}) {
		t.Fatalf("layout = %#v", result.Layout)
	}
	if len(result.Frames) != 5 {
		t.Fatalf("frame count = %d, want 5", len(result.Frames))
	}
	if result.Frames[0].Width != 3 || result.Frames[0].Height != 3 || result.Frames[4].Width != 4 || result.Frames[4].Height != 3 {
		t.Fatalf("frame dimensions = %#v", result.Frames)
	}
	first := decodePetMediaTestPNG(t, result.Frames[0].Data)
	last := decodePetMediaTestPNG(t, result.Frames[4].Data)
	if got := color.NRGBAModel.Convert(first.At(0, 0)).(color.NRGBA); got.R != 0 || got.G != 0 {
		t.Fatalf("first frame origin = %#v", got)
	}
	if got := color.NRGBAModel.Convert(last.At(0, 0)).(color.NRGBA); got.R != 120 || got.G != 90 {
		t.Fatalf("last frame origin = %#v", got)
	}
}

func TestPetMediaAPISplitActionSheetRejectsUnsupportedRequests(t *testing.T) {
	api := NewPetMediaAPIService()
	valid := encodePetMediaTestPNG(t, image.NewNRGBA(image.Rect(0, 0, 4, 4)))
	for _, test := range []struct {
		name       string
		data       string
		frameCount int
	}{
		{name: "invalid frame count", data: valid, frameCount: 9},
		{name: "data url", data: "data:image/png;base64," + valid, frameCount: 2},
		{name: "non png", data: base64.StdEncoding.EncodeToString([]byte("not png")), frameCount: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := api.SplitActionSheet(PetActionSheetSplitRequest{Data: test.data, FrameCount: test.frameCount}); err == nil {
				t.Fatal("SplitActionSheet() unexpectedly succeeded")
			}
		})
	}
}

func TestPetMediaAPIRejectsInvalidAndOversizedInputs(t *testing.T) {
	api := NewPetMediaAPIService()
	valid := encodePetMediaTestPNG(t, image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "invalid base64", data: "not-base64"},
		{name: "data url", data: "data:image/png;base64," + valid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := api.ApplyChromaKey(PetChromaKeyRequest{Data: test.data})
			if err == nil {
				t.Fatal("ApplyChromaKey() unexpectedly succeeded")
			}
		})
	}

	overSize := base64.StdEncoding.EncodeToString(make([]byte, petMediaAPIMaxInputBytes+1))
	if _, err := api.ApplyChromaKey(PetChromaKeyRequest{Data: overSize}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized input error = %v, want too large", err)
	}
}
