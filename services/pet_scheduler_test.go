package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestPetSchedulerSchedulePlanConvertsAllSchedules(t *testing.T) {
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	clock := &petSchedulerTestClock{now: now}
	store := newPetSchedulerMemoryStore()
	scheduler := NewPetScheduler(store, nil, clock)

	plan := map[string]any{
		"version": 1,
		"steps": []any{
			map[string]any{
				"kind": "action", "action": "feed",
				"schedule": map[string]any{"kind": "now"},
			},
			map[string]any{
				"kind": "reminder", "text": "延迟提醒",
				"schedule": map[string]any{"kind": "delay", "delaySeconds": 2.5},
			},
			map[string]any{
				"kind": "action", "action": "play",
				"schedule": map[string]any{
					"kind": "at", "at": "2026-08-11T17:00", "tz": "Asia/Shanghai",
				},
			},
			map[string]any{
				"kind": "reminder", "text": "每分钟提醒",
				"schedule": map[string]any{"kind": "every", "everyMs": 60000},
			},
			map[string]any{
				"kind": "action", "action": "study",
				"schedule": map[string]any{
					"kind": "cron", "expr": " 0 10 * * * ", "tz": "UTC",
				},
			},
		},
	}

	got, err := scheduler.SchedulePlan(context.Background(), plan, PetSchedulePlanOptions{
		PlanID:      "plan-all",
		TimeZone:    "UTC",
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("SchedulePlan() error = %v", err)
	}
	if got.PlanID != "plan-all" || len(got.Jobs) != 5 || len(got.Immediate) != 1 {
		t.Fatalf("SchedulePlan() result = %#v, want five jobs and one immediate job", got)
	}
	if got.Immediate[0].ID != "plan-all-1" {
		t.Fatalf("immediate job = %#v, want now step", got.Immediate[0])
	}
	if store.stats().enqueues != 1 || store.storedJobCount() != 5 {
		t.Fatalf("store enqueue stats = %#v, stored jobs = %d", store.stats(), store.storedJobCount())
	}

	wantDue := []time.Time{
		now,
		now.Add(2500 * time.Millisecond),
		time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC),
		now.Add(time.Minute),
		time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC),
	}
	wantKinds := []PetPlanScheduleKind{
		PetPlanScheduleNow,
		PetPlanScheduleDelay,
		PetPlanScheduleAt,
		PetPlanScheduleEvery,
		PetPlanScheduleCron,
	}
	for index, job := range got.Jobs {
		if job.ID != fmt.Sprintf("plan-all-%d", index+1) {
			t.Errorf("job[%d].ID = %q, want plan-all-%d", index, job.ID, index+1)
		}
		if job.Schedule.Kind != wantKinds[index] {
			t.Errorf("job[%d].Schedule.Kind = %q, want %q", index, job.Schedule.Kind, wantKinds[index])
		}
		if !job.DueAt.Equal(wantDue[index].UTC()) || !job.AvailableAt.Equal(wantDue[index].UTC()) {
			t.Errorf("job[%d] due=%s available=%s, want %s", index, job.DueAt, job.AvailableAt, wantDue[index])
		}
		if !job.CreatedAt.Equal(now.UTC()) || job.MaxAttempts != 2 {
			t.Errorf("job[%d] created=%s maxAttempts=%d, want created=%s maxAttempts=2", index, job.CreatedAt, job.MaxAttempts, now)
		}
	}

	if got.Jobs[1].Schedule.DelaySeconds != 2.5 {
		t.Errorf("delay schedule = %#v, want 2.5 seconds", got.Jobs[1].Schedule)
	}
	if got.Jobs[2].Schedule.TZ != "Asia/Shanghai" {
		t.Errorf("at schedule timezone = %q, want Asia/Shanghai", got.Jobs[2].Schedule.TZ)
	}
	if got.Jobs[3].Schedule.EveryMS != 60000 {
		t.Errorf("every schedule interval = %d, want 60000ms", got.Jobs[3].Schedule.EveryMS)
	}
	if got.Jobs[4].Schedule.Expr != "0 10 * * *" || got.Jobs[4].Schedule.TZ != "UTC" {
		t.Errorf("cron schedule = %#v, want trimmed expression and UTC", got.Jobs[4].Schedule)
	}
}

