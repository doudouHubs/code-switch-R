package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	petAtlasDataURLPrefix    = "data:image/png;base64,"
	petAtlasMaxManifestBytes = 1 << 20
	petAtlasMaxImageBytes    = 16 << 20
)

var builtinPetSkinIDs = [...]string{"capybara", "penguin", "anya"}

// PetAtlasAsset 是前端可以直接消费的 atlas 资源形状。
// Src 只允许受控的 data URL，不能把持久化皮肤记录里的本地路径直接暴露给 Vue。
type PetAtlasAsset struct {
	Src      string          `json:"src"`
	Manifest json.RawMessage `json:"manifest"`
}

type petAtlasManifestInfo struct {
	AtlasVersion int `json:"atlasVersion"`
	Atlas        struct {
		Image  string `json:"image"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"atlas"`
}

// PetSnapshot 是 Wails 暴露给 Vue 的稳定快照。计划、梦境历史和记忆虽然来自
// PetMigrationSnapshot，但只允许经过本层清洗后进入前端协议，避免把存储结构和本地路径误当成 API。
type PetSnapshot struct {
	State         PetState                `json:"state"`
	Experience    PetExperience           `json:"experience"`
	Window        PetWindowConfig         `json:"window"`
	Care          PetCareConfig           `json:"care"`
	Agent         PetAgentConfig          `json:"agent"`
	Dream         PetDreamConfig          `json:"dream"`
	Plans         []PetPlanRecord         `json:"plans"`
	Dreams        []PetDreamHistoryRecord `json:"dreams"`
	Memories      []PetMemoryRecord       `json:"memories"`
	SkinSelection PetSkinSelection        `json:"skinSelection"`
	Skins         []PetSkinRecord         `json:"skins"`
	Atlas         *PetAtlasAsset          `json:"atlas"`
}

// PetDailyBonusResult 把每日奖励和领取后的稳定快照放在同一个响应里，
// 前端不需要先领取再额外读取，也不会把“已领取”状态显示成旧值。
type PetDailyBonusResult struct {
	Bonus    int64       `json:"bonus"`
	Snapshot PetSnapshot `json:"snapshot"`
}

// PetSettingsInput 只接收可以安全持久化的配置引用，不接收 provider 实体或 API Key。
// 指针用于区分“调用方没有提交这个配置”和“调用方提交了配置的零值”，从而支持
// SaveSettings 的部分更新而不覆盖同一宠物的其他设置。
type PetSettingsInput struct {
	Window        *PetWindowConfig  `json:"window,omitempty"`
	Care          *PetCareConfig    `json:"care,omitempty"`
	Agent         *PetAgentConfig   `json:"agent,omitempty"`
	Dream         *PetDreamConfig   `json:"dream,omitempty"`
	SkinSelection *PetSkinSelection `json:"skinSelection,omitempty"`
}

// GetSnapshot 返回指定宠物的前端快照。petID 必须由调用方明确提供，不能让空 ID
// 静默回退到 default，否则多宠物场景会把设置写入错误的持久化分区。
func (s *PetService) GetSnapshot(petID string) (PetSnapshot, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetSnapshot{}, err
	}

	snapshot, err := service.Load()
	if err != nil {
		return PetSnapshot{}, err
	}
	return newPetSnapshot(snapshot)
}

// PerformAction 把前端动作名映射到 PetService 的业务入口。
// 规则层返回的 OK=false 是正常业务结果，必须原样返回；只有参数、读取或持久化
// 失败才通过 error 返回，避免 Vue 把“宠物太饱”误报成 runtime 异常。
func (s *PetService) PerformAction(petID string, action PetAction) (PetActionResult, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetActionResult{}, err
	}

	action = PetAction(strings.TrimSpace(string(action)))
	switch action {
	case PetActionFeed:
		return service.Feed()
	case PetActionBathe:
		return service.Bathe()
	case PetActionSoak:
		return service.Soak()
	case PetActionPlay:
		return service.Play()
	case PetActionSleep:
		if _, err := service.ToggleSleep(); err != nil {
			return PetActionResult{}, err
		}
		return PetActionResult{OK: true}, nil
	case PetActionWork:
		return service.StartWork()
	case PetActionStudy:
		return service.StartStudy()
	case PetAction("petted"):
		// 兼容不经过单独 Petted 入口的调用方；前端正式入口仍是 Petted。
		if err := service.petted(); err != nil {
			return PetActionResult{}, err
		}
		return PetActionResult{OK: true}, nil
	default:
		return PetActionResult{}, fmt.Errorf("不支持的宠物动作 %q", action)
	}
}

