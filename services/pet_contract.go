package services

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

const (
	DefaultPetID = "default"

	// PetSchemaVersion 只描述共享层当前可读写的 schema 版本；业务规则版本由 PetService 单独管理。
	PetSchemaVersion = 1

	OpenCoworkPetStateKey       = "opencowork-pet"
	OpenCoworkPetExperienceKey  = "opencowork-pet-exp"
	OpenCoworkPetCareKey        = "opencowork-pet-care"
	OpenCoworkPetAgentKey       = "opencowork-pet-agent"
	OpenCoworkPetDreamConfigKey = "opencowork-pet-dream"
	OpenCoworkPetSkinsKey       = "opencowork-pet-skins"
	OpenCoworkPetEnabledKey     = "petDesktopEnabled"

	OpenCoworkPetMigrationKey     = "opencowork-pet"
	OpenCoworkPetMigrationVersion = 1
)

const (
	PetActionFeed  PetAction = "feed"
	PetActionBathe PetAction = "bathe"
	PetActionSoak  PetAction = "soak"
	PetActionPlay  PetAction = "play"
	PetActionSleep PetAction = "sleep"
	PetActionWork  PetAction = "work"
	PetActionStudy PetAction = "study"
)

// PetAction 是后续 PetService 和计划执行器共享的动作名，不包含属性增减逻辑。
type PetAction string

const (
	PetActionFailureCoins    PetActionFailureReason = "coins"
	PetActionFailureFull     PetActionFailureReason = "full"
	PetActionFailureClean    PetActionFailureReason = "clean"
	PetActionFailureHungry   PetActionFailureReason = "hungry"
	PetActionFailureLevel    PetActionFailureReason = "level"
	PetActionFailureBusy     PetActionFailureReason = "busy"
	PetActionFailureSleeping PetActionFailureReason = "sleeping"
)

type PetActionFailureReason string

type PetActionResult struct {
	OK     bool                   `json:"ok"`
	Reason PetActionFailureReason `json:"reason,omitempty"`
	Reward *PetAwayReward         `json:"reward,omitempty"`
}

const (
	PetAwayWork  PetAwayKind = "work"
	PetAwayStudy PetAwayKind = "study"
)

type PetAwayKind string

type PetAwayTask struct {
	Kind      PetAwayKind `json:"kind"`
	StartedAt int64       `json:"startedAt"`
	EndsAt    int64       `json:"endsAt"`
}

type PetAwayReward struct {
	Kind   PetAwayKind `json:"kind"`
	Coins  int64       `json:"coins"`
	Growth float64     `json:"growth"`
}

// PetState 是单只宠物的持久快照。PetService 负责动作和时间推进，DAO 只保存这个快照。
type PetState struct {
	PetID              string       `json:"id"`
	Name               string       `json:"name"`
	Hunger             float64      `json:"hunger"`
	Cleanliness        float64      `json:"cleanliness"`
	Mood               float64      `json:"mood"`
	Growth             float64      `json:"growth"`
	Coins              int64        `json:"coins"`
	Sleeping           bool         `json:"sleeping"`
	SleepEndsAt        int64        `json:"sleepEndsAt"`
	AwayTask           *PetAwayTask `json:"awayTask,omitempty"`
	LastTickAt         int64        `json:"lastTickAt"`
	AdoptedAt          int64        `json:"adoptedAt"`
	LastMilestoneDays  int64        `json:"lastMilestoneDays"`
	ProactiveDate      string       `json:"proactiveDate"`
	ProactiveCount     int64        `json:"proactiveCount"`
	LastProactiveAt    int64        `json:"lastProactiveAt"`
	CoinCreditedExp    float64      `json:"coinCreditedExp"`
	LastDailyBonusDate string       `json:"lastDailyBonusDate"`
}

type PetExperience struct {
	PetID       string  `json:"petId"`
	TotalExp    float64 `json:"totalExp"`
	TotalTokens int64   `json:"totalTokens"`
}

type PetExpLogEntry struct {
	ID      string  `json:"id"`
	PetID   string  `json:"petId,omitempty"`
	At      int64   `json:"at"`
	Model   string  `json:"model"`
	Tokens  int64   `json:"tokens"`
	Premium bool    `json:"premium"`
	Exp     float64 `json:"exp"`
}

