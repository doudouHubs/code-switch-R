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
	"unicode/utf8"
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

// PetRuntimeSnapshot 是桌宠窗口的低频 hydration 契约。它只携带运行时需要的
// 状态、经验和配置，不包含历史记录、皮肤目录或 atlas 二进制；这些内容由各自
// 的页面/API 在真正需要时读取，避免 renderer 的心跳重复构造大快照。
type PetRuntimeSnapshot struct {
	State         PetState         `json:"state"`
	Experience    PetExperience    `json:"experience"`
	Window        PetWindowConfig  `json:"window"`
	Care          PetCareConfig    `json:"care"`
	Agent         PetAgentConfig   `json:"agent"`
	Dream         PetDreamConfig   `json:"dream"`
	SkinSelection PetSkinSelection `json:"skinSelection"`
}

// PetSettingsSnapshot 是设置页首屏的轻量契约。它保留皮肤列表元数据供绑定选择，
// 但不携带计划、梦境历史、记忆和 atlas 图片；这些内容由各自的页面或资源入口按需读取。
type PetSettingsSnapshot struct {
	State         PetState         `json:"state"`
	Experience    PetExperience    `json:"experience"`
	Window        PetWindowConfig  `json:"window"`
	Care          PetCareConfig    `json:"care"`
	Agent         PetAgentConfig   `json:"agent"`
	Dream         PetDreamConfig   `json:"dream"`
	SkinSelection PetSkinSelection `json:"skinSelection"`
	Skins         []PetSkinRecord  `json:"skins"`
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

const petNameMaxLength = 20

// UpdateName 是宠物名称的唯一写入口。名称属于 PetState，而不是设置配置；
// 单独读取并保存最新快照，避免设置页只提交配置时把桌宠窗口刚产生的状态覆盖掉。
func (s *PetService) UpdateName(petID, name string) (PetSnapshot, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetSnapshot{}, err
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return PetSnapshot{}, errors.New("宠物名称不能为空")
	}
	if utf8.RuneCountInString(name) > petNameMaxLength {
		return PetSnapshot{}, fmt.Errorf("宠物名称不能超过 %d 个字符", petNameMaxLength)
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	ctx := context.Background()
	snapshot, err := service.loadSnapshotLocked(ctx, petNow(nil))
	if err != nil {
		return PetSnapshot{}, err
	}
	// 名称修改只改变 state.name，其他状态和设置必须完整保留，避免并发窗口操作被回滚。
	snapshot.State.Name = name
	if err := service.saveSnapshotLocked(ctx, snapshot); err != nil {
		return PetSnapshot{}, err
	}
	return newPetSnapshot(snapshot)
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

// GetRuntimeSnapshot 只返回桌宠窗口需要的运行时字段。完整 GetSnapshot 仍保留给
// 设置页和历史页，不能让资源拆分破坏已有的页面契约。
func (s *PetService) GetRuntimeSnapshot(petID string) (PetRuntimeSnapshot, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetRuntimeSnapshot{}, err
	}

	snapshot, err := service.Load()
	if err != nil {
		return PetRuntimeSnapshot{}, err
	}
	return newPetRuntimeSnapshot(snapshot)
}

// GetSettingsSnapshot 只返回设置页首屏需要的配置和皮肤元数据，避免打开设置时搬运
// 梦境历史、经验日志、记忆以及 MB 级 atlas。完整 GetSnapshot 继续保留给兼容调用方。
func (s *PetService) GetSettingsSnapshot(petID string) (PetSettingsSnapshot, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetSettingsSnapshot{}, err
	}

	snapshot, err := service.LoadSettingsSnapshot()
	if err != nil {
		return PetSettingsSnapshot{}, err
	}
	return newPetSettingsSnapshot(snapshot)
}

// GetAtlas 独立读取当前皮肤的展示资源。atlas 只在首次 hydration 或皮肤变化时
// 通过这个入口传输，运行时状态 tick 不再携带约 MB 级 data URL。
func (s *PetService) GetAtlas(petID string) (*PetAtlasAsset, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return nil, err
	}

	snapshot, err := service.Load()
	if err != nil {
		return nil, err
	}
	return resolvePetAtlas(snapshot), nil
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
	var result PetActionResult
	var actionErr error
	switch action {
	case PetActionFeed:
		result, actionErr = service.Feed()
	case PetActionBathe:
		result, actionErr = service.Bathe()
	case PetActionSoak:
		result, actionErr = service.Soak()
	case PetActionPlay:
		result, actionErr = service.Play()
	case PetActionSleep:
		_, actionErr = service.ToggleSleep()
		result = PetActionResult{OK: actionErr == nil}
	case PetActionWork:
		result, actionErr = service.StartWork()
	case PetActionStudy:
		result, actionErr = service.StartStudy()
	case PetAction("petted"):
		// 兼容不经过单独 Petted 入口的调用方；前端正式入口仍是 Petted。
		actionErr = service.petted()
		result = PetActionResult{OK: actionErr == nil}
	default:
		return PetActionResult{}, fmt.Errorf("不支持的宠物动作 %q", action)
	}
	if actionErr != nil {
		return PetActionResult{}, actionErr
	}
	// 业务拒绝也返回当前状态，让 renderer 能在不回读完整快照的情况下完成
	// 本地状态收敛；只有读取/持久化错误才走 error 分支。
	state, err := service.GetState()
	if err != nil {
		return PetActionResult{}, err
	}
	result.State = &state
	return result, nil
}

