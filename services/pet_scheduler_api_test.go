package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPetSchedulerAPINilContextReturnsStructuredError(t *testing.T) {
	scheduler := &petSchedulerAPIFakeScheduler{}
	canceller := &petSchedulerAPIFakeCanceller{}
	api := NewPetSchedulerAPI(scheduler, canceller)
	plan := PetSchedulerValidatePlanInput{Plan: petSchedulerAPIValidPlanJSON()}

	cases := []struct {
		name string
		call func() error
	}{
		{
			name: "validate",
			call: func() error {
				_, err := api.ValidatePlan(nil, plan)
				return err
			},
		},
		{
			name: "schedule",
			call: func() error {
				_, err := api.SchedulePlan(nil, PetSchedulerSchedulePlanInput{Plan: plan.Plan})
				return err
			},
		},
		{
			name: "run due",
			call: func() error {
				_, err := api.RunDue(nil, PetSchedulerRunDueInput{})
				return err
			},
		},
		{
			name: "cancel",
			call: func() error {
				_, err := api.Cancel(nil, PetSchedulerCancelRequest{PlanID: "plan-1"})
				return err
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.call()
			assertPetSchedulerAPIError(t, err, PetSchedulerAPIErrorInvalidContext)
			if !errors.Is(err, ErrPetSchedulerAPIInvalidContext) {
				t.Fatalf("error = %v, want ErrPetSchedulerAPIInvalidContext", err)
			}
		})
	}
}

func TestPetSchedulerAPIInvalidPlanUsesSharedStructuredValidation(t *testing.T) {
	scheduler := &petSchedulerAPIFakeScheduler{}
	api := NewPetSchedulerAPI(scheduler)
	input := PetSchedulerValidatePlanInput{Plan: json.RawMessage(`{"version":1,"steps":[]}`)}

	result, err := api.ValidatePlan(context.Background(), input)
	if result.Valid || result.Plan != nil {
		t.Fatalf("validation result = %#v, want invalid result without plan", result)
	}
	assertPetSchedulerAPIError(t, err, PetSchedulerAPIErrorCode("steps_required"))
	var apiErr *PetSchedulerAPIError
	if !errors.As(err, &apiErr) || apiErr.Path != "steps" {
		t.Fatalf("validation error = %#v, want path steps", apiErr)
	}
	if encoded, marshalErr := json.Marshal(apiErr); marshalErr != nil {
		t.Fatalf("marshal structured error: %v", marshalErr)
	} else if string(encoded) == "" || containsJSONField(encoded, "cause") {
		t.Fatalf("structured error JSON = %s, cause must stay outside Wails payload", encoded)
	}

	if _, err := api.SchedulePlan(context.Background(), PetSchedulerSchedulePlanInput{Plan: input.Plan}); err == nil {
		t.Fatal("SchedulePlan() with invalid plan returned nil error")
	}
	if scheduler.scheduleCalls != 0 {
		t.Fatalf("scheduler schedule calls = %d, invalid plan must be rejected before enqueue", scheduler.scheduleCalls)
	}
}

