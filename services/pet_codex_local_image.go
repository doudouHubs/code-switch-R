package services

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const PetCodexMaxLocalImageBytes int64 = 4 << 20

func normalizePetCodexLocalImageShape(images []PetAILocalImage) ([]PetAILocalImage, error) {
	if len(images) == 0 {
		return nil, nil
	}
	if len(images) > PetAIMaxImageCount {
		return nil, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
	}
	normalized := make([]PetAILocalImage, len(images))
	for index, image := range images {
		path := strings.TrimSpace(image.Path)
		mediaType := strings.ToLower(strings.TrimSpace(image.MediaType))
		if path == "" || strings.IndexByte(path, 0) >= 0 || hasLineBreak(path) || !filepath.IsAbs(path) {
			return nil, newPetAIError(PET_AI_INVALID_REQUEST, 0, nil)
		}
		if mediaType != "" && !isPetAIImageMediaType(mediaType) {
			return nil, newPetAIError(PET_AI_MEDIA_TYPE_INVALID, 0, nil)
		}
		normalized[index] = PetAILocalImage{Path: filepath.Clean(path), MediaType: mediaType}
	}
	return normalized, nil
}

func (r *PetCodexRuntime) validateLocalImages(images []PetAILocalImage) ([]PetAILocalImage, error) {
	normalized, err := normalizePetCodexLocalImageShape(images)
	if err != nil || len(normalized) == 0 {
		return normalized, err
	}
	if r == nil || len(r.localImageRoots) == 0 {
		return nil, newPetAIError(PET_AI_INVALID_REQUEST, 0, errors.New("Codex local image roots are unavailable"))
	}
	for index, image := range normalized {
		canonical, err := resolvePetCodexLocalImagePath(image.Path, r.localImageRoots)
		if err != nil {
			return nil, newPetAIError(PET_AI_INVALID_REQUEST, 0, err)
		}
		normalized[index].Path = canonical
	}
	return normalized, nil
}

func resolvePetCodexLocalImagePath(path string, roots []string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return "", errors.New("Codex local image path is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("Codex local image file was not found")
	}
	// Lstat 明确拒绝链接本身；随后仍对真实路径做 EvalSymlinks，覆盖父目录
	// 的链接逃逸，避免“看起来在根目录下”的路径绕到其它目录。
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		return "", errors.New("Codex local image must be a non-empty regular file")
	}
	if info.Size() > PetCodexMaxLocalImageBytes {
		return "", fmt.Errorf("Codex local image exceeds %d bytes", PetCodexMaxLocalImageBytes)
	}

	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("Codex local image path cannot be resolved")
	}
	canonical = filepath.Clean(canonical)
	canonicalInfo, err := os.Stat(canonical)
	if err != nil || !canonicalInfo.Mode().IsRegular() || canonicalInfo.Size() <= 0 {
		return "", errors.New("Codex local image target is unavailable")
	}
	if canonicalInfo.Size() > PetCodexMaxLocalImageBytes {
		return "", fmt.Errorf("Codex local image exceeds %d bytes", PetCodexMaxLocalImageBytes)
	}

	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" || strings.IndexByte(root, 0) >= 0 || !filepath.IsAbs(root) {
			continue
		}
		canonicalRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
		if rootErr != nil {
			continue
		}
		if petCodexPathWithinRoot(canonical, canonicalRoot) {
			return canonical, nil
		}
	}
	return "", errors.New("Codex local image is outside allowed roots")
}

func petCodexPathWithinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		root = strings.ToLower(root)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readPetCodexLocalImage(path, mediaType string, roots []string) (PetAIImage, bool) {
	canonical, err := resolvePetCodexLocalImagePath(path, roots)
	if err != nil {
		return PetAIImage{}, false
	}
	data, err := os.ReadFile(canonical)
	if err != nil || len(data) == 0 || int64(len(data)) > PetCodexMaxLocalImageBytes {
		return PetAIImage{}, false
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		mediaType = strings.ToLower(strings.TrimSpace(mime.TypeByExtension(filepath.Ext(canonical))))
	}
	if !isPetAIImageMediaType(mediaType) {
		return PetAIImage{}, false
	}
	return PetAIImage{
		Data:      base64.StdEncoding.EncodeToString(data),
		MediaType: mediaType,
	}, true
}
