package services

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type petHeartbeatRepositoryStub struct {
	mu       sync.Mutex
	snapshot PetHeartbeatSnapshot
	saves    []PetHeartbeatSnapshot
	loadErr  error
	saveErr  error
}

func (r *petHeartbeatRepositoryStub) LoadHeartbeat(_ context.Context, _ string) (PetHeartbeatSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return PetHeartbeatSnapshot{}, r.loadErr
	}
	return r.snapshot, nil
}

func (r *petHeartbeatRepositoryStub) SaveHeartbeat(_ context.Context, snapshot PetHeartbeatSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	r.snapshot = snapshot
	r.saves = append(r.saves, snapshot)
	return nil
}

func (r *petHeartbeatRepositoryStub) latest() PetHeartbeatSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot
}

type petHeartbeatPetStub struct {
	snapshot PetMigrationSnapshot
	err      error
}

func (r *petHeartbeatPetStub) Load() (PetMigrationSnapshot, error) {
	if r.err != nil {
		return PetMigrationSnapshot{}, r.err
	}
	return r.snapshot, nil
}

type petHeartbeatWorkspaceStub struct {
	mu    sync.Mutex
	path  string
	err   error
	petID string
	calls int
}

func (r *petHeartbeatWorkspaceStub) Resolve(_ context.Context, petID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.petID = petID
	return r.path, r.err
}

type petHeartbeatChatRunnerStub struct {
	mu        sync.Mutex
	starts    []PetChatRequest
	cancels   []string
	startErr  error
	startedCh chan PetChatRequest
}

func newPetHeartbeatChatRunnerStub() *petHeartbeatChatRunnerStub {
	return &petHeartbeatChatRunnerStub{startedCh: make(chan PetChatRequest, 8)}
}

func (r *petHeartbeatChatRunnerStub) StartChat(request PetChatRequest) (PetChatStartResult, error) {
	r.mu.Lock()
	r.starts = append(r.starts, request)
	err := r.startErr
	r.mu.Unlock()
	if err != nil {
		return PetChatStartResult{}, err
	}
	r.startedCh <- request
	return PetChatStartResult{RequestID: request.RequestID}, nil
}

func (r *petHeartbeatChatRunnerStub) CancelChat(requestID string) error {
	r.mu.Lock()
	r.cancels = append(r.cancels, requestID)
	r.mu.Unlock()
	return nil
}

func (r *petHeartbeatChatRunnerStub) setStartErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startErr = err
}

func (r *petHeartbeatChatRunnerStub) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

func (r *petHeartbeatChatRunnerStub) cancelIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.cancels...)
}

type petHeartbeatManualTimer struct {
	delay   time.Duration
	ch      chan time.Time
	mu      sync.Mutex
	stopped bool
}

func (t *petHeartbeatManualTimer) C() <-chan time.Time {
	return t.ch
}

func (t *petHeartbeatManualTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (t *petHeartbeatManualTimer) fire() {
	t.mu.Lock()
	stopped := t.stopped
	t.mu.Unlock()
	if stopped {
		return
	}
	select {
	case t.ch <- time.Now():
	default:
	}
}

type petHeartbeatTimerFactoryStub struct {
	created chan *petHeartbeatManualTimer
}

func newPetHeartbeatTimerFactoryStub() *petHeartbeatTimerFactoryStub {
	return &petHeartbeatTimerFactoryStub{created: make(chan *petHeartbeatManualTimer, 16)}
}

func (f *petHeartbeatTimerFactoryStub) create(delay time.Duration) PetHeartbeatTimer {
	timer := &petHeartbeatManualTimer{delay: delay, ch: make(chan time.Time, 1)}
	f.created <- timer
	return timer
}

func waitPetHeartbeatTimer(t *testing.T, factory *petHeartbeatTimerFactoryStub) *petHeartbeatManualTimer {
	t.Helper()
	select {
	case timer := <-factory.created:
		return timer
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for heartbeat timer")
		return nil
	}
}

func waitPetHeartbeatSnapshot(t *testing.T, service *PetHeartbeatService, predicate func(PetHeartbeatSnapshot) bool) PetHeartbeatSnapshot {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := service.GetSnapshot()
		if err == nil && predicate(snapshot) {
			return snapshot
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for heartbeat snapshot, last=%#v error=%v", snapshot, err)
			return PetHeartbeatSnapshot{}
		}
	}
}

