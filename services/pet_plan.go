package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	PetPlanVersion         = 1
	PetPlanMaxSteps        = 16
	PetPlanMaxTextLength   = 240
	PetPlanMaxDelaySeconds = 30 * 24 * 60 * 60
	PetPlanMinIntervalMS   = 60 * 1000
	PetPlanMaxIntervalMS   = 365 * 24 * 60 * 60 * 1000

	PetAutomationJobType = "pet"
)

// PetPlanValidationError 将模型输出的失败原因固定为可检查的结构，调用方可以按
// Code/Path 做机器处理，同时保留 Message 供日志或用户界面展示。
type PetPlanValidationError struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func (e *PetPlanValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newPetPlanValidationError(code, path, message string) *PetPlanValidationError {
	return &PetPlanValidationError{Code: code, Path: path, Message: message}
}

// PetAutomationJobPayload 是写入 cron payload_json 并交给宠物 renderer 的最小消息。
// 它只携带已校验的动作/提醒，不携带宠物属性计算或调度器状态。
type PetAutomationJobPayload struct {
	Version   int             `json:"version"`
	PlanID    string          `json:"planId"`
	StepID    string          `json:"stepId"`
	Kind      PetPlanStepKind `json:"kind"`
	Action    PetAction       `json:"action,omitempty"`
	Text      string          `json:"text,omitempty"`
	Label     string          `json:"label,omitempty"`
	CreatedAt float64         `json:"createdAt"`
}

// IsPetPlanAction 与源端保持同一组白名单。动作名不能由模型自由扩展，否则执行端
// 无法建立确定的行为映射，最终会把“看似合法”的 payload 变成不可预测的任务。
func IsPetPlanAction(value any) bool {
	action, ok := value.(string)
	if !ok {
		return false
	}
	switch PetAction(action) {
	case PetActionFeed, PetActionBathe, PetActionSoak, PetActionPlay,
		PetActionSleep, PetActionWork, PetActionStudy:
		return true
	default:
		return false
	}
}

// ValidatePetPlanScript 校验并归一化模型输出。模型文本和调度参数在进入持久化或
// 自动化边界前必须先经过这里，避免 malformed JSON、未知 action、超长文本等输入
// 把 renderer 的执行逻辑或数据库中的任务状态污染成无法恢复的半成品。
func ValidatePetPlanScript(value any) (PetPlanScript, error) {
	decoded, err := petPlanJSONValue(value)
	if err != nil {
		return PetPlanScript{}, newPetPlanValidationError("invalid_input", "", "plan must be an object")
	}
	return validatePetPlanValue(decoded)
}

// ParsePetPlanJSON 只负责解析单个 JSON 值，具体协议校验统一复用
// ValidatePetPlanScript，保证字符串入口和结构化入口不会产生两套规则。
func ParsePetPlanJSON(raw string) (PetPlanScript, error) {
	decoded, err := decodePetPlanJSON([]byte(raw))
	if err != nil {
		return PetPlanScript{}, newPetPlanValidationError("invalid_json", "", "plan JSON is invalid")
	}
	return validatePetPlanValue(decoded)
}

// ValidatePetAutomationPayload 校验即将写入 cron_jobs.payload_json 的消息。自动化
// 任务是持久化后的跨进程协议，不能依赖执行端“再猜一次” kind/action 的含义。
func ValidatePetAutomationPayload(value any) (PetAutomationJobPayload, error) {
	decoded, err := petPlanJSONValue(value)
	if err != nil {
		return PetAutomationJobPayload{}, newPetPlanValidationError(
			"automation_payload_version_invalid", "version", "automation payload version is invalid",
		)
	}
	return validatePetAutomationPayloadValue(decoded)
}

// ParsePetAutomationPayloadJSON 为持久化 payload 提供与 ParsePetPlanJSON 对称的安全入口。
func ParsePetAutomationPayloadJSON(raw string) (PetAutomationJobPayload, error) {
	decoded, err := decodePetPlanJSON([]byte(raw))
	if err != nil {
		return PetAutomationJobPayload{}, newPetPlanValidationError(
			"invalid_json", "", "automation payload JSON is invalid",
		)
	}
	return validatePetAutomationPayloadValue(decoded)
}

func validatePetPlanValue(value any) (PetPlanScript, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return PetPlanScript{}, newPetPlanValidationError("plan_type_invalid", "", "plan must be an object")
	}

	version, present := record["version"]
	versionNumber, validVersion := finitePetPlanNumber(version)
	if !present || !validVersion || versionNumber != PetPlanVersion {
		return PetPlanScript{}, newPetPlanValidationError(
			"plan_version_invalid", "version",
			fmt.Sprintf("unsupported plan version: %s", petPlanValueString(version, present)),
		)
	}

	rawSteps, ok := record["steps"].([]any)
	if !ok || len(rawSteps) == 0 {
		return PetPlanScript{}, newPetPlanValidationError(
			"steps_required", "steps", "plan.steps must contain at least one step",
		)
	}
	if len(rawSteps) > PetPlanMaxSteps {
		return PetPlanScript{}, newPetPlanValidationError(
			"steps_limit", "steps",
			fmt.Sprintf("plan.steps cannot exceed %d items", PetPlanMaxSteps),
		)
	}

	var title string
	if rawTitle, exists := record["title"]; exists {
		var valid bool
		title, valid = normalizedPetPlanText(rawTitle, PetPlanMaxTextLength)
		if !valid {
			return PetPlanScript{}, newPetPlanValidationError("title_invalid", "title", "plan.title is invalid")
		}
	}

	steps := make([]PetPlanStep, 0, len(rawSteps))
	for index, rawStep := range rawSteps {
		step, err := validatePetPlanStep(rawStep, index)
		if err != nil {
			return PetPlanScript{}, err
		}
		steps = append(steps, step)
	}

	return PetPlanScript{
		Version: PetPlanVersion,
		Title:   title,
		Steps:   steps,
	}, nil
}

