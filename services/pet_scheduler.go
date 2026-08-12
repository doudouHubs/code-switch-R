package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf16"
)

const (
	PetSchedulerDefaultTimeZone    = "UTC"
	PetSchedulerDefaultPollLimit   = 32
	PetSchedulerMaxPollLimit       = 128
	PetSchedulerDefaultMaxAttempts = 3
	PetSchedulerMaxAttempts        = 10
	PetSchedulerDefaultLease       = time.Minute
	PetSchedulerMinLease           = time.Second
	PetSchedulerMaxLease           = 24 * time.Hour
	PetSchedulerDefaultRetryDelay  = time.Minute
	PetSchedulerMaxRetryDelay      = 24 * time.Hour
	PetSchedulerMaxFuture          = 10 * 365 * 24 * time.Hour
	PetSchedulerMaxIDLength        = 100

	petSchedulerCronSearchYears = 8
)

var (
	ErrPetSchedulerStoreMissing        = errors.New("pet scheduler job store is not configured")
	ErrPetSchedulerInvalidConfig       = errors.New("pet scheduler configuration is invalid")
	ErrPetSchedulerInvalidPlanID       = errors.New("pet scheduler plan id is invalid")
	ErrPetSchedulerInvalidStepID       = errors.New("pet scheduler step id is invalid")
	ErrPetSchedulerScheduleInPast      = errors.New("pet scheduler schedule is in the past")
	ErrPetSchedulerScheduleTooFar      = errors.New("pet scheduler schedule is too far in the future")
	ErrPetSchedulerExpirationInvalid   = errors.New("pet scheduler expiration is invalid")
	ErrPetSchedulerUnsupportedTimeZone = errors.New("pet scheduler timezone is unsupported")
	ErrPetSchedulerUnsupportedCron     = errors.New("pet scheduler cron expression is unsupported")
	ErrPetSchedulerCronNoOccurrence    = errors.New("pet scheduler cron expression has no occurrence in search window")
	ErrPetSchedulerEmitterMissing      = errors.New("pet scheduler reminder emitter is not configured")
)

// Clock 只提供当前时间，所有时间计算和测试都从这里取值，避免调度器偷偷读取
// 系统时钟导致 delay、at、cron 在测试和恢复场景中出现不可复现的偏差。
type Clock interface {
	Now() time.Time
}

// Emitter 是 reminder 的观察边界。action 不在这里直接执行，而是由 RunDue
// 返回已校验 payload，再由上层按 PetService 的动作入口完成真正的状态变更。
type Emitter interface {
	Emit(context.Context, PetSchedulerEvent) error
}

// JobStore 是调度器与持久化实现之间的最小契约。SQLite、cron 表或其他持久化
// 实现只需要负责原子入队、到期查询、租约抢占和状态推进，不需要把数据库对象
// 暴露给本模块。接口没有内存默认实现，测试内存 store 也不代表生产持久化。
type JobStore interface {
	Enqueue(context.Context, []PetScheduledJob) error
	Due(context.Context, time.Time, int) ([]PetScheduledJob, error)
	Claim(context.Context, string, string, time.Time, time.Time) (PetJobLease, bool, error)
	Complete(context.Context, PetJobLease, time.Time, *time.Time) error
	Fail(context.Context, PetJobLease, time.Time, *time.Time, *time.Time, string) error
}

type PetSchedulerEventType string

const PetSchedulerReminderEvent PetSchedulerEventType = "pet.reminder"

// PetSchedulerEvent 只承载 reminder；其中 Payload 仍然是经过
// ValidatePetAutomationPayload 归一化后的协议对象，Emitter 不需要再次猜测文本
// 或 kind。EventID 对同一个逻辑 occurrence 保持稳定，供跨进程 emitter 做去重。
type PetSchedulerEvent struct {
	Type    PetSchedulerEventType   `json:"type"`
	EventID string                  `json:"eventId"`
	JobID   string                  `json:"jobId"`
	PlanID  string                  `json:"planId"`
	StepID  string                  `json:"stepId"`
	Payload PetAutomationJobPayload `json:"payload"`
	FiredAt time.Time               `json:"firedAt"`
}

// PetScheduledJob 是可持久化的 job 定义和当前 occurrence 信息。DueAt 是逻辑
// occurrence 的时间，AvailableAt 仅用于失败重试；二者分开后，重试不会改写
// reminder 的幂等事件 ID。租约、attempt 和 terminal 状态由 JobStore 持有。
type PetScheduledJob struct {
	ID          string                  `json:"id"`
	JobType     string                  `json:"jobType"`
	PlanID      string                  `json:"planId"`
	StepID      string                  `json:"stepId"`
	Schedule    PetPlanSchedule         `json:"schedule"`
	Payload     PetAutomationJobPayload `json:"payload"`
	CreatedAt   time.Time               `json:"createdAt"`
	DueAt       time.Time               `json:"dueAt"`
	AvailableAt time.Time               `json:"availableAt"`
	ExpiresAt   *time.Time              `json:"expiresAt,omitempty"`
	MaxAttempts int                     `json:"maxAttempts"`
}