type PetExpLogPage struct {
	Entries     []PetExpLogEntry `json:"entries"`
	Page        int              `json:"page"`
	PageSize    int              `json:"pageSize"`
	Total       int              `json:"total"`
	TotalPages  int              `json:"totalPages"`
	HasNext     bool             `json:"hasNext"`
	HasPrevious bool             `json:"hasPrevious"`
}

const (
	PetAutoCareMinThreshold     = 5
	PetAutoCareMaxThreshold     = 50
	PetAutoCareThresholdStep    = 5
	PetAutoCareDefaultThreshold = 20
)

type PetCareConfig struct {
	PetID             string `json:"petId"`
	AutoCareEnabled   bool   `json:"autoCareEnabled"`
	AutoCareThreshold int    `json:"autoCareThreshold"`
}

type PetReasoningEffort string

const (
	PetReasoningNone    PetReasoningEffort = "none"
	PetReasoningMinimal PetReasoningEffort = "minimal"
	PetReasoningLow     PetReasoningEffort = "low"
	PetReasoningMedium  PetReasoningEffort = "medium"
	PetReasoningHigh    PetReasoningEffort = "high"
)

type PetProactiveFrequency string

const (
	PetProactiveLow    PetProactiveFrequency = "low"
	PetProactiveMedium PetProactiveFrequency = "medium"
	PetProactiveHigh   PetProactiveFrequency = "high"
)

type PetVoiceMode string

const (
	PetVoiceAuto   PetVoiceMode = "auto"
	PetVoiceSpeech PetVoiceMode = "speech"
	PetVoiceChat   PetVoiceMode = "chat"
)

// PetAgentConfig 只保存 provider platform、provider/model 的引用字符串，绝不携带
// API Key 或 provider 配置副本。platform 必须显式保存：不同 provider owner 允许
// 使用相同的 ID，不能根据 ID 文本猜测应该读取 claude/codex/gemini 哪份配置。
type PetAgentConfig struct {
	PetID            string                `json:"petId"`
	ProviderPlatform *string               `json:"providerPlatform"`
	ProviderID       *string               `json:"providerId"`
	ModelID          *string               `json:"modelId"`
	ReasoningEffort  *PetReasoningEffort   `json:"reasoningEffort"`
	SystemPrompt     string                `json:"systemPrompt"`
	ProjectID        *string               `json:"projectId"`
	ProjectName      *string               `json:"projectName"`
	ProjectFolder    *string               `json:"projectFolder"`
	Proactive        bool                  `json:"proactive"`
	ProactiveFreq    PetProactiveFrequency `json:"proactiveFreq"`
	QuietStart       int                   `json:"quietStart"`
	QuietEnd         int                   `json:"quietEnd"`
	VoiceEnabled     bool                  `json:"voiceEnabled"`
	VoiceProviderID  *string               `json:"voiceProviderId"`
	VoiceModelID     *string               `json:"voiceModelId"`
	Voice            string                `json:"voice"`
	VoiceMode        PetVoiceMode          `json:"voiceMode"`
	VoiceInstruction string                `json:"voiceInstruction"`
	VoiceTag         string                `json:"voiceTag"`
}

type PetDreamEmotion string

const (
	PetDreamPleasant PetDreamEmotion = "pleasant"
	PetDreamCalm     PetDreamEmotion = "calm"
	PetDreamTense    PetDreamEmotion = "tense"
	PetDreamAfraid   PetDreamEmotion = "afraid"
)

const (
	PetDreamMinSleepTalkLength           = 5
	PetDreamMaxSleepTalkLength           = 100
	PetDreamDefaultSleepTalkLength       = 12
	PetDreamMinBubbleDurationSeconds     = 5
	PetDreamMaxBubbleDurationSeconds     = 60
	PetDreamDefaultBubbleDurationSeconds = 12
)

type PetDreamConfig struct {
	PetID                    string `json:"petId"`
	DreamEnabled             bool   `json:"dreamEnabled"`
	Prompt                   string `json:"prompt"`
	Keywords                 string `json:"keywords"`
	SleepTalkMinLength       int    `json:"sleepTalkMinLength"`
	BubbleMinDurationSeconds int    `json:"bubbleMinDurationSeconds"`
}

type PetWindowConfig struct {
	PetID   string `json:"petId"`
	Enabled bool   `json:"enabled"`
}

// PetWindowMode 将点击穿透和键盘焦点收敛为一个状态，避免 Vue 分别切换两个原生开关。
// Wails 没有 Electron 的 setFocusable/forward，因此 keyboard 模式只能显式 Focus。
type PetWindowMode string