func newPetHeartbeatTestService(
	t *testing.T,
	initial PetHeartbeatSnapshot,
	clock *time.Time,
	workspace string,
) (*PetHeartbeatService, *petHeartbeatRepositoryStub, *petHeartbeatChatRunnerStub, *petHeartbeatTimerFactoryStub, *PetAICompletionBroker) {
	t.Helper()
	projectName := "demo-project"
	reader := &petHeartbeatPetStub{snapshot: PetMigrationSnapshot{
		PetID: DefaultPetID,
		State: &PetState{PetID: DefaultPetID, Name: "Kapi", Hunger: 70, Cleanliness: 80, Mood: 60},
		Agent: &PetAgentConfig{PetID: DefaultPetID, SystemPrompt: "", ProjectName: &projectName},
	}}
	repository := &petHeartbeatRepositoryStub{snapshot: initial}
	runner := newPetHeartbeatChatRunnerStub()
	factory := newPetHeartbeatTimerFactoryStub()
	broker := NewPetAICompletionBroker()
	service := NewPetHeartbeatService(PetHeartbeatDependencies{
		PetID:             DefaultPetID,
		Pet:               reader,
		Repository:        repository,
		WorkspaceResolver: &petHeartbeatWorkspaceStub{path: workspace},
		ChatRunner:        runner,
		Completions:       broker,
		Now: func() time.Time {
			return *clock
		},
		TimerFactory:      factory.create,
		IdleRetryInterval: 10 * time.Millisecond,
	})
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return service, repository, runner, factory, broker
}

func petHeartbeatInitialSnapshot(enabled bool) PetHeartbeatSnapshot {
	return PetHeartbeatSnapshot{
		Config: PetHeartbeatConfig{
			PetID:           DefaultPetID,
			Enabled:         enabled,
			IntervalMinutes: 1,
			Prompt:          "检查 {{project}} 上 {{name}} 的 {{status}} 状态",
		},
		Runtime: PetHeartbeatRuntime{
			Phase:      PetHeartbeatPhaseDisabled,
			LastStatus: PetHeartbeatStatusNone,
		},
	}
}

func TestPetHeartbeatDefaultsToDisabled(t *testing.T) {
	clock := time.Date(2026, 8, 27, 9, 0, 0, 0, time.Local)
	service, repository, runner, factory, _ := newPetHeartbeatTestService(
		t,
		PetHeartbeatSnapshot{},
		&clock,
		`t:\\workspace\\demo`,
	)

	snapshot := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseDisabled
	})
	// 首次启动只初始化关闭配置；没有用户明确启用前，不能创建 timer 或提交 Agent 任务。
	if snapshot.Config.Enabled || snapshot.Config.IntervalMinutes != PetHeartbeatDefaultIntervalMinutes {
		t.Fatalf("default heartbeat config = %#v", snapshot.Config)
	}
	if len(factory.created) != 0 || runner.startCount() != 0 {
		t.Fatalf("default heartbeat started work: timers=%d starts=%d", len(factory.created), runner.startCount())
	}
	if stored := repository.latest(); stored.Config.Enabled || stored.Runtime.Phase != PetHeartbeatPhaseDisabled {
		t.Fatalf("stored default heartbeat = %#v", stored)
	}
}