// PetJobLease 是 Claim 的结果。Attempt 从 1 开始，store 必须在原子 claim 中
// 增加它；Complete/Fail 必须校验 Token，防止旧 worker 在租约失效后覆盖新状态。
type PetJobLease struct {
	Job        PetScheduledJob `json:"job"`
	Token      string          `json:"token"`
	Attempt    int             `json:"attempt"`
	LeaseUntil time.Time       `json:"leaseUntil"`
}

type PetSchedulerConfig struct {
	TimeZone    string
	PollLimit   int
	MaxAttempts int
	Lease       time.Duration
	RetryDelay  time.Duration
}

type PetSchedulePlanOptions struct {
	PlanID      string
	TimeZone    string
	MaxAttempts int
	ExpiresAt   *time.Time
}

type PetSchedulePlanResult struct {
	Plan      PetPlanScript
	PlanID    string
	Jobs      []PetScheduledJob
	Immediate []PetScheduledJob
}

type PetRunDueResult struct {
	Actions          []PetAutomationJobPayload
	Claimed          int
	Completed        int
	RemindersEmitted int
	Retried          int
	Rescheduled      int
	Failed           int
	Errors           []PetJobRunError
}

type PetJobRunError struct {
	JobID string
	Err   error
}

func (e PetJobRunError) Error() string {
	if e.Err == nil {
		return e.JobID
	}
	if e.JobID == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("job %q: %v", e.JobID, e.Err)
}

type PetRunDueError struct {
	Errors []PetJobRunError
}

func (e *PetRunDueError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "pet scheduler run due failed"
	}
	parts := make([]string, 0, len(e.Errors))
	for _, runErr := range e.Errors {
		parts = append(parts, runErr.Error())
	}
	return "pet scheduler run due failed: " + strings.Join(parts, "; ")
}

func (e *PetRunDueError) Unwrap() error {
	if e == nil || len(e.Errors) == 0 {
		return nil
	}
	joined := make([]error, 0, len(e.Errors))
	for _, runErr := range e.Errors {
		joined = append(joined, runErr.Err)
	}
	return errors.Join(joined...)
}

type PetScheduler struct {
	store     JobStore
	emitter   Emitter
	clock     Clock
	config    PetSchedulerConfig
	configErr error
}

type systemPetSchedulerClock struct{}

func (systemPetSchedulerClock) Now() time.Time {
	return time.Now()
}

var petSchedulerSequence atomic.Uint64

// NewPetScheduler 不提供内存 store，也不把数据库作为默认依赖。clock 为空时只
// 使用系统时钟；emitter 可以为空以支持纯 action 计划，但执行 reminder 时会
// 明确报错并保留 lease，不会悄悄吞掉提醒。
func NewPetScheduler(store JobStore, emitter Emitter, clock Clock, configs ...PetSchedulerConfig) *PetScheduler {
	config := defaultPetSchedulerConfig()
	var configErr error
	if len(configs) > 1 {
		configErr = fmt.Errorf("%w: only one config is allowed", ErrPetSchedulerInvalidConfig)
	} else if len(configs) == 1 {
		config, configErr = normalizePetSchedulerConfig(configs[0])
	}
	if clock == nil {
		clock = systemPetSchedulerClock{}
	}
	return &PetScheduler{
		store:     store,
		emitter:   emitter,
		clock:     clock,
		config:    config,
		configErr: configErr,
	}
}

func defaultPetSchedulerConfig() PetSchedulerConfig {
	return PetSchedulerConfig{
		TimeZone:    PetSchedulerDefaultTimeZone,
		PollLimit:   PetSchedulerDefaultPollLimit,
		MaxAttempts: PetSchedulerDefaultMaxAttempts,
		Lease:       PetSchedulerDefaultLease,
		RetryDelay:  PetSchedulerDefaultRetryDelay,
	}
}

