package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	petStudioSkinIDMaxLength = 64
	petStudioNameMaxLength   = 128
	petStudioSubjectMaxLen   = 512
	petStudioModelIDMaxLen   = 256
)

// PetStudioAPIStore 是 Studio 持久化边界所需的最小存储契约。
// PetDAO 已经实现这组方法；服务不感知 SQL、表结构或数据库连接，避免再造一套皮肤事实源。
type PetStudioAPIStore interface {
	UpsertSkin(context.Context, PetSkinRecord) error
	DeleteSkin(context.Context, string, string) error
	ListSkins(context.Context, string) ([]PetSkinRecord, error)
	SaveSkinSelection(context.Context, PetSkinSelection) error
}

// PetStudioStore 是较短的兼容命名，和 PetStore 的命名风格保持一致。
type PetStudioStore = PetStudioAPIStore

// PetStudioAPIOptions 只允许宿主注入受控资源 root，前端请求不会经过这个配置入口。
// RootDir 作为 Root 的兼容别名保留，两个字段同时设置时 Root 优先。
type PetStudioAPIOptions struct {
	Root    string
	RootDir string
}

type PetStudioOptions = PetStudioAPIOptions

// PetStudioSaveSkinRequest 只承载已经打包好的 atlas 和 manifest。
// AtlasBase64 是显式命名版本，Atlas 用于兼容前端更简短的字段名；二者都不是路径。
type PetStudioSaveSkinRequest struct {
	SkinID       string          `json:"skinId"`
	Name         string          `json:"name"`
	Atlas        string          `json:"atlas,omitempty"`
	AtlasBase64  string          `json:"atlasBase64,omitempty"`
	ManifestJSON json.RawMessage `json:"manifestJson"`
	Subject      string          `json:"subject,omitempty"`
	ModelID      string          `json:"modelId,omitempty"`
	Bind         bool            `json:"bind,omitempty"`
}

// PetStudioAPIService 是 Wails 面向 Studio 的独立适配器。
// 文件只是 atlas/manifest 载体，数据库记录仍然是皮肤的唯一事实来源。
type PetStudioAPIService struct {
	store   PetStudioAPIStore
	root    string
	initErr error
	mu      sync.Mutex
}

var _ PetStudioAPIStore = (*PetDAO)(nil)

// NewPetStudioAPIService 使用 ~/.open-cowork/pets 作为默认资源目录，与 OpenCowork
// 迁移源和前端资源约定保持一致；测试或宿主仍可显式注入受控 root。
// 测试或多实例宿主可以通过 options 注入绝对路径；构造时不提前创建目录，避免只读接口产生副作用。
func NewPetStudioAPIService(store PetStudioAPIStore, options ...PetStudioAPIOptions) *PetStudioAPIService {
	service := &PetStudioAPIService{store: store}
	configuredRoot := ""
	if len(options) > 0 {
		configuredRoot = strings.TrimSpace(options[0].Root)
		if configuredRoot == "" {
			configuredRoot = strings.TrimSpace(options[0].RootDir)
		}
	}
	if configuredRoot != "" {
		service.root = filepath.Clean(configuredRoot)
		return service
	}

	home, err := getUserHomeDir()
	if err != nil {
		service.initErr = fmt.Errorf("获取 Studio 资源 root 失败: %w", err)
		return service
	}
	service.root = defaultPetSkinRoot(home)
	return service
}