func validatePetPlanStep(value any, index int) (PetPlanStep, error) {
	stepRecord, ok := value.(map[string]any)
	if !ok {
		return PetPlanStep{}, petPlanStepError(
			index, "step_kind_invalid", "kind", fmt.Sprintf("step %d has an unsupported kind", index+1),
		)
	}

	rawKind, present := stepRecord["kind"]
	kind, kindOK := rawKind.(string)
	if !present || !kindOK || (kind != string(PetPlanActionStep) && kind != string(PetPlanReminderStep)) {
		return PetPlanStep{}, petPlanStepError(
			index, "step_kind_invalid", "kind", fmt.Sprintf("step %d has an unsupported kind", index+1),
		)
	}

	var schedule *PetPlanSchedule
	if rawSchedule, exists := stepRecord["schedule"]; exists {
		var err *PetPlanValidationError
		schedule, err = validatePetPlanSchedule(rawSchedule)
		if err != nil {
			return PetPlanStep{}, petPlanStepScheduleError(index, err)
		}
	}

	if kind == string(PetPlanActionStep) {
		rawAction, exists := stepRecord["action"]
		if !exists || !IsPetPlanAction(rawAction) {
			return PetPlanStep{}, petPlanStepError(
				index, "action_invalid", "action", fmt.Sprintf("step %d has an unsupported action", index+1),
			)
		}

		step := PetPlanStep{
			Kind:   PetPlanActionStep,
			Action: PetAction(rawAction.(string)),
		}
		step.Schedule = schedule
		if rawLabel, exists := stepRecord["label"]; exists {
			label, valid := normalizedPetPlanText(rawLabel, PetPlanMaxTextLength)
			if !valid {
				return PetPlanStep{}, petPlanStepError(
					index, "label_invalid", "label", fmt.Sprintf("step %d label is invalid", index+1),
				)
			}
			step.Label = label
		}
		return step, nil
	}

	rawText, exists := stepRecord["text"]
	text, valid := normalizedPetPlanText(rawText, PetPlanMaxTextLength)
	if !exists || !valid {
		return PetPlanStep{}, petPlanStepError(
			index, "reminder_text_invalid", "text", fmt.Sprintf("step %d reminder text is invalid", index+1),
		)
	}

	return PetPlanStep{
		Kind:     PetPlanReminderStep,
		Schedule: schedule,
		Text:     text,
	}, nil
}