func normalizePetSchedulerConfig(config PetSchedulerConfig) (PetSchedulerConfig, error) {
	defaults := defaultPetSchedulerConfig()
	if strings.TrimSpace(config.TimeZone) == "" {
		config.TimeZone = defaults.TimeZone
	} else {
		config.TimeZone = strings.TrimSpace(config.TimeZone)
	}
	if _, err := time.LoadLocation(config.TimeZone); err != nil {
		return PetSchedulerConfig{}, fmt.Errorf("%w: timezone %q: %v", ErrPetSchedulerInvalidConfig, config.TimeZone, err)
	}
	if config.PollLimit == 0 {
		config.PollLimit = defaults.PollLimit
	}
	if config.PollLimit < 1 || config.PollLimit > PetSchedulerMaxPollLimit {
		return PetSchedulerConfig{}, fmt.Errorf("%w: poll limit must be between 1 and %d", ErrPetSchedulerInvalidConfig, PetSchedulerMaxPollLimit)
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = defaults.MaxAttempts
	}
	if config.MaxAttempts < 1 || config.MaxAttempts > PetSchedulerMaxAttempts {
		return PetSchedulerConfig{}, fmt.Errorf("%w: max attempts must be between 1 and %d", ErrPetSchedulerInvalidConfig, PetSchedulerMaxAttempts)
	}
	if config.Lease == 0 {
		config.Lease = defaults.Lease
	}
	if config.Lease < PetSchedulerMinLease || config.Lease > PetSchedulerMaxLease {
		return PetSchedulerConfig{}, fmt.Errorf("%w: lease must be between %s and %s", ErrPetSchedulerInvalidConfig, PetSchedulerMinLease, PetSchedulerMaxLease)
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = defaults.RetryDelay
	}
	if config.RetryDelay < time.Millisecond || config.RetryDelay > PetSchedulerMaxRetryDelay {
		return PetSchedulerConfig{}, fmt.Errorf("%w: retry delay must be between 1ms and %s", ErrPetSchedulerInvalidConfig, PetSchedulerMaxRetryDelay)
	}
	return config, nil
}

func (s *PetScheduler) validate() error {
	if s == nil {
		return ErrPetSchedulerInvalidConfig
	}
	if s.store == nil {
		return ErrPetSchedulerStoreMissing
	}
	if s.configErr != nil {
		return s.configErr
	}
	return nil
}

func (s *PetScheduler) now() (time.Time, error) {
	if err := s.validate(); err != nil {
		return time.Time{}, err
	}
	now := s.clock.Now().Round(0)
	if now.IsZero() || now.UnixMilli() <= 0 {
		return time.Time{}, fmt.Errorf("%w: clock returned an invalid time", ErrPetSchedulerInvalidConfig)
	}
	return now, nil
}

// SchedulePlan 先复用既有协议校验，再将每个 step 变成完整 job，最后一次性
// Enqueue。批量入队是为了避免多步骤计划只落下一半；持久化是否真的写入磁盘
// 由 JobStore 实现决定，调度器不会把结果伪装成“已持久化”。
func (s *PetScheduler) SchedulePlan(ctx context.Context, rawPlan any, options PetSchedulePlanOptions) (PetSchedulePlanResult, error) {
	plan, err := ValidatePetPlanScript(rawPlan)
	if err != nil {
		return PetSchedulePlanResult{}, err
	}
	if ctx == nil {
		return PetSchedulePlanResult{}, fmt.Errorf("%w: nil context", ErrPetSchedulerInvalidConfig)
	}
	now, err := s.now()
	if err != nil {
		return PetSchedulePlanResult{}, err
	}

	planID, err := normalizePetSchedulerPlanID(options.PlanID, now)
	if err != nil {
		return PetSchedulePlanResult{}, err
	}
	timeZone := strings.TrimSpace(options.TimeZone)
	if timeZone == "" {
		timeZone = s.config.TimeZone
	}
	if _, err := loadPetSchedulerLocation(timeZone); err != nil {
		return PetSchedulePlanResult{}, err
	}
	maxAttempts := options.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = s.config.MaxAttempts
	}
	if maxAttempts < 1 || maxAttempts > PetSchedulerMaxAttempts {
		return PetSchedulePlanResult{}, fmt.Errorf("%w: max attempts must be between 1 and %d", ErrPetSchedulerInvalidConfig, PetSchedulerMaxAttempts)
	}

	var expiresAt *time.Time
	if options.ExpiresAt != nil {
		expires := options.ExpiresAt.Round(0)
		if !expires.After(now) || expires.After(now.Add(PetSchedulerMaxFuture)) {
			return PetSchedulePlanResult{}, fmt.Errorf("%w: expiration must be after now and within %s", ErrPetSchedulerExpirationInvalid, PetSchedulerMaxFuture)
		}
		expires = expires.UTC()
		expiresAt = &expires
	}

	jobs := make([]PetScheduledJob, 0, len(plan.Steps))
	immediate := make([]PetScheduledJob, 0)
	createdAt := float64(now.UnixMilli())
	for index, step := range plan.Steps {
		stepID := fmt.Sprintf("%s-%d", planID, index+1)
		if err := validatePetSchedulerID(stepID, ErrPetSchedulerInvalidStepID); err != nil {
			return PetSchedulePlanResult{}, err
		}
		schedule, dueAt, err := s.normalizeStepSchedule(step.Schedule, now, timeZone)
		if err != nil {
			return PetSchedulePlanResult{}, fmt.Errorf("step %d: %w", index+1, err)
		}
		if dueAt.Before(now) {
			return PetSchedulePlanResult{}, fmt.Errorf("step %d: %w", index+1, ErrPetSchedulerScheduleInPast)
		}
		if dueAt.After(now.Add(PetSchedulerMaxFuture)) {
			return PetSchedulePlanResult{}, fmt.Errorf("step %d: %w", index+1, ErrPetSchedulerScheduleTooFar)
		}
		if expiresAt != nil && !dueAt.Before(*expiresAt) {
			return PetSchedulePlanResult{}, fmt.Errorf("step %d: %w", index+1, ErrPetSchedulerExpirationInvalid)
		}

		payload := PetAutomationJobPayload{
			Version:   PetPlanVersion,
			PlanID:    planID,
			StepID:    stepID,
			Kind:      step.Kind,
			CreatedAt: createdAt,
		}
		if step.Kind == PetPlanActionStep {
			payload.Action = step.Action
			payload.Label = step.Label
		} else {
			payload.Text = step.Text
		}
		payload, err = ValidatePetAutomationPayload(payload)
		if err != nil {
			return PetSchedulePlanResult{}, fmt.Errorf("step %d payload: %w", index+1, err)
		}

		job := PetScheduledJob{
			ID:          stepID,
			JobType:     PetAutomationJobType,
			PlanID:      planID,
			StepID:      stepID,
			Schedule:    schedule,
			Payload:     payload,
			CreatedAt:   now.UTC(),
			DueAt:       dueAt.UTC(),
			AvailableAt: dueAt.UTC(),
			MaxAttempts: maxAttempts,
		}
		if expiresAt != nil {
			expires := *expiresAt
			job.ExpiresAt = &expires
		}
		jobs = append(jobs, job)
		if schedule.Kind == PetPlanScheduleNow {
			immediate = append(immediate, job)
		}
	}

	if err := s.store.Enqueue(ctx, jobs); err != nil {
		return PetSchedulePlanResult{}, fmt.Errorf("enqueue pet plan %q: %w", planID, err)
	}
	return PetSchedulePlanResult{
		Plan:      plan,
		PlanID:    planID,
		Jobs:      jobs,
		Immediate: immediate,
	}, nil
}

