package services

import (
	"math"
	"testing"
	"time"
)

func testPetState(now int64) PetState {
	state := DefaultPetStateAt(now)
	state.PetID = "test"
	return state
}

func TestPetLevelCurve(t *testing.T) {
	t.Parallel()
	tests := []struct {
		growth float64
		want   int
	}{
		{growth: 0, want: 1},
		{growth: GetGrowthForLevel(2), want: 2},
		{growth: GetGrowthForLevel(3) - 1, want: 2},
		{growth: GetGrowthForLevel(4), want: 4},
	}
	for _, tt := range tests {
		if got := GetPetLevel(tt.growth); got != tt.want {
			t.Fatalf("GetPetLevel(%v) = %d, want %d", tt.growth, got, tt.want)
		}
	}
	if got := GetLevelProgress(GetGrowthForLevel(2)); got != 0 {
		t.Fatalf("level boundary progress = %v, want 0", got)
	}
	if got := GetLevelProgress(GetGrowthForLevel(2) + 2500); math.Abs(got-(1.0/6.0)) > 1e-9 {
		t.Fatalf("level progress = %v, want 0.1666666667", got)
	}
}

func TestTickPetSplitsSleepBoundary(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 11, 8, 0, 0, 0, time.Local).UnixMilli()
	state := testPetState(start)
	state.Hunger = 80
	state.Cleanliness = 80
	state.Mood = 50
	state.Sleeping = true
	state.SleepEndsAt = start + time.Minute.Milliseconds()

	got := TickPet(state, start+2*time.Minute.Milliseconds())
	if got.Sleeping || got.SleepEndsAt != 0 {
		t.Fatalf("pet should auto-wake: %#v", got)
	}
	// First minute recovers +2, second minute awake and comfortable drifts +0.6.
	if math.Abs(got.Mood-52.6) > 1e-9 {
		t.Fatalf("mood = %v, want 52.6", got.Mood)
	}
	if math.Abs(got.Hunger-78.88) > 1e-9 {
		t.Fatalf("hunger = %v, want 78.88", got.Hunger)
	}
}

func TestPetCareActions(t *testing.T) {
	t.Parallel()
	now := time.Now().UnixMilli()
	state := testPetState(now)
	state.Hunger = 50
	state.Cleanliness = 40
	state.Coins = 100

	state, result := PetFeed(state)
	if !result.OK || state.Coins != 90 || state.Hunger != 85 || state.Mood != 72 {
		t.Fatalf("feed result/state mismatch: %#v %#v", result, state)
	}
	state, result = PetBathe(state)
	if !result.OK || state.Coins != 84 || state.Cleanliness != 85 || state.Mood != 73 {
		t.Fatalf("bathe result/state mismatch: %#v %#v", result, state)
	}
	state, result = PetSoak(state, GetGrowthForLevel(2))
	if !result.OK || state.Coins != 69 || state.Cleanliness != 100 || state.Mood != 100 {
		t.Fatalf("soak result/state mismatch: %#v %#v", result, state)
	}
	state.Hunger = 10
	state, result = PetPlay(state)
	if !result.OK || state.Hunger != 4 || state.Cleanliness != 96 || state.Mood != 100 {
		t.Fatalf("play result/state mismatch: %#v %#v", result, state)
	}

	state.Sleeping = true
	_, result = PetFeed(state)
	if result.Reason != PetActionFailureSleeping {
		t.Fatalf("sleeping feed reason = %q, want sleeping", result.Reason)
	}
}

func TestPetAwayTasks(t *testing.T) {
	t.Parallel()
	now := time.Now().UnixMilli()
	state := testPetState(now)
	state.Hunger = 80
	state.Coins = 100
	state, result := PetStartWork(state, GetGrowthForLevel(4), now)
	if !result.OK || state.AwayTask == nil || state.AwayTask.Kind != PetAwayWork {
		t.Fatalf("start work failed: %#v %#v", state, result)
	}
	state, ended := PetEndWorkEarly(state, now+time.Minute.Milliseconds())
	if !ended || state.AwayTask != nil || state.Coins != 100 {
		t.Fatalf("early work should not reward: %#v ended=%v", state, ended)
	}

	state, result = PetStartStudy(state, GetGrowthForLevel(6), now)
	if !result.OK || state.Coins != 80 {
		t.Fatalf("start study failed: %#v %#v", state, result)
	}
	state, reward := PetResolveAwayTask(state, now+PetStudyDuration.Milliseconds())
	if reward == nil || reward.Kind != PetAwayStudy || reward.Growth != PetStudyRewardGrowth {
		t.Fatalf("study reward = %#v", reward)
	}
	if state.AwayTask != nil || state.Growth != PetStudyRewardGrowth || state.Coins != 80 {
		t.Fatalf("study resolution state = %#v", state)
	}
}

func TestPetDailyAndExperienceCoins(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 8, 11, 9, 0, 0, 0, time.Local).UnixMilli()
	state := testPetState(day)
	var bonus int64
	state, bonus = PetClaimDailyBonus(state, day)
	if bonus != PetDailyBonusCoins || state.Coins != 140 {
		t.Fatalf("daily bonus = %d state=%#v", bonus, state)
	}
	state, bonus = PetClaimDailyBonus(state, day+time.Hour.Milliseconds())
	if bonus != 0 || state.Coins != 140 {
		t.Fatalf("duplicate daily bonus = %d state=%#v", bonus, state)
	}

	state, credited := PetCreditExpCoins(state, 500)
	if credited != PetRetroCoinCap || state.Coins != 340 || state.CoinCreditedExp != 500 {
		t.Fatalf("retro credit = %v state=%#v", credited, state)
	}
	state, credited = PetCreditExpCoins(state, 550)
	if credited != 50 || state.Coins != 390 {
		t.Fatalf("incremental credit = %v state=%#v", credited, state)
	}
	state = PetRecordProactive(state, day)
	state = PetRecordProactive(state, day+time.Hour.Milliseconds())
	if GetPetProactiveCountToday(state, day) != 2 {
		t.Fatalf("proactive count = %d", GetPetProactiveCountToday(state, day))
	}
}