func validatePetPlanSchedule(value any) (*PetPlanSchedule, *PetPlanValidationError) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, newPetPlanValidationError(
			"schedule_invalid", "schedule", "schedule must be an object with a supported kind",
		)
	}

	rawKind, present := record["kind"]
	kind, kindOK := rawKind.(string)
	if !present || !kindOK {
		return nil, newPetPlanValidationError(
			"schedule_invalid", "schedule", "schedule must be an object with a supported kind",
		)
	}

	schedule := &PetPlanSchedule{Kind: PetPlanScheduleKind(kind)}
	switch kind {
	case string(PetPlanScheduleNow):
		return schedule, nil

	case string(PetPlanScheduleDelay):
		delay, valid := finitePetPlanNumber(record["delaySeconds"])
		if !valid || delay <= 0 || delay > PetPlanMaxDelaySeconds {
			return nil, newPetPlanValidationError(
				"schedule_delay_invalid", "schedule.delaySeconds",
				fmt.Sprintf("schedule.delaySeconds must be between 0 and %d", PetPlanMaxDelaySeconds),
			)
		}
		schedule.DelaySeconds = delay
		return schedule, nil

	case string(PetPlanScheduleAt):
		rawAt, exists := record["at"]
		if !exists || !validPetPlanAt(rawAt) {
			return nil, newPetPlanValidationError(
				"schedule_at_invalid", "schedule.at", "schedule.at must be a valid timestamp or ISO date",
			)
		}
		at, err := json.Marshal(rawAt)
		if err != nil {
			return nil, newPetPlanValidationError(
				"schedule_at_invalid", "schedule.at", "schedule.at must be a valid timestamp or ISO date",
			)
		}
		schedule.At = json.RawMessage(at)
		if err := validatePetPlanTimezone(record, schedule); err != nil {
			return nil, err
		}
		return schedule, nil

	case string(PetPlanScheduleEvery):
		every, valid := finitePetPlanNumber(record["everyMs"])
		if !valid || every < PetPlanMinIntervalMS || every > PetPlanMaxIntervalMS {
			return nil, newPetPlanValidationError(
				"schedule_every_invalid", "schedule.everyMs",
				fmt.Sprintf("schedule.everyMs must be between %d and %d", PetPlanMinIntervalMS, PetPlanMaxIntervalMS),
			)
		}
		// TS 的持久化调度入口最终使用 Math.round；Go 合同是 int64，因此在这里
		// 明确归一化而不是直接截断，防止 60000.9 被错误地变成 60000。
		schedule.EveryMS = int64(math.Round(every))
		return schedule, nil

	case string(PetPlanScheduleCron):
		rawExpr, exists := record["expr"]
		expr, valid := rawExpr.(string)
		if !exists || !valid || !isNonEmptyPetPlanString(expr, 100) {
			return nil, newPetPlanValidationError(
				"schedule_expr_invalid", "schedule.expr", "schedule.expr is required",
			)
		}
		schedule.Expr = expr
		if err := validatePetPlanTimezone(record, schedule); err != nil {
			return nil, err
		}
		return schedule, nil

	default:
		return nil, newPetPlanValidationError(
			"schedule_kind_invalid", "schedule.kind", fmt.Sprintf("unsupported schedule kind: %s", kind),
		)
	}
}

func validatePetPlanTimezone(record map[string]any, schedule *PetPlanSchedule) *PetPlanValidationError {
	rawTZ, exists := record["tz"]
	if !exists {
		return nil
	}
	tz, ok := rawTZ.(string)
	if !ok || !isNonEmptyPetPlanString(tz, 80) {
		return newPetPlanValidationError(
			"schedule_timezone_invalid", "schedule.tz", "schedule.tz must be a non-empty timezone",
		)
	}
	// 校验只约束 tz 的形状，不替换原文；后续调度准备阶段仍可按业务时区策略
	// 处理它，这样 ISO 时间和用户显式时区不会在跨进程传递时丢失。
	schedule.TZ = tz
	return nil
}

func validPetPlanAt(value any) bool {
	if timestamp, ok := finitePetPlanNumber(value); ok {
		return timestamp > 0
	}
	date, ok := value.(string)
	return ok && isPetPlanISODate(date)
}