// Poll 只返回已经拿到 lease 的 job。重复 Poll 不会再次拿到仍处于有效 lease
// 的同一 job；lease 过期后的再次 claim 是故障恢复语义，真正的 action 消费方仍
// 应以 planId/stepId 建立幂等键，不能把进程级“只执行一次”当成分布式保证。
func (s *PetScheduler) Poll(ctx context.Context, limit int) ([]PetJobLease, error) {
	now, err := s.now()
	if err != nil {
		return nil, err
	}
	return s.pollAt(ctx, now, limit)
}

func (s *PetScheduler) pollAt(ctx context.Context, now time.Time, limit int) ([]PetJobLease, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrPetSchedulerInvalidConfig)
	}
	if limit == 0 {
		limit = s.config.PollLimit
	}
	if limit < 1 || limit > PetSchedulerMaxPollLimit {
		return nil, fmt.Errorf("%w: poll limit must be between 1 and %d", ErrPetSchedulerInvalidConfig, PetSchedulerMaxPollLimit)
	}
	jobs, err := s.store.Due(ctx, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list due pet jobs: %w", err)
	}
	if len(jobs) > limit {
		return nil, fmt.Errorf("%w: store returned %d jobs for limit %d", ErrPetSchedulerInvalidConfig, len(jobs), limit)
	}

	leases := make([]PetJobLease, 0, len(jobs))
	seen := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		if job.ID == "" {
			return nil, fmt.Errorf("%w: store returned an empty job id", ErrPetSchedulerInvalidConfig)
		}
		if _, exists := seen[job.ID]; exists {
			continue
		}
		seen[job.ID] = struct{}{}
		availableAt := job.AvailableAt
		if availableAt.IsZero() {
			availableAt = job.DueAt
		}
		if availableAt.After(now) || (job.ExpiresAt != nil && !now.Before(*job.ExpiresAt)) {
			continue
		}
		sequence := petSchedulerSequence.Add(1)
		token := fmt.Sprintf("pet-lease-%d-%d", now.UnixNano(), sequence)
		lease, claimed, err := s.store.Claim(ctx, job.ID, token, now, now.Add(s.config.Lease))
		if err != nil {
			return leases, fmt.Errorf("claim pet job %q: %w", job.ID, err)
		}
		if !claimed {
			continue
		}
		if lease.Token == "" {
			lease.Token = token
		}
		if lease.Job.ID == "" {
			lease.Job = job
		}
		if lease.Attempt < 1 {
			return leases, fmt.Errorf("%w: store returned invalid attempt for job %q", ErrPetSchedulerInvalidConfig, job.ID)
		}
		leases = append(leases, lease)
	}
	return leases, nil
}