const (
	PetWindowPassive     PetWindowMode = "passive"
	PetWindowInteractive PetWindowMode = "interactive"
	PetWindowKeyboard    PetWindowMode = "keyboard"
)

type PetWindowState struct {
	Version      int           `json:"version"`
	Open         bool          `json:"open"`
	Mode         PetWindowMode `json:"mode"`
	ClickThrough bool          `json:"clickThrough"`
	Focused      bool          `json:"focused"`
	AlwaysOnTop  bool          `json:"alwaysOnTop"`
}

type SetPetWindowModeRequest struct {
	Mode PetWindowMode `json:"mode"`
}

type PetCapability string

const (
	PetCapabilityChat          PetCapability = "chat"
	PetCapabilityTTS           PetCapability = "tts"
	PetCapabilityImage         PetCapability = "image"
	PetCapabilityTranscription PetCapability = "transcription"
)

// PetProviderReference 只允许携带引用和能力，不允许把 API Key 或 provider 实体复制进宠物状态。
type PetProviderReference struct {
	Platform     string        `json:"platform"`
	ProviderID   string        `json:"providerId"`
	Model        string        `json:"model"`
	Capability   PetCapability `json:"capability"`
	AutoFallback bool          `json:"autoFallback"`
}

type PetStreamEventType string

const (
	PetStreamStart     PetStreamEventType = "start"
	PetStreamDelta     PetStreamEventType = "delta"
	PetStreamTool      PetStreamEventType = "tool"
	PetStreamAudio     PetStreamEventType = "audio_delta"
	PetStreamUsage     PetStreamEventType = "usage"
	PetStreamDone      PetStreamEventType = "done"
	PetStreamError     PetStreamEventType = "error"
	PetStreamCancelled PetStreamEventType = "cancelled"
)

type PetStreamEvent struct {
	Type      PetStreamEventType `json:"type"`
	RequestID string             `json:"requestId"`
	Sequence  int64              `json:"sequence"`
	Data      json.RawMessage    `json:"data,omitempty"`
}