// SaveSkin 原子保存一个已经打包好的 atlas 和 manifest，并可选绑定为当前皮肤。
// 请求没有 outputDir/filePath 字段，所有资源都只能落到 root/<skinId>。
func (s *PetStudioAPIService) SaveSkin(petID string, request PetStudioSaveSkinRequest) (PetSkinRecord, error) {
	if err := s.validate(); err != nil {
		return PetSkinRecord{}, err
	}
	petID, err := validatePetStudioPetID(petID)
	if err != nil {
		return PetSkinRecord{}, err
	}
	skinID, err := validatePetStudioSkinID(request.SkinID)
	if err != nil {
		return PetSkinRecord{}, err
	}
	name, err := validatePetStudioText(request.Name, "皮肤名称", petStudioNameMaxLength, true)
	if err != nil {
		return PetSkinRecord{}, err
	}
	subject, err := validatePetStudioText(request.Subject, "皮肤 subject", petStudioSubjectMaxLen, false)
	if err != nil {
		return PetSkinRecord{}, err
	}
	modelID, err := validatePetStudioText(request.ModelID, "皮肤 modelId", petStudioModelIDMaxLen, false)
	if err != nil {
		return PetSkinRecord{}, err
	}

	atlasBytes, atlasWidth, atlasHeight, err := decodePetStudioAtlas(request)
	if err != nil {
		return PetSkinRecord{}, err
	}
	manifestBytes, atlas, err := validatePetStudioManifest(request.ManifestJSON, atlasWidth, atlasHeight)
	if err != nil {
		return PetSkinRecord{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	records, err := s.store.ListSkins(ctx, petID)
	if err != nil {
		return PetSkinRecord{}, fmt.Errorf("读取已有皮肤失败: %w", err)
	}
	previous, found := findPetStudioSkin(records, skinID)
	// 内置 ID 必须永远由产品资源 owner 管理，不能通过 Studio upsert 覆盖成自定义记录。
	if isBuiltinPetSkinID(skinID) || (found && previous.Builtin) {
		return PetSkinRecord{}, errors.New("不能保存内置皮肤 ID")
	}

	root, err := s.ensureRoot()
	if err != nil {
		return PetSkinRecord{}, err
	}
	target := filepath.Join(root, skinID)
	if err := validatePetStudioPath(root, target); err != nil {
		return PetSkinRecord{}, err
	}
	if found {
		if err := validatePetStudioRecordLocation(root, target, previous); err != nil {
			return PetSkinRecord{}, err
		}
	}

	stage, err := os.MkdirTemp(root, "."+skinID+".tmp-*")
	if err != nil {
		return PetSkinRecord{}, fmt.Errorf("创建 Studio 临时目录失败: %w", err)
	}
	// stage 被 rename 成正式目录后该路径不存在；失败分支统一清理，避免留下半成品。
	defer os.RemoveAll(stage)
	if err := writePetStudioAssets(stage, atlasBytes, manifestBytes); err != nil {
		return PetSkinRecord{}, err
	}

	commit, err := installPetStudioDirectory(root, target, stage)
	if err != nil {
		return PetSkinRecord{}, err
	}

	now := time.Now().UnixMilli()
	createdAt := now
	if found && previous.CreatedAt != nil {
		createdAt = *previous.CreatedAt
	}
	record := PetSkinRecord{
		PetID:        petID,
		SkinID:       skinID,
		Name:         name,
		Path:         target,
		AtlasPath:    filepath.Join(target, "atlas.png"),
		Subject:      subject,
		ModelID:      modelID,
		CreatedAt:    &createdAt,
		UpdatedAt:    &now,
		Builtin:      false,
		Atlas:        atlas,
		ManifestJSON: append(json.RawMessage(nil), manifestBytes...),
	}

	if err := s.store.UpsertSkin(ctx, record); err != nil {
		return PetSkinRecord{}, rollbackPetStudioSave(commit, fmt.Errorf("保存皮肤记录失败: %w", err))
	}
	if request.Bind {
		activeSkinID := skinID
		if err := s.store.SaveSkinSelection(ctx, PetSkinSelection{PetID: petID, ActiveSkinID: &activeSkinID}); err != nil {
			// Upsert 已成功但绑定失败时，必须先恢复数据库记录，再恢复目录；否则回滚后数据库会指向不存在的文件。
			if rollbackErr := s.rollbackSkinRecord(ctx, petID, skinID, previous, found); rollbackErr != nil {
				return PetSkinRecord{}, fmt.Errorf("绑定皮肤失败，且皮肤记录回滚失败；新资源仍保留以避免数据库指向空文件: %w", rollbackErr)
			}
			return PetSkinRecord{}, rollbackPetStudioSave(commit, fmt.Errorf("绑定皮肤失败: %w", err))
		}
	}

	// 新目录和数据库记录已经一致；旧备份是完整资源，清理失败不会撤销已提交事实，避免把可用皮肤回滚成空引用。
	_ = commit.finalize()
	return sanitizePetStudioSkinRecord(record), nil
}

// DeleteSkin 只删除数据库确认存在的自定义皮肤，且目录必须精确位于受控 root/<skinId>。
func (s *PetStudioAPIService) DeleteSkin(petID, skinID string) error {
	if err := s.validate(); err != nil {
		return err
	}
	petID, err := validatePetStudioPetID(petID)
	if err != nil {
		return err
	}
	skinID, err = validatePetStudioSkinID(skinID)
	if err != nil {
		return err
	}
	if isBuiltinPetSkinID(skinID) {
		return errors.New("不能删除内置皮肤")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	records, err := s.store.ListSkins(ctx, petID)
	if err != nil {
		return fmt.Errorf("读取待删除皮肤失败: %w", err)
	}
	record, found := findPetStudioSkin(records, skinID)
	if !found {
		return errors.New("皮肤记录不存在")
	}
	if record.Builtin {
		return errors.New("不能删除内置皮肤")
	}

	root, rootReal, err := s.inspectRoot()
	if err != nil {
		return err
	}
	target := filepath.Join(root, skinID)
	if err := validatePetStudioRecordLocation(root, target, record); err != nil {
		return err
	}
	if rootReal != "" {
		if err := validatePetStudioPath(root, target); err != nil {
			return err
		}
	}

	info, err := os.Lstat(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查待删除皮肤目录失败: %w", err)
	}
	hasDirectory := err == nil
	if hasDirectory {
		if err := validatePetStudioDirectory(root, target); err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("待删除皮肤资源不是目录")
		}
	}

	var tombstone string
	if hasDirectory {
		tombstone, err = makePetStudioTempPath(root, ".delete-*")
		if err != nil {
			return fmt.Errorf("创建删除临时目录失败: %w", err)
		}
		if err := os.Rename(target, tombstone); err != nil {
			_ = os.Remove(tombstone)
			return fmt.Errorf("暂存待删除皮肤失败: %w", err)
		}
	}

	if err := s.store.DeleteSkin(ctx, petID, skinID); err != nil {
		if tombstone != "" {
			if restoreErr := os.Rename(tombstone, target); restoreErr != nil {
				return fmt.Errorf("删除皮肤记录失败，且资源恢复失败: %w", errors.Join(err, restoreErr))
			}
		}
		return fmt.Errorf("删除皮肤记录失败: %w", err)
	}

	if tombstone == "" {
		return nil
	}
	if err := removePetStudioTree(tombstone); err != nil {
		// 文件删除失败时恢复目录和数据库记录，保持数据库事实与资源载体一致。
		restoreErr := os.Rename(tombstone, target)
		if restoreErr == nil {
			restoreErr = s.store.UpsertSkin(ctx, record)
		}
		return fmt.Errorf("删除皮肤资源失败: %w", errors.Join(err, restoreErr))
	}
	return nil
}

// ListSkins 返回数据库记录，但绝不把本地路径泄露给前端。
func (s *PetStudioAPIService) ListSkins(petID string) ([]PetSkinRecord, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	petID, err := validatePetStudioPetID(petID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.store.ListSkins(context.Background(), petID)
	if err != nil {
		return nil, fmt.Errorf("列出皮肤失败: %w", err)
	}
	if records == nil {
		records = make([]PetSkinRecord, 0)
	}
	sort.SliceStable(records, func(left, right int) bool {
		leftUpdated := petStudioTimestamp(records[left].UpdatedAt)
		rightUpdated := petStudioTimestamp(records[right].UpdatedAt)
		if leftUpdated != rightUpdated {
			return leftUpdated > rightUpdated
		}
		return records[left].SkinID < records[right].SkinID
	})
	for index := range records {
		records[index] = sanitizePetStudioSkinRecord(records[index])
	}
	return records, nil
}

func (s *PetStudioAPIService) validate() error {
	if s == nil {
		return errors.New("Pet Studio service 未配置")
	}
	if s.initErr != nil {
		return s.initErr
	}
	if s.store == nil {
		return errors.New("Pet Studio store 未配置")
	}
	if strings.TrimSpace(s.root) == "" {
		return errors.New("Pet Studio 资源 root 未配置")
	}
	return nil
}

func (s *PetStudioAPIService) ensureRoot() (string, error) {
	root, err := normalizePetStudioRoot(s.root)
	if err != nil {
		return "", err
	}
	if err := ensurePetStudioDirectory(root); err != nil {
		return "", fmt.Errorf("准备 Studio 资源 root 失败: %w", err)
	}
	return root, nil
}

// inspectRoot 不为删除操作凭空创建目录；root 不存在时仍可安全删除数据库中的孤立记录。
func (s *PetStudioAPIService) inspectRoot() (string, string, error) {
	root, err := normalizePetStudioRoot(s.root)
	if err != nil {
		return "", "", err
	}
	if err := validatePetStudioPathComponents(root); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return root, "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("检查 Studio 资源 root 失败: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", "", errors.New("Studio 资源 root 必须是普通目录，不能是 symlink")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("解析 Studio 资源 root real path 失败: %w", err)
	}
	return root, filepath.Clean(realRoot), nil
}

func validatePetStudioPetID(petID string) (string, error) {
	petID = strings.TrimSpace(petID)
	if petID == "" {
		return "", errors.New("petId 不能为空")
	}
	if strings.IndexByte(petID, 0) >= 0 || strings.ContainsAny(petID, `/\\`) {
		return "", errors.New("petId 不能包含路径字符")
	}
	if !utf8.ValidString(petID) || utf8.RuneCountInString(petID) > 128 {
		return "", errors.New("petId 无效或过长")
	}
	return petID, nil
}

func validatePetStudioSkinID(skinID string) (string, error) {
	skinID = strings.TrimSpace(skinID)
	if !isSafePetSkinID(skinID) || len(skinID) > petStudioSkinIDMaxLength {
		return "", errors.New("skinId 必须以字母开头，后续只能包含字母、数字、_ 或 -")
	}
	return skinID, nil
}

func validatePetStudioText(value, label string, maxLength int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("%s不能为空", label)
		}
		return "", nil
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxLength {
		return "", fmt.Errorf("%s过长或编码无效", label)
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return "", fmt.Errorf("%s包含不可用控制字符", label)
		}
	}
	return value, nil
}