func TestPetHeartbeatStartsNextIntervalOnlyAfterTerminal(t *testing.T) {
	clock := time.Date(2026, 8, 27, 10, 0, 0, 0, time.Local)
	service, _, runner, factory, broker := newPetHeartbeatTestService(
		t,
		petHeartbeatInitialSnapshot(true),
		&clock,
		`t:\\workspace\\demo`,
	)

	firstTimer := waitPetHeartbeatTimer(t, factory)
	if firstTimer.delay != time.Minute {
		t.Fatalf("initial timer delay = %s, want %s", firstTimer.delay, time.Minute)
	}
	firstTimer.fire()
	request := <-runner.startedCh
	running := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseRunning
	})
	if running.Runtime.NextRunAt != 0 || running.Runtime.CurrentRequestID != request.RequestID {
		t.Fatalf("running snapshot = %#v", running.Runtime)
	}
	if request.UserText != "检查 demo-project 上 Kapi 的 awake 状态" {
		t.Fatalf("rendered heartbeat prompt = %q", request.UserText)
	}

	// 非终态事件不能消耗当前任务，也不能提前创建下一轮 timer。
	broker.Publish(PetAIEvent{Type: PetAIEventProgress, RequestID: request.RequestID})
	time.Sleep(20 * time.Millisecond)
	if got := len(factory.created); got != 0 {
		t.Fatalf("timer count before terminal = %d, want 0", got)
	}
	stillRunning := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseRunning
	})
	if stillRunning.Runtime.CurrentRequestID != request.RequestID {
		t.Fatalf("non-terminal event changed request = %#v", stillRunning.Runtime)
	}

	broker.Publish(PetAIEvent{Type: PetAIEventCompleted, RequestID: request.RequestID})
	waiting := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseWaiting && snapshot.Runtime.LastStatus == PetHeartbeatStatusCompleted
	})
	wantNext := clock.Add(time.Minute).UnixMilli()
	if waiting.Runtime.NextRunAt != wantNext {
		t.Fatalf("next run = %d, want %d", waiting.Runtime.NextRunAt, wantNext)
	}
	secondTimer := waitPetHeartbeatTimer(t, factory)
	if secondTimer.delay != time.Minute {
		t.Fatalf("post-terminal timer delay = %s, want %s", secondTimer.delay, time.Minute)
	}
}

func TestPetHeartbeatWaitsForManualChatBeforeConsumingInterval(t *testing.T) {
	clock := time.Date(2026, 8, 27, 11, 0, 0, 0, time.Local)
	service, _, runner, factory, broker := newPetHeartbeatTestService(
		t,
		petHeartbeatInitialSnapshot(true),
		&clock,
		`t:\\workspace\\demo`,
	)
	runner.setStartErr(newPetAIError(PET_AI_REQUEST_IN_FLIGHT, 0, nil))

	waitPetHeartbeatTimer(t, factory).fire()
	waitingForIdle := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseWaitingForIdle
	})
	if waitingForIdle.Runtime.NextRunAt != 0 || runner.startCount() != 1 {
		t.Fatalf("waiting-for-idle snapshot = %#v, starts=%d", waitingForIdle.Runtime, runner.startCount())
	}
	idleTimer := waitPetHeartbeatTimer(t, factory)
	if idleTimer.delay != 10*time.Millisecond {
		t.Fatalf("idle retry delay = %s", idleTimer.delay)
	}

	runner.setStartErr(nil)
	idleTimer.fire()
	request := <-runner.startedCh
	running := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseRunning
	})
	if running.Runtime.CurrentRequestID != request.RequestID || runner.startCount() != 2 {
		t.Fatalf("retry running snapshot = %#v, starts=%d", running.Runtime, runner.startCount())
	}
	broker.Publish(PetAIEvent{Type: PetAIEventCompleted, RequestID: request.RequestID})
	waiting := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseWaiting && snapshot.Runtime.LastStatus == PetHeartbeatStatusCompleted
	})
	if waiting.Runtime.NextRunAt != clock.Add(time.Minute).UnixMilli() {
		t.Fatalf("retry next run = %d, want %d", waiting.Runtime.NextRunAt, clock.Add(time.Minute).UnixMilli())
	}
}

func TestPetHeartbeatRunNowAllowsOneShotWhenDisabled(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.Local)
	service, repository, runner, factory, broker := newPetHeartbeatTestService(
		t,
		petHeartbeatInitialSnapshot(false),
		&clock,
		`t:\\workspace\\demo`,
	)

	if _, err := service.RunNow(); err != nil {
		t.Fatalf("RunNow() error = %v", err)
	}
	request := <-runner.startedCh
	running := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseRunning
	})
	if running.Config.Enabled || running.Runtime.NextRunAt != 0 {
		t.Fatalf("one-shot running snapshot = %#v", running)
	}
	if len(factory.created) != 0 {
		t.Fatal("disabled one-shot unexpectedly created a timer")
	}

	broker.Publish(PetAIEvent{Type: PetAIEventCompleted, RequestID: request.RequestID})
	finished := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseDisabled && snapshot.Runtime.LastStatus == PetHeartbeatStatusCompleted
	})
	if finished.Runtime.NextRunAt != 0 || repository.latest().Runtime.Phase != PetHeartbeatPhaseDisabled {
		t.Fatalf("one-shot finished snapshot = %#v, stored=%#v", finished, repository.latest())
	}
}

