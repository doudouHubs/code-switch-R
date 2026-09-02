package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"
)

const (
	petExpLogLimit        = 100
	petLogDefaultPage     = 1
	petLogDefaultPageSize = 20
)

// PetRepository 是 PetService 与持久化层之间的最小边界。
//
// 这里故意只暴露宠物快照和经验日志操作，不把 SQL、表结构或数据库连接
// 泄漏到业务层。未来 DAO 只要实现这组方法即可接入，不需要让服务知道存储细节。
type PetRepository interface {
	LoadSnapshot(context.Context, string) (PetMigrationSnapshot, error)
	SaveSnapshot(context.Context, PetMigrationSnapshot) error
	AppendExpLog(context.Context, PetExpLogEntry) error
	ListExpLog(context.Context, string, int, int) (PetExpLogPage, error)
}

// PetSettingsSnapshotRepository 是设置页可选的轻量读取能力。
// 它不塞进 PetRepository 主契约，避免已有内存仓库和第三方实现被迫同步新增方法；
// 不支持该能力的仓库会由服务层回退到完整快照，兼容边界仍只有这一处。
type PetSettingsSnapshotRepository interface {
	LoadSettingsSnapshot(context.Context, string) (PetMigrationSnapshot, error)
}

// PetStore 是兼容旧命名的接口别名，避免调用方因为 repository/store 命名差异
// 被迫引入第二套契约。
type PetStore interface {
	PetRepository
}

// PetService 持有宠物业务协调状态，不持有任何数据库对象。
// 同一个服务实例内的所有读改写操作共用一把锁，保证动作和经验入账不会基于
// 过期快照互相覆盖；跨进程并发仍需要 DAO 在实现层提供事务或串行化保证。
type PetService struct {
	repository PetRepository
	petID      string
	mu         sync.Mutex
}

// NewPetService 创建默认宠物的业务服务。
func NewPetService(repository PetRepository) *PetService {
	return NewPetServiceForPet(repository, DefaultPetID)
}

// NewPetServiceForPet 为需要多宠物隔离的调用方提供显式宠物 ID 构造入口。
func NewPetServiceForPet(repository PetRepository, petID string) *PetService {
	petID = strings.TrimSpace(petID)
	if petID == "" {
		petID = DefaultPetID
	}
	return &PetService{
		repository: repository,
		petID:      petID,
	}
}