// RunDue 处理一个 poll 批次。action 只在 store 完成确认后进入 Actions，避免
// 调用方拿到一个实际上没有成功推进状态的 payload；reminder 先发事件再完成
// occurrence，若 emitter 失败则按 attempt/expiry 策略重试。
func (s *PetScheduler) RunDue(ctx context.Context, limit int) (PetRunDueResult, error) {
	var result PetRunDueResult
	now, err := s.now()
	if err != nil {
		return result, err
	}
	leases, err := s.pollAt(ctx, now, limit)
	if err != nil {
		return result, err
	}
	result.Claimed = len(leases)
	for _, lease := range leases {
		if err := s.runLease(ctx, lease, now, &result); err != nil {
			return result, err
		}
	}
	if len(result.Errors) > 0 {
		return result, &PetRunDueError{Errors: result.Errors}
	}
	return result, nil
}

func (s *PetScheduler) runLease(ctx context.Context, lease PetJobLease, now time.Time, result *PetRunDueResult) error {
	payload, err := ValidatePetAutomationPayload(lease.Job.Payload)
	if err != nil {
		if failErr := s.failTerminal(ctx, lease, now, err, result); failErr != nil {
			return failErr
		}
		result.Errors = append(result.Errors, PetJobRunError{JobID: lease.Job.ID, Err: err})
		return nil
	}

	nextDue, err := s.nextDueAfter(lease.Job, now)
	if err != nil {
		if failErr := s.failTerminal(ctx, lease, now, err, result); failErr != nil {
			return failErr
		}
		result.Errors = append(result.Errors, PetJobRunError{JobID: lease.Job.ID, Err: err})
		return nil
	}
	if nextDue != nil && lease.Job.ExpiresAt != nil && !nextDue.Before(*lease.Job.ExpiresAt) {
		nextDue = nil
	}

	if payload.Kind == PetPlanActionStep {
		if err := s.store.Complete(ctx, lease, now, nextDue); err != nil {
			return fmt.Errorf("complete pet action job %q: %w", lease.Job.ID, err)
		}
		result.Actions = append(result.Actions, payload)
		result.Completed++
		return nil
	}

	if s.emitter == nil {
		err := ErrPetSchedulerEmitterMissing
		result.Errors = append(result.Errors, PetJobRunError{JobID: lease.Job.ID, Err: err})
		return nil
	}
	event := PetSchedulerEvent{
		Type:    PetSchedulerReminderEvent,
		EventID: petSchedulerOccurrenceID(lease.Job),
		JobID:   lease.Job.ID,
		PlanID:  payload.PlanID,
		StepID:  payload.StepID,
		Payload: payload,
		FiredAt: now.UTC(),
	}
	if err := s.emitter.Emit(ctx, event); err != nil {
		if failErr := s.failRetryable(ctx, lease, now, err, result); failErr != nil {
			return failErr
		}
		result.Errors = append(result.Errors, PetJobRunError{JobID: lease.Job.ID, Err: err})
		return nil
	}
	if err := s.store.Complete(ctx, lease, now, nextDue); err != nil {
		return fmt.Errorf("complete pet reminder job %q: %w", lease.Job.ID, err)
	}
	result.Completed++
	result.RemindersEmitted++
	return nil
}

func (s *PetScheduler) failTerminal(ctx context.Context, lease PetJobLease, now time.Time, cause error, result *PetRunDueResult) error {
	if err := s.store.Fail(ctx, lease, now, nil, nil, cause.Error()); err != nil {
		return fmt.Errorf("fail pet job %q: %w", lease.Job.ID, err)
	}
	result.Failed++
	return nil
}

func (s *PetScheduler) failRetryable(ctx context.Context, lease PetJobLease, now time.Time, cause error, result *PetRunDueResult) error {
	maxAttempts := lease.Job.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = s.config.MaxAttempts
	}
	if lease.Attempt >= maxAttempts {
		nextDue, nextErr := s.nextDueAfter(lease.Job, now)
		if nextErr != nil {
			nextDue = nil
		}
		if nextDue != nil && lease.Job.ExpiresAt != nil && !nextDue.Before(*lease.Job.ExpiresAt) {
			nextDue = nil
		}
		if err := s.store.Fail(ctx, lease, now, nil, nextDue, cause.Error()); err != nil {
			return fmt.Errorf("stop retrying pet job %q: %w", lease.Job.ID, err)
		}
		if nextDue != nil {
			result.Rescheduled++
		} else {
			result.Failed++
		}
		return nil
	}

	retryAt := now.Add(petSchedulerRetryDelay(s.config.RetryDelay, lease.Attempt))
	if lease.Job.ExpiresAt != nil && !retryAt.Before(*lease.Job.ExpiresAt) {
		if err := s.store.Fail(ctx, lease, now, nil, nil, cause.Error()); err != nil {
			return fmt.Errorf("expire pet job %q after failed retry: %w", lease.Job.ID, err)
		}
		result.Failed++
		return nil
	}
	if err := s.store.Fail(ctx, lease, now, &retryAt, nil, cause.Error()); err != nil {
		return fmt.Errorf("retry pet job %q: %w", lease.Job.ID, err)
	}
	result.Retried++
	return nil
}