// RecordProactive 记录一次计入每日配额的主动搭话。计数必须由后端持久化，
// 否则桌宠窗口重启或多窗口同时运行会绕过频率限制。
func (s *PetService) RecordProactive(petID string, now int64) (PetSnapshot, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetSnapshot{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()

	if now <= 0 {
		now = petNow(nil)
	}
	snapshot, err := service.loadSnapshotLocked(context.Background(), now)
	if err != nil {
		return PetSnapshot{}, err
	}
	next := PetRecordProactive(*snapshot.State, now)
	snapshot.State = &next
	if err := service.saveSnapshotLocked(context.Background(), snapshot); err != nil {
		return PetSnapshot{}, err
	}
	return newPetSnapshot(snapshot)
}

// ClaimDailyBonusForPet 是按 petId 分区的 Wails 入口。原有 ClaimDailyBonus
// 保留给内部/兼容调用，桌宠窗口不能用无 petId 的方法避免串宠物。
func (s *PetService) ClaimDailyBonusForPet(petID string, now int64) (PetDailyBonusResult, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetDailyBonusResult{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()

	if now <= 0 {
		now = petNow(nil)
	}
	snapshot, err := service.loadSnapshotLocked(context.Background(), now)
	if err != nil {
		return PetDailyBonusResult{}, err
	}
	next, bonus := PetClaimDailyBonus(*snapshot.State, now)
	if bonus == 0 {
		stable, stableErr := newPetSnapshot(snapshot)
		if stableErr != nil {
			return PetDailyBonusResult{}, stableErr
		}
		return PetDailyBonusResult{Snapshot: stable}, nil
	}
	snapshot.State = &next
	if err := service.saveSnapshotLocked(context.Background(), snapshot); err != nil {
		return PetDailyBonusResult{}, err
	}
	stable, err := newPetSnapshot(snapshot)
	if err != nil {
		return PetDailyBonusResult{}, err
	}
	return PetDailyBonusResult{Bonus: bonus, Snapshot: stable}, nil
}

// MarkMilestoneForPet 只允许里程碑单调前进；较旧的客户端事件不会把已庆祝天数回退。
func (s *PetService) MarkMilestoneForPet(petID string, days int64) (PetSnapshot, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetSnapshot{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()

	if days < 0 {
		return PetSnapshot{}, errors.New("陪伴天数不能为负数")
	}
	snapshot, err := service.loadSnapshotLocked(context.Background(), petNow(nil))
	if err != nil {
		return PetSnapshot{}, err
	}
	if days > snapshot.State.LastMilestoneDays {
		next := PetMarkMilestone(*snapshot.State, days)
		snapshot.State = &next
		if err := service.saveSnapshotLocked(context.Background(), snapshot); err != nil {
			return PetSnapshot{}, err
		}
	}
	return newPetSnapshot(snapshot)
}

// SaveSettings 合并指定宠物的设置并返回保存后的完整快照。配置里的非空 petId
// 必须与路由 petID 一致；只把 petID 强制改写成目标值会掩盖前端串宠物的 bug。
func (s *PetService) SaveSettings(petID string, settings PetSettingsInput) (PetSnapshot, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetSnapshot{}, err
	}
	petID = service.petID
	if err := validatePetSettingsPetIDs(settings, petID); err != nil {
		return PetSnapshot{}, err
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	ctx := context.Background()
	snapshot, err := service.loadSnapshotLocked(ctx, petNow(nil))
	if err != nil {
		return PetSnapshot{}, err
	}

	// 先读现有快照再逐项替换，确保前端只提交一个设置页分组时，其他配置、
	// 状态、皮肤记录和未来扩展字段都不会被清空。
	if settings.Window != nil {
		window := *settings.Window
		window.PetID = petID
		snapshot.Window = &window
	}
	if settings.Care != nil {
		care := normalizeCareConfig(*settings.Care, petID)
		snapshot.Care = &care
	}
	if settings.Agent != nil {
		agent := normalizeAgentConfig(*settings.Agent, petID)
		snapshot.Agent = &agent
	}
	if settings.Dream != nil {
		dream := normalizeDreamConfig(*settings.Dream, petID)
		snapshot.DreamConfig = &dream
	}
	if settings.SkinSelection != nil {
		selection := *settings.SkinSelection
		selection.PetID = petID
		snapshot.SkinSelection = &selection
	}

	if err := service.saveSnapshotLocked(ctx, snapshot); err != nil {
		return PetSnapshot{}, err
	}
	return newPetSnapshot(snapshot)
}

func (s *PetService) apiServiceForPet(petID string) (*PetService, error) {
	if s == nil {
		return nil, errors.New("宠物服务为空")
	}
	if err := s.validate(); err != nil {
		return nil, err
	}

	petID = strings.TrimSpace(petID)
	if petID == "" {
		return nil, errors.New("petId 不能为空")
	}
	if petID == strings.TrimSpace(s.petID) {
		return s, nil
	}
	return NewPetServiceForPet(s.repository, petID), nil
}

func validatePetSettingsPetIDs(settings PetSettingsInput, petID string) error {
	checks := []struct {
		name string
		id   string
	}{
		{name: "window", id: petSettingsWindowID(settings.Window)},
		{name: "care", id: petSettingsCareID(settings.Care)},
		{name: "agent", id: petSettingsAgentID(settings.Agent)},
		{name: "dream", id: petSettingsDreamID(settings.Dream)},
		{name: "skinSelection", id: petSettingsSkinSelectionID(settings.SkinSelection)},
	}
	for _, check := range checks {
		if check.id != "" && check.id != petID {
			return fmt.Errorf("宠物设置 %s 的 petId %q 与目标 petId %q 不一致", check.name, check.id, petID)
		}
	}
	return nil
}

func petSettingsWindowID(value *PetWindowConfig) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.PetID)
}

func petSettingsCareID(value *PetCareConfig) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.PetID)
}