func TestPetSchedulerAPIScheduleDelegatesAndMarksPlanRecordGap(t *testing.T) {
	scheduler := &petSchedulerAPIFakeScheduler{
		scheduleResult: PetSchedulePlanResult{
			Plan: PetPlanScript{
				Version: PetPlanVersion,
				Title:   "早晨计划",
				Steps: []PetPlanStep{{
					Kind:     PetPlanActionStep,
					Action:   PetActionFeed,
					Schedule: &PetPlanSchedule{Kind: PetPlanScheduleNow},
				}},
			},
			PlanID:    "plan-1",
			Jobs:      []PetScheduledJob{{ID: "plan-1-1"}},
			Immediate: []PetScheduledJob{{ID: "plan-1-1"}},
		},
	}
	api := NewPetSchedulerAPI(scheduler)

	result, err := api.SchedulePlan(context.Background(), PetSchedulerSchedulePlanInput{
		Plan:        petSchedulerAPIValidPlanJSON(),
		PlanID:      " plan-1 ",
		TimeZone:    " UTC ",
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("SchedulePlan() error = %v", err)
	}
	if scheduler.scheduleCalls != 1 {
		t.Fatalf("scheduler schedule calls = %d, want 1", scheduler.scheduleCalls)
	}
	if got, ok := scheduler.scheduleRaw.(PetPlanScript); !ok || got.Version != PetPlanVersion || len(got.Steps) != 1 {
		t.Fatalf("scheduler raw plan = %#v, want normalized PetPlanScript", scheduler.scheduleRaw)
	}
	if scheduler.scheduleOptions.PlanID != "plan-1" || scheduler.scheduleOptions.TimeZone != "UTC" || scheduler.scheduleOptions.MaxAttempts != 2 {
		t.Fatalf("scheduler options = %#v, want normalized options", scheduler.scheduleOptions)
	}
	if !result.JobsEnqueued || result.PlanRecordPersisted || result.PlanRecordPersistence != PetSchedulerPlanRecordNotAttempted {
		t.Fatalf("persistence boundary = %#v, want jobs enqueued and plan record not attempted", result)
	}
	if result.Jobs == nil || result.Immediate == nil {
		t.Fatalf("schedule slices = %#v, want JSON arrays", result)
	}
	if encoded, marshalErr := json.Marshal(result); marshalErr != nil {
		t.Fatalf("marshal schedule result: %v", marshalErr)
	} else if string(encoded) == "" {
		t.Fatal("schedule result JSON is empty")
	}
}

func TestPetSchedulerAPIRunDueProjectsActionAndReminderBoundaries(t *testing.T) {
	scheduler := &petSchedulerAPIFakeScheduler{
		runResult: PetRunDueResult{
			Actions: []PetAutomationJobPayload{{
				Version:   PetPlanVersion,
				PlanID:    "plan-1",
				StepID:    "plan-1-1",
				Kind:      PetPlanActionStep,
				Action:    PetActionFeed,
				CreatedAt: 100,
			}},
			Claimed:          2,
			Completed:        2,
			RemindersEmitted: 1,
		},
	}
	api := NewPetSchedulerAPI(scheduler)

	result, err := api.RunDue(context.Background(), PetSchedulerRunDueInput{Limit: 8})
	if err != nil {
		t.Fatalf("RunDue() error = %v", err)
	}
	if scheduler.runLimit != 8 {
		t.Fatalf("scheduler run limit = %d, want 8", scheduler.runLimit)
	}
	if len(result.Actions) != 1 || result.Actions[0].Action != PetActionFeed || result.Actions[0].Kind != PetPlanActionStep {
		t.Fatalf("action result = %#v, want completed action payload only", result.Actions)
	}
	if result.RemindersEmitted != 1 || result.Claimed != 2 || result.Completed != 2 {
		t.Fatalf("run counters = %#v, want action/reminder boundary preserved", result)
	}
	if result.Errors == nil {
		t.Fatalf("run errors = nil, want a stable empty JSON array")
	}
	if encoded, marshalErr := json.Marshal(result); marshalErr != nil {
		t.Fatalf("marshal run result: %v", marshalErr)
	} else if containsJSONField(encoded, "Err") {
		t.Fatalf("run result JSON contains internal error field: %s", encoded)
	}
}

func TestPetSchedulerAPIRunDueProjectsPartialReminderError(t *testing.T) {
	jobErr := errors.New("reminder emitter unavailable")
	scheduler := &petSchedulerAPIFakeScheduler{
		runResult: PetRunDueResult{
			Claimed: 1,
			Errors:  []PetJobRunError{{JobID: "reminder-1", Err: jobErr}},
		},
		runErr: &PetRunDueError{Errors: []PetJobRunError{{JobID: "reminder-1", Err: jobErr}}},
	}
	api := NewPetSchedulerAPI(scheduler)

	result, err := api.RunDue(context.Background(), PetSchedulerRunDueInput{})
	if err == nil {
		t.Fatal("RunDue() with reminder failure returned nil error")
	}
	if len(result.Errors) != 1 || result.Errors[0].JobID != "reminder-1" || result.Errors[0].Code != PetSchedulerAPIErrorJobFailed {
		t.Fatalf("projected reminder errors = %#v", result.Errors)
	}
	assertPetSchedulerAPIError(t, err, PetSchedulerAPIErrorRunFailed)
	var apiErr *PetSchedulerAPIError
	if !errors.As(err, &apiErr) || len(apiErr.Details) != 1 || apiErr.Details[0].JobID != "reminder-1" {
		t.Fatalf("structured run error = %#v, want one reminder detail", apiErr)
	}
}

func TestPetSchedulerAPICancelRequiresInjectedPort(t *testing.T) {
	scheduler := &petSchedulerAPIFakeScheduler{}
	api := NewPetSchedulerAPI(scheduler)
	if _, err := api.Cancel(context.Background(), PetSchedulerCancelRequest{PlanID: "plan-1"}); err == nil {
		t.Fatal("Cancel() without a port returned nil error")
	} else {
		assertPetSchedulerAPIError(t, err, PetSchedulerAPIErrorCancelUnavailable)
		if !errors.Is(err, ErrPetSchedulerAPICancelUnavailable) {
			t.Fatalf("Cancel() error = %v, want cancellation-unavailable sentinel", err)
		}
	}

	canceller := &petSchedulerAPIFakeCanceller{cancelled: true}
	api = NewPetSchedulerAPI(scheduler, canceller)
	result, err := api.Cancel(context.Background(), PetSchedulerCancelRequest{PlanID: " plan-1 "})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if !result.Cancelled || result.PlanID != "plan-1" || canceller.request.PlanID != "plan-1" {
		t.Fatalf("cancel result/request = %#v/%#v", result, canceller.request)
	}

	if _, err := api.Cancel(context.Background(), PetSchedulerCancelRequest{}); err == nil {
		t.Fatal("Cancel() with empty target returned nil error")
	} else {
		assertPetSchedulerAPIError(t, err, PetSchedulerAPIErrorInvalidRequest)
	}
}

type petSchedulerAPIFakeScheduler struct {
	scheduleCalls   int
	scheduleRaw     any
	scheduleOptions PetSchedulePlanOptions
	scheduleResult  PetSchedulePlanResult
	scheduleErr     error
	runLimit        int
	runResult       PetRunDueResult
	runErr          error
}

func (s *petSchedulerAPIFakeScheduler) SchedulePlan(_ context.Context, rawPlan any, options PetSchedulePlanOptions) (PetSchedulePlanResult, error) {
	s.scheduleCalls++
	s.scheduleRaw = rawPlan
	s.scheduleOptions = options
	return s.scheduleResult, s.scheduleErr
}

func (s *petSchedulerAPIFakeScheduler) RunDue(_ context.Context, limit int) (PetRunDueResult, error) {
	s.runLimit = limit
	return s.runResult, s.runErr
}

type petSchedulerAPIFakeCanceller struct {
	request   PetSchedulerCancelRequest
	cancelled bool
	err       error
}

func (c *petSchedulerAPIFakeCanceller) Cancel(_ context.Context, request PetSchedulerCancelRequest) (bool, error) {
	c.request = request
	return c.cancelled, c.err
}

func assertPetSchedulerAPIError(t *testing.T, err error, code PetSchedulerAPIErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", code)
	}
	if got := PetSchedulerAPIErrorCodeOf(err); got != code {
		t.Fatalf("error code = %q, want %q; error=%v", got, code, err)
	}
}

func petSchedulerAPIValidPlanJSON() json.RawMessage {
	return json.RawMessage(`{"version":1,"steps":[{"kind":"action","action":"feed","schedule":{"kind":"now"}}]}`)
}

func containsJSONField(raw []byte, field string) bool {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	_, ok := value[field]
	return ok
}
