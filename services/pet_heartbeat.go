package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	PetHeartbeatDefaultIntervalMinutes = 30
	PetHeartbeatMinIntervalMinutes     = 1
	PetHeartbeatMaxIntervalMinutes     = 1440
	PetHeartbeatDefaultIdleRetry       = time.Second
)

type PetHeartbeatPhase string

const (
	PetHeartbeatPhaseDisabled       PetHeartbeatPhase = "disabled"
	PetHeartbeatPhaseWaiting        PetHeartbeatPhase = "waiting"
	PetHeartbeatPhaseWaitingForIdle PetHeartbeatPhase = "waiting_for_idle"
	PetHeartbeatPhaseRunning        PetHeartbeatPhase = "running"
)

type PetHeartbeatRunStatus string

const (
	PetHeartbeatStatusNone        PetHeartbeatRunStatus = "none"
	PetHeartbeatStatusCompleted   PetHeartbeatRunStatus = "completed"
	PetHeartbeatStatusFailed      PetHeartbeatRunStatus = "failed"
	PetHeartbeatStatusCancelled   PetHeartbeatRunStatus = "cancelled"
	PetHeartbeatStatusInterrupted PetHeartbeatRunStatus = "interrupted"
)

// PetHeartbeatConfig 是心跳的持久化配置。它只保存任务提示词和频率，不复制
// Agent provider、项目路径或任何凭据；这些信息始终从宠物 Agent 配置和项目管理器读取。
type PetHeartbeatConfig struct {
	PetID           string `json:"petId"`
	Enabled         bool   `json:"enabled"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Prompt          string `json:"prompt"`
}

// PetHeartbeatRuntime 保存调度器最近一次可观察状态。当前任务和最近结果都在
// 这一条记录中维护，避免为同一个 Agent thread 再造一套心跳历史。
type PetHeartbeatRuntime struct {
	Phase            PetHeartbeatPhase     `json:"phase"`
	NextRunAt        int64                 `json:"nextRunAt,omitempty"`
	CurrentRequestID string                `json:"currentRequestId,omitempty"`
	LastStartedAt    int64                 `json:"lastStartedAt,omitempty"`
	LastFinishedAt   int64                 `json:"lastFinishedAt,omitempty"`
	LastStatus       PetHeartbeatRunStatus `json:"lastStatus,omitempty"`
	LastErrorCode    string                `json:"lastErrorCode,omitempty"`
}

type PetHeartbeatSnapshot struct {
	Config  PetHeartbeatConfig  `json:"config"`
	Runtime PetHeartbeatRuntime `json:"runtime"`
}

type PetHeartbeatEvent struct {
	Type     string               `json:"type"`
	Snapshot PetHeartbeatSnapshot `json:"snapshot"`
}

const PetHeartbeatEventStateChanged = "state_changed"

// PetHeartbeatRepository 是心跳记录的唯一持久化边界，避免 worker 直接持有 SQL。
type PetHeartbeatRepository interface {
	LoadHeartbeat(context.Context, string) (PetHeartbeatSnapshot, error)
	SaveHeartbeat(context.Context, PetHeartbeatSnapshot) error
}

// PetHeartbeatPetReader 只读取已归一化的宠物快照，状态和 Agent 配置仍由 PetService
// 负责其既有锁与默认值规则。
type PetHeartbeatPetReader interface {
	Load() (PetMigrationSnapshot, error)
}

// PetHeartbeatChatRunner 对齐当前 Agent 管家发送/取消边界。生产环境注入的是
// PetAIAPIService，其 StartChat 会继续委托现有 PetCodexRuntime。
type PetHeartbeatChatRunner interface {
	StartChat(PetChatRequest) (PetChatStartResult, error)
	CancelChat(string) error
}

// PetHeartbeatCompletionRegistrar 在调用 StartChat 前注册 request waiter，解决
// Codex 快速完成时终态事件可能先于调用方继续执行的问题。
type PetHeartbeatCompletionRegistrar interface {
	Register(string) (<-chan PetAIEvent, func())
}

type PetHeartbeatEmitter interface {
	Emit(PetHeartbeatEvent) error
}

type PetHeartbeatEmitterFunc func(PetHeartbeatEvent) error

func (f PetHeartbeatEmitterFunc) Emit(event PetHeartbeatEvent) error {
	if f == nil {
		return nil
	}
	return f(event)
}

// PetHeartbeatTimer 把一次性 timer 抽成可替换端口，测试可以推进 channel 而不必
// 真的等待分钟级周期；生产实现仍然只使用 time.NewTimer。
type PetHeartbeatTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type PetHeartbeatTimerFactory func(time.Duration) PetHeartbeatTimer

type petHeartbeatRealTimer struct {
	timer *time.Timer
}

func (t *petHeartbeatRealTimer) C() <-chan time.Time {
	if t == nil || t.timer == nil {
		return nil
	}
	return t.timer.C
}

func (t *petHeartbeatRealTimer) Stop() bool {
	if t == nil || t.timer == nil {
		return false
	}
	return t.timer.Stop()
}

func defaultPetHeartbeatTimerFactory(delay time.Duration) PetHeartbeatTimer {
	if delay < 0 {
		delay = 0
	}
	return &petHeartbeatRealTimer{timer: time.NewTimer(delay)}
}

type PetHeartbeatDependencies struct {
	PetID             string
	Pet               PetHeartbeatPetReader
	Repository        PetHeartbeatRepository
	WorkspaceResolver PetWorkspaceResolver
	ChatRunner        PetHeartbeatChatRunner
	Completions       PetHeartbeatCompletionRegistrar
	Emitter           PetHeartbeatEmitter
	Now               func() time.Time
	TimerFactory      PetHeartbeatTimerFactory
	IdleRetryInterval time.Duration
}

type PetHeartbeatErrorCode string

const (
	PetHeartbeatErrorInvalidRequest        PetHeartbeatErrorCode = "invalid_request"
	PetHeartbeatErrorDependencyUnavailable PetHeartbeatErrorCode = "dependency_unavailable"
	PetHeartbeatErrorBusy                  PetHeartbeatErrorCode = "busy"
	PetHeartbeatErrorNotRunning            PetHeartbeatErrorCode = "not_running"
	PetHeartbeatErrorWorkspaceUnavailable  PetHeartbeatErrorCode = "workspace_unavailable"
	PetHeartbeatErrorPersistenceFailed     PetHeartbeatErrorCode = "persistence_failed"
	PetHeartbeatErrorCancellationFailed    PetHeartbeatErrorCode = "cancellation_failed"
	PetHeartbeatErrorStartFailed           PetHeartbeatErrorCode = "start_failed"
)

type PetHeartbeatError struct {
	Code    PetHeartbeatErrorCode `json:"code"`
	Message string                `json:"message"`
	Cause   error                 `json:"-"`
}

func (e *PetHeartbeatError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *PetHeartbeatError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newPetHeartbeatError(code PetHeartbeatErrorCode, message string, cause error) *PetHeartbeatError {
	return &PetHeartbeatError{Code: code, Message: message, Cause: cause}
}

type petHeartbeatCommandKind uint8

const (
	petHeartbeatCommandSave petHeartbeatCommandKind = iota + 1
	petHeartbeatCommandRunNow
	petHeartbeatCommandCancel
	petHeartbeatCommandShutdown
)

type petHeartbeatCommand struct {
	kind   petHeartbeatCommandKind
	config PetHeartbeatConfig
	result chan petHeartbeatCommandResult
}

type petHeartbeatCommandResult struct {
	snapshot PetHeartbeatSnapshot
	err      error
}

// PetHeartbeatService 拥有心跳 worker 的状态机。所有周期推进都在单一 goroutine
// 中完成，API 只通过 command channel 改状态，从根上避免保存配置、终态事件和 timer
// 同时修改 currentRequestId 的竞态。
type PetHeartbeatService struct {
	petID             string
	pet               PetHeartbeatPetReader
	repository        PetHeartbeatRepository
	workspaceResolver PetWorkspaceResolver
	chatRunner        PetHeartbeatChatRunner
	completions       PetHeartbeatCompletionRegistrar
	emitter           PetHeartbeatEmitter
	now               func() time.Time
	timerFactory      PetHeartbeatTimerFactory
	idleRetryInterval time.Duration

	mu       sync.RWMutex
	snapshot PetHeartbeatSnapshot
	started  bool
	closing  bool
	closed   bool
	commands chan petHeartbeatCommand
	done     chan struct{}
	terminal chan PetAIEvent
}

var petHeartbeatRequestSequence uint64

func NewPetHeartbeatService(deps PetHeartbeatDependencies) *PetHeartbeatService {
	petID := strings.TrimSpace(deps.PetID)
	if petID == "" {
		petID = DefaultPetID
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	timerFactory := deps.TimerFactory
	if timerFactory == nil {
		timerFactory = defaultPetHeartbeatTimerFactory
	}
	idleRetry := deps.IdleRetryInterval
	if idleRetry <= 0 {
		idleRetry = PetHeartbeatDefaultIdleRetry
	}
	return &PetHeartbeatService{
		petID:             petID,
		pet:               deps.Pet,
		repository:        deps.Repository,
		workspaceResolver: deps.WorkspaceResolver,
		chatRunner:        deps.ChatRunner,
		completions:       deps.Completions,
		emitter:           deps.Emitter,
		now:               now,
		timerFactory:      timerFactory,
		idleRetryInterval: idleRetry,
	}
}

func defaultPetHeartbeatSnapshot(petID string) PetHeartbeatSnapshot {
	return PetHeartbeatSnapshot{
		Config: PetHeartbeatConfig{
			PetID:           petID,
			Enabled:         false,
			IntervalMinutes: PetHeartbeatDefaultIntervalMinutes,
		},
		Runtime: PetHeartbeatRuntime{
			Phase:      PetHeartbeatPhaseDisabled,
			LastStatus: PetHeartbeatStatusNone,
		},
	}
}

func normalizePetHeartbeatSnapshot(snapshot PetHeartbeatSnapshot, petID string) PetHeartbeatSnapshot {
	defaults := defaultPetHeartbeatSnapshot(petID)
	config := snapshot.Config
	config.PetID = petID
	if config.IntervalMinutes < PetHeartbeatMinIntervalMinutes || config.IntervalMinutes > PetHeartbeatMaxIntervalMinutes {
		config.IntervalMinutes = defaults.Config.IntervalMinutes
	}
	config.Prompt = strings.TrimSpace(config.Prompt)

	runtime := snapshot.Runtime
	switch runtime.Phase {
	case PetHeartbeatPhaseDisabled, PetHeartbeatPhaseWaiting, PetHeartbeatPhaseWaitingForIdle, PetHeartbeatPhaseRunning:
	default:
		runtime.Phase = PetHeartbeatPhaseDisabled
	}
	switch runtime.LastStatus {
	case PetHeartbeatStatusNone, PetHeartbeatStatusCompleted, PetHeartbeatStatusFailed,
		PetHeartbeatStatusCancelled, PetHeartbeatStatusInterrupted:
	default:
		runtime.LastStatus = PetHeartbeatStatusNone
	}
	runtime.CurrentRequestID = strings.TrimSpace(runtime.CurrentRequestID)
	if runtime.Phase != PetHeartbeatPhaseRunning {
		runtime.CurrentRequestID = ""
	}
	if !config.Enabled {
		runtime.Phase = PetHeartbeatPhaseDisabled
		runtime.NextRunAt = 0
		runtime.CurrentRequestID = ""
	}
	return PetHeartbeatSnapshot{Config: config, Runtime: runtime}
}

func (s *PetHeartbeatService) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.repository == nil || s.pet == nil || s.workspaceResolver == nil ||
		s.chatRunner == nil || s.completions == nil {
		return newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳依赖未配置", nil)
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.closed || s.closing {
		s.mu.Unlock()
		return newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务已关闭", nil)
	}
	s.mu.Unlock()

	snapshot, err := s.repository.LoadHeartbeat(ctx, s.petID)
	if err != nil {
		return newPetHeartbeatError(PetHeartbeatErrorPersistenceFailed, "读取心跳配置失败", err)
	}
	snapshot = normalizePetHeartbeatSnapshot(snapshot, s.petID)
	changed := false
	now := s.now()
	if now.IsZero() {
		now = time.Now()
	}
	if snapshot.Runtime.LastStatus == "" {
		snapshot.Runtime.LastStatus = PetHeartbeatStatusNone
		changed = true
	}
	// 进程异常退出时无法收到旧 request 的终态；恢复必须把它标成 interrupted，
	// 并从本次启动时刻重新等待完整周期，绝不能重复提交上一条 user message。
	// 即使旧数据缺少 requestId，也不能把 running 留在无 timer 的卡死状态。
	if snapshot.Runtime.Phase == PetHeartbeatPhaseRunning {
		snapshot.Runtime.LastStatus = PetHeartbeatStatusInterrupted
		snapshot.Runtime.LastFinishedAt = now.UnixMilli()
		snapshot.Runtime.CurrentRequestID = ""
		if snapshot.Config.Enabled {
			snapshot.Runtime.Phase = PetHeartbeatPhaseWaiting
			snapshot.Runtime.NextRunAt = now.Add(petHeartbeatInterval(snapshot.Config)).UnixMilli()
		} else {
			snapshot.Runtime.Phase = PetHeartbeatPhaseDisabled
			snapshot.Runtime.NextRunAt = 0
		}
		changed = true
	}
	if snapshot.Runtime.Phase == PetHeartbeatPhaseWaitingForIdle {
		// waiting_for_idle 没有真正提交任务，重启后也不沿用旧的“立即重试”意图，
		// 否则应用刚启动就可能和恢复中的人工聊天互相抢占。
		snapshot.Runtime.Phase = PetHeartbeatPhaseWaiting
		snapshot.Runtime.NextRunAt = now.Add(petHeartbeatInterval(snapshot.Config)).UnixMilli()
		changed = true
	}
	if snapshot.Config.Enabled && snapshot.Runtime.Phase == PetHeartbeatPhaseDisabled {
		snapshot.Runtime.Phase = PetHeartbeatPhaseWaiting
		snapshot.Runtime.NextRunAt = now.Add(petHeartbeatInterval(snapshot.Config)).UnixMilli()
		changed = true
	}
	if snapshot.Config.Enabled && snapshot.Runtime.Phase == PetHeartbeatPhaseWaiting && snapshot.Runtime.NextRunAt == 0 {
		// 旧版本或手工修复可能只保存了 waiting；没有明确的到期时间时，
		// 必须从本次启动开始等待完整周期，不能因为零值把任务立即补发。
		snapshot.Runtime.NextRunAt = now.Add(petHeartbeatInterval(snapshot.Config)).UnixMilli()
		changed = true
	}
	if !snapshot.Config.Enabled {
		snapshot.Runtime.Phase = PetHeartbeatPhaseDisabled
		snapshot.Runtime.NextRunAt = 0
		changed = true
	}
	if changed {
		if err := s.repository.SaveHeartbeat(ctx, snapshot); err != nil {
			return newPetHeartbeatError(PetHeartbeatErrorPersistenceFailed, "恢复心跳状态失败", err)
		}
	}

	s.mu.Lock()
	if s.started || s.closed || s.closing {
		s.mu.Unlock()
		return nil
	}
	s.snapshot = snapshot
	s.started = true
	s.commands = make(chan petHeartbeatCommand, 16)
	s.done = make(chan struct{})
	s.terminal = make(chan PetAIEvent, 2)
	done := s.done
	s.mu.Unlock()

	go s.loop(snapshot, done)
	return nil
}

func (s *PetHeartbeatService) GetSnapshot() (PetHeartbeatSnapshot, error) {
	if s == nil {
		return PetHeartbeatSnapshot{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务不可用", nil)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.started {
		return PetHeartbeatSnapshot{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务尚未启动", nil)
	}
	return s.snapshot, nil
}

func (s *PetHeartbeatService) SaveConfig(config PetHeartbeatConfig) (PetHeartbeatSnapshot, error) {
	result, err := s.sendCommand(context.Background(), petHeartbeatCommand{kind: petHeartbeatCommandSave, config: config})
	return result.snapshot, err
}

func (s *PetHeartbeatService) RunNow() (PetHeartbeatSnapshot, error) {
	result, err := s.sendCommand(context.Background(), petHeartbeatCommand{kind: petHeartbeatCommandRunNow})
	return result.snapshot, err
}

func (s *PetHeartbeatService) Cancel() (PetHeartbeatSnapshot, error) {
	result, err := s.sendCommand(context.Background(), petHeartbeatCommand{kind: petHeartbeatCommandCancel})
	return result.snapshot, err
}

func (s *PetHeartbeatService) sendCommand(ctx context.Context, command petHeartbeatCommand) (petHeartbeatCommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command.result = make(chan petHeartbeatCommandResult, 1)
	s.mu.RLock()
	started := s.started
	closing := s.closing
	closed := s.closed
	commands := s.commands
	done := s.done
	s.mu.RUnlock()
	if !started || closing || closed || commands == nil {
		return petHeartbeatCommandResult{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务不可用", nil)
	}
	select {
	case commands <- command:
	case <-done:
		return petHeartbeatCommandResult{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务已停止", nil)
	case <-ctx.Done():
		return petHeartbeatCommandResult{}, ctx.Err()
	}
	select {
	case result := <-command.result:
		return result, result.err
	case <-done:
		return petHeartbeatCommandResult{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳服务已停止", nil)
	case <-ctx.Done():
		return petHeartbeatCommandResult{}, ctx.Err()
	}
}

func (s *PetHeartbeatService) loop(state PetHeartbeatSnapshot, done chan struct{}) {
	defer close(done)
	var timer PetHeartbeatTimer
	var timerC <-chan time.Time
	setTimer := func(delay time.Duration) {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		if delay < 0 {
			delay = 0
		}
		timer = s.timerFactory(delay)
		if timer != nil {
			timerC = timer.C()
		}
	}
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
		}
		timer = nil
		timerC = nil
	}
	configureTimer := func() {
		stopTimer()
		if state.Runtime.Phase == PetHeartbeatPhaseWaiting {
			delay := time.Duration(state.Runtime.NextRunAt-s.now().UnixMilli()) * time.Millisecond
			setTimer(delay)
		} else if state.Runtime.Phase == PetHeartbeatPhaseWaitingForIdle {
			setTimer(s.idleRetryInterval)
		}
	}

	configureTimer()
	s.publishSnapshot(state)
	for {
		select {
		case command := <-s.commands:
			switch command.kind {
			case petHeartbeatCommandSave:
				command.result <- s.handleSave(&state, configureTimer, command.config)
			case petHeartbeatCommandRunNow:
				command.result <- s.handleRunNow(&state, stopTimer, configureTimer)
			case petHeartbeatCommandCancel:
				command.result <- s.handleCancel(&state, configureTimer)
			case petHeartbeatCommandShutdown:
				command.result <- s.handleShutdown(&state, stopTimer, configureTimer)
				return
			default:
				command.result <- petHeartbeatCommandResult{snapshot: state, err: newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "未知心跳操作", nil)}
			}
		case <-timerC:
			timer = nil
			timerC = nil
			if state.Runtime.Phase == PetHeartbeatPhaseWaiting || state.Runtime.Phase == PetHeartbeatPhaseWaitingForIdle {
				_ = s.attemptRun(&state, configureTimer)
			}
		case event := <-s.terminal:
			s.handleTerminal(&state, event, configureTimer)
		}
	}
}

func (s *PetHeartbeatService) handleSave(
	state *PetHeartbeatSnapshot,
	configureTimer func(),
	config PetHeartbeatConfig,
) petHeartbeatCommandResult {
	if state == nil {
		return petHeartbeatCommandResult{err: newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "心跳状态不可用", nil)}
	}
	config, err := s.normalizeConfig(config)
	if err != nil {
		return petHeartbeatCommandResult{snapshot: *state, err: err}
	}
	previous := *state
	if config.Enabled && !state.Config.Enabled {
		if err := s.validateWorkspace(); err != nil {
			return petHeartbeatCommandResult{snapshot: *state, err: err}
		}
	}
	state.Config = config
	if state.Runtime.Phase == PetHeartbeatPhaseRunning {
		if err := s.persist(*state); err != nil {
			*state = previous
			return petHeartbeatCommandResult{snapshot: *state, err: err}
		}
		s.publishSnapshot(*state)
		return petHeartbeatCommandResult{snapshot: *state}
	}
	if !config.Enabled {
		state.Runtime.Phase = PetHeartbeatPhaseDisabled
		state.Runtime.NextRunAt = 0
		state.Runtime.CurrentRequestID = ""
	} else if state.Runtime.Phase != PetHeartbeatPhaseWaitingForIdle {
		state.Runtime.Phase = PetHeartbeatPhaseWaiting
		state.Runtime.NextRunAt = s.now().Add(petHeartbeatInterval(config)).UnixMilli()
	}
	if err := s.persist(*state); err != nil {
		*state = previous
		configureTimer()
		return petHeartbeatCommandResult{snapshot: *state, err: err}
	}
	configureTimer()
	s.publishSnapshot(*state)
	return petHeartbeatCommandResult{snapshot: *state}
}

func (s *PetHeartbeatService) handleRunNow(
	state *PetHeartbeatSnapshot,
	stopTimer func(),
	configureTimer func(),
) petHeartbeatCommandResult {
	if state.Runtime.Phase == PetHeartbeatPhaseRunning || state.Runtime.Phase == PetHeartbeatPhaseWaitingForIdle {
		return petHeartbeatCommandResult{snapshot: *state, err: newPetHeartbeatError(PetHeartbeatErrorBusy, "已有心跳任务正在执行或等待 Agent 空闲", nil)}
	}
	if err := validatePetHeartbeatConfig(state.Config, true); err != nil {
		return petHeartbeatCommandResult{snapshot: *state, err: err}
	}
	stopTimer()
	// enabled=false 的 RunNow 是单次任务；它只临时绕过周期，不改变保存的开关。
	oneShot := !state.Config.Enabled
	err := s.attemptRunWithMode(state, configureTimer, oneShot)
	return petHeartbeatCommandResult{snapshot: *state, err: err}
}

func (s *PetHeartbeatService) handleCancel(
	state *PetHeartbeatSnapshot,
	configureTimer func(),
) petHeartbeatCommandResult {
	switch state.Runtime.Phase {
	case PetHeartbeatPhaseRunning:
		requestID := state.Runtime.CurrentRequestID
		if requestID == "" {
			return petHeartbeatCommandResult{snapshot: *state, err: newPetHeartbeatError(PetHeartbeatErrorNotRunning, "当前没有可取消的心跳任务", nil)}
		}
		if err := s.chatRunner.CancelChat(requestID); err != nil {
			return petHeartbeatCommandResult{snapshot: *state, err: newPetHeartbeatError(PetHeartbeatErrorCancellationFailed, "取消心跳任务失败", err)}
		}
		// 取消请求只表示已发出；最终状态必须等待 PetAI 的 cancelled 终态事件。
		return petHeartbeatCommandResult{snapshot: *state}
	case PetHeartbeatPhaseWaitingForIdle:
		state.Runtime.Phase = PetHeartbeatPhaseWaiting
		state.Runtime.NextRunAt = s.now().Add(petHeartbeatInterval(state.Config)).UnixMilli()
		if !state.Config.Enabled {
			state.Runtime.Phase = PetHeartbeatPhaseDisabled
			state.Runtime.NextRunAt = 0
		}
		if err := s.persist(*state); err != nil {
			return petHeartbeatCommandResult{snapshot: *state, err: err}
		}
		configureTimer()
		s.publishSnapshot(*state)
		return petHeartbeatCommandResult{snapshot: *state}
	case PetHeartbeatPhaseWaiting:
		return petHeartbeatCommandResult{snapshot: *state, err: newPetHeartbeatError(PetHeartbeatErrorNotRunning, "当前没有正在执行的心跳任务", nil)}
	default:
		return petHeartbeatCommandResult{snapshot: *state}
	}
}

func (s *PetHeartbeatService) handleShutdown(
	state *PetHeartbeatSnapshot,
	stopTimer func(),
	configureTimer func(),
) petHeartbeatCommandResult {
	stopTimer()
	if state.Runtime.Phase == PetHeartbeatPhaseRunning && state.Runtime.CurrentRequestID != "" {
		requestID := state.Runtime.CurrentRequestID
		state.Runtime.LastStatus = PetHeartbeatStatusInterrupted
		state.Runtime.LastFinishedAt = s.now().UnixMilli()
		state.Runtime.LastErrorCode = ""
		state.Runtime.CurrentRequestID = ""
		if state.Config.Enabled {
			state.Runtime.Phase = PetHeartbeatPhaseWaiting
			state.Runtime.NextRunAt = s.now().Add(petHeartbeatInterval(state.Config)).UnixMilli()
		} else {
			state.Runtime.Phase = PetHeartbeatPhaseDisabled
			state.Runtime.NextRunAt = 0
		}
		if err := s.persist(*state); err != nil {
			// 退出仍要继续；下次启动会把残留 running 记录再次归类为 interrupted。
			servicesWritePetHeartbeatDiagnostic("shutdown-state-save-failed", err)
		}
		_ = s.chatRunner.CancelChat(requestID)
	} else if state.Runtime.Phase == PetHeartbeatPhaseWaitingForIdle {
		state.Runtime.Phase = PetHeartbeatPhaseWaiting
		state.Runtime.NextRunAt = s.now().Add(petHeartbeatInterval(state.Config)).UnixMilli()
		if !state.Config.Enabled {
			state.Runtime.Phase = PetHeartbeatPhaseDisabled
			state.Runtime.NextRunAt = 0
		}
		_ = s.persist(*state)
		configureTimer()
	}
	s.publishSnapshot(*state)
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return petHeartbeatCommandResult{snapshot: *state}
}

func (s *PetHeartbeatService) attemptRun(state *PetHeartbeatSnapshot, configureTimer func()) error {
	return s.attemptRunWithMode(state, configureTimer, false)
}

func (s *PetHeartbeatService) attemptRunWithMode(
	state *PetHeartbeatSnapshot,
	configureTimer func(),
	oneShot bool,
) error {
	request, err := s.buildRequest()
	if err != nil {
		return s.finishUnsubmittedFailure(state, configureTimer, oneShot, err)
	}
	previous := *state
	now := s.now()
	state.Runtime.Phase = PetHeartbeatPhaseRunning
	state.Runtime.NextRunAt = 0
	state.Runtime.CurrentRequestID = request.RequestID
	state.Runtime.LastStartedAt = now.UnixMilli()
	state.Runtime.LastErrorCode = ""
	if err := s.persist(*state); err != nil {
		*state = previous
		return newPetHeartbeatError(PetHeartbeatErrorPersistenceFailed, "保存心跳运行状态失败", err)
	}
	s.publishSnapshot(*state)

	// waiter 必须早于 StartChat 注册；Codex 允许 turn 在 start RPC 返回前就完成。
	waiter, unregister := s.completions.Register(request.RequestID)
	_, startErr := s.chatRunner.StartChat(request)
	if startErr != nil {
		unregister()
		if PetAIErrorCodeOf(startErr) == string(PET_AI_REQUEST_IN_FLIGHT) {
			*state = previous
			state.Runtime.Phase = PetHeartbeatPhaseWaitingForIdle
			state.Runtime.NextRunAt = 0
			if err := s.persist(*state); err != nil {
				return newPetHeartbeatError(PetHeartbeatErrorPersistenceFailed, "保存等待空闲状态失败", err)
			}
			configureTimer()
			s.publishSnapshot(*state)
			return nil
		}
		return s.finishSubmittedFailure(state, configureTimer, oneShot, startErr)
	}
	go s.forwardCompletion(request.RequestID, waiter, unregister)
	return nil
}

func (s *PetHeartbeatService) forwardCompletion(
	requestID string,
	waiter <-chan PetAIEvent,
	unregister func(),
) {
	defer unregister()
	select {
	case event := <-waiter:
		if strings.TrimSpace(event.RequestID) != requestID {
			return
		}
		select {
		case s.terminal <- event:
		case <-s.done:
		}
	case <-s.done:
	}
}

func (s *PetHeartbeatService) handleTerminal(
	state *PetHeartbeatSnapshot,
	event PetAIEvent,
	configureTimer func(),
) {
	if state.Runtime.Phase != PetHeartbeatPhaseRunning || state.Runtime.CurrentRequestID != event.RequestID {
		return
	}
	status := PetHeartbeatStatusFailed
	switch event.Type {
	case PetAIEventCompleted:
		status = PetHeartbeatStatusCompleted
	case PetAIEventCancelled:
		status = PetHeartbeatStatusCancelled
	case PetAIEventFailed:
		status = PetHeartbeatStatusFailed
	default:
		return
	}
	state.Runtime.LastStatus = status
	state.Runtime.LastFinishedAt = s.now().UnixMilli()
	state.Runtime.LastErrorCode = ""
	if event.Error != nil {
		state.Runtime.LastErrorCode = strings.TrimSpace(event.Error.Code)
	}
	state.Runtime.CurrentRequestID = ""
	if state.Config.Enabled {
		state.Runtime.Phase = PetHeartbeatPhaseWaiting
		state.Runtime.NextRunAt = s.now().Add(petHeartbeatInterval(state.Config)).UnixMilli()
	} else {
		state.Runtime.Phase = PetHeartbeatPhaseDisabled
		state.Runtime.NextRunAt = 0
	}
	if err := s.persist(*state); err != nil {
		servicesWritePetHeartbeatDiagnostic("terminal-state-save-failed", err)
	}
	configureTimer()
	s.publishSnapshot(*state)
}

func (s *PetHeartbeatService) finishUnsubmittedFailure(
	state *PetHeartbeatSnapshot,
	configureTimer func(),
	oneShot bool,
	err error,
) error {
	state.Runtime.LastStatus = PetHeartbeatStatusFailed
	state.Runtime.LastFinishedAt = s.now().UnixMilli()
	state.Runtime.LastErrorCode = petHeartbeatErrorCode(err)
	state.Runtime.CurrentRequestID = ""
	if state.Config.Enabled && !oneShot {
		state.Runtime.Phase = PetHeartbeatPhaseWaiting
		state.Runtime.NextRunAt = s.now().Add(petHeartbeatInterval(state.Config)).UnixMilli()
	} else {
		state.Runtime.Phase = PetHeartbeatPhaseDisabled
		state.Runtime.NextRunAt = 0
	}
	if saveErr := s.persist(*state); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	configureTimer()
	s.publishSnapshot(*state)
	return err
}

func (s *PetHeartbeatService) finishSubmittedFailure(
	state *PetHeartbeatSnapshot,
	configureTimer func(),
	oneShot bool,
	err error,
) error {
	state.Runtime.LastStatus = PetHeartbeatStatusFailed
	state.Runtime.LastFinishedAt = s.now().UnixMilli()
	state.Runtime.LastErrorCode = petHeartbeatErrorCode(err)
	state.Runtime.CurrentRequestID = ""
	if state.Config.Enabled && !oneShot {
		state.Runtime.Phase = PetHeartbeatPhaseWaiting
		state.Runtime.NextRunAt = s.now().Add(petHeartbeatInterval(state.Config)).UnixMilli()
	} else {
		state.Runtime.Phase = PetHeartbeatPhaseDisabled
		state.Runtime.NextRunAt = 0
	}
	if saveErr := s.persist(*state); saveErr != nil {
		return errors.Join(err, saveErr)
	}
	configureTimer()
	s.publishSnapshot(*state)
	return err
}

func (s *PetHeartbeatService) buildRequest() (PetChatRequest, error) {
	if err := validatePetHeartbeatConfig(s.snapshotValue().Config, true); err != nil {
		return PetChatRequest{}, err
	}
	snapshot, err := s.pet.Load()
	if err != nil {
		return PetChatRequest{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "读取宠物 Agent 配置失败", err)
	}
	if snapshot.State == nil || snapshot.Agent == nil {
		return PetChatRequest{}, newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "宠物快照缺少 Agent 配置", nil)
	}
	workspace, err := s.resolveWorkspace()
	if err != nil {
		return PetChatRequest{}, err
	}
	config := s.snapshotValue().Config
	status := heartbeatPetStatus(*snapshot.State)
	project := petWorkspaceString(snapshot.Agent.ProjectName)
	projectID := petWorkspaceString(snapshot.Agent.ProjectID)
	if project == "" {
		project = filepath.Base(filepath.Clean(workspace))
	}
	if project == "." || project == string(filepath.Separator) || project == "" {
		project = "当前项目"
	}
	prompt, err := renderPetHeartbeatPrompt(config.Prompt, map[string]string{
		"name":    strings.TrimSpace(snapshot.State.Name),
		"status":  status,
		"project": project,
	})
	if err != nil {
		return PetChatRequest{}, newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "心跳提示词模板无效", err)
	}
	if strings.TrimSpace(prompt) == "" {
		return PetChatRequest{}, newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "心跳提示词不能为空", nil)
	}
	if runeLen(prompt) > PetAIMaxUserTextLength {
		return PetChatRequest{}, newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "心跳提示词过长", nil)
	}
	requestID := newPetHeartbeatRequestID()
	return PetChatRequest{
		PetID:     s.petID,
		ProjectID: projectID,
		RequestID: requestID,
		Persona:   BuildPetAgentPersona(snapshot.Agent.SystemPrompt, snapshot.State.Name),
		RuntimeContext: buildPetRuntimeContextAt(s.now()),
		UserText:  prompt,
	}, nil
}

func (s *PetHeartbeatService) validateWorkspace() error {
	_, err := s.resolveWorkspace()
	return err
}

func (s *PetHeartbeatService) resolveWorkspace() (string, error) {
	if s == nil || s.workspaceResolver == nil {
		return "", newPetHeartbeatError(PetHeartbeatErrorDependencyUnavailable, "项目绑定解析服务不可用", nil)
	}
	workspace, err := s.workspaceResolver.Resolve(context.Background(), s.petID)
	if err != nil || strings.TrimSpace(workspace) == "" {
		return "", newPetHeartbeatError(PetHeartbeatErrorWorkspaceUnavailable, "当前宠物没有有效的绑定项目", err)
	}
	return filepath.Clean(strings.TrimSpace(workspace)), nil
}

func (s *PetHeartbeatService) normalizeConfig(config PetHeartbeatConfig) (PetHeartbeatConfig, error) {
	if strings.TrimSpace(config.PetID) != "" && strings.TrimSpace(config.PetID) != s.petID {
		return PetHeartbeatConfig{}, newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "心跳配置的 petId 与目标宠物不一致", nil)
	}
	config.PetID = s.petID
	config.Prompt = strings.TrimSpace(config.Prompt)
	if err := validatePetHeartbeatConfig(config, config.Enabled); err != nil {
		return PetHeartbeatConfig{}, err
	}
	return config, nil
}

func validatePetHeartbeatConfig(config PetHeartbeatConfig, promptRequired bool) error {
	if config.IntervalMinutes < PetHeartbeatMinIntervalMinutes || config.IntervalMinutes > PetHeartbeatMaxIntervalMinutes {
		return newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "心跳频率必须在 1 到 1440 分钟之间", nil)
	}
	config.Prompt = strings.TrimSpace(config.Prompt)
	if promptRequired && config.Prompt == "" {
		return newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "启用心跳或立即执行时提示词不能为空", nil)
	}
	if runeLen(config.Prompt) > PetAIMaxUserTextLength {
		return newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "心跳提示词过长", nil)
	}
	if _, err := renderPetHeartbeatPrompt(config.Prompt, map[string]string{
		"name": "Kapi", "status": "awake", "project": "当前项目",
	}); err != nil {
		return newPetHeartbeatError(PetHeartbeatErrorInvalidRequest, "心跳提示词模板无效", err)
	}
	return nil
}

func renderPetHeartbeatPrompt(template string, values map[string]string) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", nil
	}
	var result strings.Builder
	for len(template) > 0 {
		start := strings.Index(template, "{{")
		close := strings.Index(template, "}}")
		if close >= 0 && (start < 0 || close < start) {
			return "", errors.New("模板包含未匹配的结束标记")
		}
		if start < 0 {
			result.WriteString(template)
			break
		}
		result.WriteString(template[:start])
		body := template[start+2:]
		bodyEnd := strings.Index(body, "}}")
		if bodyEnd < 0 {
			return "", errors.New("模板缺少结束标记")
		}
		key := strings.TrimSpace(body[:bodyEnd])
		if key == "" {
			return "", errors.New("模板变量不能为空")
		}
		value, ok := values[key]
		if !ok {
			return "", fmt.Errorf("不支持的模板变量 %q", key)
		}
		result.WriteString(value)
		template = body[bodyEnd+2:]
	}
	return strings.TrimSpace(result.String()), nil
}

func petHeartbeatInterval(config PetHeartbeatConfig) time.Duration {
	return time.Duration(config.IntervalMinutes) * time.Minute
}

func heartbeatPetStatus(state PetState) string {
	if state.AwayTask != nil {
		switch state.AwayTask.Kind {
		case PetAwayWork:
			return "working"
		case PetAwayStudy:
			return "studying"
		}
	}
	if state.Sleeping {
		return "sleeping"
	}
	return "awake"
}

func newPetHeartbeatRequestID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return "pet-heartbeat-" + hex.EncodeToString(buffer)
	}
	sequence := atomic.AddUint64(&petHeartbeatRequestSequence, 1)
	return fmt.Sprintf("pet-heartbeat-%d-%d", time.Now().UnixNano(), sequence)
}

func petHeartbeatErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if code := PetAIErrorCodeOf(err); code != "" {
		return code
	}
	var heartbeatErr *PetHeartbeatError
	if errors.As(err, &heartbeatErr) && heartbeatErr != nil {
		return string(heartbeatErr.Code)
	}
	return string(PetHeartbeatErrorStartFailed)
}

func (s *PetHeartbeatService) persist(snapshot PetHeartbeatSnapshot) error {
	if err := s.repository.SaveHeartbeat(context.Background(), snapshot); err != nil {
		return newPetHeartbeatError(PetHeartbeatErrorPersistenceFailed, "保存心跳状态失败", err)
	}
	return nil
}

func (s *PetHeartbeatService) publishSnapshot(snapshot PetHeartbeatSnapshot) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
	if s.emitter != nil {
		_ = s.emitter.Emit(PetHeartbeatEvent{Type: PetHeartbeatEventStateChanged, Snapshot: snapshot})
	}
}

func (s *PetHeartbeatService) snapshotValue() PetHeartbeatSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *PetHeartbeatService) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if !s.started || s.closed {
		s.mu.Unlock()
		return nil
	}
	done := s.done
	if s.closing {
		s.mu.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.closing = true
	commands := s.commands
	s.mu.Unlock()

	command := petHeartbeatCommand{kind: petHeartbeatCommandShutdown, result: make(chan petHeartbeatCommandResult, 1)}
	select {
	case commands <- command:
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case result := <-command.result:
		if result.err != nil {
			return result.err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func servicesWritePetHeartbeatDiagnostic(event string, err error) {
	if err == nil {
		return
	}
	WriteRuntimeDiagnosticAsync(event, fmt.Sprintf("error=%q", err.Error()))
}
