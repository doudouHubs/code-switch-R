package services

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type petSchedulerRuntimeStub struct {
	result  PetRunDueResult
	err     error
	calls   int
	limit   int
	context context.Context
}

func (s *petSchedulerRuntimeStub) RunDue(ctx context.Context, limit int) (PetRunDueResult, error) {
	s.calls++
	s.limit = limit
	s.context = ctx
	return s.result, s.err
}

func TestPetSchedulerEmitterFuncForwardsEventAndContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), "request", "scheduler-test")
	want := PetSchedulerEvent{
		Type:    PetSchedulerReminderEvent,
		EventID: "plan-1-1@2026-08-11T08:00:00Z",
		JobID:   "job-1",
		PlanID:  "plan-1",
		StepID:  "step-1",
		Payload: PetAutomationJobPayload{PlanID: "plan-1", StepID: "step-1", Kind: PetPlanReminderStep, Text: "喝水"},
	}
	var gotContext context.Context
	var gotEvent PetSchedulerEvent
	emitter := PetSchedulerEmitterFunc(func(gotCtx context.Context, got PetSchedulerEvent) error {
		gotContext = gotCtx
		gotEvent = got
		return nil
	})

	if err := emitter.Emit(ctx, want); err != nil {
		t.Fatalf("Emit() error = %v", err)
	}
	if gotContext != ctx || gotEvent.EventID != want.EventID || gotEvent.Payload.Text != "喝水" {
		t.Fatalf("emitted context/event = %#v/%#v, want original values", gotContext, gotEvent)
	}

	var nilEmitter PetSchedulerEmitterFunc
	if err := nilEmitter.Emit(ctx, want); err != nil {
		t.Fatalf("nil emitter Emit() error = %v, want nil", err)
	}
}

func TestPetSchedulerRuntimeRunOnceExecutesActionAndPreservesBusinessRejection(t *testing.T) {
	stub := &petSchedulerRuntimeStub{
		result: PetRunDueResult{
			Actions: []PetAutomationJobPayload{{
				Version: PetPlanVersion,
				PlanID:  "plan-1",
				StepID:  "step-1",
				Kind:    PetPlanActionStep,
				Action:  PetActionFeed,
			}},
			Claimed:   1,
			Completed: 1,
		},
	}
	repository := &memoryPetRepository{snapshot: PetMigrationSnapshot{State: &PetState{Hunger: 100}}}
	runtime := NewPetSchedulerRuntime(stub, NewPetService(repository))

	result, err := runtime.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil for business rejection", err)
	}
	if stub.calls != 1 || stub.limit != 0 {
		t.Fatalf("scheduler calls/limit = %d/%d, want one default-limit call", stub.calls, stub.limit)
	}
	if len(result.Actions) != 1 {
		t.Fatalf("runtime actions = %#v, want one execution result", result.Actions)
	}
	item := result.Actions[0]
	if item.Action != PetActionFeed || item.Result.OK || item.Result.Reason != PetActionFailureFull {
		t.Fatalf("action result = %#v, want structured full rejection", item)
	}
	if item.Err != nil || item.Error != "" {
		t.Fatalf("business rejection error fields = %#v/%q, want empty", item.Err, item.Error)
	}
	if result.Scheduler.Completed != 1 || len(result.Scheduler.Actions) != 1 {
		t.Fatalf("scheduler result = %#v, want completed payload preserved", result.Scheduler)
	}
}

