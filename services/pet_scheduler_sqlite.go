package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrPetSQLiteJobStoreNilDB          = errors.New("pet sqlite job store database is nil")
	ErrPetSQLiteJobStoreInvalidContext = errors.New("pet sqlite job store context is invalid")
	ErrPetSQLiteJobStoreInvalidJob     = errors.New("pet sqlite job is invalid")
	ErrPetSQLiteJobStoreDuplicateJob   = errors.New("pet sqlite job already exists")
	ErrPetSQLiteJobStoreJobNotFound    = errors.New("pet sqlite job was not found")
	ErrPetSQLiteJobStoreJobExpired     = errors.New("pet sqlite job has expired")
	ErrPetSQLiteJobStoreLeaseInvalid   = errors.New("pet sqlite job lease is invalid")
	ErrPetSQLiteJobStoreLeaseExpired   = errors.New("pet sqlite job lease has expired")
	ErrPetSQLiteJobStoreInvalidState   = errors.New("pet sqlite job state transition is invalid")
)

const (
	petSQLiteJobTable = "pet_scheduler_jobs"

	petSQLiteJobStatePending   = "pending"
	petSQLiteJobStateLeased    = "leased"
	petSQLiteJobStateCompleted = "completed"
	petSQLiteJobStateFailed    = "failed"

	petSQLiteJobSchema = `
CREATE TABLE IF NOT EXISTS pet_scheduler_jobs (
    id TEXT PRIMARY KEY,
    job_type TEXT NOT NULL,
    plan_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    schedule_json TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    due_at INTEGER NOT NULL,
    available_at INTEGER NOT NULL,
    expires_at INTEGER,
    max_attempts INTEGER NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending',
    lease_token TEXT,
    lease_until INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    CHECK (state IN ('pending', 'leased', 'completed', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_pet_scheduler_jobs_due
    ON pet_scheduler_jobs(state, available_at, due_at, expires_at);

CREATE INDEX IF NOT EXISTS idx_pet_scheduler_jobs_lease
    ON pet_scheduler_jobs(state, lease_until);
`

	petSQLiteJobColumns = `
    id,
    job_type,
    plan_id,
    step_id,
    schedule_json,
    payload_json,
    created_at,
    due_at,
    available_at,
    expires_at,
    max_attempts,
    attempt,
    state,
    lease_token,
    lease_until,
    last_error
`
)

// PetSQLiteJobStore 将调度器状态和租约状态放在同一张专用表中，避免依赖
// PetDAO 的共享 schema。时间列统一使用 Unix milliseconds，便于 SQLite 做边界比较。
type PetSQLiteJobStore struct {
	db *sql.DB
}

var _ JobStore = (*PetSQLiteJobStore)(nil)

type petSQLiteEncodedJob struct {
	jobType      string
	planID       string
	stepID       string
	scheduleJSON string
	payloadJSON  string
	createdAt    int64
	dueAt        int64
	availableAt  int64
	expiresAt    any
	maxAttempts  int
	id           string
}

type petSQLiteJobRecord struct {
	job        PetScheduledJob
	state      string
	attempt    int
	leaseToken sql.NullString
	leaseUntil sql.NullInt64
}

// NewPetSQLiteJobStore 创建调度器专用表和索引。CREATE IF NOT EXISTS 让应用重启、
// 多次构造 store 以及测试重复初始化都不会重复建表，也不会要求调用方先跑全局迁移。
func NewPetSQLiteJobStore(db *sql.DB) (*PetSQLiteJobStore, error) {
	if db == nil {
		return nil, ErrPetSQLiteJobStoreNilDB
	}
	if _, err := db.ExecContext(context.Background(), petSQLiteJobSchema); err != nil {
		return nil, fmt.Errorf("create %s schema: %w", petSQLiteJobTable, err)
	}
	return &PetSQLiteJobStore{db: db}, nil
}