func decodePetStudioAtlas(request PetStudioSaveSkinRequest) ([]byte, int, int, error) {
	atlas := strings.TrimSpace(request.Atlas)
	legacyAtlas := strings.TrimSpace(request.AtlasBase64)
	if atlas != "" && legacyAtlas != "" && atlas != legacyAtlas {
		return nil, 0, 0, errors.New("atlas 与 atlasBase64 不能同时提交不同内容")
	}
	if atlas == "" {
		atlas = legacyAtlas
	}
	if atlas == "" || strings.Contains(atlas, ",") || strings.ContainsAny(atlas, " \t\r\n") {
		return nil, 0, 0, errors.New("atlas 必须是 bare base64，不能是 data URL")
	}
	maxEncoded := base64.StdEncoding.EncodedLen(int(petAtlasMaxImageBytes))
	if len(atlas) > maxEncoded || int64(base64.StdEncoding.DecodedLen(len(atlas))) > petAtlasMaxImageBytes {
		return nil, 0, 0, errors.New("atlas 图片过大")
	}
	decoded, err := base64.StdEncoding.DecodeString(atlas)
	if err != nil || len(decoded) == 0 {
		return nil, 0, 0, errors.New("atlas base64 无效")
	}
	if int64(len(decoded)) > petAtlasMaxImageBytes {
		return nil, 0, 0, errors.New("atlas 图片过大")
	}
	config, err := png.DecodeConfig(bytes.NewReader(decoded))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil, 0, 0, errors.New("atlas 必须是有效 PNG 图片")
	}
	if config.Width > PetAtlasMaxTextureSize || config.Height > PetAtlasMaxTextureSize {
		return nil, 0, 0, fmt.Errorf("atlas 尺寸不能超过 %d", PetAtlasMaxTextureSize)
	}
	return decoded, config.Width, config.Height, nil
}

