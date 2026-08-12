package services

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestPetSQLiteJobStoreCreateTableIdempotent(t *testing.T) {
	db := openPetSQLiteTestDB(t, ":memory:")
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		t.Fatalf("首次创建 job store 失败: %v", err)
	}
	if _, err := NewPetSQLiteJobStore(db); err != nil {
		t.Fatalf("重复创建 job store 失败: %v", err)
	}
	if store == nil {
		t.Fatal("首次创建没有返回 store")
	}

	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, petSQLiteJobTable).Scan(&tableCount); err != nil {
		t.Fatalf("查询 job 表失败: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("job 表数量=%d，期望 1", tableCount)
	}

	var indexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('idx_pet_scheduler_jobs_due', 'idx_pet_scheduler_jobs_lease')`).Scan(&indexCount); err != nil {
		t.Fatalf("查询 job 索引失败: %v", err)
	}
	if indexCount != 2 {
		t.Fatalf("job 索引数量=%d，期望 2", indexCount)
	}
}

func TestPetSQLiteJobStoreEnqueueClaimComplete(t *testing.T) {
	db := openPetSQLiteTestDB(t, ":memory:")
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		t.Fatalf("创建 job store 失败: %v", err)
	}
	now := petSQLiteTestTime()
	job := newPetSQLiteTestJob("job-complete", now)

	if err := store.Enqueue(context.Background(), []PetScheduledJob{job}); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	due, err := store.Due(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("读取 due 失败: %v", err)
	}
	if len(due) != 1 || !reflect.DeepEqual(due[0], job) {
		t.Fatalf("读取的 job 不匹配: got=%+v want=%+v", due, job)
	}

	lease, claimed, err := store.Claim(context.Background(), job.ID, "worker-a", now, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("抢租约失败: %v", err)
	}
	if !claimed {
		t.Fatal("due job 没有被抢到")
	}
	if lease.Attempt != 1 || lease.Token != "worker-a" || !lease.LeaseUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("租约字段错误: %+v", lease)
	}
	if _, claimed, err := store.Claim(context.Background(), job.ID, "worker-b", now, now.Add(2*time.Minute)); err != nil || claimed {
		t.Fatalf("有效租约期间不应再次抢占: claimed=%v err=%v", claimed, err)
	}

	if err := store.Complete(context.Background(), lease, now.Add(time.Second), nil); err != nil {
		t.Fatalf("完成 job 失败: %v", err)
	}
	if due, err := store.Due(context.Background(), now.Add(2*time.Second), 10); err != nil {
		t.Fatalf("完成后读取 due 失败: %v", err)
	} else if len(due) != 0 {
		t.Fatalf("已完成 job 仍被读取: %+v", due)
	}

	var state string
	if err := db.QueryRow(`SELECT state FROM pet_scheduler_jobs WHERE id = ?`, job.ID).Scan(&state); err != nil {
		t.Fatalf("读取终态失败: %v", err)
	}
	if state != petSQLiteJobStateCompleted {
		t.Fatalf("终态=%q，期望 %q", state, petSQLiteJobStateCompleted)
	}
}

func TestPetSQLiteJobStoreCompleteNextDueResetsAttempt(t *testing.T) {
	db := openPetSQLiteTestDB(t, ":memory:")
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		t.Fatalf("创建 job store 失败: %v", err)
	}
	now := petSQLiteTestTime()
	job := newPetSQLiteTestJob("job-complete-next", now)
	if err := store.Enqueue(context.Background(), []PetScheduledJob{job}); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	lease, claimed, err := store.Claim(context.Background(), job.ID, "worker", now, now.Add(time.Minute))
	if err != nil || !claimed || lease.Attempt != 1 {
		t.Fatalf("首次 claim 失败: lease=%+v claimed=%v err=%v", lease, claimed, err)
	}
	nextDue := now.Add(time.Minute)
	if err := store.Complete(context.Background(), lease, now, &nextDue); err != nil {
		t.Fatalf("Complete(nextDue) 失败: %v", err)
	}
	due, err := store.Due(context.Background(), nextDue, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("nextDue 到期后读取失败: due=%+v err=%v", due, err)
	}
	if !due[0].DueAt.Equal(nextDue) || !due[0].AvailableAt.Equal(nextDue) {
		t.Fatalf("Complete(nextDue) 时间错误: %+v", due[0])
	}

	lease, claimed, err = store.Claim(context.Background(), job.ID, "worker-next", nextDue, nextDue.Add(time.Minute))
	if err != nil || !claimed || lease.Attempt != 1 {
		t.Fatalf("next occurrence attempt 未重置: lease=%+v claimed=%v err=%v", lease, claimed, err)
	}
}

func TestPetSQLiteJobStoreEnqueueRollbackOnDuplicate(t *testing.T) {
	db := openPetSQLiteTestDB(t, ":memory:")
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		t.Fatalf("创建 job store 失败: %v", err)
	}
	now := petSQLiteTestTime()
	first := newPetSQLiteTestJob("job-existing", now)
	if err := store.Enqueue(context.Background(), []PetScheduledJob{first}); err != nil {
		t.Fatalf("准备已有 job 失败: %v", err)
	}

	second := newPetSQLiteTestJob("job-should-rollback", now)
	err = store.Enqueue(context.Background(), []PetScheduledJob{second, first})
	if !errors.Is(err, ErrPetSQLiteJobStoreDuplicateJob) {
		t.Fatalf("重复 job 错误不明确: %v", err)
	}
	due, err := store.Due(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("回滚后读取 due 失败: %v", err)
	}
	if len(due) != 1 || due[0].ID != first.ID {
		t.Fatalf("重复入队未完整回滚: %+v", due)
	}
}

func TestPetSQLiteJobStoreTokenPreventsOldWorker(t *testing.T) {
	db := openPetSQLiteTestDB(t, ":memory:")
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		t.Fatalf("创建 job store 失败: %v", err)
	}
	now := petSQLiteTestTime()
	job := newPetSQLiteTestJob("job-token", now)
	if err := store.Enqueue(context.Background(), []PetScheduledJob{job}); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	oldLease, claimed, err := store.Claim(context.Background(), job.ID, "old-worker", now, now.Add(time.Second))
	if err != nil || !claimed {
		t.Fatalf("旧 worker 抢租约失败: claimed=%v err=%v", claimed, err)
	}
	newNow := now.Add(2 * time.Second)
	newLease, claimed, err := store.Claim(context.Background(), job.ID, "new-worker", newNow, newNow.Add(time.Minute))
	if err != nil || !claimed || newLease.Attempt != 2 {
		t.Fatalf("新 worker 接管失败: lease=%+v claimed=%v err=%v", newLease, claimed, err)
	}

	err = store.Complete(context.Background(), oldLease, newNow, nil)
	if !errors.Is(err, ErrPetSQLiteJobStoreLeaseInvalid) {
		t.Fatalf("旧 token 未被拒绝: %v", err)
	}
	if err := store.Complete(context.Background(), newLease, newNow, nil); err != nil {
		t.Fatalf("新 token 完成 job 失败: %v", err)
	}
}

func TestPetSQLiteJobStoreFailRetryAndNextDue(t *testing.T) {
	db := openPetSQLiteTestDB(t, ":memory:")
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		t.Fatalf("创建 job store 失败: %v", err)
	}
	now := petSQLiteTestTime()
	job := newPetSQLiteTestJob("job-retry", now)
	if err := store.Enqueue(context.Background(), []PetScheduledJob{job}); err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	lease, claimed, err := store.Claim(context.Background(), job.ID, "worker-1", now, now.Add(10*time.Minute))
	if err != nil || !claimed {
		t.Fatalf("首次抢租约失败: claimed=%v err=%v", claimed, err)
	}
	retryAt := now.Add(2 * time.Second)
	if err := store.Fail(context.Background(), lease, now.Add(time.Second), &retryAt, nil, "temporary failure"); err != nil {
		t.Fatalf("写入 retry 状态失败: %v", err)
	}
	if due, err := store.Due(context.Background(), now.Add(time.Second), 10); err != nil {
		t.Fatalf("retry 前读取 due 失败: %v", err)
	} else if len(due) != 0 {
		t.Fatalf("retry 尚未到期却被读取: %+v", due)
	}
	due, err := store.Due(context.Background(), retryAt, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("retry 到期后读取失败: due=%+v err=%v", due, err)
	}
	if !due[0].DueAt.Equal(now) || !due[0].AvailableAt.Equal(retryAt) {
		t.Fatalf("retry 错误地改写了 occurrence 时间: %+v", due[0])
	}

	lease, claimed, err = store.Claim(context.Background(), job.ID, "worker-2", retryAt, retryAt.Add(10*time.Minute))
	if err != nil || !claimed || lease.Attempt != 2 {
		t.Fatalf("retry claim 错误: lease=%+v claimed=%v err=%v", lease, claimed, err)
	}
	nextDue := retryAt.Add(time.Minute)
	if err := store.Fail(context.Background(), lease, retryAt, nil, &nextDue, "reschedule"); err != nil {
		t.Fatalf("写入 nextDue 状态失败: %v", err)
	}
	if due, err := store.Due(context.Background(), nextDue.Add(-time.Millisecond), 10); err != nil {
		t.Fatalf("nextDue 前读取失败: %v", err)
	} else if len(due) != 0 {
		t.Fatalf("nextDue 尚未到期却被读取: %+v", due)
	}
	if due, err := store.Due(context.Background(), nextDue, 10); err != nil {
		t.Fatalf("nextDue 到期读取失败: %v", err)
	} else if len(due) != 1 {
		t.Fatalf("nextDue 到期没有读取到 job: %+v", due)
	}

	lease, claimed, err = store.Claim(context.Background(), job.ID, "worker-3", nextDue, nextDue.Add(10*time.Minute))
	if err != nil || !claimed || lease.Attempt != 1 {
		t.Fatalf("新 occurrence 未重置 attempt: lease=%+v claimed=%v err=%v", lease, claimed, err)
	}
	if err := store.Fail(context.Background(), lease, nextDue, nil, nil, "terminal failure"); err != nil {
		t.Fatalf("写入终态失败: %v", err)
	}
	if due, err := store.Due(context.Background(), nextDue.Add(time.Second), 10); err != nil {
		t.Fatalf("终态后读取失败: %v", err)
	} else if len(due) != 0 {
		t.Fatalf("失败终态仍被读取: %+v", due)
	}
}

func TestPetSQLiteJobStoreRestartKeepsJobs(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "pet-scheduler.db")
	now := petSQLiteTestTime()
	job := newPetSQLiteTestJob("job-restart", now)

	db1 := openPetSQLiteTestDB(t, dsn)
	store1, err := NewPetSQLiteJobStore(db1)
	if err != nil {
		t.Fatalf("首次打开 store 失败: %v", err)
	}
	if err := store1.Enqueue(context.Background(), []PetScheduledJob{job}); err != nil {
		t.Fatalf("首次入队失败: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("关闭首次数据库失败: %v", err)
	}

	db2 := openPetSQLiteTestDB(t, dsn)
	store2, err := NewPetSQLiteJobStore(db2)
	if err != nil {
		t.Fatalf("重启后打开 store 失败: %v", err)
	}
	due, err := store2.Due(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("重启后读取 due 失败: %v", err)
	}
	if len(due) != 1 || due[0].ID != job.ID {
		t.Fatalf("重启后 job 丢失: %+v", due)
	}
	lease, claimed, err := store2.Claim(context.Background(), job.ID, "restarted-worker", now, now.Add(time.Minute))
	if err != nil || !claimed || lease.Attempt != 1 {
		t.Fatalf("重启后 claim 失败: lease=%+v claimed=%v err=%v", lease, claimed, err)
	}
}

func TestPetSQLiteJobStoreRejectsExpiredAndInvalidInputs(t *testing.T) {
	db := openPetSQLiteTestDB(t, ":memory:")
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		t.Fatalf("创建 job store 失败: %v", err)
	}
	now := petSQLiteTestTime()

	if err := store.Enqueue(context.Background(), []PetScheduledJob{{ID: "   "}}); !errors.Is(err, ErrPetSQLiteJobStoreInvalidJob) {
		t.Fatalf("空 ID 错误不明确: %v", err)
	}
	if _, _, err := store.Claim(context.Background(), "", "token", now, now.Add(time.Second)); !errors.Is(err, ErrPetSQLiteJobStoreInvalidJob) {
		t.Fatalf("Claim 空 ID 错误不明确: %v", err)
	}
	if _, _, err := store.Claim(context.Background(), "missing", "token", now, now); !errors.Is(err, ErrPetSQLiteJobStoreLeaseInvalid) {
		t.Fatalf("无效 lease deadline 未被拒绝: %v", err)
	}

	expired := newPetSQLiteTestJob("job-expired", now.Add(-time.Minute))
	expiresAt := now.Add(-time.Second)
	expired.ExpiresAt = &expiresAt
	if err := store.Enqueue(context.Background(), []PetScheduledJob{expired}); err != nil {
		t.Fatalf("准备过期 job 失败: %v", err)
	}
	if _, _, err := store.Claim(context.Background(), expired.ID, "token", now, now.Add(time.Minute)); !errors.Is(err, ErrPetSQLiteJobStoreJobExpired) {
		t.Fatalf("过期 job 未被明确拒绝: %v", err)
	}
}

func TestPetSQLiteJobStoreContextCancellation(t *testing.T) {
	db := openPetSQLiteTestDB(t, ":memory:")
	store, err := NewPetSQLiteJobStore(db)
	if err != nil {
		t.Fatalf("创建 job store 失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Due(ctx, petSQLiteTestTime(), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消 context 未向下传递: %v", err)
	}
}

func openPetSQLiteTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newPetSQLiteTestJob(id string, now time.Time) PetScheduledJob {
	return PetScheduledJob{
		ID:       id,
		JobType:  PetAutomationJobType,
		PlanID:   "plan-test",
		StepID:   id,
		Schedule: PetPlanSchedule{Kind: PetPlanScheduleNow},
		Payload: PetAutomationJobPayload{
			Version:   PetPlanVersion,
			PlanID:    "plan-test",
			StepID:    id,
			Kind:      PetPlanReminderStep,
			Text:      "test reminder",
			CreatedAt: float64(now.UnixMilli()),
		},
		CreatedAt:   now,
		DueAt:       now,
		AvailableAt: now,
		MaxAttempts: 3,
	}
}

func petSQLiteTestTime() time.Time {
	return time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
}