func (s *PetSQLiteJobStore) validateContext(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrPetSQLiteJobStoreNilDB
	}
	if ctx == nil {
		return ErrPetSQLiteJobStoreInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Enqueue 先完整编码和校验整个批次，再在单个事务中插入。任何一个 job
// 失败都会回滚前面的插入，避免计划只持久化一半而在恢复后变成残缺计划。
func (s *PetSQLiteJobStore) Enqueue(ctx context.Context, jobs []PetScheduledJob) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}

	encoded := make([]petSQLiteEncodedJob, 0, len(jobs))
	seen := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		if _, exists := seen[job.ID]; exists {
			return fmt.Errorf("%w: job %q is duplicated in the batch", ErrPetSQLiteJobStoreDuplicateJob, job.ID)
		}
		seen[job.ID] = struct{}{}

		item, err := encodePetSQLiteJob(job)
		if err != nil {
			return err
		}
		encoded = append(encoded, item)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin enqueue transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const insertSQL = `
INSERT INTO pet_scheduler_jobs (
    id, job_type, plan_id, step_id, schedule_json, payload_json,
    created_at, due_at, available_at, expires_at, max_attempts,
    attempt, state, lease_token, lease_until, last_error
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'pending', NULL, NULL, '')`

	for _, item := range encoded {
		if _, err := tx.ExecContext(ctx, insertSQL,
			item.id,
			item.jobType,
			item.planID,
			item.stepID,
			item.scheduleJSON,
			item.payloadJSON,
			item.createdAt,
			item.dueAt,
			item.availableAt,
			item.expiresAt,
			item.maxAttempts,
		); err != nil {
			if isPetSQLiteUniqueError(err) {
				return fmt.Errorf("%w: job %q", ErrPetSQLiteJobStoreDuplicateJob, item.id)
			}
			return fmt.Errorf("enqueue pet job %q: %w", item.id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit enqueue transaction: %w", err)
	}
	return nil
}

// Cancel 将尚未完成的 job 推进到终态，作为 PetSchedulerAPI 的取消 owner。
// 调度表当前只需要区分“可再次执行”和“不可再次执行”，因此复用 failed 终态并写入
// 固定原因；这样无需引入第二套 cancelled schema，也能保证 pending/leased job 不会
// 在下次心跳重新被 Due 选出。已经完成或不存在的 job 返回 false，保持幂等语义。
func (s *PetSQLiteJobStore) Cancel(ctx context.Context, request PetSchedulerCancelRequest) (bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return false, err
	}
	planID := strings.TrimSpace(request.PlanID)
	jobID := strings.TrimSpace(request.JobID)
	if (planID == "") == (jobID == "") {
		return false, fmt.Errorf("%w: exactly one of planId or jobId is required", ErrPetSQLiteJobStoreInvalidJob)
	}
	if planID != "" {
		if err := validatePetSchedulerID(planID, ErrPetSchedulerInvalidPlanID); err != nil {
			return false, err
		}
	} else if err := validatePetSchedulerID(jobID, ErrPetSchedulerInvalidStepID); err != nil {
		return false, err
	}

	where, target := "plan_id = ?", planID
	if jobID != "" {
		where, target = "id = ?", jobID
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE pet_scheduler_jobs
SET state = 'failed', lease_token = NULL, lease_until = NULL, last_error = 'cancelled'
WHERE `+where+` AND state IN ('pending', 'leased')
`, target)
	if err != nil {
		return false, fmt.Errorf("cancel pet scheduler job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check cancelled pet scheduler job: %w", err)
	}
	return rows > 0, nil
}

// Due 只返回已经满足逻辑到期、重试可用且未过期的记录。已失效的租约也会
// 被返回给 Claim，从而允许新 worker 在进程崩溃后接管；有效租约则直接排除。
func (s *PetSQLiteJobStore) Due(ctx context.Context, now time.Time, limit int) ([]PetScheduledJob, error) {
	if err := s.validateContext(ctx); err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, fmt.Errorf("%w: due limit must be positive", ErrPetSQLiteJobStoreInvalidState)
	}
	nowMS, err := petSQLiteRequiredMillis(now, "now")
	if err != nil {
		return nil, err
	}

	query := `SELECT ` + petSQLiteJobColumns + `
FROM pet_scheduler_jobs
WHERE state IN ('pending', 'leased')
  AND due_at <= ?
  AND available_at <= ?
  AND (expires_at IS NULL OR expires_at > ?)
  AND (lease_until IS NULL OR lease_until <= ?)
ORDER BY available_at ASC, due_at ASC, created_at ASC, id ASC
LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, nowMS, nowMS, nowMS, nowMS, limit)
	if err != nil {
		return nil, fmt.Errorf("list due pet jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]PetScheduledJob, 0)
	for rows.Next() {
		record, err := scanPetSQLiteJobRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("decode due pet job: %w", err)
		}
		jobs = append(jobs, record.job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due pet jobs: %w", err)
	}
	return jobs, nil
}

// Claim 在事务中先读取候选，再用带状态、时间和租约条件的 UPDATE 完成抢占。
// SQLite 的写事务会串行化竞争 worker，attempt 与 lease_token 因此和抢占动作
// 一起提交，不会出现“拿到 token 但 attempt 没加一”的半完成状态。
func (s *PetSQLiteJobStore) Claim(ctx context.Context, jobID, token string, now, leaseUntil time.Time) (PetJobLease, bool, error) {
	if err := s.validateContext(ctx); err != nil {
		return PetJobLease{}, false, err
	}
	if strings.TrimSpace(jobID) == "" {
		return PetJobLease{}, false, fmt.Errorf("%w: job id is empty", ErrPetSQLiteJobStoreInvalidJob)
	}
	if strings.TrimSpace(token) == "" {
		return PetJobLease{}, false, fmt.Errorf("%w: lease token is empty", ErrPetSQLiteJobStoreLeaseInvalid)
	}
	nowMS, err := petSQLiteRequiredMillis(now, "now")
	if err != nil {
		return PetJobLease{}, false, err
	}
	leaseUntilMS, err := petSQLiteRequiredMillis(leaseUntil, "leaseUntil")
	if err != nil {
		return PetJobLease{}, false, err
	}
	if leaseUntilMS <= nowMS {
		return PetJobLease{}, false, fmt.Errorf("%w: leaseUntil must be after now", ErrPetSQLiteJobStoreLeaseInvalid)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PetJobLease{}, false, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	record, err := loadPetSQLiteJobRecord(ctx, tx, jobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PetJobLease{}, false, nil
		}
		return PetJobLease{}, false, fmt.Errorf("load pet job %q for claim: %w", jobID, err)
	}
	if record.job.ExpiresAt != nil && record.job.ExpiresAt.UnixMilli() <= nowMS {
		return PetJobLease{}, false, fmt.Errorf("%w: job %q", ErrPetSQLiteJobStoreJobExpired, jobID)
	}
	if record.state == petSQLiteJobStateCompleted || record.state == petSQLiteJobStateFailed {
		return PetJobLease{}, false, nil
	}
	if record.state != petSQLiteJobStatePending && record.state != petSQLiteJobStateLeased {
		return PetJobLease{}, false, fmt.Errorf("%w: job %q has state %q", ErrPetSQLiteJobStoreInvalidState, jobID, record.state)
	}
	if record.job.DueAt.UnixMilli() > nowMS || record.job.AvailableAt.UnixMilli() > nowMS {
		return PetJobLease{}, false, nil
	}
	if record.leaseUntil.Valid && record.leaseUntil.Int64 > nowMS {
		return PetJobLease{}, false, nil
	}
	if record.attempt < 0 || record.attempt == int(^uint(0)>>1) {
		return PetJobLease{}, false, fmt.Errorf("%w: job %q attempt is invalid", ErrPetSQLiteJobStoreInvalidJob, jobID)
	}
	newAttempt := record.attempt + 1

	const updateSQL = `
UPDATE pet_scheduler_jobs
SET state = 'leased', lease_token = ?, lease_until = ?, attempt = ?, last_error = ''
WHERE id = ?
  AND state IN ('pending', 'leased')
  AND due_at <= ?
  AND available_at <= ?
  AND (expires_at IS NULL OR expires_at > ?)
  AND (lease_until IS NULL OR lease_until <= ?)`
	result, err := tx.ExecContext(ctx, updateSQL, token, leaseUntilMS, newAttempt, jobID, nowMS, nowMS, nowMS, nowMS)
	if err != nil {
		return PetJobLease{}, false, fmt.Errorf("claim pet job %q: %w", jobID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return PetJobLease{}, false, fmt.Errorf("check claim for pet job %q: %w", jobID, err)
	}
	if rowsAffected != 1 {
		return PetJobLease{}, false, nil
	}

	if err := tx.Commit(); err != nil {
		return PetJobLease{}, false, fmt.Errorf("commit claim for pet job %q: %w", jobID, err)
	}
	return PetJobLease{
		Job:        record.job,
		Token:      token,
		Attempt:    newAttempt,
		LeaseUntil: time.UnixMilli(leaseUntilMS).UTC(),
	}, true, nil
}

// Complete 用 lease token 做 compare-and-set。nextDue 不为空时代表当前
// occurrence 成功完成并进入下一次 occurrence，attempt 必须重置；为空则进入
// completed 终态，防止历史 job 在重启后再次被 Due 返回。
func (s *PetSQLiteJobStore) Complete(ctx context.Context, lease PetJobLease, now time.Time, nextDue *time.Time) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	nowMS, err := petSQLiteRequiredMillis(now, "now")
	if err != nil {
		return err
	}
	id, token, err := validatePetSQLiteLeaseIdentity(lease)
	if err != nil {
		return err
	}

	var nextDueMS int64
	if nextDue != nil {
		nextDueMS, err = petSQLiteRequiredMillis(*nextDue, "nextDue")
		if err != nil {
			return err
		}
		if nextDueMS <= nowMS {
			return fmt.Errorf("%w: nextDue must be after now", ErrPetSQLiteJobStoreInvalidState)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	record, err := loadPetSQLiteJobRecord(ctx, tx, id)
	if err != nil {
		return normalizePetSQLiteLeaseLoadError(id, err)
	}
	if err := validatePetSQLiteActiveLease(record, token, nowMS); err != nil {
		return err
	}
	if nextDueMS != 0 && record.job.ExpiresAt != nil && nextDueMS >= record.job.ExpiresAt.UnixMilli() {
		return fmt.Errorf("%w: nextDue is not before job expiration", ErrPetSQLiteJobStoreJobExpired)
	}

	var result sql.Result
	if nextDueMS != 0 {
		const rescheduleSQL = `
UPDATE pet_scheduler_jobs
SET state = 'pending', due_at = ?, available_at = ?, attempt = 0,
    lease_token = NULL, lease_until = NULL, last_error = ''
WHERE id = ? AND state = 'leased' AND lease_token = ? AND lease_until > ?`
		result, err = tx.ExecContext(ctx, rescheduleSQL, nextDueMS, nextDueMS, id, token, nowMS)
	} else {
		const completeSQL = `
UPDATE pet_scheduler_jobs
SET state = 'completed', lease_token = NULL, lease_until = NULL, last_error = ''
WHERE id = ? AND state = 'leased' AND lease_token = ? AND lease_until > ?`
		result, err = tx.ExecContext(ctx, completeSQL, id, token, nowMS)
	}
	if err != nil {
		return fmt.Errorf("complete pet job %q: %w", id, err)
	}
	if rowsAffected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check complete for pet job %q: %w", id, err)
	} else if rowsAffected != 1 {
		return fmt.Errorf("%w: lease changed while completing job %q", ErrPetSQLiteJobStoreLeaseInvalid, id)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit complete for pet job %q: %w", id, err)
	}
	return nil
}

// Fail 同样以 token 做 compare-and-set。retryAt 只推进 AvailableAt 并保留
// DueAt，nextDue 则开启新的 occurrence 并重置 attempt；两个都为空时写入失败
// 终态。这样重试不会改变 reminder 的 occurrence ID，周期调度也不会继承旧次数。
func (s *PetSQLiteJobStore) Fail(ctx context.Context, lease PetJobLease, now time.Time, retryAt *time.Time, nextDue *time.Time, reason string) error {
	if err := s.validateContext(ctx); err != nil {
		return err
	}
	nowMS, err := petSQLiteRequiredMillis(now, "now")
	if err != nil {
		return err
	}
	id, token, err := validatePetSQLiteLeaseIdentity(lease)
	if err != nil {
		return err
	}
	if retryAt != nil && nextDue != nil {
		return fmt.Errorf("%w: retryAt and nextDue cannot both be set", ErrPetSQLiteJobStoreInvalidState)
	}

	var retryAtMS, nextDueMS int64
	if retryAt != nil {
		retryAtMS, err = petSQLiteRequiredMillis(*retryAt, "retryAt")
		if err != nil {
			return err
		}
		if retryAtMS <= nowMS {
			return fmt.Errorf("%w: retryAt must be after now", ErrPetSQLiteJobStoreInvalidState)
		}
	}
	if nextDue != nil {
		nextDueMS, err = petSQLiteRequiredMillis(*nextDue, "nextDue")
		if err != nil {
			return err
		}
		if nextDueMS <= nowMS {
			return fmt.Errorf("%w: nextDue must be after now", ErrPetSQLiteJobStoreInvalidState)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fail transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	record, err := loadPetSQLiteJobRecord(ctx, tx, id)
	if err != nil {
		return normalizePetSQLiteLeaseLoadError(id, err)
	}
	if err := validatePetSQLiteActiveLease(record, token, nowMS); err != nil {
		return err
	}
	if retryAtMS != 0 && record.job.ExpiresAt != nil && retryAtMS >= record.job.ExpiresAt.UnixMilli() {
		return fmt.Errorf("%w: retryAt is not before job expiration", ErrPetSQLiteJobStoreJobExpired)
	}
	if nextDueMS != 0 && record.job.ExpiresAt != nil && nextDueMS >= record.job.ExpiresAt.UnixMilli() {
		return fmt.Errorf("%w: nextDue is not before job expiration", ErrPetSQLiteJobStoreJobExpired)
	}

	var result sql.Result
	switch {
	case retryAtMS != 0:
		const retrySQL = `
UPDATE pet_scheduler_jobs
SET state = 'pending', available_at = ?, lease_token = NULL, lease_until = NULL, last_error = ?
WHERE id = ? AND state = 'leased' AND lease_token = ? AND lease_until > ?`
		result, err = tx.ExecContext(ctx, retrySQL, retryAtMS, reason, id, token, nowMS)
	case nextDueMS != 0:
		const rescheduleSQL = `
UPDATE pet_scheduler_jobs
SET state = 'pending', due_at = ?, available_at = ?, attempt = 0,
    lease_token = NULL, lease_until = NULL, last_error = ?
WHERE id = ? AND state = 'leased' AND lease_token = ? AND lease_until > ?`
		result, err = tx.ExecContext(ctx, rescheduleSQL, nextDueMS, nextDueMS, reason, id, token, nowMS)
	default:
		const terminalSQL = `
UPDATE pet_scheduler_jobs
SET state = 'failed', lease_token = NULL, lease_until = NULL, last_error = ?
WHERE id = ? AND state = 'leased' AND lease_token = ? AND lease_until > ?`
		result, err = tx.ExecContext(ctx, terminalSQL, reason, id, token, nowMS)
	}
	if err != nil {
		return fmt.Errorf("fail pet job %q: %w", id, err)
	}
	if rowsAffected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("check fail for pet job %q: %w", id, err)
	} else if rowsAffected != 1 {
		return fmt.Errorf("%w: lease changed while failing job %q", ErrPetSQLiteJobStoreLeaseInvalid, id)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fail for pet job %q: %w", id, err)
	}
	return nil
}

func encodePetSQLiteJob(job PetScheduledJob) (petSQLiteEncodedJob, error) {
	if strings.TrimSpace(job.ID) == "" {
		return petSQLiteEncodedJob{}, fmt.Errorf("%w: job id is empty", ErrPetSQLiteJobStoreInvalidJob)
	}
	if job.MaxAttempts < 1 {
		return petSQLiteEncodedJob{}, fmt.Errorf("%w: job %q maxAttempts must be positive", ErrPetSQLiteJobStoreInvalidJob, job.ID)
	}

	createdAt, err := petSQLiteRequiredMillis(job.CreatedAt, "createdAt")
	if err != nil {
		return petSQLiteEncodedJob{}, fmt.Errorf("job %q: %w", job.ID, err)
	}
	dueAt, err := petSQLiteRequiredMillis(job.DueAt, "dueAt")
	if err != nil {
		return petSQLiteEncodedJob{}, fmt.Errorf("job %q: %w", job.ID, err)
	}
	availableAt, err := petSQLiteRequiredMillis(job.AvailableAt, "availableAt")
	if err != nil {
		return petSQLiteEncodedJob{}, fmt.Errorf("job %q: %w", job.ID, err)
	}

	scheduleJSON, err := json.Marshal(job.Schedule)
	if err != nil {
		return petSQLiteEncodedJob{}, fmt.Errorf("marshal schedule for job %q: %w", job.ID, err)
	}
	payloadJSON, err := json.Marshal(job.Payload)
	if err != nil {
		return petSQLiteEncodedJob{}, fmt.Errorf("marshal payload for job %q: %w", job.ID, err)
	}

	var expiresAt any
	if job.ExpiresAt != nil {
		expiresAt, err = petSQLiteRequiredMillis(*job.ExpiresAt, "expiresAt")
		if err != nil {
			return petSQLiteEncodedJob{}, fmt.Errorf("job %q: %w", job.ID, err)
		}
	}
	return petSQLiteEncodedJob{
		id:           job.ID,
		jobType:      job.JobType,
		planID:       job.PlanID,
		stepID:       job.StepID,
		scheduleJSON: string(scheduleJSON),
		payloadJSON:  string(payloadJSON),
		createdAt:    createdAt,
		dueAt:        dueAt,
		availableAt:  availableAt,
		expiresAt:    expiresAt,
		maxAttempts:  job.MaxAttempts,
	}, nil
}

func petSQLiteRequiredMillis(value time.Time, field string) (int64, error) {
	if value.IsZero() {
		return 0, fmt.Errorf("%w: %s is zero", ErrPetSQLiteJobStoreInvalidJob, field)
	}
	return value.UnixMilli(), nil
}

func validatePetSQLiteLeaseIdentity(lease PetJobLease) (string, string, error) {
	id := lease.Job.ID
	if strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("%w: lease job id is empty", ErrPetSQLiteJobStoreLeaseInvalid)
	}
	token := lease.Token
	if strings.TrimSpace(token) == "" {
		return "", "", fmt.Errorf("%w: lease token is empty", ErrPetSQLiteJobStoreLeaseInvalid)
	}
	return id, token, nil
}

func validatePetSQLiteActiveLease(record petSQLiteJobRecord, token string, nowMS int64) error {
	if record.state != petSQLiteJobStateLeased {
		return fmt.Errorf("%w: job %q is not leased", ErrPetSQLiteJobStoreLeaseInvalid, record.job.ID)
	}
	if !record.leaseToken.Valid || record.leaseToken.String != token {
		return fmt.Errorf("%w: token does not own job %q", ErrPetSQLiteJobStoreLeaseInvalid, record.job.ID)
	}
	if !record.leaseUntil.Valid {
		return fmt.Errorf("%w: job %q has no lease deadline", ErrPetSQLiteJobStoreLeaseInvalid, record.job.ID)
	}
	if record.leaseUntil.Int64 <= nowMS {
		return fmt.Errorf("%w: job %q", ErrPetSQLiteJobStoreLeaseExpired, record.job.ID)
	}
	return nil
}

func normalizePetSQLiteLeaseLoadError(jobID string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: job %q", ErrPetSQLiteJobStoreJobNotFound, jobID)
	}
	return fmt.Errorf("load pet job %q for lease transition: %w", jobID, err)
}

func loadPetSQLiteJobRecord(ctx context.Context, tx *sql.Tx, jobID string) (petSQLiteJobRecord, error) {
	query := `SELECT ` + petSQLiteJobColumns + ` FROM pet_scheduler_jobs WHERE id = ?`
	return scanPetSQLiteJobRecord(tx.QueryRowContext(ctx, query, jobID))
}

type petSQLiteScanner interface {
	Scan(dest ...any) error
}

func scanPetSQLiteJobRecord(scanner petSQLiteScanner) (petSQLiteJobRecord, error) {
	var (
		id, jobType, planID, stepID string
		scheduleJSON, payloadJSON   string
		createdAt, dueAt            int64
		availableAt                 int64
		expiresAt                   sql.NullInt64
		maxAttempts, attempt        int
		state                       string
		leaseToken                  sql.NullString
		leaseUntil                  sql.NullInt64
		lastError                   string
	)
	if err := scanner.Scan(
		&id,
		&jobType,
		&planID,
		&stepID,
		&scheduleJSON,
		&payloadJSON,
		&createdAt,
		&dueAt,
		&availableAt,
		&expiresAt,
		&maxAttempts,
		&attempt,
		&state,
		&leaseToken,
		&leaseUntil,
		&lastError,
	); err != nil {
		return petSQLiteJobRecord{}, err
	}
	if strings.TrimSpace(id) == "" || maxAttempts < 1 || attempt < 0 {
		return petSQLiteJobRecord{}, fmt.Errorf("%w: stored job %q has invalid identity or attempt", ErrPetSQLiteJobStoreInvalidJob, id)
	}
	switch state {
	case petSQLiteJobStatePending, petSQLiteJobStateLeased, petSQLiteJobStateCompleted, petSQLiteJobStateFailed:
	default:
		return petSQLiteJobRecord{}, fmt.Errorf("%w: stored job %q has state %q", ErrPetSQLiteJobStoreInvalidState, id, state)
	}
	if strings.TrimSpace(scheduleJSON) == "" || strings.TrimSpace(scheduleJSON) == "null" {
		return petSQLiteJobRecord{}, fmt.Errorf("%w: job %q schedule_json is empty", ErrPetSQLiteJobStoreInvalidJob, id)
	}
	if strings.TrimSpace(payloadJSON) == "" || strings.TrimSpace(payloadJSON) == "null" {
		return petSQLiteJobRecord{}, fmt.Errorf("%w: job %q payload_json is empty", ErrPetSQLiteJobStoreInvalidJob, id)
	}

	var schedule PetPlanSchedule
	if err := json.Unmarshal([]byte(scheduleJSON), &schedule); err != nil {
		return petSQLiteJobRecord{}, fmt.Errorf("%w: job %q schedule_json: %v", ErrPetSQLiteJobStoreInvalidJob, id, err)
	}
	var payload PetAutomationJobPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return petSQLiteJobRecord{}, fmt.Errorf("%w: job %q payload_json: %v", ErrPetSQLiteJobStoreInvalidJob, id, err)
	}

	job := PetScheduledJob{
		ID:          id,
		JobType:     jobType,
		PlanID:      planID,
		StepID:      stepID,
		Schedule:    schedule,
		Payload:     payload,
		CreatedAt:   time.UnixMilli(createdAt).UTC(),
		DueAt:       time.UnixMilli(dueAt).UTC(),
		AvailableAt: time.UnixMilli(availableAt).UTC(),
		MaxAttempts: maxAttempts,
	}
	if expiresAt.Valid {
		expires := time.UnixMilli(expiresAt.Int64).UTC()
		job.ExpiresAt = &expires
	}
	return petSQLiteJobRecord{
		job:        job,
		state:      state,
		attempt:    attempt,
		leaseToken: leaseToken,
		leaseUntil: leaseUntil,
	}, nil
}

func isPetSQLiteUniqueError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed")
}