func validatePetStudioManifest(raw json.RawMessage, atlasWidth, atlasHeight int) (json.RawMessage, PetAtlasMetadata, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}))
	if len(trimmed) == 0 || int64(len(trimmed)) > petAtlasMaxManifestBytes || !json.Valid(trimmed) {
		return nil, PetAtlasMetadata{}, errors.New("manifest JSON 无效或过大")
	}
	// 与 DAO 保持同一套 API key 清洗，确保磁盘载体和数据库 JSON 不出现两份事实。
	sanitized, err := sanitizePetSkinManifest(trimmed)
	if err != nil {
		return nil, PetAtlasMetadata{}, fmt.Errorf("清洗 manifest JSON 失败: %w", err)
	}
	sanitized = bytes.TrimSpace(sanitized)
	if int64(len(sanitized)) > petAtlasMaxManifestBytes {
		return nil, PetAtlasMetadata{}, errors.New("manifest JSON 过大")
	}

	var manifest PetAtlasManifest
	if err := json.Unmarshal(sanitized, &manifest); err != nil {
		return nil, PetAtlasMetadata{}, fmt.Errorf("解析 manifest JSON 失败: %w", err)
	}
	if manifest.AtlasVersion != PetAtlasVersion {
		return nil, PetAtlasMetadata{}, errors.New("manifest atlasVersion 不受支持")
	}
	if manifest.Atlas.AtlasVersion != 0 && manifest.Atlas.AtlasVersion != PetAtlasVersion {
		return nil, PetAtlasMetadata{}, errors.New("manifest atlas.atlasVersion 与 atlasVersion 不一致")
	}
	if manifest.Atlas.Image != "atlas.png" {
		return nil, PetAtlasMetadata{}, errors.New("manifest atlas.image 必须是 atlas.png")
	}
	if manifest.Atlas.Width != atlasWidth || manifest.Atlas.Height != atlasHeight {
		return nil, PetAtlasMetadata{}, errors.New("manifest atlas 尺寸与图片 metadata 不一致")
	}
	if manifest.Atlas.Anchor != "" && manifest.Atlas.Anchor != "bottom-center" {
		return nil, PetAtlasMetadata{}, errors.New("manifest atlas.anchor 不受支持")
	}
	if manifest.Atlas.Layout != "" && manifest.Atlas.Layout != "action-rows" {
		return nil, PetAtlasMetadata{}, errors.New("manifest atlas.layout 不受支持")
	}
	if len(manifest.Animations) == 0 {
		return nil, PetAtlasMetadata{}, errors.New("manifest 必须包含 animations")
	}
	idle, ok := manifest.Animations["idle"]
	if !ok || len(idle.Frames) == 0 {
		return nil, PetAtlasMetadata{}, errors.New("manifest 必须包含 idle 动画")
	}
	for actionID, animation := range manifest.Animations {
		if !validPetActionID(actionID) {
			return nil, PetAtlasMetadata{}, fmt.Errorf("manifest action ID 无效: %q", actionID)
		}
		if len(animation.Frames) == 0 {
			return nil, PetAtlasMetadata{}, fmt.Errorf("manifest 动画 %q 没有帧", actionID)
		}
		for index, frame := range animation.Frames {
			if !petStudioBoundsWithin(frame.X, frame.Y, frame.Width, frame.Height, atlasWidth, atlasHeight) {
				return nil, PetAtlasMetadata{}, fmt.Errorf("manifest 动画 %q 第 %d 帧越过 atlas", actionID, index)
			}
			if frame.DurationMS != 0 && (frame.DurationMS < 16 || frame.DurationMS > 60_000) {
				return nil, PetAtlasMetadata{}, fmt.Errorf("manifest 动画 %q 第 %d 帧 duration 无效", actionID, index)
			}
			if frame.SubjectBounds.Width != 0 || frame.SubjectBounds.Height != 0 || frame.SubjectBounds.X != 0 || frame.SubjectBounds.Y != 0 {
				if !petStudioBoundsWithin(frame.SubjectBounds.X, frame.SubjectBounds.Y, frame.SubjectBounds.Width, frame.SubjectBounds.Height, frame.Width, frame.Height) {
					return nil, PetAtlasMetadata{}, fmt.Errorf("manifest 动画 %q 第 %d 帧 subjectBounds 无效", actionID, index)
				}
			}
		}
	}

	atlas := manifest.Atlas
	atlas.AtlasVersion = PetAtlasVersion
	if atlas.Anchor == "" {
		atlas.Anchor = "bottom-center"
	}
	if atlas.Layout == "" {
		atlas.Layout = "action-rows"
	}
	return append(json.RawMessage(nil), sanitized...), atlas, nil
}

