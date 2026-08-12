package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrPetSchedulerRuntimeInvalidContext     = errors.New("pet scheduler runtime context is invalid")
	ErrPetSchedulerRuntimeSchedulerMissing   = errors.New("pet scheduler runtime scheduler is unavailable")
	ErrPetSchedulerRuntimeServiceMissing     = errors.New("pet scheduler runtime pet service is unavailable")
	ErrPetSchedulerRuntimeUnknownAction      = errors.New("pet scheduler runtime action is unsupported")
	ErrPetSchedulerRuntimeInvalidConstructor = errors.New("pet scheduler runtime constructor is invalid")
)

// PetSchedulerEmitterFunc 让主控直接把 reminder 事件适配到 Wails，而 scheduler
// 只依赖 Emitter 契约，不需要导入 Wails 或持有窗口生命周期。
type PetSchedulerEmitterFunc func(context.Context, PetSchedulerEvent) error

func (f PetSchedulerEmitterFunc) Emit(ctx context.Context, event PetSchedulerEvent) error {
	if f == nil {
		return nil
	}
	return f(ctx, event)
}

// PetSchedulerRuntimeScheduler 是运行时真正需要的最小调度端口。保留这个窄接口
// 让内存 stub 可以独立验证编排逻辑，同时 *PetScheduler 仍是生产注入实现。
type PetSchedulerRuntimeScheduler interface {
	RunDue(context.Context, int) (PetRunDueResult, error)
}

var _ PetSchedulerRuntimeScheduler = (*PetScheduler)(nil)

// PetSchedulerRuntimeActionResult 记录一个已由 scheduler 完成确认的 action
// payload 的实际执行结果。Err 只供 Go 调用方判断技术错误，Error 是安全的文本
// 投影；PetActionResult.OK=false 仍然是 PetService 的正常业务拒绝，不会写入 Err。
type PetSchedulerRuntimeActionResult struct {
	Payload PetAutomationJobPayload `json:"payload"`
	Action  PetAction               `json:"action"`
	Result  PetActionResult         `json:"result"`
	Error   string                  `json:"error,omitempty"`
	Err     error                   `json:"-"`
}

// PetSchedulerRuntimeResult 同时保留 scheduler 的原始批次结果和每项 action
// 执行结果。这样调用方不会把“job 已完成确认”误读成“宠物动作已执行成功”。
type PetSchedulerRuntimeResult struct {
	Scheduler PetRunDueResult                   `json:"scheduler"`
	Actions   []PetSchedulerRuntimeActionResult `json:"actions"`
}

// PetSchedulerRuntime 是 scheduler 与 PetService 之间的被动薄适配层：scheduler
// 负责租约、reminder 投递和完成确认，PetService 负责动作白名单对应的属性规则。
// 运行时只消费主控主动调用的 RunDue，不启动 ticker，避免重复拥有调度周期、退出
// 顺序和数据库写入生命周期。
type PetSchedulerRuntime struct {
	scheduler PetSchedulerRuntimeScheduler
	service   *PetService
	initErr   error
}

// NewPetSchedulerRuntime 创建调度运行时。PetService 可以省略，适用于只投递
// reminder 的主控；若 scheduler 返回 action payload，运行时会明确报告依赖缺失。
func NewPetSchedulerRuntime(scheduler PetSchedulerRuntimeScheduler, services ...*PetService) *PetSchedulerRuntime {
	runtime := &PetSchedulerRuntime{scheduler: scheduler}
	switch len(services) {
	case 0:
		return runtime
	case 1:
		runtime.service = services[0]
		return runtime
	default:
		runtime.initErr = fmt.Errorf("%w: only one PetService is allowed", ErrPetSchedulerRuntimeInvalidConstructor)
		return runtime
	}
}

