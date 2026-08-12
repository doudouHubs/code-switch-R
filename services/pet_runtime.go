package services

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// PetRuntimeDefaultCooldown 与源项目的 30 秒状态 tick 对齐。
// 运行时不创建 ticker，主控可以把 Tick 接到已有调度器上；冷却只负责防止
// 同一轮被重复驱动时连续扣费，且不写入宠物快照。
const PetRuntimeDefaultCooldown = PetTickInterval

type PetRuntimeStatus string

const (
	PetRuntimeStatusExecuted     PetRuntimeStatus = "executed"
	PetRuntimeStatusActionFailed PetRuntimeStatus = "action_failed"
	PetRuntimeStatusDisabled     PetRuntimeStatus = "disabled"
	PetRuntimeStatusSleeping     PetRuntimeStatus = "sleeping"
	PetRuntimeStatusAway         PetRuntimeStatus = "away"
	PetRuntimeStatusRewarded     PetRuntimeStatus = "away_rewarded"
	PetRuntimeStatusNoNeed       PetRuntimeStatus = "no_need"
	PetRuntimeStatusCoolingDown  PetRuntimeStatus = "cooling_down"
	PetRuntimeStatusError        PetRuntimeStatus = "error"
)

type PetRuntimeSkipReason string

const (
	PetRuntimeSkipDisabled   PetRuntimeSkipReason = "disabled"
	PetRuntimeSkipSleeping   PetRuntimeSkipReason = "sleeping"
	PetRuntimeSkipAway       PetRuntimeSkipReason = "away"
	PetRuntimeSkipAwayReward PetRuntimeSkipReason = "away_reward"
	PetRuntimeSkipNoNeed     PetRuntimeSkipReason = "no_need"
	PetRuntimeSkipCooldown   PetRuntimeSkipReason = "cooldown"
)