// PetStreamUsagePayload 是主控接收 usage 后调用 PetService.AddExperienceFromUsage
// 所需的稳定事实载荷。字段保持 provider 原始 usage 语义，不把 billable input
// 或费用结果混进来；价格和 premium 仍由 PetService 读取 canonical pricing 计算。
type PetStreamUsagePayload struct {
	ID                string `json:"id"`
	At                int64  `json:"at"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	InputTokens       int    `json:"inputTokens"`
	OutputTokens      int    `json:"outputTokens"`
	ReasoningTokens   int    `json:"reasoningTokens,omitempty"`
	CacheCreateTokens int    `json:"cacheCreateTokens,omitempty"`
	CacheReadTokens   int    `json:"cacheReadTokens,omitempty"`
	Ephemeral5mTokens int    `json:"ephemeral5mTokens,omitempty"`
	Ephemeral1hTokens int    `json:"ephemeral1hTokens,omitempty"`
	ServiceTier       string `json:"serviceTier,omitempty"`
}

type PetMemoryRecord struct {
	PetID     string `json:"petId"`
	ID        string `json:"id"`
	Date      string `json:"date"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type PetPlanScheduleKind string

const (
	PetPlanScheduleNow   PetPlanScheduleKind = "now"
	PetPlanScheduleDelay PetPlanScheduleKind = "delay"
	PetPlanScheduleAt    PetPlanScheduleKind = "at"
	PetPlanScheduleEvery PetPlanScheduleKind = "every"
	PetPlanScheduleCron  PetPlanScheduleKind = "cron"
)

type PetPlanStepKind string

const (
	PetPlanActionStep   PetPlanStepKind = "action"
	PetPlanReminderStep PetPlanStepKind = "reminder"
)

type PetPlanSchedule struct {
	Kind         PetPlanScheduleKind `json:"kind"`
	DelaySeconds float64             `json:"delaySeconds,omitempty"`
	At           json.RawMessage     `json:"at,omitempty"`
	EveryMS      int64               `json:"everyMs,omitempty"`
	Expr         string              `json:"expr,omitempty"`
	TZ           string              `json:"tz,omitempty"`
}

type PetPlanStep struct {
	Kind     PetPlanStepKind  `json:"kind"`
	Action   PetAction        `json:"action,omitempty"`
	Schedule *PetPlanSchedule `json:"schedule,omitempty"`
	Label    string           `json:"label,omitempty"`
	Text     string           `json:"text,omitempty"`
}

type PetPlanScript struct {
	Version int           `json:"version"`
	Title   string        `json:"title,omitempty"`
	Steps   []PetPlanStep `json:"steps"`
}

type PetPlanRecord struct {
	PetID     string        `json:"petId"`
	PlanID    string        `json:"planId"`
	Version   int           `json:"version"`
	Title     string        `json:"title"`
	Script    PetPlanScript `json:"script"`
	CreatedAt int64         `json:"createdAt"`
	UpdatedAt int64         `json:"updatedAt"`
}

type PetAtlasMetadata struct {
	AtlasVersion int    `json:"atlasVersion"`
	Image        string `json:"image"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Anchor       string `json:"anchor"`
	Layout       string `json:"layout"`
}

type PetSkinRecord struct {
	PetID                      string           `json:"petId"`
	SkinID                     string           `json:"skinId"`
	Name                       string           `json:"name"`
	Path                       string           `json:"path"`
	AtlasPath                  string           `json:"atlasPath"`
	Subject                    string           `json:"subject,omitempty"`
	ModelID                    string           `json:"modelId,omitempty"`
	CreatedAt                  *int64           `json:"createdAt,omitempty"`
	UpdatedAt                  *int64           `json:"updatedAt,omitempty"`
	Builtin                    bool             `json:"builtin"`
	AssetVersion               *int             `json:"assetVersion,omitempty"`
	SpriteNormalizationVersion *int             `json:"spriteNormalizationVersion,omitempty"`
	Atlas                      PetAtlasMetadata `json:"atlas"`
	ManifestJSON               json.RawMessage  `json:"manifestJson"`
}

type PetSkinSelection struct {
	PetID        string  `json:"petId"`
	ActiveSkinID *string `json:"activeSkinId"`
}

const (
	PetDreamHistoryPageSize       = 20
	PetDreamHistoryMaxPageSize    = 50
	PetDreamHistoryTitleMaxLength = 32
)

type PetDreamHistoryRecord struct {
	PetID           string           `json:"petId"`
	ID              string           `json:"id"`
	CreatedAt       int64            `json:"createdAt"`
	Title           string           `json:"title"`
	CreativePrompt  string           `json:"creativePrompt"`
	EffectivePrompt string           `json:"effectivePrompt"`
	Keywords        []string         `json:"keywords"`
	ThemeID         *string          `json:"themeId"`
	ThemeLabel      *string          `json:"themeLabel"`
	Dream           string           `json:"dream"`
	SleepTalk       string           `json:"sleepTalk"`
	Emotion         *PetDreamEmotion `json:"emotion"`
	SelfAppears     *bool            `json:"selfAppears"`
	ImagePath       *string          `json:"imagePath"`
}

type PetDreamHistoryPage struct {
	Records     []PetDreamHistoryRecord `json:"records"`
	Page        int                     `json:"page"`
	PageSize    int                     `json:"pageSize"`
	Total       int                     `json:"total"`
	TotalPages  int                     `json:"totalPages"`
	HasNext     bool                    `json:"hasNext"`
	HasPrevious bool                    `json:"hasPrevious"`
}

type PetMigrationDiagnosticKind string

const (
	PetMigrationDiagnosticMissing   PetMigrationDiagnosticKind = "missing"
	PetMigrationDiagnosticInvalid   PetMigrationDiagnosticKind = "invalid"
	PetMigrationDiagnosticIO        PetMigrationDiagnosticKind = "io"
	PetMigrationDiagnosticDatabase  PetMigrationDiagnosticKind = "database"
	PetMigrationDiagnosticReference PetMigrationDiagnosticKind = "missing_reference"
)

type PetMigrationDiagnostic struct {
	Kind     PetMigrationDiagnosticKind `json:"kind"`
	Source   string                     `json:"source"`
	Key      string                     `json:"key,omitempty"`
	RecordID string                     `json:"recordId,omitempty"`
	Message  string                     `json:"message"`
}

type PetMigrationReport struct {
	MigrationKey      string                   `json:"migrationKey"`
	Version           int                      `json:"version"`
	SourceRoot        string                   `json:"sourceRoot"`
	SourceFingerprint string                   `json:"sourceFingerprint,omitempty"`
	AlreadyApplied    bool                     `json:"alreadyApplied"`
	Imported          int                      `json:"imported"`
	Skipped           int                      `json:"skipped"`
	Failed            int                      `json:"failed"`
	Missing           int                      `json:"missing"`
	MissingReferences int                      `json:"missingReferences"`
	Diagnostics       []PetMigrationDiagnostic `json:"diagnostics"`
}

type PetMigrationSnapshot struct {
	PetID         string
	State         *PetState
	Experience    *PetExperience
	ExpLog        []PetExpLogEntry
	Care          *PetCareConfig
	Agent         *PetAgentConfig
	DreamConfig   *PetDreamConfig
	Window        *PetWindowConfig
	PlanRecords   []PetPlanRecord
	Skins         []PetSkinRecord
	SkinSelection *PetSkinSelection
	Dreams        []PetDreamHistoryRecord
	Memories      []PetMemoryRecord
}

type PetMigrationMarker struct {
	MigrationKey      string `json:"migrationKey"`
	Version           int    `json:"version"`
	Status            string `json:"status"`
	SourceFingerprint string `json:"sourceFingerprint"`
	StartedAt         int64  `json:"startedAt"`
	CompletedAt       *int64 `json:"completedAt,omitempty"`
	LastError         string `json:"lastError,omitempty"`
}

func DefaultPetStateAt(now int64) PetState {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	return PetState{
		PetID:              DefaultPetID,
		Name:               "Kapi",
		Hunger:             80,
		Cleanliness:        80,
		Mood:               70,
		Coins:              120,
		LastTickAt:         now,
		AdoptedAt:          now,
		ProactiveDate:      "",
		LastDailyBonusDate: "",
	}
}

func NormalizePetState(state PetState, now int64) PetState {
	defaults := DefaultPetStateAt(now)
	if strings.TrimSpace(state.PetID) == "" {
		state.PetID = defaults.PetID
	}
	if strings.TrimSpace(state.Name) == "" {
		state.Name = defaults.Name
	} else {
		state.Name = strings.TrimSpace(state.Name)
	}
	state.Hunger = clampFinite(state.Hunger, 0, 100, defaults.Hunger)
	state.Cleanliness = clampFinite(state.Cleanliness, 0, 100, defaults.Cleanliness)
	state.Mood = clampFinite(state.Mood, 0, 100, defaults.Mood)
	state.Growth = clampFinite(state.Growth, 0, math.MaxFloat64, defaults.Growth)
	if state.Coins < 0 {
		state.Coins = 0
	}
	if state.SleepEndsAt < 0 {
		state.SleepEndsAt = 0
	}
	if state.LastTickAt <= 0 {
		state.LastTickAt = defaults.LastTickAt
	}
	if state.AdoptedAt <= 0 {
		state.AdoptedAt = defaults.AdoptedAt
	}
	if state.LastMilestoneDays < 0 {
		state.LastMilestoneDays = 0
	}
	if state.ProactiveCount < 0 {
		state.ProactiveCount = 0
	}
	if state.LastProactiveAt < 0 {
		state.LastProactiveAt = 0
	}
	if state.CoinCreditedExp < 0 || math.IsNaN(state.CoinCreditedExp) || math.IsInf(state.CoinCreditedExp, 0) {
		state.CoinCreditedExp = 0
	}
	if state.ProactiveDate == "" {
		state.ProactiveDate = defaults.ProactiveDate
	}
	if state.LastDailyBonusDate == "" {
		state.LastDailyBonusDate = defaults.LastDailyBonusDate
	}
	if state.AwayTask != nil {
		if !IsPetAwayKind(state.AwayTask.Kind) || state.AwayTask.StartedAt <= 0 || state.AwayTask.EndsAt < state.AwayTask.StartedAt {
			state.AwayTask = nil
		}
	}
	return state
}

func NormalizePetCareThreshold(value float64) int {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return PetAutoCareDefaultThreshold
	}
	value = math.Min(PetAutoCareMaxThreshold, math.Max(PetAutoCareMinThreshold, value))
	return int(math.Round(value/PetAutoCareThresholdStep) * PetAutoCareThresholdStep)
}

func NormalizePetDreamLength(value float64, fallback, min, max int) int {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	value = math.Min(float64(max), math.Max(float64(min), value))
	return int(math.Round(value))
}

func IsPetAwayKind(value PetAwayKind) bool {
	return value == PetAwayWork || value == PetAwayStudy
}

func IsPetDreamEmotion(value PetDreamEmotion) bool {
	return value == PetDreamPleasant || value == PetDreamCalm || value == PetDreamTense || value == PetDreamAfraid
}

func clampFinite(value, min, max, fallback float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return math.Min(max, math.Max(min, value))
}
