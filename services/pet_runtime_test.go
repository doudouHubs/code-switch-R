package services

import (
	"errors"
	"testing"
	"time"
)

func runtimeTestTime() time.Time {
	return time.Now().Add(-time.Minute).Truncate(time.Millisecond)
}

func newRuntimeTestSnapshot(petID string, now int64) PetMigrationSnapshot {
	state := DefaultPetStateAt(now)
	state.PetID = petID
	experience := PetExperience{PetID: petID}
	care := PetCareConfig{
		PetID:             petID,
		AutoCareEnabled:   true,
		AutoCareThreshold: PetAutoCareDefaultThreshold,
	}
	return PetMigrationSnapshot{
		PetID:      petID,
		State:      &state,
		Experience: &experience,
		Care:       &care,
	}
}

func newRuntimeTestRuntime(
	repository *memoryPetRepository,
	petID string,
	now *time.Time,
	options ...PetRuntimeOptions,
) *PetRuntime {
	service := NewPetServiceForPet(repository, petID)
	base := PetRuntimeOptions{
		Clock: func() time.Time {
			return *now
		},
		Random: func() float64 { return 0 },
	}
	options = append([]PetRuntimeOptions{base}, options...)
	return NewPetRuntime(service, options...)
}

func TestPetRuntimeUsesSourceActionOrderAndThresholdBoundary(t *testing.T) {
	now := runtimeTestTime()
	nowMs := now.UnixMilli()
	repository := &memoryPetRepository{
		snapshots: map[string]PetMigrationSnapshot{
			"default": newRuntimeTestSnapshot("default", nowMs),
		},
	}
	snapshot := repository.getSnapshotForPet("default")
	snapshot.State.Hunger = 10
	snapshot.State.Cleanliness = 15
	snapshot.State.Mood = 5
	snapshot.State.Coins = 100
	repository.setSnapshotForPet(DefaultPetID, snapshot)

	runtime := newRuntimeTestRuntime(repository, DefaultPetID, &now, PetRuntimeOptions{
		Random: func() float64 { return 0.999 },
	})
	result, err := runtime.RunOnce()
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Status != PetRuntimeStatusExecuted {
		t.Fatalf("status = %q, want executed", result.Status)
	}
	if len(result.Actions) != 1 || result.Actions[0].Action != PetActionPlay || !result.Actions[0].OK {
		t.Fatalf("actions = %#v, want the lowest mood play action", result.Actions)
	}
	if len(result.ExecutedActions) != 1 || result.ExecutedActions[0] != PetActionPlay {
		t.Fatalf("executed actions = %#v", result.ExecutedActions)
	}
	if result.Snapshot.Mood != 23 || result.Snapshot.Hunger != 4 || result.Snapshot.Cleanliness != 11 {
		t.Fatalf("snapshot after play = %#v", result.Snapshot)
	}

	// 需求值等于阈值时不能触发，验证源项目使用的是 value < threshold。
	now = now.Add(PetRuntimeDefaultCooldown)
	snapshot = repository.getSnapshotForPet(DefaultPetID)
	snapshot.State.Hunger = 20
	snapshot.State.Cleanliness = 20
	snapshot.State.Mood = 20
	// 把测试快照锚在边界时刻，隔离自动衰减对阈值断言的干扰。
	snapshot.State.LastTickAt = now.UnixMilli()
	repository.setSnapshotForPet(DefaultPetID, snapshot)
	result, err = runtime.RunOnce()
	if err != nil {
		t.Fatalf("boundary RunOnce() error = %v", err)
	}
	if result.Status != PetRuntimeStatusNoNeed || !result.Skipped || result.SkipReason != PetRuntimeSkipNoNeed {
		t.Fatalf("boundary result = %#v", result)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("boundary actions = %#v, want none", result.Actions)
	}
}

