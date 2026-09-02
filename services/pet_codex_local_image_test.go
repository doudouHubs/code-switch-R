package services

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPetCodexLocalImageValidationKeepsFilesInsideAllowedRoots(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	inside := filepath.Join(root, "incoming.png")
	outside := filepath.Join(outsideRoot, "outside.png")
	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(inside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}

	runtime := &PetCodexRuntime{localImageRoots: []string{root}}
	valid, err := runtime.validateLocalImages([]PetAILocalImage{{Path: inside, MediaType: "image/png"}})
	if err != nil || len(valid) != 1 {
		t.Fatalf("valid local image = %#v, err=%v", valid, err)
	}
	canonical, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if valid[0].Path != filepath.Clean(canonical) {
		t.Fatalf("canonical local image path = %q, want %q", valid[0].Path, canonical)
	}

	tooLarge := filepath.Join(root, "too-large.png")
	if err := os.WriteFile(tooLarge, []byte{1}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(tooLarge, PetCodexMaxLocalImageBytes+1); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"relative":  filepath.Join("relative", "incoming.png"),
		"outside":   outside,
		"missing":   filepath.Join(root, "missing.png"),
		"directory": root,
		"too large": tooLarge,
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := runtime.validateLocalImages([]PetAILocalImage{{Path: path, MediaType: "image/png"}}); err == nil {
				t.Fatalf("path %q was accepted", path)
			}
		})
	}

	symlink := filepath.Join(root, "escape.png")
	if err := os.Symlink(outside, symlink); err == nil {
		t.Cleanup(func() { _ = os.Remove(symlink) })
		if _, err := runtime.validateLocalImages([]PetAILocalImage{{Path: symlink, MediaType: "image/png"}}); err == nil {
			t.Fatal("symlink outside allowed root was accepted")
		}
	}
}

func TestPetCodexBuildTurnStartParamsUsesLocalImageProtocol(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "image.jpg")
	if err := os.WriteFile(path, []byte{0xff, 0xd8, 0xff, 0xd9}, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &PetCodexRuntime{localImageRoots: []string{root}}
	images, err := runtime.validateLocalImages([]PetAILocalImage{{Path: path, MediaType: "image/jpeg"}})
	if err != nil {
		t.Fatal(err)
	}
	params := runtime.buildTurnStartParams(&petCodexActiveTurn{
		state: &petCodexPetState{threadID: "thread-local-image", workspace: root},
		request: petCodexChatInput{
			RequestID:   "request-local-image",
			UserText:    "请看看这张图",
			LocalImages: images,
		},
	})
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "data:image") || strings.Contains(string(raw), "base64") {
		t.Fatalf("local image payload unexpectedly contains base64: %s", raw)
	}
	input, ok := params["input"].([]map[string]any)
	if !ok || len(input) != 2 {
		t.Fatalf("turn input = %#v", params["input"])
	}
	if input[1]["type"] != "localImage" || input[1]["path"] != images[0].Path {
		t.Fatalf("local image input = %#v", input[1])
	}
}

func TestPetCodexHistoryReadsLocalImageWithoutExposingPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "history.webp")
	data := []byte("webp-history-image")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"thread": map[string]any{
			"id":  "history-local-image-thread",
			"cwd": root,
			"turns": []any{map[string]any{
				"id": "history-local-image-turn",
				"items": []any{map[string]any{
					"type": "userMessage",
					"content": []any{map[string]any{
						"type": "localImage",
						"path": path,
					}},
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	history, err := parsePetCodexHistoryResponse(raw, root, "history-local-image-thread", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 1 || len(history.Messages[0].Images) != 1 {
		t.Fatalf("history = %#v", history)
	}
	if history.Messages[0].Images[0].MediaType != "image/webp" || history.Messages[0].Images[0].Data != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("history image = %#v", history.Messages[0].Images[0])
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), path) {
		t.Fatalf("history exposed local path: %s", encoded)
	}
}

func TestPetChatRequestDoesNotSerializeLocalImagePath(t *testing.T) {
	request, err := json.Marshal(PetChatRequest{
		PetID: "pet-local-image",
		LocalImages: []PetAILocalImage{{
			Path:      `C:\private\image.png`,
			MediaType: "image/png",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(request), "private") || strings.Contains(string(request), "localImages") {
		t.Fatalf("local image path entered public request JSON: %s", request)
	}
}
