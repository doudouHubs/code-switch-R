package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PetCronMigrationDiagnostic 描述一条源记录为什么没有进入目标调度器。
// Source/ID/Reason 始终成组输出，方便启动日志或 UI 直接定位坏记录。
type PetCronMigrationDiagnostic struct {
	Source string `json:"source"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
}

// PetCronMigrationReport 是一次迁移的可观察结果。AlreadyApplied 只在本次扫描
// 的所有合法活动记录都已经存在于目标且没有新写入时置 true，不把“全是脏数据”
// 误报成已经完成。
type PetCronMigrationReport struct {
	SourceDB       string                       `json:"sourceDb"`
	Scanned        int                          `json:"scanned"`
	Imported       int                          `json:"imported"`
	Skipped        int                          `json:"skipped"`
	Diagnostics    []PetCronMigrationDiagnostic `json:"diagnostics"`
	AlreadyApplied bool                         `json:"alreadyApplied"`
}

// PetCronMigrationOptions 控制迁移时的时间和目标重试参数。Now 为空时取一次
// 系统 UTC 时间，保证本次 at/every/cron 计算使用同一个时间基准。
type PetCronMigrationOptions struct {
	Now             time.Time
	MaxAttempts     int
	DefaultTimeZone string
}

// PetCronMigrationJobIDLister 是幂等检查的窄接口。JobStore 本身为了保持调度
// 契约最小没有列全量能力，迁移器只在实现该接口的 store 上查询已有 ID。
type PetCronMigrationJobIDLister interface {
	PetCronMigrationJobIDs(context.Context) ([]string, error)
}

// PetCronMigrationJobIDs 为 SQLite 目标提供安全的 ID 查询，不读取租约和
// payload，避免迁移幂等检查被一条历史脏 job 阻断。
func (s *PetSQLiteJobStore) PetCronMigrationJobIDs(ctx context.Context) ([]string, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM pet_scheduler_jobs")
	if err != nil {
		return nil, fmt.Errorf("list target pet job IDs: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan target pet job ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate target pet job IDs: %w", err)
	}
	return ids, nil
}

// MigrateOpenCoworkPetCronJobs 只迁移 OpenCowork cron_jobs 中活动的 pet 任务。
// disabled/deleted 记录虽然会被扫描，但明确跳过并报告：目标没有“已禁用但
// 可恢复”的迁移状态，强行写入会让启动后的调度行为悄悄改变。
func MigrateOpenCoworkPetCronJobs(ctx context.Context, sourceRoot string, store JobStore, options ...PetCronMigrationOptions) (PetCronMigrationReport, error) {
	dbPath, err := resolvePetCronSourceDB(sourceRoot)
	report := PetCronMigrationReport{SourceDB: dbPath, Diagnostics: make([]PetCronMigrationDiagnostic, 0)}
	if err != nil {
		return report, err
	}
	if ctx == nil {
		return report, errors.New("pet cron migration context is nil")
	}
	if store == nil {
		return report, ErrPetSchedulerStoreMissing
	}
	if len(options) > 1 {
		return report, errors.New("pet cron migration accepts at most one options value")
	}
	config, err := normalizePetCronMigrationOptions(options)
	if err != nil {
		return report, err
	}

	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			addPetCronMigrationDiagnostic(&report, dbPath, "", "source data.db does not exist")
			return report, nil
		}
		return report, fmt.Errorf("stat OpenCowork source DB: %w", err)
	}

	// mode=ro 是迁移的边界约束：源库可能仍被 OpenCowork 使用，迁移只能观察
	// 它，不能因为连接、事务或意外 SQL 改写用户的源数据。
	db, err := sql.Open("sqlite", petCronReadOnlySQLiteDSN(dbPath))
	if err != nil {
		return report, fmt.Errorf("open OpenCowork source DB read-only: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return report, fmt.Errorf("ping OpenCowork source DB read-only: %w", err)
	}

	rows, err := db.QueryContext(ctx, `
SELECT id, schedule_kind, schedule_at, schedule_every, schedule_expr, schedule_tz,
       job_type, payload_json, enabled, delete_after_run, deleted_at, created_at
  FROM cron_jobs
 WHERE job_type = 'pet'
 ORDER BY created_at ASC, id ASC`)
	if err != nil {
		if isPetCronMissingTableError(err) {
			addPetCronMigrationDiagnostic(&report, dbPath, "", "source cron_jobs table does not exist")
			return report, nil
		}
		return report, fmt.Errorf("query OpenCowork cron_jobs: %w", err)
	}
	defer rows.Close()

	knownIDs, err := petCronMigrationTargetIDs(ctx, store)
	if err != nil {
		return report, err
	}
	jobs := make([]PetScheduledJob, 0)
	activeValid := 0
	duplicates := 0
	for rows.Next() {
		report.Scanned++
		record, err := scanPetCronSourceJob(rows)
		if err != nil {
			report.Skipped++
			addPetCronMigrationDiagnostic(&report, dbPath, "", fmt.Sprintf("source row could not be decoded: %v", err))
			continue
		}
		if !record.enabled.Valid || record.enabled.Int64 != 1 {
			report.Skipped++
			addPetCronMigrationDiagnostic(&report, dbPath, record.id, "source job is disabled; only enabled=1 active jobs are migrated")
			continue
		}
		if record.deletedAt.Valid {
			report.Skipped++
			addPetCronMigrationDiagnostic(&report, dbPath, record.id, "source job is deleted; only deleted_at IS NULL active jobs are migrated")
			continue
		}

		job, err := convertPetCronSourceJob(record, config)
		if err != nil {
			report.Skipped++
			addPetCronMigrationDiagnostic(&report, dbPath, record.id, err.Error())
			continue
		}
		activeValid++
		if _, exists := knownIDs[job.ID]; exists {
			duplicates++
			report.Skipped++
			addPetCronMigrationDiagnostic(&report, dbPath, record.id, "target job ID already exists; source job was not overwritten")
			continue
		}
		jobs = append(jobs, job)
		knownIDs[job.ID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("iterate OpenCowork cron_jobs: %w", err)
	}

	for _, job := range jobs {
		// 单条入队让一个冲突 ID 只影响自己，并把并发重跑造成的唯一键冲突
		// 转成诊断；批量入队会把本来合法的其他源记录一并回滚。
		if err := store.Enqueue(ctx, []PetScheduledJob{job}); err != nil {
			if isPetCronDuplicateError(err) {
				duplicates++
				report.Skipped++
				addPetCronMigrationDiagnostic(&report, dbPath, job.ID, "target job ID became occupied during migration; source job was not overwritten")
				continue
			}
			return report, fmt.Errorf("enqueue migrated pet cron job %q: %w", job.ID, err)
		}
		report.Imported++
	}
	report.AlreadyApplied = activeValid > 0 && duplicates == activeValid && report.Imported == 0
	return report, nil
}

type petCronMigrationConfig struct {
	now             time.Time
	maxAttempts     int
	defaultTimeZone string
}

func normalizePetCronMigrationOptions(options []PetCronMigrationOptions) (petCronMigrationConfig, error) {
	config := PetCronMigrationOptions{}
	if len(options) == 1 {
		config = options[0]
	}
	now := config.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.Round(0).UTC()
	if now.UnixMilli() <= 0 {
		return petCronMigrationConfig{}, errors.New("pet cron migration now is invalid")
	}
	maxAttempts := config.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = PetSchedulerDefaultMaxAttempts
	}
	if maxAttempts < 1 || maxAttempts > PetSchedulerMaxAttempts {
		return petCronMigrationConfig{}, fmt.Errorf("pet cron migration max attempts must be between 1 and %d", PetSchedulerMaxAttempts)
	}
	timeZone := strings.TrimSpace(config.DefaultTimeZone)
	if timeZone == "" {
		timeZone = PetSchedulerDefaultTimeZone
	}
	if _, err := loadPetSchedulerLocation(timeZone); err != nil {
		return petCronMigrationConfig{}, err
	}
	return petCronMigrationConfig{now: now, maxAttempts: maxAttempts, defaultTimeZone: timeZone}, nil
}

type petCronSourceJob struct {
	id            string
	scheduleKind  sql.NullString
	scheduleAt    any
	scheduleEvery sql.NullInt64
	scheduleExpr  sql.NullString
	scheduleTZ    sql.NullString
	jobType       sql.NullString
	payloadJSON   sql.NullString
	enabled       sql.NullInt64
	deleteAfter   sql.NullInt64
	deletedAt     sql.NullInt64
	createdAt     sql.NullInt64
}

func scanPetCronSourceJob(scanner interface{ Scan(...any) error }) (petCronSourceJob, error) {
	var record petCronSourceJob
	var id sql.NullString
	if err := scanner.Scan(
		&id,
		&record.scheduleKind,
		&record.scheduleAt,
		&record.scheduleEvery,
		&record.scheduleExpr,
		&record.scheduleTZ,
		&record.jobType,
		&record.payloadJSON,
		&record.enabled,
		&record.deleteAfter,
		&record.deletedAt,
		&record.createdAt,
	); err != nil {
		return petCronSourceJob{}, err
	}
	if id.Valid {
		record.id = strings.TrimSpace(id.String)
	}
	return record, nil
}

func convertPetCronSourceJob(record petCronSourceJob, config petCronMigrationConfig) (PetScheduledJob, error) {
	if record.id == "" {
		return PetScheduledJob{}, errors.New("source job id is missing")
	}
	if err := validatePetSchedulerID(record.id, ErrPetSchedulerInvalidStepID); err != nil {
		return PetScheduledJob{}, fmt.Errorf("source job ID is not a valid target ID: %w", err)
	}
	if !record.scheduleKind.Valid || strings.TrimSpace(record.scheduleKind.String) == "" {
		return PetScheduledJob{}, errors.New("source schedule_kind is missing")
	}
	if !record.payloadJSON.Valid || strings.TrimSpace(record.payloadJSON.String) == "" {
		return PetScheduledJob{}, errors.New("source payload_json is missing")
	}
	if !record.createdAt.Valid || record.createdAt.Int64 <= 0 {
		return PetScheduledJob{}, errors.New("source created_at is missing or invalid")
	}

	// 先过目标已有校验，再把归一化后的结构交给 Enqueue；fail-closed 是为了
	// 绝不把源端脏 JSON 变成目标跨进程协议，避免执行器启动后才爆炸。
	payload, err := ValidatePetAutomationPayload(json.RawMessage(record.payloadJSON.String))
	if err != nil {
		return PetScheduledJob{}, fmt.Errorf("source payload_json is invalid: %w", err)
	}

	schedule, dueAt, err := convertPetCronSchedule(record, config)
	if err != nil {
		return PetScheduledJob{}, err
	}
	if dueAt.Before(config.now) {
		return PetScheduledJob{}, errors.New("source schedule is in the past; one-shot task was not retriggered")
	}
	if dueAt.After(config.now.Add(PetSchedulerMaxFuture)) {
		return PetScheduledJob{}, fmt.Errorf("source schedule is beyond target future limit of %s", PetSchedulerMaxFuture)
	}

	return PetScheduledJob{
		ID:          record.id,
		JobType:     PetAutomationJobType,
		PlanID:      payload.PlanID,
		StepID:      payload.StepID,
		Schedule:    schedule,
		Payload:     payload,
		CreatedAt:   time.UnixMilli(record.createdAt.Int64).UTC(),
		DueAt:       dueAt.UTC(),
		AvailableAt: dueAt.UTC(),
		MaxAttempts: config.maxAttempts,
	}, nil
}

func convertPetCronSchedule(record petCronSourceJob, config petCronMigrationConfig) (PetPlanSchedule, time.Time, error) {
	kind := strings.ToLower(strings.TrimSpace(record.scheduleKind.String))
	timeZone := strings.TrimSpace(record.scheduleTZ.String)
	if timeZone == "" {
		timeZone = config.defaultTimeZone
	}

	switch PetPlanScheduleKind(kind) {
	case PetPlanScheduleAt:
		at, err := petCronScheduleAtRaw(record.scheduleAt)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, err
		}
		loc, err := loadPetSchedulerLocation(timeZone)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, fmt.Errorf("source at timezone is invalid: %w", err)
		}
		dueAt, err := parsePetSchedulerAt(at, loc)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, fmt.Errorf("source schedule_at is invalid: %w", err)
		}
		return PetPlanSchedule{Kind: PetPlanScheduleAt, At: at, TZ: timeZone}, dueAt.UTC(), nil

	case PetPlanScheduleEvery:
		if !record.scheduleEvery.Valid {
			return PetPlanSchedule{}, time.Time{}, errors.New("source schedule_every is missing")
		}
		if record.scheduleEvery.Int64 < PetPlanMinIntervalMS || record.scheduleEvery.Int64 > PetPlanMaxIntervalMS {
			return PetPlanSchedule{}, time.Time{}, fmt.Errorf("source schedule_every must be between %d and %d milliseconds", PetPlanMinIntervalMS, PetPlanMaxIntervalMS)
		}
		if record.deleteAfter.Valid && record.deleteAfter.Int64 == 1 {
			return PetPlanSchedule{}, time.Time{}, errors.New("delete_after_run cannot be represented for a recurring every schedule")
		}
		dueAt := config.now.Add(time.Duration(record.scheduleEvery.Int64) * time.Millisecond).UTC()
		return PetPlanSchedule{Kind: PetPlanScheduleEvery, EveryMS: record.scheduleEvery.Int64}, dueAt, nil

	case PetPlanScheduleCron:
		expr := strings.TrimSpace(record.scheduleExpr.String)
		if expr == "" {
			return PetPlanSchedule{}, time.Time{}, errors.New("source schedule_expr is missing")
		}
		if record.deleteAfter.Valid && record.deleteAfter.Int64 == 1 {
			return PetPlanSchedule{}, time.Time{}, errors.New("delete_after_run cannot be represented for a recurring cron schedule")
		}
		loc, err := loadPetSchedulerLocation(timeZone)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, fmt.Errorf("source cron timezone is invalid: %w", err)
		}
		parsed, err := parsePetSchedulerCron(expr)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, fmt.Errorf("source schedule_expr is invalid: %w", err)
		}
		dueAt, err := parsed.next(config.now, loc)
		if err != nil {
			return PetPlanSchedule{}, time.Time{}, fmt.Errorf("source cron has no next occurrence: %w", err)
		}
		return PetPlanSchedule{Kind: PetPlanScheduleCron, Expr: expr, TZ: timeZone}, dueAt.UTC(), nil

	default:
		return PetPlanSchedule{}, time.Time{}, fmt.Errorf("source schedule_kind %q is unsupported; only at/every/cron are migrated", record.scheduleKind.String)
	}
}

func petCronScheduleAtRaw(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, errors.New("source schedule_at is missing")
	}
	if raw, ok := value.([]byte); ok {
		if len(bytesTrimSpace(raw)) == 0 {
			return nil, errors.New("source schedule_at is empty")
		}
		return json.RawMessage(bytesTrimSpace(raw)), nil
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, errors.New("source schedule_at is empty")
		}
		encoded, err := json.Marshal(text)
		if err != nil {
			return nil, fmt.Errorf("encode source schedule_at: %w", err)
		}
		return encoded, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode source schedule_at: %w", err)
	}
	return encoded, nil
}

func bytesTrimSpace(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func petCronMigrationTargetIDs(ctx context.Context, store JobStore) (map[string]struct{}, error) {
	lister, ok := store.(PetCronMigrationJobIDLister)
	if !ok {
		// 自定义 JobStore 若没有列举能力，JobStore 契约本身无法证明重启幂等；
		// 仍允许迁移，但只把冲突交给 Enqueue 的原子约束处理。
		return make(map[string]struct{}), nil
	}
	ids, err := lister.PetCronMigrationJobIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect target pet job IDs: %w", err)
	}
	known := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		known[id] = struct{}{}
	}
	return known, nil
}

func resolvePetCronSourceDB(sourceRoot string) (string, error) {
	if strings.TrimSpace(sourceRoot) == "" {
		return "", errors.New("OpenCowork source root is required")
	}
	root, err := filepath.Abs(filepath.Clean(sourceRoot))
	if err != nil {
		return "", fmt.Errorf("resolve OpenCowork source root: %w", err)
	}
	return filepath.Join(root, "data.db"), nil
}

func petCronReadOnlySQLiteDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?mode=ro"
}

func isPetCronMissingTableError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

func isPetCronDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPetSQLiteJobStoreDuplicateJob) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate") || strings.Contains(message, "unique constraint")
}

func addPetCronMigrationDiagnostic(report *PetCronMigrationReport, source, id, reason string) {
	report.Diagnostics = append(report.Diagnostics, PetCronMigrationDiagnostic{Source: source, ID: id, Reason: reason})
}

var _ PetCronMigrationJobIDLister = (*PetSQLiteJobStore)(nil)