func petStudioBoundsWithin(x, y, width, height, maxWidth, maxHeight int) bool {
	return x >= 0 && y >= 0 && width > 0 && height > 0 && x <= maxWidth-width && y <= maxHeight-height
}

func writePetStudioAssets(stage string, atlas, manifest []byte) error {
	if err := atomicWriteFile(filepath.Join(stage, "atlas.png"), atlas, 0o600); err != nil {
		return fmt.Errorf("写入 atlas.png 失败: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(stage, "pet.json"), manifest, 0o600); err != nil {
		return fmt.Errorf("写入 pet.json 失败: %w", err)
	}
	if err := validatePetStudioDirectory(filepath.Dir(stage), stage); err != nil {
		return fmt.Errorf("校验 Studio 临时目录失败: %w", err)
	}
	for _, name := range []string{"atlas.png", "pet.json"} {
		info, err := os.Lstat(filepath.Join(stage, name))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("Studio 临时目录缺少受控文件: %s", name)
		}
	}
	return nil
}

type petStudioDirectoryCommit struct {
	root      string
	target    string
	backup    string
	installed bool
}

func installPetStudioDirectory(root, target, stage string) (*petStudioDirectoryCommit, error) {
	commit := &petStudioDirectoryCommit{root: root, target: target}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("Studio 目标目录不是受控普通目录")
		}
		if err := validatePetStudioDirectory(root, target); err != nil {
			return nil, err
		}
		backup, err := makePetStudioTempPath(root, ".backup-*")
		if err != nil {
			return nil, fmt.Errorf("创建 Studio 旧资源备份位失败: %w", err)
		}
		if err := os.Rename(target, backup); err != nil {
			_ = os.Remove(backup)
			return nil, fmt.Errorf("暂存旧 Studio 资源失败: %w", err)
		}
		commit.backup = backup
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("检查 Studio 目标目录失败: %w", err)
	}

	// stage 与 target 同 root、同文件系统；目录 rename 让两个文件作为一个完整资源单元提交。
	if err := os.Rename(stage, target); err != nil {
		if commit.backup != "" {
			if restoreErr := os.Rename(commit.backup, target); restoreErr != nil {
				return nil, fmt.Errorf("安装 Studio 新资源失败，且旧资源恢复失败: %w", errors.Join(err, restoreErr))
			}
			commit.backup = ""
		}
		return nil, fmt.Errorf("安装 Studio 新资源失败: %w", err)
	}
	commit.installed = true
	return commit, nil
}