// PetRuntimeAction 记录一次自动照料动作尝试。业务拒绝不是 Go error，
// 但必须通过 Reason 暴露给上层；读取或持久化错误则通过 Error 和 RunOnce 的 error 返回。
type PetRuntimeAction struct {
	Action PetAction              `json:"action"`
	OK     bool                   `json:"ok"`
	Reason PetActionFailureReason `json:"reason,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// PetRuntimeResult 是一次可观测的运行结果。Actions 按尝试顺序保存，
// ExecutedActions 只保存成功动作；Snapshot 始终尽量反映本轮最后一次已知状态。
type PetRuntimeResult struct {
	PetID           string               `json:"petId"`
	At              int64                `json:"at"`
	Status          PetRuntimeStatus     `json:"status"`
	Skipped         bool                 `json:"skipped"`
	SkipReason      PetRuntimeSkipReason `json:"skipReason,omitempty"`
	Failed          bool                 `json:"failed"`
	CooldownUntil   int64                `json:"cooldownUntil,omitempty"`
	Config          PetCareConfig        `json:"config"`
	Snapshot        PetState             `json:"snapshot"`
	Reward          *PetAwayReward       `json:"reward,omitempty"`
	Actions         []PetRuntimeAction   `json:"actions"`
	ExecutedActions []PetAction          `json:"executedActions"`
	Error           string               `json:"error,omitempty"`
}

// PetRuntimeEmitter 是可选的事件边界。运行时不依赖 Wails、main 或具体窗口，
// 主控若需要广播，只需适配这个接口即可。
type PetRuntimeEmitter interface {
	EmitPetRuntime(PetRuntimeResult)
}

type PetRuntimeEmitterFunc func(PetRuntimeResult)

func (f PetRuntimeEmitterFunc) EmitPetRuntime(result PetRuntimeResult) {
	if f != nil {
		f(result)
	}
}

// PetRuntimeOptions 只注入运行时依赖和调度策略，不改变 PetService 的持久化契约。
// Clock 返回的时间必须能转换为正的 Unix 毫秒；Random 的语义与 Math.random 一致。
type PetRuntimeOptions struct {
	Clock    func() time.Time
	Random   func() float64
	Cooldown time.Duration
	Emitter  PetRuntimeEmitter
}

// PetRuntime 是单只宠物的自动照料协调器。它是被动驱动对象：调用方负责决定
// 何时调用 Tick/RunOnce，因此不会偷偷启动第二个 ticker 或后台 goroutine。
type PetRuntime struct {
	service  *PetService
	clock    func() time.Time
	random   func() float64
	cooldown time.Duration
	emitter  PetRuntimeEmitter

	mu sync.Mutex

	// 该状态只存在于当前进程，用于防止同一轮重复驱动，不污染持久化快照。
	lastAttemptAt  int64
	hasLastAttempt bool
}

// NewPetRuntime 创建一个由调用方手动驱动的自动照料运行时。
func NewPetRuntime(service *PetService, options ...PetRuntimeOptions) *PetRuntime {
	runtime := &PetRuntime{
		service:  service,
		clock:    time.Now,
		random:   rand.Float64,
		cooldown: PetRuntimeDefaultCooldown,
	}
	for _, option := range options {
		if option.Clock != nil {
			runtime.clock = option.Clock
		}
		if option.Random != nil {
			runtime.random = option.Random
		}
		if option.Cooldown > 0 {
			runtime.cooldown = option.Cooldown
		}
		if option.Emitter != nil {
			runtime.emitter = option.Emitter
		}
	}
	return runtime
}

// NewPetRuntimeWithClock 是测试和主控接入时的便捷构造入口。
func NewPetRuntimeWithClock(service *PetService, clock func() time.Time) *PetRuntime {
	return NewPetRuntime(service, PetRuntimeOptions{Clock: clock})
}

// Tick 与 RunOnce 同义，保留 Tick 名称方便主控把它接到已有状态心跳。
// 这里没有 ticker；调度生命周期完全由调用方拥有。
func (r *PetRuntime) Tick(at ...time.Time) (PetRuntimeResult, error) {
	return r.RunOnce(at...)
}

// RunOnce 推进一次宠物状态并最多成功执行一个自动照料动作。
// 可选 at 主要用于主控显式传入时间或测试；未传时使用注入的 Clock。
func (r *PetRuntime) RunOnce(at ...time.Time) (PetRuntimeResult, error) {
	if r == nil {
		return PetRuntimeResult{}, errors.New("宠物运行时为空")
	}

	nowMs, err := r.resolveNow(at)
	if err != nil {
		result := r.newResult(0)
		r.setRuntimeError(&result, err)
		r.emit(result)
		return result, err
	}
	return r.runOnceAtMillis(nowMs)
}

// RunOnceAtMillis 为不需要构造 time.Time 的调度器提供显式毫秒入口。
func (r *PetRuntime) RunOnceAtMillis(nowMs int64) (PetRuntimeResult, error) {
	if r == nil {
		return PetRuntimeResult{}, errors.New("宠物运行时为空")
	}
	if nowMs <= 0 {
		result := r.newResult(nowMs)
		err := errors.New("宠物运行时的时间必须是正的 Unix 毫秒")
		r.setRuntimeError(&result, err)
		r.emit(result)
		return result, err
	}
	return r.runOnceAtMillis(nowMs)
}

func (r *PetRuntime) resolveNow(at []time.Time) (int64, error) {
	if len(at) > 1 {
		return 0, errors.New("宠物运行时一次只能接收一个时间")
	}
	now := time.Time{}
	if len(at) == 1 {
		now = at[0]
	} else if r.clock != nil {
		now = r.clock()
	}
	if now.IsZero() || now.UnixMilli() <= 0 {
		return 0, errors.New("宠物运行时的时钟必须返回正的时间")
	}
	return now.UnixMilli(), nil
}

func (r *PetRuntime) runOnceAtMillis(nowMs int64) (PetRuntimeResult, error) {
	r.mu.Lock()
	result, err := r.runOnceLocked(nowMs)
	r.mu.Unlock()

	r.emit(result)
	return result, err
}

func (r *PetRuntime) runOnceLocked(nowMs int64) (PetRuntimeResult, error) {
	result := r.newResult(nowMs)
	if r.service == nil {
		err := errors.New("宠物运行时未配置 PetService")
		r.setRuntimeError(&result, err)
		return result, err
	}

	// 先结算离线衰减和睡眠边界，保证自动照料看到的是本轮真实状态。
	state, err := r.service.Tick(nowMs)
	if err != nil {
		r.setRuntimeError(&result, err)
		return result, err
	}
	result.Snapshot = state

	// 源项目每次进入桌宠逻辑都会先兑现已完成的 away task；有奖励时本轮
	// 只报告奖励，不继续扣费照料，避免一次心跳同时产生两组反馈。
	reward, err := r.service.ResolveAwayTask(nowMs)
	if err != nil {
		r.setRuntimeError(&result, err)
		return result, err
	}
	if reward != nil {
		result.Reward = reward
		if err := r.refreshSnapshot(&result); err != nil {
			r.setRuntimeError(&result, err)
			return result, err
		}
		result.Status = PetRuntimeStatusRewarded
		result.Skipped = true
		result.SkipReason = PetRuntimeSkipAwayReward
		return result, nil
	}

	config, err := r.service.GetCareConfig()
	if err != nil {
		r.setRuntimeError(&result, err)
		return result, err
	}
	result.Config = config

	if !config.AutoCareEnabled {
		result.Status = PetRuntimeStatusDisabled
		result.Skipped = true
		result.SkipReason = PetRuntimeSkipDisabled
		return result, nil
	}
	if state.Sleeping {
		result.Status = PetRuntimeStatusSleeping
		result.Skipped = true
		result.SkipReason = PetRuntimeSkipSleeping
		return result, nil
	}
	if state.AwayTask != nil {
		result.Status = PetRuntimeStatusAway
		result.Skipped = true
		result.SkipReason = PetRuntimeSkipAway
		return result, nil
	}

	actions := petRuntimeAutoCareActionOrder(state, config.AutoCareThreshold, r.random)
	if len(actions) == 0 {
		result.Status = PetRuntimeStatusNoNeed
		result.Skipped = true
		result.SkipReason = PetRuntimeSkipNoNeed
		return result, nil
	}

	if active, until := r.cooldownActive(nowMs); active {
		result.Status = PetRuntimeStatusCoolingDown
		result.Skipped = true
		result.SkipReason = PetRuntimeSkipCooldown
		result.CooldownUntil = until
		return result, nil
	}

	// 只要这一轮产生了候选动作就记录尝试时间，即使规则拒绝动作也不在
	// 同一调度窗口里反复扣查询/制造事件；到下一次冷却边界再重新尝试。
	r.lastAttemptAt = nowMs
	r.hasLastAttempt = true
	for _, action := range actions {
		actionResult, actionErr := r.executeAction(action)
		attempt := PetRuntimeAction{
			Action: action,
			OK:     actionResult.OK,
			Reason: actionResult.Reason,
		}
		if actionErr != nil {
			attempt.Error = actionErr.Error()
			result.Actions = append(result.Actions, attempt)
			result.Failed = true
			result.Status = PetRuntimeStatusError
			result.Error = actionErr.Error()
			return result, actionErr
		}
		result.Actions = append(result.Actions, attempt)
		if !actionResult.OK {
			continue
		}

		result.ExecutedActions = append(result.ExecutedActions, action)
		result.Status = PetRuntimeStatusExecuted
		if err := r.refreshSnapshot(&result); err != nil {
			r.setRuntimeError(&result, err)
			return result, err
		}
		return result, nil
	}

	result.Status = PetRuntimeStatusActionFailed
	result.Failed = true
	return result, nil
}

func (r *PetRuntime) executeAction(action PetAction) (PetActionResult, error) {
	switch action {
	case PetActionFeed:
		return r.service.Feed()
	case PetActionBathe:
		return r.service.Bathe()
	case PetActionSoak:
		return r.service.Soak()
	case PetActionPlay:
		return r.service.Play()
	default:
		return PetActionResult{}, fmt.Errorf("自动照料不支持动作 %q", action)
	}
}

func (r *PetRuntime) refreshSnapshot(result *PetRuntimeResult) error {
	state, err := r.service.GetState()
	if err != nil {
		return err
	}
	result.Snapshot = state
	return nil
}

func (r *PetRuntime) cooldownActive(nowMs int64) (bool, int64) {
	if !r.hasLastAttempt {
		return false, 0
	}
	if nowMs < r.lastAttemptAt {
		// 时钟回拨时丢弃进程内的节流标记，避免宠物被永久卡在冷却中。
		r.hasLastAttempt = false
		r.lastAttemptAt = 0
		return false, 0
	}

	elapsed := nowMs - r.lastAttemptAt
	cooldownMs := r.cooldown.Milliseconds()
	if elapsed == 0 || (cooldownMs > 0 && elapsed < cooldownMs) {
		until := r.lastAttemptAt + cooldownMs
		if cooldownMs == 0 {
			until = r.lastAttemptAt
		}
		return true, until
	}
	return false, 0
}

func (r *PetRuntime) newResult(nowMs int64) PetRuntimeResult {
	result := PetRuntimeResult{
		At:              nowMs,
		Actions:         make([]PetRuntimeAction, 0),
		ExecutedActions: make([]PetAction, 0),
	}
	if r != nil && r.service != nil {
		result.PetID = strings.TrimSpace(r.service.petID)
	}
	return result
}

func (r *PetRuntime) setRuntimeError(result *PetRuntimeResult, err error) {
	if result == nil || err == nil {
		return
	}
	result.Status = PetRuntimeStatusError
	result.Failed = true
	result.Error = err.Error()
}

func (r *PetRuntime) emit(result PetRuntimeResult) {
	if r == nil || r.emitter == nil {
		return
	}
	r.emitter.EmitPetRuntime(result)
}

type petRuntimeNeed struct {
	value   float64
	actions []PetAction
}

// petRuntimeAutoCareActionOrder 对齐源项目 getAutoCareActionOrder：先处理最低
// 需求，同一属性的等效动作随机排列，soak 只尝试一次，规则失败后再继续下一个动作。
func petRuntimeAutoCareActionOrder(
	state PetState,
	threshold int,
	random func() float64,
) []PetAction {
	needs := []petRuntimeNeed{
		{value: state.Hunger, actions: []PetAction{PetActionFeed}},
		{value: state.Cleanliness, actions: []PetAction{PetActionBathe, PetActionSoak}},
		{value: state.Mood, actions: []PetAction{PetActionPlay, PetActionSoak}},
	}
	sort.SliceStable(needs, func(left, right int) bool {
		return needs[left].value < needs[right].value
	})

	seen := make(map[PetAction]struct{}, len(needs))
	order := make([]PetAction, 0, len(needs))
	for _, need := range needs {
		if need.value >= float64(threshold) {
			continue
		}
		for _, action := range shufflePetRuntimeActions(need.actions, random) {
			if _, exists := seen[action]; exists {
				continue
			}
			seen[action] = struct{}{}
			order = append(order, action)
		}
	}
	return order
}

func shufflePetRuntimeActions(items []PetAction, random func() float64) []PetAction {
	result := append([]PetAction(nil), items...)
	if random == nil {
		random = rand.Float64
	}
	for index := len(result) - 1; index > 0; index-- {
		target := int(random() * float64(index+1))
		if target < 0 {
			target = 0
		}
		if target > index {
			target = index
		}
		result[index], result[target] = result[target], result[index]
	}
	return result
}
