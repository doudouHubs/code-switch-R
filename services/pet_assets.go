package services

import (
	"errors"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
)

const petBuiltinAssetRoot = "resources/pets"

// PetAssetSource 是内置宠物资源的最小读取边界；embed.FS、fstest.MapFS
// 以及其他符合 io/fs 目录读取语义的 fs.FS 都可以直接注入，不让服务层依赖 main 包。
type PetAssetSource interface {
	Open(name string) (fs.File, error)
}

type petAssetSourceHolder struct {
	source PetAssetSource
}

var injectedPetAssetSource atomic.Pointer[petAssetSourceHolder]

// SetPetAssetSource 注入包含 resources/pets 目录的资源源。
// 传入 nil 会清除注入源并恢复开发态的受控磁盘探测；主程序应在注册 PetService
// 前注入 embed.FS，使打包后的 atlas 不再依赖工作区目录。
func SetPetAssetSource(source PetAssetSource) {
	if source == nil {
		injectedPetAssetSource.Store(nil)
		return
	}
	injectedPetAssetSource.Store(&petAssetSourceHolder{source: source})
}

func currentPetAssetSource() PetAssetSource {
	holder := injectedPetAssetSource.Load()
	if holder == nil {
		return nil
	}
	return holder.source
}

func isBuiltinPetSkinID(skinID string) bool {
	for _, allowed := range builtinPetSkinIDs {
		if skinID == allowed {
			return true
		}
	}
	return false
}

func loadBuiltinPetAtlasFromSource(source PetAssetSource, skinID string) (*PetAtlasAsset, error) {
	if !isSafePetSkinID(skinID) || !isBuiltinPetSkinID(skinID) {
		return nil, errors.New("内置皮肤 ID 未知或不安全")
	}
	root := path.Join(petBuiltinAssetRoot, skinID)
	manifestPath := path.Join(root, "pet.json")
	manifestBytes, err := readPetAssetSourceFile(source, manifestPath, petAtlasMaxManifestBytes)
	if err != nil {
		return nil, err
	}
	manifest, err := parsePetAtlasManifest(manifestBytes)
	if err != nil {
		return nil, err
	}

	// parsePetAtlasManifest 已将 image 限定为固定文件名；这里仍通过
	// fs.ValidPath 和逐级目录检查落地边界，防止未来放宽 manifest 规则时
	// 资源源读取逻辑出现路径穿越或 symlink 逃逸。
	atlasPath := path.Join(root, manifest.Atlas.Image)
	atlasBytes, err := readPetAssetSourceFile(source, atlasPath, petAtlasMaxImageBytes)
	if err != nil {
		return nil, err
	}
	return buildPetAtlasAssetFromBytes(manifestBytes, atlasBytes, manifest)
}

func loadBuiltinPetManifest(skinID string) ([]byte, error) {
	if !isSafePetSkinID(skinID) || !isBuiltinPetSkinID(skinID) {
		return nil, errors.New("内置皮肤 ID 未知或不安全")
	}
	if source := currentPetAssetSource(); source != nil {
		manifestPath := path.Join(petBuiltinAssetRoot, skinID, "pet.json")
		if err := validatePetAssetSourcePath(source, manifestPath); err != nil {
			return nil, err
		}
		return readPetAssetSourceFile(source, manifestPath, petAtlasMaxManifestBytes)
	}
	for _, root := range builtinPetResourceRoots() {
		skinRoot := filepath.Join(root, skinID)
		manifestPath := filepath.Join(skinRoot, "pet.json")
		if err := validatePetAtlasPath(skinRoot, manifestPath); err != nil {
			continue
		}
		if manifest, err := readPetAssetFile(manifestPath, petAtlasMaxManifestBytes); err == nil {
			return manifest, nil
		}
	}
	return nil, errors.New("内置皮肤 manifest 不存在或损坏")
}

func readPetAssetSourceFile(source PetAssetSource, name string, maxBytes int64) ([]byte, error) {
	if source == nil {
		return nil, errors.New("内置皮肤资源源为空")
	}
	if err := validatePetAssetSourcePath(source, name); err != nil {
		return nil, err
	}

	info, err := fs.Stat(source, name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("内置皮肤资源不是普通文件")
	}
	if info.Size() <= 0 || info.Size() > maxBytes {
		return nil, errors.New("内置皮肤资源大小不在允许范围内")
	}

	file, err := source.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// 不能只相信 FileInfo.Size：自定义 fs.FS 可能返回不准确的尺寸，
	// 读取时再多读一个字节，确保大文件不会穿过上限。
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || int64(len(data)) > maxBytes {
		return nil, errors.New("内置皮肤资源读取后超过大小限制")
	}
	return data, nil
}

func validatePetAssetSourcePath(source PetAssetSource, name string) error {
	if !fs.ValidPath(name) {
		return errors.New("内置皮肤资源路径无效")
	}

	// fs.FS 没有 Lstat；逐级查看 DirEntry 是抽象接口下仍能拒绝 symlink
	// 的最小可靠做法。无法列目录时直接失败，不能为了兼容未知实现而放宽安全边界。
	components := strings.Split(name, "/")
	parent := "."
	for index, component := range components {
		entries, err := fs.ReadDir(source, parent)
		if err != nil {
			return err
		}
		var entry fs.DirEntry
		for _, candidate := range entries {
			if candidate.Name() == component {
				entry = candidate
				break
			}
		}
		if entry == nil {
			return fs.ErrNotExist
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.New("内置皮肤资源路径包含 symlink")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return errors.New("内置皮肤资源路径包含 symlink")
		}
		if index < len(components)-1 && !info.IsDir() {
			return errors.New("内置皮肤资源路径中间项不是目录")
		}
		parent = path.Join(parent, component)
	}
	return nil
}