func TestPetHeartbeatCancelWaitsForCancelledTerminal(t *testing.T) {
	clock := time.Date(2026, 8, 27, 13, 0, 0, 0, time.Local)
	service, _, runner, factory, broker := newPetHeartbeatTestService(
		t,
		petHeartbeatInitialSnapshot(true),
		&clock,
		`t:\\workspace\\demo`,
	)

	// RunNow 会停止启动时为周期配置创建的 timer；先取出它，避免把已停止的
	// 初始 timer 与取消后的新 timer 混在同一个测试队列里。
	waitPetHeartbeatTimer(t, factory)
	if _, err := service.RunNow(); err != nil {
		t.Fatalf("RunNow() error = %v", err)
	}
	request := <-runner.startedCh
	waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseRunning
	})
	beforeTerminal, err := service.Cancel()
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if beforeTerminal.Runtime.Phase != PetHeartbeatPhaseRunning || beforeTerminal.Runtime.CurrentRequestID != request.RequestID {
		t.Fatalf("Cancel() changed state before terminal = %#v", beforeTerminal.Runtime)
	}
	if got := runner.cancelIDs(); len(got) != 1 || got[0] != request.RequestID {
		t.Fatalf("cancel request IDs = %#v", got)
	}
	if len(factory.created) != 0 {
		t.Fatal("cancel request created a timer before terminal")
	}

	broker.Publish(PetAIEvent{Type: PetAIEventCancelled, RequestID: request.RequestID})
	finished := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseWaiting && snapshot.Runtime.LastStatus == PetHeartbeatStatusCancelled
	})
	if finished.Runtime.NextRunAt != clock.Add(time.Minute).UnixMilli() {
		t.Fatalf("cancelled next run = %d, want %d", finished.Runtime.NextRunAt, clock.Add(time.Minute).UnixMilli())
	}
	waitPetHeartbeatTimer(t, factory)
}

func TestPetHeartbeatIgnoresStaleTerminalEvent(t *testing.T) {
	clock := time.Date(2026, 8, 27, 14, 0, 0, 0, time.Local)
	service, _, runner, factory, broker := newPetHeartbeatTestService(
		t,
		petHeartbeatInitialSnapshot(true),
		&clock,
		`t:\\workspace\\demo`,
	)

	waitPetHeartbeatTimer(t, factory).fire()
	firstRequest := <-runner.startedCh
	waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseRunning
	})
	broker.Publish(PetAIEvent{Type: PetAIEventCompleted, RequestID: firstRequest.RequestID})
	waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseWaiting
	})
	waitPetHeartbeatTimer(t, factory).fire()
	secondRequest := <-runner.startedCh
	waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseRunning && snapshot.Runtime.CurrentRequestID == secondRequest.RequestID
	})

	// 旧任务的终态到达时，当前 requestId 已经变化，不能把第二轮误判为完成。
	service.terminal <- PetAIEvent{Type: PetAIEventCompleted, RequestID: firstRequest.RequestID}
	time.Sleep(20 * time.Millisecond)
	current := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseRunning
	})
	if current.Runtime.CurrentRequestID != secondRequest.RequestID {
		t.Fatalf("stale terminal changed current request = %#v", current.Runtime)
	}

	broker.Publish(PetAIEvent{Type: PetAIEventCompleted, RequestID: secondRequest.RequestID})
	waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseWaiting && snapshot.Runtime.LastStatus == PetHeartbeatStatusCompleted
	})
}