func TestPetSchedulerRuntimeReturnsTechnicalActionErrorPerItem(t *testing.T) {
	saveErr := errors.New("save failed")
	stub := &petSchedulerRuntimeStub{result: PetRunDueResult{Actions: []PetAutomationJobPayload{{
		Version: PetPlanVersion,
		PlanID:  "plan-1",
		StepID:  "step-1",
		Kind:    PetPlanActionStep,
		Action:  PetActionFeed,
	}}}}
	runtime := NewPetSchedulerRuntime(
		stub,
		NewPetService(&memoryPetRepository{saveErr: saveErr}),
	)

	result, err := runtime.RunDue(context.Background(), 4)
	if !errors.Is(err, saveErr) {
		t.Fatalf("RunDue() error = %v, want persistence error", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Result.OK || !errors.Is(result.Actions[0].Err, saveErr) {
		t.Fatalf("technical action result = %#v, want failed item with save error", result.Actions)
	}
	if result.Actions[0].Error == "" {
		t.Fatal("technical action result error text is empty")
	}
}

func TestPetSchedulerRuntimeRejectsUnknownActionBeforePetService(t *testing.T) {
	loadErr := errors.New("service must not be called")
	stub := &petSchedulerRuntimeStub{result: PetRunDueResult{Actions: []PetAutomationJobPayload{{
		Version: PetPlanVersion,
		PlanID:  "plan-1",
		StepID:  "step-1",
		Kind:    PetPlanActionStep,
		Action:  PetAction("dance"),
	}}}}
	runtime := NewPetSchedulerRuntime(stub, NewPetService(&memoryPetRepository{loadErr: loadErr}))

	result, err := runtime.RunDue(context.Background(), 1)
	if !errors.Is(err, ErrPetSchedulerRuntimeUnknownAction) {
		t.Fatalf("RunDue() error = %v, want unknown-action error", err)
	}
	if errors.Is(err, loadErr) {
		t.Fatalf("RunDue() error = %v, PetService must not be called", err)
	}
	if len(result.Actions) != 1 || !errors.Is(result.Actions[0].Err, ErrPetSchedulerRuntimeUnknownAction) {
		t.Fatalf("unknown action result = %#v, want structured validation error", result.Actions)
	}
	if !strings.Contains(result.Actions[0].Error, "dance") {
		t.Fatalf("unknown action error text = %q, want action name", result.Actions[0].Error)
	}
}

func TestPetSchedulerRuntimeFailsFastOnInvalidContextAndMissingDependencies(t *testing.T) {
	stub := &petSchedulerRuntimeStub{}
	runtime := NewPetSchedulerRuntime(stub)

	if _, err := runtime.RunDue(nil, 0); !errors.Is(err, ErrPetSchedulerRuntimeInvalidContext) {
		t.Fatalf("nil-context error = %v, want invalid-context error", err)
	}
	if stub.calls != 0 {
		t.Fatalf("scheduler calls after nil context = %d, want zero", stub.calls)
	}

	missingScheduler := NewPetSchedulerRuntime(nil)
	if _, err := missingScheduler.RunOnce(context.Background()); !errors.Is(err, ErrPetSchedulerRuntimeSchedulerMissing) {
		t.Fatalf("missing-scheduler error = %v, want dependency error", err)
	}

	serviceMissing := &petSchedulerRuntimeStub{result: PetRunDueResult{Actions: []PetAutomationJobPayload{{
		Version: PetPlanVersion,
		PlanID:  "plan-1",
		StepID:  "step-1",
		Kind:    PetPlanActionStep,
		Action:  PetActionPlay,
	}}}}
	runtime = NewPetSchedulerRuntime(serviceMissing)
	result, err := runtime.RunOnce(context.Background())
	if !errors.Is(err, ErrPetSchedulerRuntimeServiceMissing) {
		t.Fatalf("missing-service error = %v, want dependency error", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Result.OK || !errors.Is(result.Actions[0].Err, ErrPetSchedulerRuntimeServiceMissing) {
		t.Fatalf("missing-service action result = %#v, want non-success item", result.Actions)
	}
}

func TestPetSchedulerRuntimeAllowsReminderOnlyRunWithoutPetService(t *testing.T) {
	stub := &petSchedulerRuntimeStub{result: PetRunDueResult{RemindersEmitted: 1, Completed: 1}}
	runtime := NewPetSchedulerRuntime(stub)

	result, err := runtime.RunDue(context.Background(), 2)
	if err != nil {
		t.Fatalf("reminder-only RunDue() error = %v, want nil", err)
	}
	if result.Scheduler.RemindersEmitted != 1 || result.Actions == nil || len(result.Actions) != 0 {
		t.Fatalf("reminder-only result = %#v, want scheduler result and empty action results", result)
	}
}