func petSchedulerRetryDelay(base time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for index := 1; index < attempt; index++ {
		if delay > PetSchedulerMaxRetryDelay/2 {
			return PetSchedulerMaxRetryDelay
		}
		delay *= 2
	}
	if delay > PetSchedulerMaxRetryDelay {
		return PetSchedulerMaxRetryDelay
	}
	return delay
}

func (s *PetScheduler) normalizeStepSchedule(schedule *PetPlanSchedule, now time.Time, defaultTimeZone string) (PetPlanSchedule, time.Time, error) {
	if schedule == nil {
		return PetPlanSchedule{Kind: PetPlanScheduleNow}, now, nil
	}
	normalized := *schedule
	switch schedule.Kind {
	case PetPlanScheduleNow:
		return normalized, now, nil
	case PetPlanScheduleDelay:
		delayMS := math.Round(schedule.DelaySeconds * 1000)
		if delayMS <= 0 || delayMS > float64(math.MaxInt64/int64(time.Millisecond)) {
			return PetPlanSchedule{}, time.Time{}, fmt.Errorf("invalid delay duration")
		}
		due := now.Add(time.Duration(delayMS) * time.Millisecond)
		return normalized, due, nil
	case PetPlanScheduleAt:
		timeZone := strings.TrimSpace(schedule.TZ)
		if timeZone == "" {
			timeZone = defaultTimeZone
		}
		loc, err := loadPetSchedulerLocation(timeZone)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, err
		}
		due, err := parsePetSchedulerAt(schedule.At, loc)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, err
		}
		normalized.TZ = timeZone
		return normalized, due, nil
	case PetPlanScheduleEvery:
		if schedule.EveryMS < PetPlanMinIntervalMS || schedule.EveryMS > PetPlanMaxIntervalMS {
			return PetPlanSchedule{}, time.Time{}, fmt.Errorf("every interval is outside validation bounds")
		}
		due := now.Add(time.Duration(schedule.EveryMS) * time.Millisecond)
		return normalized, due, nil
	case PetPlanScheduleCron:
		timeZone := strings.TrimSpace(schedule.TZ)
		if timeZone == "" {
			timeZone = defaultTimeZone
		}
		loc, err := loadPetSchedulerLocation(timeZone)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, err
		}
		parsed, err := parsePetSchedulerCron(schedule.Expr)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, err
		}
		due, err := parsed.next(now, loc)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, err
		}
		normalized.Expr = strings.TrimSpace(schedule.Expr)
		normalized.TZ = timeZone
		return normalized, due, nil
	default:
		return PetPlanSchedule{}, time.Time{}, fmt.Errorf("unsupported schedule kind %q", schedule.Kind)
	}
}

func (s *PetScheduler) nextDueAfter(job PetScheduledJob, after time.Time) (*time.Time, error) {
	switch job.Schedule.Kind {
	case PetPlanScheduleEvery:
		next := after.Add(time.Duration(job.Schedule.EveryMS) * time.Millisecond).UTC()
		return &next, nil
	case PetPlanScheduleCron:
		timeZone := strings.TrimSpace(job.Schedule.TZ)
		if timeZone == "" {
			timeZone = s.config.TimeZone
		}
		loc, err := loadPetSchedulerLocation(timeZone)
		if err != nil {
			return nil, err
		}
		parsed, err := parsePetSchedulerCron(job.Schedule.Expr)
		if err != nil {
			return nil, err
		}
		next, err := parsed.next(after, loc)
		if err != nil {
			return nil, err
		}
		next = next.UTC()
		return &next, nil
	default:
		return nil, nil
	}
}

func normalizePetSchedulerPlanID(raw string, now time.Time) (string, error) {
	planID := strings.TrimSpace(raw)
	if planID == "" {
		planID = fmt.Sprintf("pet-plan-%d-%d", now.UnixMilli(), petSchedulerSequence.Add(1))
	}
	if err := validatePetSchedulerID(planID, ErrPetSchedulerInvalidPlanID); err != nil {
		return "", err
	}
	return planID, nil
}

func validatePetSchedulerID(value string, sentinel error) error {
	if value == "" || petSchedulerUTF16Length(value) > PetSchedulerMaxIDLength {
		return fmt.Errorf("%w: id must be non-empty and no longer than %d UTF-16 units", sentinel, PetSchedulerMaxIDLength)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("%w: id contains a control character", sentinel)
		}
	}
	return nil
}

