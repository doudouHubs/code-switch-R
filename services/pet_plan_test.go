package services

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidatePetPlanScriptNormalizesTextAndKeepsScheduleTimezone(t *testing.T) {
	raw := map[string]any{
		"version": 1,
		"title":   "  每日照护  ",
		"steps": []any{
			map[string]any{
				"kind":   "action",
				"action": "feed",
				"label":  "  早餐  ",
				"schedule": map[string]any{
					"kind": "at",
					"at":   "2026-08-11T09:00:00+08:00",
					"tz":   "Asia/Shanghai",
				},
			},
			map[string]any{
				"kind": "reminder",
				"text": "  喝水  ",
				"schedule": map[string]any{
					"kind":         "every",
					"everyMs":      60000.5,
					"ignoredField": "ignored",
				},
			},
		},
	}

	plan, err := ValidatePetPlanScript(raw)
	if err != nil {
		t.Fatalf("ValidatePetPlanScript() error = %v", err)
	}
	if plan.Title != "每日照护" || len(plan.Steps) != 2 {
		t.Fatalf("normalized plan = %#v", plan)
	}
	if plan.Steps[0].Label != "早餐" {
		t.Fatalf("normalized label = %q", plan.Steps[0].Label)
	}
	if plan.Steps[0].Schedule == nil || plan.Steps[0].Schedule.TZ != "Asia/Shanghai" {
		t.Fatalf("timezone was not preserved: %#v", plan.Steps[0].Schedule)
	}
	if plan.Steps[1].Text != "喝水" || plan.Steps[1].Schedule.EveryMS != 60001 {
		t.Fatalf("normalized reminder/schedule = %#v", plan.Steps[1])
	}
}

func TestValidatePetPlanScriptSupportsAllScheduleKinds(t *testing.T) {
	tests := []struct {
		name     string
		schedule map[string]any
		check    func(*testing.T, *PetPlanSchedule)
	}{
		{
			name:     "now",
			schedule: map[string]any{"kind": "now"},
			check: func(t *testing.T, got *PetPlanSchedule) {
				if got.Kind != PetPlanScheduleNow {
					t.Fatalf("kind = %q", got.Kind)
				}
			},
		},
		{
			name:     "delay",
			schedule: map[string]any{"kind": "delay", "delaySeconds": 30.5},
			check: func(t *testing.T, got *PetPlanSchedule) {
				if got.DelaySeconds != 30.5 {
					t.Fatalf("delay = %v", got.DelaySeconds)
				}
			},
		},
		{
			name:     "at timestamp",
			schedule: map[string]any{"kind": "at", "at": float64(1760000000000), "tz": "UTC"},
			check: func(t *testing.T, got *PetPlanSchedule) {
				if string(got.At) != "1760000000000" || got.TZ != "UTC" {
					t.Fatalf("at = %s tz = %q", got.At, got.TZ)
				}
			},
		},
		{
			name:     "cron",
			schedule: map[string]any{"kind": "cron", "expr": " 0 9 * * 1 ", "tz": "Asia/Shanghai"},
			check: func(t *testing.T, got *PetPlanSchedule) {
				if got.Expr != " 0 9 * * 1 " || got.TZ != "Asia/Shanghai" {
					t.Fatalf("cron = %#v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := ValidatePetPlanScript(map[string]any{
				"version": 1,
				"steps": []any{map[string]any{
					"kind":     "action",
					"action":   "play",
					"schedule": tt.schedule,
				}},
			})
			if err != nil {
				t.Fatalf("ValidatePetPlanScript() error = %v", err)
			}
			tt.check(t, plan.Steps[0].Schedule)
		})
	}
}