func TestPetHeartbeatRestoresInterruptedRun(t *testing.T) {
	clock := time.Date(2026, 8, 27, 15, 0, 0, 0, time.Local)
	initial := petHeartbeatInitialSnapshot(true)
	initial.Runtime = PetHeartbeatRuntime{
		Phase:            PetHeartbeatPhaseRunning,
		CurrentRequestID: "old-heartbeat-request",
		LastStartedAt:    clock.Add(-time.Minute).UnixMilli(),
	}
	service, repository, runner, factory, _ := newPetHeartbeatTestService(
		t,
		initial,
		&clock,
		`t:\\workspace\\demo`,
	)

	recovered := waitPetHeartbeatSnapshot(t, service, func(snapshot PetHeartbeatSnapshot) bool {
		return snapshot.Runtime.Phase == PetHeartbeatPhaseWaiting && snapshot.Runtime.LastStatus == PetHeartbeatStatusInterrupted
	})
	if recovered.Runtime.CurrentRequestID != "" || recovered.Runtime.LastFinishedAt != clock.UnixMilli() {
		t.Fatalf("recovered runtime = %#v", recovered.Runtime)
	}
	if recovered.Runtime.NextRunAt != clock.Add(time.Minute).UnixMilli() {
		t.Fatalf("recovered next run = %d, want %d", recovered.Runtime.NextRunAt, clock.Add(time.Minute).UnixMilli())
	}
	if runner.startCount() != 0 {
		t.Fatalf("recovery replayed old request, starts=%d", runner.startCount())
	}
	if stored := repository.latest(); stored.Runtime.LastStatus != PetHeartbeatStatusInterrupted {
		t.Fatalf("stored recovery runtime = %#v", stored.Runtime)
	}
	waitPetHeartbeatTimer(t, factory)
}

func TestPetHeartbeatRequiresWorkspaceWhenEnablingOrRunning(t *testing.T) {
	clock := time.Date(2026, 8, 27, 16, 0, 0, 0, time.Local)
	initial := petHeartbeatInitialSnapshot(false)
	service, repository, runner, factory, _ := newPetHeartbeatTestService(t, initial, &clock, "")

	config := initial.Config
	config.Enabled = true
	if _, err := service.SaveConfig(config); err == nil {
		t.Fatal("SaveConfig() unexpectedly enabled heartbeat without a workspace")
	} else {
		var heartbeatErr *PetHeartbeatError
		if !errors.As(err, &heartbeatErr) || heartbeatErr.Code != PetHeartbeatErrorWorkspaceUnavailable {
			t.Fatalf("SaveConfig() error = %v", err)
		}
	}
	if got := repository.latest(); got.Config.Enabled || got.Runtime.Phase != PetHeartbeatPhaseDisabled {
		t.Fatalf("workspace failure changed stored state = %#v", got)
	}
	if _, err := service.RunNow(); err == nil {
		t.Fatal("RunNow() unexpectedly started without a workspace")
	}
	if runner.startCount() != 0 || len(factory.created) != 0 {
		t.Fatalf("workspace failure attempted chat/timer: starts=%d timers=%d", runner.startCount(), len(factory.created))
	}
}

func TestPetDAOHeartbeatFirstReadCreatesTableAndRoundTrips(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old-pet.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 只创建一个旧版本可能存在的表，验证首次读取能增量补齐 pet_heartbeat，
	// 而不是要求用户删除旧库或提前运行新的全量迁移。
	if _, err := db.Exec(`CREATE TABLE pet_state (pet_id TEXT PRIMARY KEY, state_json TEXT NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	dao := NewPetDAO(db)
	first, err := dao.LoadHeartbeat(context.Background(), DefaultPetID)
	if err != nil {
		t.Fatalf("first LoadHeartbeat() error = %v", err)
	}
	if first.Config.Enabled || first.Config.IntervalMinutes != PetHeartbeatDefaultIntervalMinutes || first.Runtime.Phase != PetHeartbeatPhaseDisabled {
		t.Fatalf("first heartbeat snapshot = %#v", first)
	}

	first.Config.Enabled = true
	first.Config.Prompt = "检查项目"
	first.Runtime.Phase = PetHeartbeatPhaseWaiting
	first.Runtime.NextRunAt = 123
	if err := dao.SaveHeartbeat(context.Background(), first); err != nil {
		t.Fatalf("SaveHeartbeat() error = %v", err)
	}
	second, err := dao.LoadHeartbeat(context.Background(), " ")
	if err != nil {
		t.Fatalf("round-trip LoadHeartbeat() error = %v", err)
	}
	if second.Config.PetID != DefaultPetID || second.Config.Prompt != "检查项目" || second.Runtime.NextRunAt != 123 {
		t.Fatalf("round-trip heartbeat snapshot = %#v", second)
	}
}