func petSchedulerUTF16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func loadPetSchedulerLocation(timeZone string) (*time.Location, error) {
	loc, err := time.LoadLocation(strings.TrimSpace(timeZone))
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrPetSchedulerUnsupportedTimeZone, timeZone, err)
	}
	return loc, nil
}

func parsePetSchedulerAt(raw json.RawMessage, loc *time.Location) (time.Time, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return time.Time{}, fmt.Errorf("invalid schedule.at: %w", err)
	}
	switch typed := value.(type) {
	case json.Number:
		milliseconds, err := strconv.ParseFloat(string(typed), 64)
		if err != nil || math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) || milliseconds <= 0 {
			return time.Time{}, fmt.Errorf("invalid numeric schedule.at")
		}
		milliseconds = math.Round(milliseconds)
		if milliseconds > float64(math.MaxInt64) {
			return time.Time{}, fmt.Errorf("numeric schedule.at overflows time")
		}
		return time.UnixMilli(int64(milliseconds)).UTC(), nil
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return time.Time{}, fmt.Errorf("schedule.at is empty")
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed, nil
		}
		for _, layout := range []string{
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04:05",
			"2006-01-02T15:04",
			"2006-01-02",
		} {
			if parsed, err := time.ParseInLocation(layout, value, loc); err == nil {
				return parsed, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("invalid schedule.at")
}

func petSchedulerOccurrenceID(job PetScheduledJob) string {
	dueAt := job.DueAt
	if dueAt.IsZero() {
		dueAt = job.AvailableAt
	}
	return fmt.Sprintf("%s:%d", job.ID, dueAt.UTC().UnixNano())
}

type petCronField struct {
	values   []bool
	min      int
	max      int
	wildcard bool
}

type petCronExpression struct {
	seconds     petCronField
	minutes     petCronField
	hours       petCronField
	daysOfMonth petCronField
	months      petCronField
	daysOfWeek  petCronField
}

func parsePetSchedulerCron(raw string) (petCronExpression, error) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) != 5 && len(fields) != 6 {
		return petCronExpression{}, fmt.Errorf("%w: only 5 or 6 numeric fields are supported", ErrPetSchedulerUnsupportedCron)
	}
	index := 0
	seconds := petCronField{min: 0, max: 59, values: make([]bool, 60)}
	if len(fields) == 5 {
		seconds.values[0] = true
	} else {
		var err error
		seconds, err = parsePetCronField(fields[index], 0, 59)
		if err != nil {
			return petCronExpression{}, fmt.Errorf("%w: seconds: %v", ErrPetSchedulerUnsupportedCron, err)
		}
		index++
	}
	minutes, err := parsePetCronField(fields[index], 0, 59)
	if err != nil {
		return petCronExpression{}, fmt.Errorf("%w: minutes: %v", ErrPetSchedulerUnsupportedCron, err)
	}
	index++
	hours, err := parsePetCronField(fields[index], 0, 23)
	if err != nil {
		return petCronExpression{}, fmt.Errorf("%w: hours: %v", ErrPetSchedulerUnsupportedCron, err)
	}
	index++
	daysOfMonth, err := parsePetCronField(fields[index], 1, 31)
	if err != nil {
		return petCronExpression{}, fmt.Errorf("%w: day-of-month: %v", ErrPetSchedulerUnsupportedCron, err)
	}
	index++
	months, err := parsePetCronField(fields[index], 1, 12)
	if err != nil {
		return petCronExpression{}, fmt.Errorf("%w: month: %v", ErrPetSchedulerUnsupportedCron, err)
	}
	index++
	daysOfWeek, err := parsePetCronField(fields[index], 0, 7)
	if err != nil {
		return petCronExpression{}, fmt.Errorf("%w: day-of-week: %v", ErrPetSchedulerUnsupportedCron, err)
	}
	// Cron 同时限制日期和星期时采用常见的 OR 语义；7 与 0 都代表周日。
	daysOfWeek.values[0] = daysOfWeek.values[0] || daysOfWeek.values[7]
	daysOfWeek.values[7] = daysOfWeek.values[0]
	return petCronExpression{
		seconds:     seconds,
		minutes:     minutes,
		hours:       hours,
		daysOfMonth: daysOfMonth,
		months:      months,
		daysOfWeek:  daysOfWeek,
	}, nil
}