func TestValidatePetPlanScriptBoundariesReturnStructuredErrors(t *testing.T) {
	longText := strings.Repeat("a", PetPlanMaxTextLength+1)
	longEmoji := strings.Repeat("😀", PetPlanMaxTextLength/2+1)
	tests := []struct {
		name string
		plan map[string]any
		code string
	}{
		{
			name: "unsupported version",
			plan: map[string]any{"version": 2, "steps": []any{map[string]any{"kind": "action", "action": "feed"}}},
			code: "plan_version_invalid",
		},
		{
			name: "too many steps",
			plan: map[string]any{"version": 1, "steps": makePetPlanSteps(PetPlanMaxSteps + 1)},
			code: "steps_limit",
		},
		{
			name: "empty title",
			plan: map[string]any{"version": 1, "title": " \t", "steps": validPetPlanSteps()},
			code: "title_invalid",
		},
		{
			name: "long title",
			plan: map[string]any{"version": 1, "title": longText, "steps": validPetPlanSteps()},
			code: "title_invalid",
		},
		{
			name: "emoji follows UTF16 boundary",
			plan: map[string]any{"version": 1, "title": longEmoji, "steps": validPetPlanSteps()},
			code: "title_invalid",
		},
		{
			name: "delay zero",
			plan: planWithSchedule(map[string]any{"kind": "delay", "delaySeconds": 0}),
			code: "schedule_delay_invalid",
		},
		{
			name: "delay too large",
			plan: planWithSchedule(map[string]any{"kind": "delay", "delaySeconds": PetPlanMaxDelaySeconds + 1}),
			code: "schedule_delay_invalid",
		},
		{
			name: "every too small",
			plan: planWithSchedule(map[string]any{"kind": "every", "everyMs": PetPlanMinIntervalMS - 1}),
			code: "schedule_every_invalid",
		},
		{
			name: "every too large",
			plan: planWithSchedule(map[string]any{"kind": "every", "everyMs": PetPlanMaxIntervalMS + 1}),
			code: "schedule_every_invalid",
		},
		{
			name: "invalid at",
			plan: planWithSchedule(map[string]any{"kind": "at", "at": "not-a-date"}),
			code: "schedule_at_invalid",
		},
		{
			name: "invalid timezone",
			plan: planWithSchedule(map[string]any{"kind": "at", "at": "2026-08-11T09:00:00", "tz": " "}),
			code: "schedule_timezone_invalid",
		},
		{
			name: "invalid cron expression",
			plan: planWithSchedule(map[string]any{"kind": "cron", "expr": " "}),
			code: "schedule_expr_invalid",
		},
		{
			name: "invalid action",
			plan: map[string]any{"version": 1, "steps": []any{map[string]any{"kind": "action", "action": "dance"}}},
			code: "action_invalid",
		},
		{
			name: "long reminder",
			plan: map[string]any{"version": 1, "steps": []any{map[string]any{"kind": "reminder", "text": longText}}},
			code: "reminder_text_invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidatePetPlanScript(tt.plan)
			if err == nil {
				t.Fatal("ValidatePetPlanScript() error = nil")
			}
			var validationErr *PetPlanValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error type = %T, want *PetPlanValidationError", err)
			}
			if validationErr.Code != tt.code || validationErr.Message == "" || validationErr.Path == "" {
				t.Fatalf("structured error = %#v", validationErr)
			}
		})
	}
}