func TestPetSchedulerPollLeaseIsIdempotentAcrossExpiry(t *testing.T) {
	start := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	clock := &petSchedulerTestClock{now: start}
	job := petSchedulerTestActionJob("job-1", start)
	store := newPetSchedulerMemoryStore(job)
	scheduler := NewPetScheduler(store, nil, clock, PetSchedulerConfig{
		TimeZone:    "UTC",
		PollLimit:   4,
		MaxAttempts: 3,
		Lease:       time.Second,
		RetryDelay:  time.Second,
	})
	ctx := context.Background()

	first, err := scheduler.Poll(ctx, 1)
	if err != nil {
		t.Fatalf("first Poll() error = %v", err)
	}
	if len(first) != 1 || first[0].Attempt != 1 || first[0].Token == "" {
		t.Fatalf("first leases = %#v, want one attempt-1 lease with token", first)
	}

	second, err := scheduler.Poll(ctx, 1)
	if err != nil {
		t.Fatalf("duplicate Poll() error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("duplicate Poll() leases = %#v, valid lease must suppress duplicate claim", second)
	}

	clock.now = start.Add(time.Second + time.Nanosecond)
	recovered, err := scheduler.Poll(ctx, 1)
	if err != nil {
		t.Fatalf("recovery Poll() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].Attempt != 2 || recovered[0].Token == first[0].Token {
		t.Fatalf("recovered leases = %#v, want a new attempt and token after expiry", recovered)
	}

	// 旧 worker 即使在租约过期后回写，也不能覆盖已经被新 worker 抢到的 token。
	if err := store.Complete(ctx, first[0], clock.now, nil); err == nil {
		t.Fatal("Complete() accepted stale lease token")
	}
	if err := store.Complete(ctx, recovered[0], clock.now, nil); err != nil {
		t.Fatalf("Complete() with current lease error = %v", err)
	}
	if state, ok := store.snapshot("job-1"); !ok || !state.completed || state.failed {
		t.Fatalf("job state after current completion = %#v, want completed", state)
	}
	if err := store.Complete(ctx, recovered[0], clock.now, nil); err == nil {
		t.Fatal("Complete() accepted a lease twice")
	}

	final, err := scheduler.Poll(ctx, 1)
	if err != nil {
		t.Fatalf("Poll() after completion error = %v", err)
	}
	if len(final) != 0 {
		t.Fatalf("Poll() after completion = %#v, completed job must not reappear", final)
	}
	stats := store.stats()
	if stats.claims != 2 || stats.completes != 1 {
		t.Fatalf("lease operation stats = %#v, want two claims and one successful completion", stats)
	}
}

func TestPetSchedulerRunDueHandlesReminderRetryAndActionCompletion(t *testing.T) {
	t.Run("reminder emitter failure retries same occurrence", func(t *testing.T) {
		now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
		clock := &petSchedulerTestClock{now: now}
		store := newPetSchedulerMemoryStore(petSchedulerTestReminderJob("reminder-1", now))
		emitErr := errors.New("emitter temporarily unavailable")
		emitter := &petSchedulerTestEmitter{failuresRemaining: 1, err: emitErr}
		scheduler := NewPetScheduler(store, emitter, clock, PetSchedulerConfig{
			TimeZone:    "UTC",
			PollLimit:   4,
			MaxAttempts: 3,
			Lease:       time.Second,
			RetryDelay:  10 * time.Millisecond,
		})
		ctx := context.Background()

		first, err := scheduler.RunDue(ctx, 0)
		if err == nil || !errors.Is(err, emitErr) {
			t.Fatalf("first RunDue() error = %v, want emitter error", err)
		}
		if first.Claimed != 1 || first.Retried != 1 || first.Completed != 0 || first.RemindersEmitted != 0 || len(first.Errors) != 1 {
			t.Fatalf("first RunDue() result = %#v, want one retry and one reported error", first)
		}
		state, ok := store.snapshot("reminder-1")
		if !ok || state.attempt != 1 || state.completed || state.failed || state.token != "" {
			t.Fatalf("state after emitter failure = %#v, want released retryable job", state)
		}
		retryAt := now.Add(10 * time.Millisecond)
		if !state.job.AvailableAt.Equal(retryAt) || !state.job.DueAt.Equal(now) {
			t.Fatalf("retry timing = available %s due %s, want available %s and unchanged due", state.job.AvailableAt, state.job.DueAt, retryAt)
		}
		if len(emitter.events) != 1 {
			t.Fatalf("emitter events after failure = %d, want one attempted emission", len(emitter.events))
		}
		firstEvent := emitter.events[0]
		if firstEvent.Type != PetSchedulerReminderEvent || firstEvent.JobID != "reminder-1" || firstEvent.Payload.Text != "喝水" || !firstEvent.FiredAt.Equal(now.UTC()) {
			t.Fatalf("first reminder event = %#v", firstEvent)
		}

		clock.now = retryAt
		second, err := scheduler.RunDue(ctx, 0)
		if err != nil {
			t.Fatalf("second RunDue() error = %v", err)
		}
		if second.Claimed != 1 || second.Completed != 1 || second.RemindersEmitted != 1 || second.Retried != 0 || len(second.Errors) != 0 {
			t.Fatalf("second RunDue() result = %#v, want successful completion and emission", second)
		}
		if len(emitter.events) != 2 || emitter.events[1].EventID != firstEvent.EventID {
			t.Fatalf("retry event IDs = %#v, occurrence ID must remain stable", emitter.events)
		}
		if !emitter.events[1].FiredAt.Equal(retryAt.UTC()) {
			t.Fatalf("retry event firedAt = %s, want %s", emitter.events[1].FiredAt, retryAt)
		}
		state, ok = store.snapshot("reminder-1")
		if !ok || !state.completed || state.failed {
			t.Fatalf("state after successful retry = %#v, want completed", state)
		}
		stats := store.stats()
		if stats.fails != 1 || stats.completes != 1 {
			t.Fatalf("reminder store stats = %#v, want one failure and one completion", stats)
		}
	})

	t.Run("action is returned only after completion", func(t *testing.T) {
		now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
		clock := &petSchedulerTestClock{now: now}
		store := newPetSchedulerMemoryStore(petSchedulerTestActionJob("action-1", now))
		scheduler := NewPetScheduler(store, nil, clock)

		result, err := scheduler.RunDue(context.Background(), 0)
		if err != nil {
			t.Fatalf("RunDue() error = %v", err)
		}
		if result.Claimed != 1 || result.Completed != 1 || len(result.Actions) != 1 || result.Actions[0].Action != PetActionFeed {
			t.Fatalf("RunDue() result = %#v, want one completed action", result)
		}
		if state, ok := store.snapshot("action-1"); !ok || !state.completed || state.failed {
			t.Fatalf("action state = %#v, want completed", state)
		}
		if stats := store.stats(); stats.completes != 1 || stats.fails != 0 {
			t.Fatalf("action store stats = %#v, want one completion and no failure", stats)
		}
	})
}

type petSchedulerTestClock struct {
	now time.Time
}

func (c *petSchedulerTestClock) Now() time.Time {
	return c.now
}

type petSchedulerMemoryJob struct {
	job        PetScheduledJob
	attempt    int
	token      string
	leaseUntil time.Time
	completed  bool
	failed     bool
	lastError  string
}

type petSchedulerMemoryStore struct {
	mu        sync.Mutex
	jobs      map[string]*petSchedulerMemoryJob
	enqueues  int
	claims    int
	completes int
	fails     int
}

type petSchedulerMemoryStoreStats struct {
	enqueues  int
	claims    int
	completes int
	fails     int
}

// 这个替身把 job 状态、attempt 和 token 放在同一把锁内，模拟持久化 store 的原子 lease 边界；
// 测试因此能区分“有效租约内不重复 claim”和“租约过期后允许恢复”，而不是误把 map 行为当成幂等保证。
func newPetSchedulerMemoryStore(jobs ...PetScheduledJob) *petSchedulerMemoryStore {
	store := &petSchedulerMemoryStore{jobs: make(map[string]*petSchedulerMemoryJob, len(jobs))}
	for _, job := range jobs {
		store.jobs[job.ID] = &petSchedulerMemoryJob{job: clonePetSchedulerTestJob(job)}
	}
	return store
}

func (s *petSchedulerMemoryStore) Enqueue(_ context.Context, jobs []PetScheduledJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range jobs {
		if job.ID == "" {
			return errors.New("job id is empty")
		}
		if _, exists := s.jobs[job.ID]; exists {
			return fmt.Errorf("job %q already exists", job.ID)
		}
	}
	for _, job := range jobs {
		copy := clonePetSchedulerTestJob(job)
		s.jobs[job.ID] = &petSchedulerMemoryJob{job: copy}
	}
	s.enqueues++
	return nil
}

func (s *petSchedulerMemoryStore) Due(_ context.Context, now time.Time, limit int) ([]PetScheduledJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs := make([]PetScheduledJob, 0, len(s.jobs))
	for _, state := range s.jobs {
		if state.completed || state.failed {
			continue
		}
		availableAt := state.job.AvailableAt
		if availableAt.IsZero() {
			availableAt = state.job.DueAt
		}
		if availableAt.After(now) || (state.job.ExpiresAt != nil && !now.Before(*state.job.ExpiresAt)) {
			continue
		}
		if state.token != "" && now.Before(state.leaseUntil) {
			continue
		}
		jobs = append(jobs, clonePetSchedulerTestJob(state.job))
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].AvailableAt.Equal(jobs[j].AvailableAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].AvailableAt.Before(jobs[j].AvailableAt)
	})
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}