func TestPetRuntimeCooldownIsIdempotentAndHonorsExactBoundary(t *testing.T) {
	now := runtimeTestTime()
	nowMs := now.UnixMilli()
	repository := &memoryPetRepository{
		snapshots: map[string]PetMigrationSnapshot{
			"default": newRuntimeTestSnapshot("default", nowMs),
		},
	}
	snapshot := repository.getSnapshotForPet(DefaultPetID)
	snapshot.State.Hunger = 0
	snapshot.State.Coins = 100
	snapshot.Care.AutoCareThreshold = PetAutoCareMaxThreshold
	repository.setSnapshotForPet(DefaultPetID, snapshot)

	const cooldown = 2 * time.Second
	runtime := newRuntimeTestRuntime(repository, DefaultPetID, &now, PetRuntimeOptions{Cooldown: cooldown})
	first, err := runtime.RunOnce()
	if err != nil || first.Status != PetRuntimeStatusExecuted {
		t.Fatalf("first result = %#v, error = %v", first, err)
	}
	coinsAfterFirst := first.Snapshot.Coins

	second, err := runtime.RunOnce()
	if err != nil {
		t.Fatalf("same-time RunOnce() error = %v", err)
	}
	if second.Status != PetRuntimeStatusCoolingDown || !second.Skipped {
		t.Fatalf("same-time result = %#v, want cooldown", second)
	}
	if second.Snapshot.Coins != coinsAfterFirst {
		t.Fatalf("same-time run changed coins: got %d want %d", second.Snapshot.Coins, coinsAfterFirst)
	}

	now = now.Add(cooldown - time.Millisecond)
	beforeBoundary, err := runtime.RunOnce()
	if err != nil || beforeBoundary.Status != PetRuntimeStatusCoolingDown {
		t.Fatalf("before-boundary result = %#v, error = %v", beforeBoundary, err)
	}

	now = now.Add(time.Millisecond)
	atBoundary, err := runtime.RunOnce()
	if err != nil || atBoundary.Status != PetRuntimeStatusExecuted {
		t.Fatalf("boundary result = %#v, error = %v", atBoundary, err)
	}
	if atBoundary.Snapshot.Coins != coinsAfterFirst-PetFeedCost {
		t.Fatalf("boundary coins = %d, want %d", atBoundary.Snapshot.Coins, coinsAfterFirst-PetFeedCost)
	}
}

func TestPetRuntimeSkipsSleepingAndAwayThenResolvesReward(t *testing.T) {
	now := runtimeTestTime()
	nowMs := now.UnixMilli()
	repository := &memoryPetRepository{
		snapshots: map[string]PetMigrationSnapshot{
			"default": newRuntimeTestSnapshot("default", nowMs),
		},
	}
	snapshot := repository.getSnapshotForPet(DefaultPetID)
	snapshot.State.Hunger = 5
	snapshot.State.Sleeping = true
	snapshot.State.SleepEndsAt = nowMs + time.Minute.Milliseconds()
	repository.setSnapshotForPet(DefaultPetID, snapshot)

	runtime := newRuntimeTestRuntime(repository, DefaultPetID, &now)
	result, err := runtime.RunOnce()
	if err != nil {
		t.Fatalf("sleeping RunOnce() error = %v", err)
	}
	if result.Status != PetRuntimeStatusSleeping || len(result.Actions) != 0 {
		t.Fatalf("sleeping result = %#v", result)
	}

	snapshot = repository.getSnapshotForPet(DefaultPetID)
	snapshot.State.Sleeping = false
	snapshot.State.SleepEndsAt = 0
	snapshot.State.AwayTask = &PetAwayTask{
		Kind:      PetAwayWork,
		StartedAt: nowMs,
		EndsAt:    nowMs + time.Minute.Milliseconds(),
	}
	repository.setSnapshotForPet(DefaultPetID, snapshot)
	now = now.Add(time.Minute)
	result, err = runtime.RunOnce()
	if err != nil {
		t.Fatalf("away reward RunOnce() error = %v", err)
	}
	if result.Status != PetRuntimeStatusRewarded || result.Reward == nil {
		t.Fatalf("away reward result = %#v", result)
	}
	if result.Reward.Kind != PetAwayWork || result.Reward.Coins != PetWorkRewardCoins {
		t.Fatalf("away reward = %#v", result.Reward)
	}
	if result.Snapshot.AwayTask != nil {
		t.Fatalf("away task was not resolved: %#v", result.Snapshot.AwayTask)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("reward run should not auto-care: %#v", result.Actions)
	}
}