func petSettingsAgentID(value *PetAgentConfig) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.PetID)
}

func petSettingsDreamID(value *PetDreamConfig) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.PetID)
}

func petSettingsSkinSelectionID(value *PetSkinSelection) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.PetID)
}

func newPetSnapshot(snapshot PetMigrationSnapshot) (PetSnapshot, error) {
	if snapshot.State == nil {
		return PetSnapshot{}, errors.New("宠物快照缺少 state")
	}
	if snapshot.Experience == nil {
		return PetSnapshot{}, errors.New("宠物快照缺少 experience")
	}
	if snapshot.Window == nil {
		return PetSnapshot{}, errors.New("宠物快照缺少 window")
	}
	if snapshot.Care == nil {
		return PetSnapshot{}, errors.New("宠物快照缺少 care")
	}
	if snapshot.Agent == nil {
		return PetSnapshot{}, errors.New("宠物快照缺少 agent")
	}
	if snapshot.DreamConfig == nil {
		return PetSnapshot{}, errors.New("宠物快照缺少 dream")
	}

	agent := *snapshot.Agent
	// ProjectFolder 是后端项目引用的本地路径，前端只需要 projectId/projectName；
	// 即使旧数据库里存在该值，也不能随着稳定快照穿过 Wails 边界。
	agent.ProjectFolder = nil

	selection := PetSkinSelection{PetID: snapshot.PetID}
	if snapshot.SkinSelection != nil {
		selection = *snapshot.SkinSelection
		selection.PetID = snapshot.PetID
	}

	// 内置资源来自 embed.FS，不经过数据库迁移，所以必须在 API 边界合并到列表；
	// 否则 atlas 能渲染，设置页却看不到可选的 anya/penguin/capybara。
	mergedSkins := mergeBuiltinPetSkins(snapshot.PetID, snapshot.Skins)
	// nil slice 会被 JSON 编码成 null；前端契约要求 skins 始终是数组，
	// 因此没有皮肤记录时显式返回 []，让调用方无需分支处理两种空值。
	skins := make([]PetSkinRecord, len(mergedSkins))
	for index, skin := range mergedSkins {
		// Path/AtlasPath 只是后端持久化引用，不能成为浏览器资源地址或随快照泄漏。
		skin.Path = ""
		skin.AtlasPath = ""
		skins[index] = skin
	}

	// 这些记录属于前端需要展示的已落盘数据，但不能直接复用迁移快照里的切片：
	// 除了避免调用方修改内部数据，还要把所有空切片固定成 []，并清掉梦境归档中的本地图片路径。
	plans := make([]PetPlanRecord, len(snapshot.PlanRecords))
	for index, plan := range snapshot.PlanRecords {
		plan.Script.Steps = make([]PetPlanStep, len(plan.Script.Steps))
		copy(plan.Script.Steps, snapshot.PlanRecords[index].Script.Steps)
		plans[index] = plan
	}
	dreams := make([]PetDreamHistoryRecord, len(snapshot.Dreams))
	for index, dream := range snapshot.Dreams {
		dream.Keywords = make([]string, len(dream.Keywords))
		copy(dream.Keywords, snapshot.Dreams[index].Keywords)
		// ImagePath 是数据库中的本地归档引用，前端协议只保留字段形状，不返回路径内容。
		dream.ImagePath = nil
		dreams[index] = dream
	}
	memories := make([]PetMemoryRecord, len(snapshot.Memories))
	copy(memories, snapshot.Memories)

	atlas := resolvePetAtlas(snapshot)

	return PetSnapshot{
		State:         *snapshot.State,
		Experience:    *snapshot.Experience,
		Window:        *snapshot.Window,
		Care:          *snapshot.Care,
		Agent:         agent,
		Dream:         *snapshot.DreamConfig,
		Plans:         plans,
		Dreams:        dreams,
		Memories:      memories,
		SkinSelection: selection,
		Skins:         skins,
		Atlas:         atlas,
	}, nil
}