func (c *petStudioDirectoryCommit) rollback() error {
	if c == nil {
		return nil
	}
	var rollbackErr error
	if c.installed {
		if err := removePetStudioTree(c.target); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		} else {
			c.installed = false
		}
	}
	if c.backup != "" {
		if err := os.Rename(c.backup, c.target); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		} else {
			c.backup = ""
		}
	}
	return rollbackErr
}

func (c *petStudioDirectoryCommit) finalize() error {
	if c == nil || c.backup == "" {
		return nil
	}
	err := removePetStudioTree(c.backup)
	if err == nil {
		c.backup = ""
	}
	return err
}

func rollbackPetStudioSave(commit *petStudioDirectoryCommit, cause error) error {
	if rollbackErr := commit.rollback(); rollbackErr != nil {
		return fmt.Errorf("%w；文件回滚失败: %v", cause, rollbackErr)
	}
	return cause
}

func (s *PetStudioAPIService) rollbackSkinRecord(ctx context.Context, petID, skinID string, previous PetSkinRecord, found bool) error {
	if found {
		return s.store.UpsertSkin(ctx, previous)
	}
	return s.store.DeleteSkin(ctx, petID, skinID)
}

func findPetStudioSkin(records []PetSkinRecord, skinID string) (PetSkinRecord, bool) {
	for _, record := range records {
		if record.SkinID == skinID {
			return record, true
		}
	}
	return PetSkinRecord{}, false
}

