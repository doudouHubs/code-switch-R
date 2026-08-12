package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PetSchedulerAPIErrorCode 是调度桥接层对外的稳定错误分类。
// 计划协议本身的细粒度校验码会沿用 PetPlanValidationError.Code；调度器和
// 依赖错误则只通过这里的固定分类暴露，调用方不需要依赖底层实现文案。
type PetSchedulerAPIErrorCode string

const (
	PetSchedulerAPIErrorInvalidContext        PetSchedulerAPIErrorCode = "invalid_context"
	PetSchedulerAPIErrorInvalidRequest        PetSchedulerAPIErrorCode = "invalid_request"
	PetSchedulerAPIErrorDependencyUnavailable PetSchedulerAPIErrorCode = "dependency_unavailable"
	PetSchedulerAPIErrorContextCanceled       PetSchedulerAPIErrorCode = "context_canceled"
	PetSchedulerAPIErrorDeadlineExceeded      PetSchedulerAPIErrorCode = "context_deadline_exceeded"
	PetSchedulerAPIErrorScheduleFailed        PetSchedulerAPIErrorCode = "schedule_failed"
	PetSchedulerAPIErrorPlanRecordFailed      PetSchedulerAPIErrorCode = "plan_record_failed"
	PetSchedulerAPIErrorRunFailed             PetSchedulerAPIErrorCode = "run_failed"
	PetSchedulerAPIErrorJobFailed             PetSchedulerAPIErrorCode = "job_failed"
	PetSchedulerAPIErrorCancelUnavailable     PetSchedulerAPIErrorCode = "cancel_unavailable"
	PetSchedulerAPIErrorCancelFailed          PetSchedulerAPIErrorCode = "cancel_failed"
)

var (
	ErrPetSchedulerAPIInvalidContext    = errors.New("pet scheduler api context is invalid")
	ErrPetSchedulerAPIUnavailable       = errors.New("pet scheduler api scheduler is unavailable")
	ErrPetSchedulerAPICancelUnavailable = errors.New("pet scheduler api cancellation is unavailable")
	ErrPetSchedulerAPIInvalidRequest    = errors.New("pet scheduler api request is invalid")
)

// PetSchedulerAPIError 是 Wails 边界可序列化的错误投影。
// cause 只保留在 Go 错误链里，避免把 error 接口或数据库对象泄漏进 JSON；Details
// 用于保留 RunDue 的部分失败信息，调用方可以在一次 poll 中同时看到成功动作和失败 job。
type PetSchedulerAPIError struct {
	Code    PetSchedulerAPIErrorCode `json:"code"`
	Path    string                   `json:"path,omitempty"`
	JobID   string                   `json:"jobId,omitempty"`
	Message string                   `json:"message"`
	Details []PetSchedulerJobError   `json:"details,omitempty"`
	cause   error
}