// resolvePetAtlas 按 active skin -> 快照内置皮肤 -> 仓库内置皮肤的顺序选资源。
// 资源属于可选展示能力，任何读取、解码或契约不完整都只让 Atlas 为空，不能把
// 状态快照接口拖成错误；前端拿到非空 manifest 后还会经过同一套严格解析器。
func resolvePetAtlas(snapshot PetMigrationSnapshot) *PetAtlasAsset {
	activeSkinID := ""
	if snapshot.SkinSelection != nil && snapshot.SkinSelection.ActiveSkinID != nil {
		activeSkinID = strings.TrimSpace(*snapshot.SkinSelection.ActiveSkinID)
	}
	if activeSkinID != "" {
		for _, skin := range snapshot.Skins {
			if strings.TrimSpace(skin.SkinID) != activeSkinID {
				continue
			}
			if asset, err := loadPetSkinAtlas(skin); err == nil {
				return asset
			}
			break
		}
		if asset, err := loadBuiltinPetAtlas(activeSkinID); err == nil {
			return asset
		}
	}

	for _, skin := range snapshot.Skins {
		if !skin.Builtin {
			continue
		}
		if asset, err := loadPetSkinAtlas(skin); err == nil {
			return asset
		}
	}

	for _, skinID := range builtinPetSkinIDs {
		if asset, err := loadBuiltinPetAtlas(skinID); err == nil {
			return asset
		}
	}
	return nil
}