func TestPetRuntimeReportsBusinessFailuresAndPersistenceErrors(t *testing.T) {
	now := runtimeTestTime()
	nowMs := now.UnixMilli()
	repository := &memoryPetRepository{
		snapshots: map[string]PetMigrationSnapshot{
			"default": newRuntimeTestSnapshot("default", nowMs),
		},
	}
	snapshot := repository.getSnapshotForPet(DefaultPetID)
	snapshot.State.Hunger = 5
	snapshot.State.Cleanliness = 5
	snapshot.State.Mood = 5
	snapshot.State.Coins = 0
	repository.setSnapshotForPet(DefaultPetID, snapshot)

	runtime := newRuntimeTestRuntime(repository, DefaultPetID, &now)
	result, err := runtime.RunOnce()
	if err != nil {
		t.Fatalf("business failure should not return error: %v", err)
	}
	if result.Status != PetRuntimeStatusActionFailed || !result.Failed {
		t.Fatalf("failure result = %#v", result)
	}
	if len(result.Actions) != 4 {
		t.Fatalf("failure actions = %#v, want all fallback attempts", result.Actions)
	}
	if result.Actions[0].Action != PetActionFeed || result.Actions[0].Reason != PetActionFailureCoins {
		t.Fatalf("feed failure = %#v", result.Actions[0])
	}
	if result.Actions[1].Action != PetActionSoak || result.Actions[1].Reason != PetActionFailureLevel {
		t.Fatalf("soak failure = %#v", result.Actions[1])
	}
	if result.Actions[2].Action != PetActionBathe || result.Actions[2].Reason != PetActionFailureCoins {
		t.Fatalf("bathe failure = %#v", result.Actions[2])
	}
	if result.Actions[3].Action != PetActionPlay || result.Actions[3].Reason != PetActionFailureHungry {
		t.Fatalf("play failure = %#v", result.Actions[3])
	}

	// 持久化失败不是业务拒绝：必须同时保留动作错误并返回 error，不能把它伪装成 action_failed。
	repository = &memoryPetRepository{
		snapshots: map[string]PetMigrationSnapshot{
			"default": newRuntimeTestSnapshot("default", nowMs),
		},
		saveErr: errors.New("save failed"),
	}
	snapshot = repository.getSnapshotForPet(DefaultPetID)
	snapshot.State.Hunger = 5
	snapshot.State.Coins = 100
	repository.setSnapshotForPet(DefaultPetID, snapshot)
	runtime = newRuntimeTestRuntime(repository, DefaultPetID, &now)
	result, err = runtime.RunOnce()
	if err == nil || result.Status != PetRuntimeStatusError || result.Error == "" {
		t.Fatalf("persistence error result = %#v, error = %v", result, err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Error == "" {
		t.Fatalf("persistence action error = %#v", result.Actions)
	}
}

func TestPetRuntimeClockEmitterAndMultiPetIsolation(t *testing.T) {
	now := runtimeTestTime()
	nowMs := now.UnixMilli()
	repository := &memoryPetRepository{
		snapshots: map[string]PetMigrationSnapshot{
			"alpha": newRuntimeTestSnapshot("alpha", nowMs),
			"beta":  newRuntimeTestSnapshot("beta", nowMs),
		},
	}
	alpha := repository.getSnapshotForPet("alpha")
	alpha.State.Hunger = 0
	alpha.State.Coins = 100
	repository.setSnapshotForPet("alpha", alpha)

	beta := repository.getSnapshotForPet("beta")
	beta.State.Hunger = 80
	beta.State.Cleanliness = 80
	beta.State.Mood = 80
	repository.setSnapshotForPet("beta", beta)

	events := make([]PetRuntimeResult, 0, 1)
	alphaRuntime := newRuntimeTestRuntime(repository, "alpha", &now, PetRuntimeOptions{
		Emitter: PetRuntimeEmitterFunc(func(result PetRuntimeResult) {
			events = append(events, result)
		}),
	})
	betaRuntime := newRuntimeTestRuntime(repository, "beta", &now)

	alphaResult, err := alphaRuntime.Tick()
	if err != nil {
		t.Fatalf("alpha Tick() error = %v", err)
	}
	if alphaResult.PetID != "alpha" || alphaResult.At != nowMs || alphaResult.Status != PetRuntimeStatusExecuted {
		t.Fatalf("alpha result = %#v", alphaResult)
	}
	if len(events) != 1 || events[0].PetID != "alpha" {
		t.Fatalf("emitted events = %#v", events)
	}

	betaResult, err := betaRuntime.RunOnce()
	if err != nil {
		t.Fatalf("beta RunOnce() error = %v", err)
	}
	if betaResult.PetID != "beta" || betaResult.Status != PetRuntimeStatusNoNeed {
		t.Fatalf("beta result = %#v", betaResult)
	}
	if got := repository.getSnapshotForPet("beta").State.Coins; got != beta.State.Coins {
		t.Fatalf("beta coins changed by alpha runtime: got %d want %d", got, beta.State.Coins)
	}
}

func (r *memoryPetRepository) setSnapshotForPet(petID string, snapshot PetMigrationSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshots == nil {
		r.snapshots = make(map[string]PetMigrationSnapshot)
	}
	r.snapshots[petID] = clonePetSnapshotForTest(snapshot)
}