func (e *PetSchedulerAPIError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Path != "" {
		return fmt.Sprintf("%s (%s): %s", e.Code, e.Path, e.Message)
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *PetSchedulerAPIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is 让调用方可以按稳定错误码判断，而不是把具体调度器或数据库错误写进分支。
func (e *PetSchedulerAPIError) Is(target error) bool {
	other, ok := target.(*PetSchedulerAPIError)
	return ok && e != nil && other != nil && e.Code == other.Code
}

// PetSchedulerAPIErrorCodeOf 返回错误链中的调度桥接错误码。
func PetSchedulerAPIErrorCodeOf(err error) PetSchedulerAPIErrorCode {
	var apiErr *PetSchedulerAPIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr.Code
	}
	return ""
}

// PetSchedulerBridgeScheduler 是桥接层依赖的最小 scheduler 端口。
// *PetScheduler 直接满足它；测试和后续运行时也可以注入等价实现，而不需要把
// JobStore 或数据库对象暴露给 Wails。计划校验仍复用现有共享函数，不在这里重写规则。
type PetSchedulerBridgeScheduler interface {
	SchedulePlan(context.Context, any, PetSchedulePlanOptions) (PetSchedulePlanResult, error)
	RunDue(context.Context, int) (PetRunDueResult, error)
}

// PetSchedulerPlanRecordPort 是计划记录的唯一持久化 owner。调度 job 和宠物计划
// 记录属于两张不同的表，桥接层只依赖这个窄接口，不直接持有 PetDAO 或 SQL。
type PetSchedulerPlanRecordPort interface {
	UpsertPlan(context.Context, PetPlanRecord) error
}

var _ PetSchedulerBridgeScheduler = (*PetScheduler)(nil)

// PetSchedulerCancelPort 是取消能力的独立注入边界。现有 PetScheduler/JobStore
// 只定义了入队、claim 和完成推进，没有取消契约，因此桥接层不能自行访问数据库或
// 猜测状态迁移；由持久化 owner 在具备语义后提供这个端口即可。
type PetSchedulerCancelPort interface {
	Cancel(context.Context, PetSchedulerCancelRequest) (bool, error)
}

type PetSchedulerCancelFunc func(context.Context, PetSchedulerCancelRequest) (bool, error)

func (f PetSchedulerCancelFunc) Cancel(ctx context.Context, request PetSchedulerCancelRequest) (bool, error) {
	return f(ctx, request)
}

type PetSchedulerValidatePlanInput struct {
	Plan json.RawMessage `json:"plan"`
}

type PetSchedulerSchedulePlanInput struct {
	Plan        json.RawMessage `json:"plan"`
	PlanID      string          `json:"planId,omitempty"`
	TimeZone    string          `json:"timeZone,omitempty"`
	MaxAttempts int             `json:"maxAttempts,omitempty"`
	ExpiresAt   *time.Time      `json:"expiresAt,omitempty"`
}

type PetSchedulerRunDueInput struct {
	Limit int `json:"limit,omitempty"`
}

type PetSchedulerCancelRequest struct {
	PlanID string `json:"planId,omitempty"`
	JobID  string `json:"jobId,omitempty"`
}

type PetSchedulerValidatePlanResult struct {
	Valid bool           `json:"valid"`
	Plan  *PetPlanScript `json:"plan,omitempty"`
}

type PetSchedulerPlanRecordPersistence string

const PetSchedulerPlanRecordNotAttempted PetSchedulerPlanRecordPersistence = "not_attempted"

const (
	PetSchedulerPlanRecordPersisted PetSchedulerPlanRecordPersistence = "persisted"
	PetSchedulerPlanRecordFailed    PetSchedulerPlanRecordPersistence = "failed"
)

type PetSchedulerSchedulePlanResult struct {
	Plan                  PetPlanScript                     `json:"plan"`
	PlanID                string                            `json:"planId"`
	Jobs                  []PetScheduledJob                 `json:"jobs"`
	Immediate             []PetScheduledJob                 `json:"immediate"`
	JobsEnqueued          bool                              `json:"jobsEnqueued"`
	PlanRecordPersisted   bool                              `json:"planRecordPersisted"`
	PlanRecordPersistence PetSchedulerPlanRecordPersistence `json:"planRecordPersistence"`
}

type PetSchedulerJobError struct {
	JobID   string                   `json:"jobId,omitempty"`
	Code    PetSchedulerAPIErrorCode `json:"code"`
	Path    string                   `json:"path,omitempty"`
	Message string                   `json:"message"`
}

type PetSchedulerRunDueResult struct {
	Actions          []PetAutomationJobPayload `json:"actions"`
	Claimed          int                       `json:"claimed"`
	Completed        int                       `json:"completed"`
	RemindersEmitted int                       `json:"remindersEmitted"`
	Retried          int                       `json:"retried"`
	Rescheduled      int                       `json:"rescheduled"`
	Failed           int                       `json:"failed"`
	Errors           []PetSchedulerJobError    `json:"errors"`
}

type PetSchedulerCancelResult struct {
	PlanID    string `json:"planId,omitempty"`
	JobID     string `json:"jobId,omitempty"`
	Cancelled bool   `json:"cancelled"`
}

// PetSchedulerAPI 只负责 Wails 输入输出和依赖编排：数据库归 JobStore/持久化 owner，
// action 的属性规则归 PetService/调度器。这样 API 层不会出现第二套计划校验、动作
// 执行或数据库写入逻辑，后续替换 SQLite 实现也不需要改前端契约。
type PetSchedulerAPI struct {
	scheduler PetSchedulerBridgeScheduler
	canceller PetSchedulerCancelPort
	planStore PetSchedulerPlanRecordPort
	petID     string
	initErr   error
}

// NewPetSchedulerAPI 创建调度桥接服务。canceller 省略时，若注入的 scheduler 自身
// 未来实现了 PetSchedulerCancelPort 会自动复用；当前 *PetScheduler 没有该能力，
// 因此 Cancel 会返回明确的 cancel_unavailable，而不是伪造取消成功。
func NewPetSchedulerAPI(scheduler PetSchedulerBridgeScheduler, cancellers ...PetSchedulerCancelPort) *PetSchedulerAPI {
	api := &PetSchedulerAPI{scheduler: scheduler}
	if len(cancellers) > 1 {
		api.initErr = fmt.Errorf("%w: only one cancellation port is allowed", ErrPetSchedulerAPIInvalidRequest)
	} else if len(cancellers) == 1 && cancellers[0] != nil {
		api.canceller = cancellers[0]
	} else if candidate, ok := scheduler.(PetSchedulerCancelPort); ok {
		api.canceller = candidate
	}
	return api
}

// NewPetSchedulerAPIForPet 在保留旧构造器的基础上接入计划记录 owner。主应用只有
// 一个默认桌宠时也必须显式传入 petID，避免未来多宠物扩展时把计划写进错误分区。
func NewPetSchedulerAPIForPet(
	scheduler PetSchedulerBridgeScheduler,
	petID string,
	planStore PetSchedulerPlanRecordPort,
	cancellers ...PetSchedulerCancelPort,
) *PetSchedulerAPI {
	api := NewPetSchedulerAPI(scheduler, cancellers...)
	api.petID = strings.TrimSpace(petID)
	if api.petID == "" {
		api.petID = DefaultPetID
	}
	api.planStore = planStore
	return api
}

// ValidatePlan 只调用共享计划校验器并返回归一化计划；它不接触数据库，也不提前
// 生成 job，避免“预览校验”产生不可见的调度副作用。
func (a *PetSchedulerAPI) ValidatePlan(ctx context.Context, input PetSchedulerValidatePlanInput) (PetSchedulerValidatePlanResult, error) {
	if err := a.validate(ctx); err != nil {
		return PetSchedulerValidatePlanResult{}, err
	}
	plan, err := ValidatePetPlanScript(input.Plan)
	if err != nil {
		return PetSchedulerValidatePlanResult{Valid: false}, petSchedulerAPIErrorFrom(err, PetSchedulerAPIErrorInvalidRequest, "plan")
	}
	return PetSchedulerValidatePlanResult{Valid: true, Plan: petSchedulerAPIPlanPointer(plan)}, nil
}

// SchedulePlan 让 scheduler 完成时间归一化和批量入队；若注入了计划记录 owner，
// 再把同一份归一化计划写入 pet_plans。两者不是跨数据库事务，因此记录失败时尽力
// 通过同一个取消端口回滚 job，并明确返回错误，不能把半成功状态包装成成功。
func (a *PetSchedulerAPI) SchedulePlan(ctx context.Context, input PetSchedulerSchedulePlanInput) (PetSchedulerSchedulePlanResult, error) {
	if err := a.validate(ctx); err != nil {
		return PetSchedulerSchedulePlanResult{}, err
	}
	plan, err := ValidatePetPlanScript(input.Plan)
	if err != nil {
		return PetSchedulerSchedulePlanResult{}, petSchedulerAPIErrorFrom(err, PetSchedulerAPIErrorInvalidRequest, "plan")
	}

	scheduled, err := a.scheduler.SchedulePlan(ctx, plan, PetSchedulePlanOptions{
		PlanID:      strings.TrimSpace(input.PlanID),
		TimeZone:    strings.TrimSpace(input.TimeZone),
		MaxAttempts: input.MaxAttempts,
		ExpiresAt:   input.ExpiresAt,
	})
	if err != nil {
		return PetSchedulerSchedulePlanResult{}, petSchedulerAPIErrorFrom(err, PetSchedulerAPIErrorScheduleFailed, "")
	}
	result := PetSchedulerSchedulePlanResult{
		Plan:                  petSchedulerAPIPlan(scheduled.Plan),
		PlanID:                scheduled.PlanID,
		Jobs:                  petSchedulerAPIJobs(scheduled.Jobs),
		Immediate:             petSchedulerAPIJobs(scheduled.Immediate),
		JobsEnqueued:          true,
		PlanRecordPersisted:   false,
		PlanRecordPersistence: PetSchedulerPlanRecordNotAttempted,
	}
	if a.planStore == nil {
		return result, nil
	}

	now := time.Now().UnixMilli()
	record := PetPlanRecord{
		PetID:     a.petID,
		PlanID:    scheduled.PlanID,
		Version:   scheduled.Plan.Version,
		Title:     scheduled.Plan.Title,
		Script:    petSchedulerAPIPlan(scheduled.Plan),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := a.planStore.UpsertPlan(ctx, record); err != nil {
		result.PlanRecordPersistence = PetSchedulerPlanRecordFailed
		if a.canceller != nil {
			if rollbackErr := a.cancelPlanAfterPersistenceFailure(ctx, scheduled.PlanID); rollbackErr != nil {
				return result, petSchedulerAPIError(
					PetSchedulerAPIErrorPlanRecordFailed,
					"planRecord",
					"计划记录落盘失败，且已入队任务回滚失败",
					errors.Join(err, rollbackErr),
					nil,
				)
			}
		}
		return result, petSchedulerAPIErrorFrom(err, PetSchedulerAPIErrorPlanRecordFailed, "planRecord")
	}
	result.PlanRecordPersisted = true
	result.PlanRecordPersistence = PetSchedulerPlanRecordPersisted
	return result, nil
}

func (a *PetSchedulerAPI) cancelPlanAfterPersistenceFailure(ctx context.Context, planID string) error {
	if a == nil || a.canceller == nil {
		return ErrPetSchedulerAPICancelUnavailable
	}
	_, err := a.canceller.Cancel(ctx, PetSchedulerCancelRequest{PlanID: planID})
	return err
}

// RunDue 不执行 action。scheduler 只有在 job 完成确认后才会把 action payload 放进
// Actions；桥接层把它作为稳定 JSON 返回，后续调用方再通过 PetService.PerformAction
// 进入属性、忙碌和睡眠规则。reminder 已由 emitter 负责投递，这里只返回投递数量。
func (a *PetSchedulerAPI) RunDue(ctx context.Context, input PetSchedulerRunDueInput) (PetSchedulerRunDueResult, error) {
	if err := a.validate(ctx); err != nil {
		return PetSchedulerRunDueResult{}, err
	}

	run, err := a.scheduler.RunDue(ctx, input.Limit)
	result := petSchedulerAPIRunDueResult(run)
	if err != nil {
		apiErr := petSchedulerAPIErrorFrom(err, PetSchedulerAPIErrorRunFailed, "")
		apiErr.Details = result.Errors
		return result, apiErr
	}
	return result, nil
}

// Cancel 只转发到注入的取消端口。当前调度器没有取消状态迁移，未注入端口时必须
// fail-fast 返回能力缺口；桥接层不能通过另一个数据库连接自行删 job 或改状态。
func (a *PetSchedulerAPI) Cancel(ctx context.Context, input PetSchedulerCancelRequest) (PetSchedulerCancelResult, error) {
	if err := a.validate(ctx); err != nil {
		return PetSchedulerCancelResult{}, err
	}
	if a.canceller == nil {
		return PetSchedulerCancelResult{}, petSchedulerAPIError(
			PetSchedulerAPIErrorCancelUnavailable,
			"",
			"scheduler cancellation is not configured",
			ErrPetSchedulerAPICancelUnavailable,
			nil,
		)
	}

	target, err := normalizePetSchedulerCancelRequest(input)
	if err != nil {
		return PetSchedulerCancelResult{}, err
	}
	cancelled, err := a.canceller.Cancel(ctx, target)
	if err != nil {
		return PetSchedulerCancelResult{PlanID: target.PlanID, JobID: target.JobID}, petSchedulerAPIErrorFrom(err, PetSchedulerAPIErrorCancelFailed, "")
	}
	return PetSchedulerCancelResult{
		PlanID:    target.PlanID,
		JobID:     target.JobID,
		Cancelled: cancelled,
	}, nil
}

func (a *PetSchedulerAPI) validate(ctx context.Context) error {
	if ctx == nil {
		return petSchedulerAPIError(
			PetSchedulerAPIErrorInvalidContext,
			"",
			"context is required",
			ErrPetSchedulerAPIInvalidContext,
			nil,
		)
	}
	if a == nil || a.scheduler == nil {
		return petSchedulerAPIError(
			PetSchedulerAPIErrorDependencyUnavailable,
			"",
			"scheduler is not configured",
			ErrPetSchedulerAPIUnavailable,
			nil,
		)
	}
	if a.initErr != nil {
		return petSchedulerAPIErrorFrom(a.initErr, PetSchedulerAPIErrorInvalidRequest, "")
	}
	return nil
}

func normalizePetSchedulerCancelRequest(input PetSchedulerCancelRequest) (PetSchedulerCancelRequest, error) {
	input.PlanID = strings.TrimSpace(input.PlanID)
	input.JobID = strings.TrimSpace(input.JobID)
	if (input.PlanID == "") == (input.JobID == "") {
		return PetSchedulerCancelRequest{}, petSchedulerAPIError(
			PetSchedulerAPIErrorInvalidRequest,
			"target",
			"exactly one of planId or jobId is required",
			ErrPetSchedulerAPIInvalidRequest,
			nil,
		)
	}
	if input.PlanID != "" {
		if err := validatePetSchedulerID(input.PlanID, ErrPetSchedulerInvalidPlanID); err != nil {
			return PetSchedulerCancelRequest{}, petSchedulerAPIErrorFrom(err, PetSchedulerAPIErrorInvalidRequest, "planId")
		}
	} else if err := validatePetSchedulerID(input.JobID, ErrPetSchedulerInvalidStepID); err != nil {
		return PetSchedulerCancelRequest{}, petSchedulerAPIErrorFrom(err, PetSchedulerAPIErrorInvalidRequest, "jobId")
	}
	return input, nil
}

func petSchedulerAPIError(code PetSchedulerAPIErrorCode, path, message string, cause error, details []PetSchedulerJobError) *PetSchedulerAPIError {
	return &PetSchedulerAPIError{
		Code:    code,
		Path:    path,
		Message: message,
		Details: details,
		cause:   cause,
	}
}

func petSchedulerAPIErrorFrom(err error, fallback PetSchedulerAPIErrorCode, path string) *PetSchedulerAPIError {
	if err == nil {
		return nil
	}
	var validationErr *PetPlanValidationError
	if errors.As(err, &validationErr) && validationErr != nil {
		return petSchedulerAPIError(
			PetSchedulerAPIErrorCode(validationErr.Code),
			validationErr.Path,
			validationErr.Message,
			err,
			nil,
		)
	}

	code := fallback
	switch {
	case errors.Is(err, context.Canceled):
		code = PetSchedulerAPIErrorContextCanceled
	case errors.Is(err, context.DeadlineExceeded):
		code = PetSchedulerAPIErrorDeadlineExceeded
	case errors.Is(err, ErrPetSchedulerStoreMissing), errors.Is(err, ErrPetSchedulerEmitterMissing):
		code = PetSchedulerAPIErrorDependencyUnavailable
	case errors.Is(err, ErrPetSchedulerInvalidConfig), errors.Is(err, ErrPetSchedulerInvalidPlanID), errors.Is(err, ErrPetSchedulerInvalidStepID):
		code = PetSchedulerAPIErrorInvalidRequest
	}
	return petSchedulerAPIError(code, path, err.Error(), err, nil)
}

func petSchedulerAPIRunDueResult(run PetRunDueResult) PetSchedulerRunDueResult {
	result := PetSchedulerRunDueResult{
		Actions:          make([]PetAutomationJobPayload, len(run.Actions)),
		Claimed:          run.Claimed,
		Completed:        run.Completed,
		RemindersEmitted: run.RemindersEmitted,
		Retried:          run.Retried,
		Rescheduled:      run.Rescheduled,
		Failed:           run.Failed,
		Errors:           make([]PetSchedulerJobError, 0, len(run.Errors)),
	}
	copy(result.Actions, run.Actions)
	for _, runErr := range run.Errors {
		result.Errors = append(result.Errors, petSchedulerAPIJobError(runErr))
	}
	return result
}

func petSchedulerAPIJobError(runErr PetJobRunError) PetSchedulerJobError {
	if runErr.Err == nil {
		return PetSchedulerJobError{
			JobID:   runErr.JobID,
			Code:    PetSchedulerAPIErrorJobFailed,
			Message: "pet scheduler job failed",
		}
	}
	apiErr := petSchedulerAPIErrorFrom(runErr.Err, PetSchedulerAPIErrorJobFailed, "")
	return PetSchedulerJobError{
		JobID:   runErr.JobID,
		Code:    apiErr.Code,
		Path:    apiErr.Path,
		Message: apiErr.Message,
	}
}

func petSchedulerAPIPlanPointer(plan PetPlanScript) *PetPlanScript {
	copy := petSchedulerAPIPlan(plan)
	return &copy
}

func petSchedulerAPIPlan(plan PetPlanScript) PetPlanScript {
	plan.Steps = append([]PetPlanStep(nil), plan.Steps...)
	for index := range plan.Steps {
		if plan.Steps[index].Schedule == nil {
			continue
		}
		schedule := *plan.Steps[index].Schedule
		schedule.At = append(json.RawMessage(nil), schedule.At...)
		plan.Steps[index].Schedule = &schedule
	}
	if plan.Steps == nil {
		plan.Steps = make([]PetPlanStep, 0)
	}
	return plan
}

func petSchedulerAPIJobs(jobs []PetScheduledJob) []PetScheduledJob {
	result := make([]PetScheduledJob, len(jobs))
	for index, job := range jobs {
		result[index] = job
		result[index].Schedule.At = append(json.RawMessage(nil), job.Schedule.At...)
		if job.ExpiresAt != nil {
			expiresAt := *job.ExpiresAt
			result[index].ExpiresAt = &expiresAt
		}
	}
	return result
}