// Load 读取并补齐完整宠物快照。默认值只在服务内补齐，不在一次只读请求中
// 隐式写库，避免初始化失败被误报成读取失败；后续成功动作会把完整快照落盘。
func (s *PetService) Load() (PetMigrationSnapshot, error) {
	if err := s.validate(); err != nil {
		return PetMigrationSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loadSnapshotLocked(context.Background(), petNow(nil))
}

// LoadSettingsSnapshot 只读取设置页需要的状态、配置和皮肤元数据，不读取历史记录。
// 轻量能力由 DAO 自己决定查询边界；旧仓库仍走完整快照，避免破坏现有实现。
func (s *PetService) LoadSettingsSnapshot() (PetMigrationSnapshot, error) {
	if err := s.validate(); err != nil {
		return PetMigrationSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	var (
		snapshot PetMigrationSnapshot
		err      error
	)
	if reader, ok := s.repository.(PetSettingsSnapshotRepository); ok {
		snapshot, err = reader.LoadSettingsSnapshot(ctx, s.petID)
	} else {
		// 兼容旧 repository；这条路径只在宿主未实现轻量读取时触发。
		snapshot, err = s.repository.LoadSnapshot(ctx, s.petID)
	}
	if err != nil {
		return PetMigrationSnapshot{}, fmt.Errorf("读取宠物设置快照 %q 失败: %w", s.petID, err)
	}
	return normalizePetSnapshot(snapshot, s.petID, petNow(nil)), nil
}

// GetState 返回当前已经应用默认值和异常值归一化的宠物状态。
func (s *PetService) GetState() (PetState, error) {
	snapshot, err := s.Load()
	if err != nil {
		return PetState{}, err
	}
	if snapshot.State == nil {
		return PetState{}, errors.New("宠物快照缺少 state")
	}
	return *snapshot.State, nil
}

// GetExperience 返回当前经验总账，供 Wails 设置页或等级展示直接读取。
func (s *PetService) GetExperience() (PetExperience, error) {
	snapshot, err := s.Load()
	if err != nil {
		return PetExperience{}, err
	}
	if snapshot.Experience == nil {
		return PetExperience{}, errors.New("宠物快照缺少 experience")
	}
	return *snapshot.Experience, nil
}

// Tick 推进宠物时间状态。now 可选，主要用于测试和恢复离线时间；未传或异常
// 时间会回退到当前毫秒时间，具体衰减和睡眠边界仍由规则层统一计算。
func (s *PetService) Tick(now ...int64) (PetState, error) {
	if err := s.validate(); err != nil {
		return PetState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	nowMs := petNow(now)
	snapshot, err := s.loadSnapshotLocked(ctx, nowMs)
	if err != nil {
		return PetState{}, err
	}
	next := TickPet(*snapshot.State, nowMs)
	if reflect.DeepEqual(next, *snapshot.State) {
		return next, nil
	}
	snapshot.State = &next
	if err := s.saveSnapshotLocked(ctx, snapshot); err != nil {
		return PetState{}, err
	}
	return next, nil
}

func (s *PetService) Feed() (PetActionResult, error) {
	return s.runAction(func(state PetState, _ PetMigrationSnapshot, _ int64) (PetState, PetActionResult) {
		return PetFeed(state)
	})
}

func (s *PetService) Bathe() (PetActionResult, error) {
	return s.runAction(func(state PetState, _ PetMigrationSnapshot, _ int64) (PetState, PetActionResult) {
		return PetBathe(state)
	})
}

func (s *PetService) Soak() (PetActionResult, error) {
	return s.runAction(func(state PetState, snapshot PetMigrationSnapshot, _ int64) (PetState, PetActionResult) {
		return PetSoak(state, snapshot.Experience.TotalExp)
	})
}

func (s *PetService) Play() (PetActionResult, error) {
	return s.runAction(func(state PetState, _ PetMigrationSnapshot, _ int64) (PetState, PetActionResult) {
		return PetPlay(state)
	})
}

// ToggleSleep 返回切换后的状态。规则层对 away task 的切换请求是无操作，服务层
// 仍会把 Tick 产生的时间结算保存下来，但不会凭空改变 away task。
func (s *PetService) ToggleSleep(now ...int64) (PetState, error) {
	if err := s.validate(); err != nil {
		return PetState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	nowMs := petNow(now)
	snapshot, err := s.loadSnapshotLocked(ctx, nowMs)
	if err != nil {
		return PetState{}, err
	}
	next := PetToggleSleep(*snapshot.State, nowMs)
	if reflect.DeepEqual(next, *snapshot.State) {
		return next, nil
	}
	snapshot.State = &next
	if err := s.saveSnapshotLocked(ctx, snapshot); err != nil {
		return PetState{}, err
	}
	return next, nil
}

func (s *PetService) StartWork(now ...int64) (PetActionResult, error) {
	return s.runActionAt(petNow(now), func(state PetState, snapshot PetMigrationSnapshot, at int64) (PetState, PetActionResult) {
		return PetStartWork(state, snapshot.Experience.TotalExp, at)
	})
}

func (s *PetService) StartStudy(now ...int64) (PetActionResult, error) {
	return s.runActionAt(petNow(now), func(state PetState, snapshot PetMigrationSnapshot, at int64) (PetState, PetActionResult) {
		return PetStartStudy(state, snapshot.Experience.TotalExp, at)
	})
}

// ResolveAwayTask 只在任务已完成时发放一次奖励。第一次成功后 state.AwayTask
// 被规则层清空，重复调用自然返回 nil，避免重复加钱或重复增加成长值。
func (s *PetService) ResolveAwayTask(now ...int64) (*PetAwayReward, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	nowMs := petNow(now)
	snapshot, err := s.loadSnapshotLocked(ctx, nowMs)
	if err != nil {
		return nil, err
	}
	next, reward := PetResolveAwayTask(*snapshot.State, nowMs)
	if reward == nil {
		return nil, nil
	}
	snapshot.State = &next
	if err := s.saveSnapshotLocked(ctx, snapshot); err != nil {
		return nil, err
	}
	return reward, nil
}

// Petted 是 Wails 暴露的固定签名入口；petId 必须显式传入，避免 runtime 把
// 前端动作误写到 default 分区。规则动作本身仍由内部 petted helper 执行。
func (s *PetService) Petted(petID string) error {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return err
	}
	return service.petted()
}

// PettedForPet 是桌宠窗口使用的轻量动作入口。旧的 Petted 保留 error-only
// 签名给兼容调用方；新入口额外返回状态，避免每次抚摸都回读完整快照。
func (s *PetService) PettedForPet(petID string) (PetActionResult, error) {
	service, err := s.apiServiceForPet(petID)
	if err != nil {
		return PetActionResult{}, err
	}
	if err := service.petted(); err != nil {
		return PetActionResult{}, err
	}
	state, err := service.GetState()
	if err != nil {
		return PetActionResult{}, err
	}
	return PetActionResult{OK: true, State: &state}, nil
}

func (s *PetService) petted() error {
	if err := s.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	snapshot, err := s.loadSnapshotLocked(ctx, petNow(nil))
	if err != nil {
		return err
	}
	next := PetPetted(*snapshot.State)
	if reflect.DeepEqual(next, *snapshot.State) {
		return nil
	}
	snapshot.State = &next
	return s.saveSnapshotLocked(ctx, snapshot)
}

// AddExperience 把一次模型用量作为一个不可拆分的业务操作入账：总经验、总
// token、日志、首次经验换币和宠物完整快照必须基于同一份最新快照计算。
func (s *PetService) AddExperience(entry PetExpLogEntry) (PetExperience, error) {
	if err := s.validate(); err != nil {
		return PetExperience{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	nowMs := petNow(nil)
	snapshot, err := s.loadSnapshotLocked(ctx, nowMs)
	if err != nil {
		return PetExperience{}, err
	}
	entry, err = normalizeExpEntry(entry, s.petID, nowMs)
	if err != nil {
		return PetExperience{}, err
	}

	// 调用方重试同一个用量事件时，日志 ID 是幂等键。检查快照内日志即可
	// 覆盖同一服务实例的重试；跨实例场景仍应由 DAO 对日志 ID 建唯一约束。
	if entry.ID != "" && hasExpLogID(snapshot.ExpLog, entry.ID) {
		return *snapshot.Experience, nil
	}

	experience := *snapshot.Experience
	newTotalExp := roundPetExp(experience.TotalExp + entry.Exp)
	if math.IsNaN(newTotalExp) || math.IsInf(newTotalExp, 0) {
		return PetExperience{}, errors.New("经验总账溢出")
	}
	if entry.Tokens > 0 && experience.TotalTokens > maxInt64-entry.Tokens {
		return PetExperience{}, errors.New("token 总账溢出")
	}
	experience.TotalExp = newTotalExp
	if entry.Tokens > 0 {
		experience.TotalTokens += entry.Tokens
	}
	experience.PetID = s.petID

	// PetCreditExpCoins 内部复用规则层的 cap=200 逻辑，服务层只负责把
	// 经验总账和宠物金币放进同一份 snapshot 后再提交。
	nextState, _ := PetCreditExpCoins(*snapshot.State, experience.TotalExp)
	snapshot.State = &nextState
	snapshot.Experience = &experience
	snapshot.ExpLog = prependExpLog(snapshot.ExpLog, entry)

	// 接口没有暴露数据库事务，因此先追加日志失败时不提交快照；同实例
	// 的互斥锁避免了 SaveSnapshot 基于旧总账覆盖新总账的竞态。
	if err := s.repository.AppendExpLog(ctx, entry); err != nil {
		return PetExperience{}, fmt.Errorf("追加宠物经验日志失败: %w", err)
	}
	if err := s.saveSnapshotLocked(ctx, snapshot); err != nil {
		return PetExperience{}, err
	}
	return experience, nil
}

// ClaimDailyBonus 返回本次实际领取的金币；同一天重复调用返回 0 且不写库。
func (s *PetService) ClaimDailyBonus(now ...int64) (int64, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	nowMs := petNow(now)
	snapshot, err := s.loadSnapshotLocked(ctx, nowMs)
	if err != nil {
		return 0, err
	}
	next, bonus := PetClaimDailyBonus(*snapshot.State, nowMs)
	if bonus == 0 {
		return 0, nil
	}
	snapshot.State = &next
	if err := s.saveSnapshotLocked(ctx, snapshot); err != nil {
		return 0, err
	}
	return bonus, nil
}

func (s *PetService) GetCareConfig() (PetCareConfig, error) {
	snapshot, err := s.Load()
	if err != nil {
		return PetCareConfig{}, err
	}
	return *snapshot.Care, nil
}

func (s *PetService) LoadCareConfig() (PetCareConfig, error) {
	return s.GetCareConfig()
}

func (s *PetService) SaveCareConfig(config PetCareConfig) error {
	return s.saveConfig(func(snapshot *PetMigrationSnapshot) {
		normalized := normalizeCareConfig(config, s.petID)
		snapshot.Care = &normalized
	})
}

func (s *PetService) GetAgentConfig() (PetAgentConfig, error) {
	snapshot, err := s.Load()
	if err != nil {
		return PetAgentConfig{}, err
	}
	return *snapshot.Agent, nil
}

func (s *PetService) LoadAgentConfig() (PetAgentConfig, error) {
	return s.GetAgentConfig()
}

func (s *PetService) SaveAgentConfig(config PetAgentConfig) error {
	return s.saveConfig(func(snapshot *PetMigrationSnapshot) {
		normalized := normalizeAgentConfig(config, s.petID)
		snapshot.Agent = &normalized
	})
}

func (s *PetService) GetDreamConfig() (PetDreamConfig, error) {
	snapshot, err := s.Load()
	if err != nil {
		return PetDreamConfig{}, err
	}
	return *snapshot.DreamConfig, nil
}

func (s *PetService) LoadDreamConfig() (PetDreamConfig, error) {
	return s.GetDreamConfig()
}

func (s *PetService) SaveDreamConfig(config PetDreamConfig) error {
	return s.saveConfig(func(snapshot *PetMigrationSnapshot) {
		normalized := normalizeDreamConfig(config, s.petID)
		snapshot.DreamConfig = &normalized
	})
}

func (s *PetService) GetWindowConfig() (PetWindowConfig, error) {
	snapshot, err := s.Load()
	if err != nil {
		return PetWindowConfig{}, err
	}
	return *snapshot.Window, nil
}

func (s *PetService) LoadWindowConfig() (PetWindowConfig, error) {
	return s.GetWindowConfig()
}

func (s *PetService) SaveWindowConfig(config PetWindowConfig) error {
	return s.saveConfig(func(snapshot *PetMigrationSnapshot) {
		config.PetID = s.petID
		snapshot.Window = &config
	})
}

// ListExpLog 是仓库分页能力的 Wails 友好入口。分页参数在服务层做最小兜底，
// 具体上限和索引策略仍由 DAO 负责，避免服务层复制数据库细节。
func (s *PetService) ListExpLog(page, pageSize int) (PetExpLogPage, error) {
	if err := s.validate(); err != nil {
		return PetExpLogPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if page < 1 {
		page = petLogDefaultPage
	}
	if pageSize < 1 {
		pageSize = petLogDefaultPageSize
	}
	result, err := s.repository.ListExpLog(context.Background(), s.petID, page, pageSize)
	if err != nil {
		return PetExpLogPage{}, fmt.Errorf("读取宠物经验日志失败: %w", err)
	}
	return result, nil
}

func (s *PetService) ListExperienceLog(page, pageSize int) (PetExpLogPage, error) {
	return s.ListExpLog(page, pageSize)
}

func (s *PetService) runAction(action func(PetState, PetMigrationSnapshot, int64) (PetState, PetActionResult)) (PetActionResult, error) {
	return s.runActionAt(petNow(nil), action)
}

func (s *PetService) runActionAt(now int64, action func(PetState, PetMigrationSnapshot, int64) (PetState, PetActionResult)) (PetActionResult, error) {
	if err := s.validate(); err != nil {
		return PetActionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	snapshot, err := s.loadSnapshotLocked(ctx, now)
	if err != nil {
		return PetActionResult{}, err
	}
	next, result := action(*snapshot.State, snapshot, now)
	if !result.OK {
		return result, nil
	}
	snapshot.State = &next
	if err := s.saveSnapshotLocked(ctx, snapshot); err != nil {
		return PetActionResult{}, err
	}
	return result, nil
}

func (s *PetService) saveConfig(update func(*PetMigrationSnapshot)) error {
	if err := s.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	snapshot, err := s.loadSnapshotLocked(ctx, petNow(nil))
	if err != nil {
		return err
	}
	update(&snapshot)
	return s.saveSnapshotLocked(ctx, snapshot)
}

func (s *PetService) validate() error {
	if s == nil {
		return errors.New("宠物服务为空")
	}
	if s.repository == nil {
		return errors.New("宠物仓库未配置")
	}
	if strings.TrimSpace(s.petID) == "" {
		return errors.New("宠物 ID 为空")
	}
	return nil
}

func (s *PetService) loadSnapshotLocked(ctx context.Context, now int64) (PetMigrationSnapshot, error) {
	snapshot, err := s.repository.LoadSnapshot(ctx, s.petID)
	if err != nil {
		return PetMigrationSnapshot{}, fmt.Errorf("读取宠物快照 %q 失败: %w", s.petID, err)
	}
	return normalizePetSnapshot(snapshot, s.petID, now), nil
}

func (s *PetService) saveSnapshotLocked(ctx context.Context, snapshot PetMigrationSnapshot) error {
	snapshot.PetID = s.petID
	if err := s.repository.SaveSnapshot(ctx, snapshot); err != nil {
		return fmt.Errorf("保存宠物完整快照 %q 失败: %w", s.petID, err)
	}
	return nil
}

func normalizePetSnapshot(snapshot PetMigrationSnapshot, petID string, now int64) PetMigrationSnapshot {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	snapshot.PetID = petID

	state := DefaultPetStateAt(now)
	if snapshot.State != nil {
		state = *snapshot.State
	}
	state.PetID = petID
	state = NormalizePetState(state, now)
	// 时钟回拨或损坏数据可能让 LastTickAt 落在未来；不校正会让后续所有 Tick
	// 永远得到负 elapsed，因此这里把它锚回当前读取时刻。
	if state.LastTickAt > now {
		state.LastTickAt = now
	}
	if !state.Sleeping {
		state.SleepEndsAt = 0
	}
	snapshot.State = &state

	experience := PetExperience{PetID: petID}
	if snapshot.Experience != nil {
		experience = *snapshot.Experience
	}
	experience.PetID = petID
	if math.IsNaN(experience.TotalExp) || math.IsInf(experience.TotalExp, 0) || experience.TotalExp < 0 {
		experience.TotalExp = 0
	}
	experience.TotalExp = roundPetExp(experience.TotalExp)
	if experience.TotalTokens < 0 {
		experience.TotalTokens = 0
	}
	snapshot.Experience = &experience

	logs := make([]PetExpLogEntry, len(snapshot.ExpLog))
	copy(logs, snapshot.ExpLog)
	for i := range logs {
		if logs[i].PetID == "" {
			logs[i].PetID = petID
		}
	}
	snapshot.ExpLog = logs

	care := defaultPetCareConfig(petID)
	if snapshot.Care != nil {
		care = normalizeCareConfig(*snapshot.Care, petID)
	}
	snapshot.Care = &care

	agent := defaultPetAgentConfig(petID)
	if snapshot.Agent != nil {
		agent = normalizeAgentConfig(*snapshot.Agent, petID)
	}
	snapshot.Agent = &agent

	dream := defaultPetDreamConfig(petID)
	if snapshot.DreamConfig != nil {
		dream = normalizeDreamConfig(*snapshot.DreamConfig, petID)
	}
	snapshot.DreamConfig = &dream

	// Vue 设置页和前端 fallback 都把新宠物窗口视为启用；仅当持久化快照
	// 已存在 window 配置时才保留用户明确保存的 false，避免首次启动出现
	// 前后端默认值不一致导致窗口状态被意外关闭。
	window := PetWindowConfig{PetID: petID, Enabled: true}
	if snapshot.Window != nil {
		window = *snapshot.Window
		window.PetID = petID
	}
	snapshot.Window = &window

	return snapshot
}

func defaultPetCareConfig(petID string) PetCareConfig {
	return PetCareConfig{
		PetID:             petID,
		AutoCareEnabled:   false,
		AutoCareThreshold: PetAutoCareDefaultThreshold,
	}
}

func normalizeCareConfig(config PetCareConfig, petID string) PetCareConfig {
	if config.AutoCareThreshold <= 0 {
		config.AutoCareThreshold = PetAutoCareDefaultThreshold
	} else {
		config.AutoCareThreshold = NormalizePetCareThreshold(float64(config.AutoCareThreshold))
	}
	config.PetID = petID
	return config
}

func defaultPetAgentConfig(petID string) PetAgentConfig {
	return PetAgentConfig{
		PetID:         petID,
		ProactiveFreq: PetProactiveLow,
		QuietStart:    22,
		QuietEnd:      9,
		VoiceMode:     PetVoiceAuto,
	}
}

func normalizeAgentConfig(config PetAgentConfig, petID string) PetAgentConfig {
	if config.ProviderPlatform != nil {
		platform := strings.ToLower(strings.TrimSpace(*config.ProviderPlatform))
		if platform == "" {
			config.ProviderPlatform = nil
		} else {
			config.ProviderPlatform = &platform
		}
	}
	if config.ProactiveFreq != PetProactiveLow && config.ProactiveFreq != PetProactiveMedium && config.ProactiveFreq != PetProactiveHigh {
		config.ProactiveFreq = PetProactiveLow
	}
	if config.VoiceMode != PetVoiceAuto && config.VoiceMode != PetVoiceSpeech && config.VoiceMode != PetVoiceChat {
		config.VoiceMode = PetVoiceAuto
	}
	config.PetID = petID
	return config
}

func defaultPetDreamConfig(petID string) PetDreamConfig {
	return PetDreamConfig{
		PetID:                    petID,
		DreamEnabled:             true,
		SleepTalkMinLength:       PetDreamDefaultSleepTalkLength,
		BubbleMinDurationSeconds: PetDreamDefaultBubbleDurationSeconds,
	}
}

func normalizeDreamConfig(config PetDreamConfig, petID string) PetDreamConfig {
	if config.SleepTalkMinLength <= 0 {
		config.SleepTalkMinLength = PetDreamDefaultSleepTalkLength
	} else {
		config.SleepTalkMinLength = NormalizePetDreamLength(
			float64(config.SleepTalkMinLength),
			PetDreamDefaultSleepTalkLength,
			PetDreamMinSleepTalkLength,
			PetDreamMaxSleepTalkLength,
		)
	}
	if config.BubbleMinDurationSeconds <= 0 {
		config.BubbleMinDurationSeconds = PetDreamDefaultBubbleDurationSeconds
	} else {
		config.BubbleMinDurationSeconds = NormalizePetDreamLength(
			float64(config.BubbleMinDurationSeconds),
			PetDreamDefaultBubbleDurationSeconds,
			PetDreamMinBubbleDurationSeconds,
			PetDreamMaxBubbleDurationSeconds,
		)
	}
	config.ImageProviderPlatform = normalizePetReferenceString(config.ImageProviderPlatform)
	config.ImageProviderID = normalizePetReferenceString(config.ImageProviderID)
	config.ImageModelID = normalizePetReferenceString(config.ImageModelID)
	// 图片引用必须作为一个整体存在；缺一项时清空，避免运行时拿半截配置误判为可用。
	if config.ImageProviderPlatform == nil || config.ImageProviderID == nil || config.ImageModelID == nil {
		config.ImageProviderPlatform = nil
		config.ImageProviderID = nil
		config.ImageModelID = nil
	}
	config.PetID = petID
	return config
}

func normalizePetReferenceString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeExpEntry(entry PetExpLogEntry, petID string, now int64) (PetExpLogEntry, error) {
	if math.IsNaN(entry.Exp) || math.IsInf(entry.Exp, 0) || entry.Exp <= 0 {
		return PetExpLogEntry{}, errors.New("经验值必须是大于 0 的有限数字")
	}
	entry.Exp = roundPetExp(entry.Exp)
	if entry.Exp <= 0 {
		return PetExpLogEntry{}, errors.New("经验值精度归一化后为 0")
	}
	if entry.Tokens < 0 {
		entry.Tokens = 0
	}
	if entry.At <= 0 {
		entry.At = now
	}
	entry.PetID = petID
	if strings.TrimSpace(entry.ID) == "" {
		entry.ID = fmt.Sprintf("pet-exp-%d", time.Now().UnixNano())
	}
	return entry, nil
}

func prependExpLog(log []PetExpLogEntry, entry PetExpLogEntry) []PetExpLogEntry {
	result := make([]PetExpLogEntry, 0, minInt(len(log)+1, petExpLogLimit))
	result = append(result, entry)
	result = append(result, log...)
	if len(result) > petExpLogLimit {
		result = result[:petExpLogLimit]
	}
	return result
}

func hasExpLogID(log []PetExpLogEntry, id string) bool {
	for _, item := range log {
		if item.ID == id {
			return true
		}
	}
	return false
}

func roundPetExp(value float64) float64 {
	return math.Round(value*100) / 100
}

func petNow(now []int64) int64 {
	if len(now) > 0 && now[0] > 0 {
		return now[0]
	}
	return time.Now().UnixMilli()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

const maxInt64 = int64(^uint64(0) >> 1)