func isPetPlanISODate(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}

	// 源端示例同时使用带时区和本地时间 ISO 字符串；这里显式列出允许的
	// RFC3339/ISO 形态，避免把 Go 的宽松日期解析误当成有效计划时间。
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func validatePetAutomationPayloadValue(value any) (PetAutomationJobPayload, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return PetAutomationJobPayload{}, newPetPlanValidationError(
			"automation_payload_version_invalid", "version", "automation payload version is invalid",
		)
	}

	version, present := record["version"]
	versionNumber, validVersion := finitePetPlanNumber(version)
	if !present || !validVersion || versionNumber != PetPlanVersion {
		return PetAutomationJobPayload{}, newPetPlanValidationError(
			"automation_payload_version_invalid", "version", "automation payload version is invalid",
		)
	}

	planID, planIDOK := normalizedPetPlanText(record["planId"], 100)
	stepID, stepIDOK := normalizedPetPlanText(record["stepId"], 100)
	if !planIDOK || !stepIDOK {
		return PetAutomationJobPayload{}, newPetPlanValidationError(
			"automation_payload_identifiers_invalid", "", "automation payload identifiers are invalid",
		)
	}

	rawKind, present := record["kind"]
	kind, kindOK := rawKind.(string)
	if !present || !kindOK || (kind != string(PetPlanActionStep) && kind != string(PetPlanReminderStep)) {
		return PetAutomationJobPayload{}, newPetPlanValidationError(
			"automation_payload_kind_invalid", "kind", "automation payload kind is invalid",
		)
	}

	var action PetAction
	var text string
	if kind == string(PetPlanActionStep) {
		rawAction, exists := record["action"]
		if !exists || !IsPetPlanAction(rawAction) {
			return PetAutomationJobPayload{}, newPetPlanValidationError(
				"automation_payload_action_invalid", "action", "automation payload action is invalid",
			)
		}
		action = PetAction(rawAction.(string))
	} else {
		var valid bool
		text, valid = normalizedPetPlanText(record["text"], PetPlanMaxTextLength)
		if !valid {
			return PetAutomationJobPayload{}, newPetPlanValidationError(
				"automation_payload_text_invalid", "text", "automation payload text is invalid",
			)
		}
	}

	createdAt, validCreatedAt := finitePetPlanNumber(record["createdAt"])
	if !validCreatedAt || createdAt <= 0 {
		return PetAutomationJobPayload{}, newPetPlanValidationError(
			"automation_payload_created_at_invalid", "createdAt", "automation payload createdAt is invalid",
		)
	}

	label := ""
	if rawLabel, exists := record["label"]; exists {
		var valid bool
		label, valid = normalizedPetPlanText(rawLabel, PetPlanMaxTextLength)
		if !valid {
			return PetAutomationJobPayload{}, newPetPlanValidationError(
				"automation_payload_label_invalid", "label", "automation payload label is invalid",
			)
		}
	}

	return PetAutomationJobPayload{
		Version:   PetPlanVersion,
		PlanID:    planID,
		StepID:    stepID,
		Kind:      PetPlanStepKind(kind),
		Action:    action,
		Text:      text,
		Label:     label,
		CreatedAt: createdAt,
	}, nil
}

func petPlanStepError(index int, code, path, message string) *PetPlanValidationError {
	return newPetPlanValidationError(code, fmt.Sprintf("steps[%d].%s", index, path), message)
}

func petPlanStepScheduleError(index int, err *PetPlanValidationError) *PetPlanValidationError {
	path := fmt.Sprintf("steps[%d]", index)
	if err.Path != "" {
		path += "." + err.Path
	}
	return newPetPlanValidationError(err.Code, path, fmt.Sprintf("step %d: %s", index+1, err.Message))
}

func normalizedPetPlanText(value any, maxLength int) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" || utf16PetPlanLength(text) > maxLength {
		return "", false
	}
	return text, true
}

func isNonEmptyPetPlanString(value string, maxLength int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && utf16PetPlanLength(trimmed) <= maxLength
}

func utf16PetPlanLength(value string) int {
	// JavaScript String.length 按 UTF-16 code unit 计数；用 rune 数会让 emoji
	// 在 Go 端少算一个单位，导致两端对 240 字符边界的判断不一致。
	return len(utf16.Encode([]rune(value)))
}

func finitePetPlanNumber(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int8:
		number = float64(typed)
	case int16:
		number = float64(typed)
	case int32:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case uint:
		number = float64(typed)
	case uint8:
		number = float64(typed)
	case uint16:
		number = float64(typed)
	case uint32:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func petPlanValueString(value any, present bool) string {
	if !present {
		return "undefined"
	}
	if value == nil {
		return "null"
	}
	if text, ok := value.(string); ok {
		return text
	}
	if number, ok := value.(json.Number); ok {
		return string(number)
	}
	return fmt.Sprint(value)
}

func petPlanJSONValue(value any) (any, error) {
	if raw, ok := value.(json.RawMessage); ok {
		return decodePetPlanJSON(raw)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return decodePetPlanJSON(data)
}

func decodePetPlanJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}