// RunOnce 执行一次默认批次。周期触发由主控已有的心跳或外部 scheduler 拥有，
// 运行时不另起后台 goroutine，避免同一 job 被两个时间源同时驱动。
func (r *PetSchedulerRuntime) RunOnce(ctx context.Context) (PetSchedulerRuntimeResult, error) {
	return r.RunDue(ctx, 0)
}

// RunDue 先让 scheduler 完成一个批次，再只执行 scheduler 返回的 action payload。
// reminder 已在 scheduler 内通过 emitter 处理，不会在这里重复投递；只有完成确认
// 后进入 Actions 的 payload 才有资格交给 PetService。
func (r *PetSchedulerRuntime) RunDue(ctx context.Context, limit int) (PetSchedulerRuntimeResult, error) {
	if err := r.validate(ctx); err != nil {
		return PetSchedulerRuntimeResult{}, err
	}

	run, schedulerErr := r.scheduler.RunDue(ctx, limit)
	result := PetSchedulerRuntimeResult{
		Scheduler: run,
		Actions:   make([]PetSchedulerRuntimeActionResult, 0, len(run.Actions)),
	}
	actionErr := r.executeActions(run.Actions, &result)
	return result, joinPetSchedulerRuntimeErrors(schedulerErr, actionErr)
}

func (r *PetSchedulerRuntime) validate(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrPetSchedulerRuntimeInvalidContext)
	}
	if r == nil || r.scheduler == nil {
		return ErrPetSchedulerRuntimeSchedulerMissing
	}
	if r.initErr != nil {
		return r.initErr
	}
	return nil
}

func (r *PetSchedulerRuntime) executeActions(
	payloads []PetAutomationJobPayload,
	result *PetSchedulerRuntimeResult,
) error {
	if len(payloads) == 0 {
		return nil
	}

	// action payload 理论上已经经过 scheduler 的计划校验，但 runtime 仍要守住
	// 执行边界：stub、恢复数据或未来替换的 scheduler 都不能把未知动作送进服务。
	for _, payload := range payloads {
		if err := validatePetSchedulerRuntimeAction(payload.Action); err != nil {
			result.Actions = append(result.Actions, petSchedulerRuntimeActionError(payload, err))
			return err
		}
	}

	if r.service == nil {
		err := fmt.Errorf("%w: action payloads require PetService", ErrPetSchedulerRuntimeServiceMissing)
		for _, payload := range payloads {
			result.Actions = append(result.Actions, petSchedulerRuntimeActionError(payload, err))
		}
		return err
	}

	var actionErrors []error
	for _, payload := range payloads {
		petID := strings.TrimSpace(r.service.petID)
		if petID == "" {
			petID = DefaultPetID
		}
		actionResult, err := r.service.PerformAction(petID, payload.Action)
		item := PetSchedulerRuntimeActionResult{
			Payload: payload,
			Action:  payload.Action,
			Result:  actionResult,
		}
		if err != nil {
			// 持久化、读取等技术错误要逐项保留，并汇总到 RunDue 的 Go error；
			// 继续处理同一批次其余已确认动作，调用方才能看到完整执行结果。
			item.Err = err
			item.Error = err.Error()
			actionErrors = append(actionErrors, err)
		}
		result.Actions = append(result.Actions, item)
	}
	return errors.Join(actionErrors...)
}

func validatePetSchedulerRuntimeAction(action PetAction) error {
	if IsPetPlanAction(string(action)) {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrPetSchedulerRuntimeUnknownAction, action)
}

func petSchedulerRuntimeActionError(payload PetAutomationJobPayload, err error) PetSchedulerRuntimeActionResult {
	item := PetSchedulerRuntimeActionResult{
		Payload: payload,
		Action:  payload.Action,
		Err:     err,
	}
	if err != nil {
		item.Error = err.Error()
	}
	return item
}

func joinPetSchedulerRuntimeErrors(errs ...error) error {
	valid := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			valid = append(valid, err)
		}
	}
	switch len(valid) {
	case 0:
		return nil
	case 1:
		return valid[0]
	default:
		return errors.Join(valid...)
	}
}