func parsePetCronField(raw string, min, max int) (petCronField, error) {
	field := petCronField{min: min, max: max, values: make([]bool, max-min+1)}
	parts := strings.Split(raw, ",")
	if raw == "" || len(parts) == 0 {
		return petCronField{}, fmt.Errorf("field is empty")
	}
	for _, part := range parts {
		if part == "" {
			return petCronField{}, fmt.Errorf("empty list item")
		}
		base := part
		step := 1
		if strings.Count(part, "/") > 1 {
			return petCronField{}, fmt.Errorf("more than one step separator")
		}
		if slash := strings.IndexByte(part, '/'); slash >= 0 {
			base = part[:slash]
			parsedStep, err := strconv.Atoi(part[slash+1:])
			if err != nil || parsedStep < 1 {
				return petCronField{}, fmt.Errorf("step must be a positive integer")
			}
			step = parsedStep
		}
		if base == "*" {
			field.wildcard = true
		} else if strings.Contains(base, "-") {
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 || bounds[0] == "" || bounds[1] == "" {
				return petCronField{}, fmt.Errorf("range is malformed")
			}
			start, errStart := strconv.Atoi(bounds[0])
			end, errEnd := strconv.Atoi(bounds[1])
			if errStart != nil || errEnd != nil || start < min || end > max || start > end {
				return petCronField{}, fmt.Errorf("range is outside %d-%d", min, max)
			}
			for value := start; value <= end; value += step {
				field.values[value-min] = true
			}
			continue
		} else {
			value, err := strconv.Atoi(base)
			if err != nil || value < min || value > max {
				return petCronField{}, fmt.Errorf("value is outside %d-%d", min, max)
			}
			field.values[value-min] = true
			continue
		}
		for value := min; value <= max; value += step {
			field.values[value-min] = true
		}
	}
	return field, nil
}

func (cron petCronExpression) next(after time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		return time.Time{}, fmt.Errorf("%w: nil location", ErrPetSchedulerUnsupportedTimeZone)
	}
	localAfter := after.In(loc).Round(0)
	candidate := localAfter.Truncate(time.Second).Add(time.Second)
	searchUntil := candidate.AddDate(petSchedulerCronSearchYears, 0, 0)
	for candidate.Before(searchUntil) {
		year, month, day := candidate.Date()
		monthNumber := int(month)
		if !cronFieldContains(cron.months, monthNumber) {
			if nextMonth, ok := cronNextValue(cron.months, monthNumber+1); ok {
				candidate = time.Date(year, time.Month(nextMonth), 1, 0, 0, 0, 0, loc)
			} else {
				firstMonth, _ := cronNextValue(cron.months, cron.months.min)
				candidate = time.Date(year+1, time.Month(firstMonth), 1, 0, 0, 0, 0, loc)
			}
			continue
		}
		if !cronDayMatches(cron, candidate) {
			candidate = time.Date(year, month, day+1, 0, 0, 0, 0, loc)
			continue
		}

		hour, minute, second := candidate.Clock()
		if nextHour, ok := cronNextValue(cron.hours, hour); !ok {
			candidate = time.Date(year, month, day+1, 0, 0, 0, 0, loc)
			continue
		} else if nextHour != hour {
			candidate = time.Date(year, month, day, nextHour, 0, 0, 0, loc)
			continue
		}
		if nextMinute, ok := cronNextValue(cron.minutes, minute); !ok {
			candidate = time.Date(year, month, day, hour+1, 0, 0, 0, loc)
			continue
		} else if nextMinute != minute {
			candidate = time.Date(year, month, day, hour, nextMinute, 0, 0, loc)
			continue
		}
		if nextSecond, ok := cronNextValue(cron.seconds, second); !ok {
			candidate = time.Date(year, month, day, hour, minute+1, 0, 0, loc)
			continue
		} else if nextSecond != second {
			candidate = time.Date(year, month, day, hour, minute, nextSecond, 0, loc)
			continue
		}

		if candidate.After(localAfter) && cronMatchesTime(cron, candidate) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Second)
	}
	return time.Time{}, fmt.Errorf("%w: no match within %d years", ErrPetSchedulerCronNoOccurrence, petSchedulerCronSearchYears)
}

func cronFieldContains(field petCronField, value int) bool {
	return value >= field.min && value <= field.max && field.values[value-field.min]
}

func cronNextValue(field petCronField, from int) (int, bool) {
	for value := from; value <= field.max; value++ {
		if cronFieldContains(field, value) {
			return value, true
		}
	}
	return 0, false
}

func cronDayMatches(cron petCronExpression, at time.Time) bool {
	domMatch := cronFieldContains(cron.daysOfMonth, at.Day())
	dowMatch := cronFieldContains(cron.daysOfWeek, int(at.Weekday()))
	if cron.daysOfMonth.wildcard || cron.daysOfWeek.wildcard {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

func cronMatchesTime(cron petCronExpression, at time.Time) bool {
	return cronFieldContains(cron.seconds, at.Second()) &&
		cronFieldContains(cron.minutes, at.Minute()) &&
		cronFieldContains(cron.hours, at.Hour()) &&
		cronFieldContains(cron.months, int(at.Month())) &&
		cronDayMatches(cron, at)
}