func TestParsePetPlanJSONRejectsMalformedAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"steps":[{"kind":"action","action":"feed"}]`,
		`{"version":1,"steps":[{"kind":"action","action":"feed"}]} {"extra":true}`,
	} {
		_, err := ParsePetPlanJSON(raw)
		if err == nil {
			t.Fatalf("ParsePetPlanJSON(%q) error = nil", raw)
		}
		var validationErr *PetPlanValidationError
		if !errors.As(err, &validationErr) || validationErr.Code != "invalid_json" {
			t.Fatalf("error = %#v", err)
		}
	}
}

func TestValidatePetAutomationPayload(t *testing.T) {
	payload, err := ValidatePetAutomationPayload(map[string]any{
		"version":   1,
		"planId":    "  plan-1  ",
		"stepId":    " step-1 ",
		"kind":      "reminder",
		"text":      "  喝水  ",
		"label":     "  重要  ",
		"createdAt": 1760000000000,
	})
	if err != nil {
		t.Fatalf("ValidatePetAutomationPayload() error = %v", err)
	}
	if payload.PlanID != "plan-1" || payload.StepID != "step-1" || payload.Text != "喝水" || payload.Label != "重要" {
		t.Fatalf("normalized payload = %#v", payload)
	}

	actionPayload, err := ValidatePetAutomationPayload(map[string]any{
		"version":   1,
		"planId":    "plan-1",
		"stepId":    "step-2",
		"kind":      "action",
		"action":    "study",
		"createdAt": 1760000000000,
	})
	if err != nil || actionPayload.Action != PetActionStudy || actionPayload.Text != "" {
		t.Fatalf("action payload = %#v error = %v", actionPayload, err)
	}
}

func TestValidatePetAutomationPayloadBoundaries(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"version":   1,
			"planId":    "plan-1",
			"stepId":    "step-1",
			"kind":      "action",
			"action":    "feed",
			"createdAt": 1,
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
		code   string
	}{
		{
			name:   "invalid version",
			mutate: func(value map[string]any) { value["version"] = 2 },
			code:   "automation_payload_version_invalid",
		},
		{
			name:   "empty identifier",
			mutate: func(value map[string]any) { value["planId"] = " " },
			code:   "automation_payload_identifiers_invalid",
		},
		{
			name:   "invalid action",
			mutate: func(value map[string]any) { value["action"] = "dance" },
			code:   "automation_payload_action_invalid",
		},
		{
			name: "missing reminder text",
			mutate: func(value map[string]any) {
				value["kind"] = "reminder"
				delete(value, "action")
			},
			code: "automation_payload_text_invalid",
		},
		{
			name:   "invalid createdAt",
			mutate: func(value map[string]any) { value["createdAt"] = 0 },
			code:   "automation_payload_created_at_invalid",
		},
		{
			name:   "invalid label",
			mutate: func(value map[string]any) { value["label"] = " " },
			code:   "automation_payload_label_invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := base()
			tt.mutate(value)
			_, err := ValidatePetAutomationPayload(value)
			if err == nil {
				t.Fatal("ValidatePetAutomationPayload() error = nil")
			}
			var validationErr *PetPlanValidationError
			if !errors.As(err, &validationErr) || validationErr.Code != tt.code {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestParsePetAutomationPayloadJSON(t *testing.T) {
	raw := `{"version":1,"planId":"plan-1","stepId":"step-1","kind":"action","action":"feed","createdAt":1760000000000}`
	payload, err := ParsePetAutomationPayloadJSON(raw)
	if err != nil {
		t.Fatalf("ParsePetAutomationPayloadJSON() error = %v", err)
	}
	if payload.Action != PetActionFeed || payload.CreatedAt != 1760000000000 {
		t.Fatalf("payload = %#v", payload)
	}

	if _, err := ParsePetAutomationPayloadJSON(raw + " trailing"); err == nil {
		t.Fatal("ParsePetAutomationPayloadJSON() accepted trailing data")
	}
}

func validPetPlanSteps() []any {
	return []any{map[string]any{"kind": "action", "action": "feed"}}
}

func makePetPlanSteps(count int) []any {
	steps := make([]any, count)
	for index := range steps {
		steps[index] = map[string]any{"kind": "action", "action": "feed"}
	}
	return steps
}

func planWithSchedule(schedule map[string]any) map[string]any {
	return map[string]any{
		"version": 1,
		"steps": []any{map[string]any{
			"kind":     "action",
			"action":   "feed",
			"schedule": schedule,
		}},
	}
}

func TestPetPlanJSONRoundTripKeepsAtShape(t *testing.T) {
	plan, err := ParsePetPlanJSON(`{"version":1,"steps":[{"kind":"action","action":"feed","schedule":{"kind":"at","at":"2026-08-11T09:00:00","tz":"Asia/Shanghai"}}]}`)
	if err != nil {
		t.Fatalf("ParsePetPlanJSON() error = %v", err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"at":"2026-08-11T09:00:00"`) || !strings.Contains(string(encoded), `"tz":"Asia/Shanghai"`) {
		t.Fatalf("encoded plan = %s", encoded)
	}
}