func loadPetSkinAtlas(skin PetSkinRecord) (*PetAtlasAsset, error) {
	if strings.TrimSpace(skin.SkinID) == "" {
		return nil, errors.New("皮肤缺少 skinId")
	}
	if skin.Builtin {
		// Builtin 记录只能引用产品 allowlist 内的皮肤；否则数据库里的未知 ID
		// 可能借助同名目录绕过内置资源边界，退化成任意磁盘资源加载。
		if !isBuiltinPetSkinID(skin.SkinID) {
			return nil, errors.New("内置皮肤 ID 未知")
		}
		// 内置皮肤优先走固定仓库目录，避免数据库里旧的本地路径遮蔽随包资源。
		asset, err := loadBuiltinPetAtlas(skin.SkinID)
		if err == nil {
			return asset, nil
		}
		if currentPetAssetSource() != nil {
			// 打包态资源源是唯一事实来源，缺失或损坏时不能退回数据库里的旧路径。
			return nil, err
		}
	}

	root, err := resolvePetSkinRoot(skin)
	if err != nil {
		return nil, err
	}
	manifestBytes, err := petSkinManifestBytes(root, skin.ManifestJSON)
	if err != nil {
		return nil, err
	}
	manifest, err := parsePetAtlasManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	atlasPath := filepath.Join(root, manifest.Atlas.Image)
	if strings.TrimSpace(skin.AtlasPath) != "" {
		requested := skin.AtlasPath
		if !filepath.IsAbs(requested) {
			requested = filepath.Join(root, requested)
		}
		requested = filepath.Clean(requested)
		if canonicalPathForCompare(requested) != canonicalPathForCompare(atlasPath) {
			return nil, errors.New("皮肤 atlas 路径与 manifest 不一致")
		}
	}
	if err := validatePetAtlasPath(root, atlasPath); err != nil {
		return nil, err
	}
	return buildPetAtlasAsset(manifestBytes, atlasPath, manifest)
}

func loadBuiltinPetAtlas(skinID string) (*PetAtlasAsset, error) {
	if !isSafePetSkinID(skinID) || !isBuiltinPetSkinID(skinID) {
		return nil, errors.New("内置皮肤 ID 未知或不安全")
	}
	if source := currentPetAssetSource(); source != nil {
		// 注入源是打包态的内置资源事实来源；读取失败时保持 fail-closed，
		// 不能再回退到工作区磁盘，否则发布包会重新依赖开发机目录。
		return loadBuiltinPetAtlasFromSource(source, skinID)
	}
	for _, root := range builtinPetResourceRoots() {
		if asset, err := loadPetAtlasDirectory(filepath.Join(root, skinID)); err == nil {
			return asset, nil
		}
	}
	return nil, errors.New("内置皮肤资源不存在或损坏")
}

func loadPetAtlasDirectory(root string) (*PetAtlasAsset, error) {
	root, err := normalizePetAtlasRoot(root)
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(root, "pet.json")
	if err := validatePetAtlasPath(root, manifestPath); err != nil {
		return nil, err
	}
	manifestBytes, err := readPetAssetFile(manifestPath, petAtlasMaxManifestBytes)
	if err != nil {
		return nil, err
	}
	manifest, err := parsePetAtlasManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	atlasPath := filepath.Join(root, manifest.Atlas.Image)
	if err := validatePetAtlasPath(root, atlasPath); err != nil {
		return nil, err
	}
	return buildPetAtlasAsset(manifestBytes, atlasPath, manifest)
}

func resolvePetSkinRoot(skin PetSkinRecord) (string, error) {
	root := strings.TrimSpace(skin.Path)
	if root == "" && strings.TrimSpace(skin.AtlasPath) != "" && filepath.IsAbs(skin.AtlasPath) {
		root = filepath.Dir(skin.AtlasPath)
	}
	return normalizePetAtlasRoot(root)
}

func petSkinManifestBytes(root string, persisted json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(persisted, []byte{0xEF, 0xBB, 0xBF}))
	if len(trimmed) > 0 && string(trimmed) != "null" {
		// DAO 已做过 JSON 语法清洗；这里仍重新校验，避免损坏数据越过资源边界。
		if !json.Valid(trimmed) {
			return nil, errors.New("皮肤 manifest JSON 损坏")
		}
		return append([]byte(nil), trimmed...), nil
	}
	manifestPath := filepath.Join(root, "pet.json")
	if err := validatePetAtlasPath(root, manifestPath); err != nil {
		return nil, err
	}
	return readPetAssetFile(manifestPath, petAtlasMaxManifestBytes)
}