func (s *petSchedulerMemoryStore) Claim(_ context.Context, jobID, token string, now, leaseUntil time.Time) (PetJobLease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, exists := s.jobs[jobID]
	if !exists {
		return PetJobLease{}, false, fmt.Errorf("job %q does not exist", jobID)
	}
	if token == "" {
		return PetJobLease{}, false, errors.New("lease token is empty")
	}
	if state.completed || state.failed || (state.token != "" && now.Before(state.leaseUntil)) {
		return PetJobLease{}, false, nil
	}
	if state.job.ExpiresAt != nil && !now.Before(*state.job.ExpiresAt) {
		return PetJobLease{}, false, nil
	}
	state.attempt++
	state.token = token
	state.leaseUntil = leaseUntil
	s.claims++
	return PetJobLease{
		Job:        clonePetSchedulerTestJob(state.job),
		Token:      token,
		Attempt:    state.attempt,
		LeaseUntil: leaseUntil,
	}, true, nil
}

func (s *petSchedulerMemoryStore) Complete(_ context.Context, lease PetJobLease, now time.Time, nextDue *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.validateLeaseLocked(lease, now)
	if err != nil {
		return err
	}
	if nextDue == nil {
		state.completed = true
	} else {
		state.job.DueAt = nextDue.UTC()
		state.job.AvailableAt = nextDue.UTC()
	}
	state.token = ""
	state.leaseUntil = time.Time{}
	s.completes++
	return nil
}