// EndWorkEarlyForPet 是桌宠 away 胶囊的唯一提前结束入口。只有未完成的 work
// 任务允许提前结束，study 或已经到期的任务必须交给正常结算流程处理，避免奖励规则被
// 前端的双击行为绕过。
func (s *PetService) EndWorkEarlyForPet(petID string, now int64) (PetActionResult, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetActionResult{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()

	if now <= 0 {
		now = petNow(nil)
	}
	snapshot, err := service.loadSnapshotLocked(context.Background(), now)
	if err != nil {
		return PetActionResult{}, err
	}
	next, ended := PetEndWorkEarly(*snapshot.State, now)
	if !ended {
		state := *snapshot.State
		return PetActionResult{OK: false, Reason: PetActionFailureBusy, State: &state}, nil
	}
	snapshot.State = &next
	if err := service.saveSnapshotLocked(context.Background(), snapshot); err != nil {
		return PetActionResult{}, err
	}
	return PetActionResult{OK: true, State: &next}, nil
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

// RecordProactiveState 是桌宠主动搭话使用的轻量入口。完整 RecordProactive
// 保留给兼容调用方，窗口运行时不应因为记一次配额又搬运 atlas 和历史记录。
func (s *PetService) RecordProactiveState(petID string, now int64) (PetState, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetState{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()

	if now <= 0 {
		now = petNow(nil)
	}
	snapshot, err := service.loadSnapshotLocked(context.Background(), now)
	if err != nil {
		return PetState{}, err
	}
	next := PetRecordProactive(*snapshot.State, now)
	snapshot.State = &next
	if err := service.saveSnapshotLocked(context.Background(), snapshot); err != nil {
		return PetState{}, err
	}
	return next, nil
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
	runtimeSnapshot, err := newPetRuntimeSnapshot(snapshot)
	if err != nil {
		return PetSnapshot{}, err
	}

	skins := sanitizePetSkinRecords(snapshot.PetID, snapshot.Skins)

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
		State:         runtimeSnapshot.State,
		Experience:    runtimeSnapshot.Experience,
		Window:        runtimeSnapshot.Window,
		Care:          runtimeSnapshot.Care,
		Agent:         runtimeSnapshot.Agent,
		Dream:         runtimeSnapshot.Dream,
		Plans:         plans,
		Dreams:        dreams,
		Memories:      memories,
		SkinSelection: runtimeSnapshot.SkinSelection,
		Skins:         skins,
		Atlas:         atlas,
	}, nil
}

func newPetSettingsSnapshot(snapshot PetMigrationSnapshot) (PetSettingsSnapshot, error) {
	runtimeSnapshot, err := newPetRuntimeSnapshot(snapshot)
	if err != nil {
		return PetSettingsSnapshot{}, err
	}
	return PetSettingsSnapshot{
		State:         runtimeSnapshot.State,
		Experience:    runtimeSnapshot.Experience,
		Window:        runtimeSnapshot.Window,
		Care:          runtimeSnapshot.Care,
		Agent:         runtimeSnapshot.Agent,
		Dream:         runtimeSnapshot.Dream,
		SkinSelection: runtimeSnapshot.SkinSelection,
		Skins:         sanitizePetSkinRecords(snapshot.PetID, snapshot.Skins),
	}, nil
}

func sanitizePetSkinRecords(petID string, persisted []PetSkinRecord) []PetSkinRecord {
	// 内置资源来自 embed.FS，不经过数据库迁移，所以必须在 API 边界合并到列表；
	// 同时清掉本地路径，避免资源引用和文件系统信息穿过 Wails 边界。
	mergedSkins := mergeBuiltinPetSkins(petID, persisted)
	skins := make([]PetSkinRecord, len(mergedSkins))
	for index, skin := range mergedSkins {
		skin.Path = ""
		skin.AtlasPath = ""
		skins[index] = skin
	}
	return skins
}

func newPetRuntimeSnapshot(snapshot PetMigrationSnapshot) (PetRuntimeSnapshot, error) {
	if snapshot.State == nil {
		return PetRuntimeSnapshot{}, errors.New("宠物快照缺少 state")
	}
	if snapshot.Experience == nil {
		return PetRuntimeSnapshot{}, errors.New("宠物快照缺少 experience")
	}
	if snapshot.Window == nil {
		return PetRuntimeSnapshot{}, errors.New("宠物快照缺少 window")
	}
	if snapshot.Care == nil {
		return PetRuntimeSnapshot{}, errors.New("宠物快照缺少 care")
	}
	if snapshot.Agent == nil {
		return PetRuntimeSnapshot{}, errors.New("宠物快照缺少 agent")
	}
	if snapshot.DreamConfig == nil {
		return PetRuntimeSnapshot{}, errors.New("宠物快照缺少 dream")
	}

	agent := *snapshot.Agent
	// ProjectFolder 是后端项目引用的本地路径，前端只需要 projectId/projectName；
	// 即使旧数据库里存在该值，也不能随着运行时配置穿过 Wails 边界。
	agent.ProjectFolder = nil

	selection := PetSkinSelection{PetID: snapshot.PetID}
	if snapshot.SkinSelection != nil {
		selection = *snapshot.SkinSelection
		selection.PetID = snapshot.PetID
	}

	return PetRuntimeSnapshot{
		State:         *snapshot.State,
		Experience:    *snapshot.Experience,
		Window:        *snapshot.Window,
		Care:          *snapshot.Care,
		Agent:         agent,
		Dream:         *snapshot.DreamConfig,
		SkinSelection: selection,
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