func parsePetAtlasManifest(raw []byte) (petAtlasManifestInfo, error) {
	raw = bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}))
	if len(raw) == 0 || !json.Valid(raw) {
		return petAtlasManifestInfo{}, errors.New("皮肤 manifest JSON 无效")
	}
	var manifest petAtlasManifestInfo
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return petAtlasManifestInfo{}, fmt.Errorf("解析皮肤 manifest: %w", err)
	}
	if manifest.AtlasVersion != PetAtlasVersion {
		return petAtlasManifestInfo{}, errors.New("皮肤 manifest atlasVersion 不受支持")
	}
	if manifest.Atlas.Image != "atlas.png" && manifest.Atlas.Image != "atlas.next.png" {
		return petAtlasManifestInfo{}, errors.New("皮肤 manifest atlas.image 不是受控文件名")
	}
	if manifest.Atlas.Width <= 0 || manifest.Atlas.Height <= 0 {
		return petAtlasManifestInfo{}, errors.New("皮肤 manifest atlas 尺寸无效")
	}
	return manifest, nil
}

func buildPetAtlasAsset(manifestBytes []byte, atlasPath string, manifest petAtlasManifestInfo) (*PetAtlasAsset, error) {
	atlasBytes, err := readPetAssetFile(atlasPath, petAtlasMaxImageBytes)
	if err != nil {
		return nil, err
	}
	return buildPetAtlasAssetFromBytes(manifestBytes, atlasBytes, manifest)
}

func buildPetAtlasAssetFromBytes(manifestBytes, atlasBytes []byte, manifest petAtlasManifestInfo) (*PetAtlasAsset, error) {
	config, err := png.DecodeConfig(bytes.NewReader(atlasBytes))
	if err != nil {
		return nil, fmt.Errorf("解码皮肤 atlas: %w", err)
	}
	if config.Width != manifest.Atlas.Width || config.Height != manifest.Atlas.Height {
		return nil, errors.New("皮肤 atlas 尺寸与 manifest 不一致")
	}
	return &PetAtlasAsset{
		Src:      petAtlasDataURLPrefix + base64.StdEncoding.EncodeToString(atlasBytes),
		Manifest: json.RawMessage(bytes.TrimSpace(bytes.TrimPrefix(manifestBytes, []byte{0xEF, 0xBB, 0xBF}))),
	}, nil
}

func normalizePetAtlasRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("皮肤资源根目录必须是绝对路径")
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("皮肤资源根目录不是普通目录")
	}
	return root, nil
}

func validatePetAtlasPath(root, candidate string) error {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if !pathWithin(root, candidate) {
		return errors.New("皮肤资源路径越过受控目录")
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	return rejectSymlinkEscape(rootReal, candidate)
}

func readPetAssetFile(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("皮肤资源不是普通文件")
	}
	if info.Size() <= 0 || info.Size() > maxBytes {
		return nil, errors.New("皮肤资源大小不在允许范围内")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("皮肤资源读取后超过大小限制")
	}
	return data, nil
}

func isSafePetSkinID(skinID string) bool {
	if skinID == "" {
		return false
	}
	for index, character := range skinID {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(index > 0 && character >= '0' && character <= '9') ||
			(index > 0 && (character == '_' || character == '-')) {
			continue
		}
		return false
	}
	return true
}

func builtinPetResourceRoots() []string {
	starts := make([]string, 0, 3)
	if _, file, _, ok := runtime.Caller(0); ok {
		starts = append(starts, filepath.Dir(file))
	}
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(executable))
	}

	roots := make([]string, 0, len(starts)*8)
	seen := make(map[string]struct{})
	for _, start := range starts {
		start, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for depth := 0; depth < 8; depth++ {
			root := filepath.Join(start, "resources", "pets")
			key := strings.ToLower(filepath.Clean(root))
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				roots = append(roots, root)
			}
			parent := filepath.Dir(start)
			if parent == start {
				break
			}
			start = parent
		}
	}
	return roots
}