func (s *petSchedulerMemoryStore) Fail(_ context.Context, lease PetJobLease, now time.Time, retryAt, nextDue *time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.validateLeaseLocked(lease, now)
	if err != nil {
		return err
	}
	state.lastError = reason
	if retryAt != nil {
		state.job.AvailableAt = retryAt.UTC()
	} else if nextDue != nil {
		state.job.DueAt = nextDue.UTC()
		state.job.AvailableAt = nextDue.UTC()
	} else {
		state.failed = true
	}
	state.token = ""
	state.leaseUntil = time.Time{}
	s.fails++
	return nil
}

func (s *petSchedulerMemoryStore) validateLeaseLocked(lease PetJobLease, now time.Time) (*petSchedulerMemoryJob, error) {
	state, exists := s.jobs[lease.Job.ID]
	if !exists {
		return nil, fmt.Errorf("job %q does not exist", lease.Job.ID)
	}
	if state.completed || state.failed || lease.Token == "" || state.token != lease.Token || !now.Before(state.leaseUntil) {
		return nil, fmt.Errorf("lease for job %q is stale", lease.Job.ID)
	}
	return state, nil
}

func (s *petSchedulerMemoryStore) snapshot(jobID string) (petSchedulerMemoryJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return petSchedulerMemoryJob{}, false
	}
	copy := *state
	copy.job = clonePetSchedulerTestJob(state.job)
	return copy, true
}

func (s *petSchedulerMemoryStore) stats() petSchedulerMemoryStoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return petSchedulerMemoryStoreStats{
		enqueues:  s.enqueues,
		claims:    s.claims,
		completes: s.completes,
		fails:     s.fails,
	}
}

func (s *petSchedulerMemoryStore) storedJobCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

func clonePetSchedulerTestJob(job PetScheduledJob) PetScheduledJob {
	if job.ExpiresAt != nil {
		expiresAt := *job.ExpiresAt
		job.ExpiresAt = &expiresAt
	}
	return job
}

type petSchedulerTestEmitter struct {
	events            []PetSchedulerEvent
	failuresRemaining int
	err               error
}

func (e *petSchedulerTestEmitter) Emit(_ context.Context, event PetSchedulerEvent) error {
	e.events = append(e.events, event)
	if e.failuresRemaining > 0 {
		e.failuresRemaining--
		return e.err
	}
	return nil
}

func petSchedulerTestActionJob(id string, dueAt time.Time) PetScheduledJob {
	return PetScheduledJob{
		ID:          id,
		JobType:     PetAutomationJobType,
		PlanID:      "plan-test",
		StepID:      id,
		Schedule:    PetPlanSchedule{Kind: PetPlanScheduleNow},
		Payload:     PetAutomationJobPayload{Version: PetPlanVersion, PlanID: "plan-test", StepID: id, Kind: PetPlanActionStep, Action: PetActionFeed, CreatedAt: float64(dueAt.UnixMilli())},
		CreatedAt:   dueAt.UTC(),
		DueAt:       dueAt.UTC(),
		AvailableAt: dueAt.UTC(),
		MaxAttempts: 3,
	}
}

func petSchedulerTestReminderJob(id string, dueAt time.Time) PetScheduledJob {
	job := petSchedulerTestActionJob(id, dueAt)
	job.Payload.Kind = PetPlanReminderStep
	job.Payload.Action = ""
	job.Payload.Text = "喝水"
	return job
}