func validatePetStudioRecordLocation(root, target string, record PetSkinRecord) error {
	if path := strings.TrimSpace(record.Path); path != "" {
		if !filepath.IsAbs(path) || canonicalPathForCompare(filepath.Clean(path)) != canonicalPathForCompare(target) || !pathWithin(root, filepath.Clean(path)) {
			return errors.New("数据库皮肤目录越过 Studio 受控 root")
		}
	}
	if path := strings.TrimSpace(record.AtlasPath); path != "" {
		expected := filepath.Join(target, "atlas.png")
		if !filepath.IsAbs(path) || canonicalPathForCompare(filepath.Clean(path)) != canonicalPathForCompare(expected) || !pathWithin(root, filepath.Clean(path)) {
			return errors.New("数据库 atlas 路径越过 Studio 受控 root")
		}
	}
	return nil
}

func normalizePetStudioRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || strings.IndexByte(root, 0) >= 0 || !filepath.IsAbs(root) {
		return "", errors.New("Studio 资源 root 必须是绝对路径")
	}
	return filepath.Clean(root), nil
}

func ensurePetStudioDirectory(path string) error {
	path = filepath.Clean(path)
	if err := validatePetStudioPathComponents(path); err != nil {
		return err
	}
	missing := make([]string, 0)
	current := path
	for {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := os.Mkdir(missing[index], 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(missing[index])
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("Studio 资源目录不能是 symlink 或非目录")
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Studio 资源 root 必须是普通目录，不能是 symlink")
	}
	if _, err := filepath.EvalSymlinks(path); err != nil {
		return fmt.Errorf("解析 Studio 资源 root real path 失败: %w", err)
	}
	return nil
}

func validatePetStudioPathComponents(path string) error {
	path = filepath.Clean(path)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("路径组件不能是 symlink: %s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("路径组件不是目录: %s", current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("检查路径组件失败: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func validatePetStudioPath(root, candidate string) error {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if !pathWithin(root, candidate) {
		return errors.New("Studio 路径越过受控 root")
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("解析 Studio root real path 失败: %w", err)
	}
	if err := rejectSymlinkEscape(filepath.Clean(rootReal), candidate); err != nil {
		return err
	}
	return nil
}

func validatePetStudioDirectory(root, path string) error {
	if err := validatePetStudioPath(root, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Studio 资源目录不是受控普通目录")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || !pathWithin(filepath.Clean(mustEvalPetStudioRoot(root)), filepath.Clean(realPath)) {
		if err == nil {
			err = errors.New("目录 real path 越过 Studio root")
		}
		return err
	}
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Studio 资源树拒绝 symlink: %s", current)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Studio 资源不是普通文件: %s", current)
		}
		return nil
	})
}

func mustEvalPetStudioRoot(root string) string {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return filepath.Clean(root)
	}
	return filepath.Clean(realRoot)
}

func removePetStudioTree(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("拒绝删除 symlink 资源")
	}
	if info.IsDir() {
		if err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("拒绝删除包含 symlink 的资源树: %s", current)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return os.RemoveAll(path)
}

func makePetStudioTempPath(root, pattern string) (string, error) {
	path, err := os.MkdirTemp(root, pattern)
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func sanitizePetStudioSkinRecord(record PetSkinRecord) PetSkinRecord {
	record.Path = ""
	record.AtlasPath = ""
	return record
}

func petStudioTimestamp(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
