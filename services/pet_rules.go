package services

import (
	"math"
	"time"
)

const (
	PetTickInterval                    = 30 * time.Second
	PetWorkDuration                    = 30 * time.Minute
	PetStudyDuration                   = 20 * time.Minute
	PetSleepDuration                   = 20 * time.Minute
	PetSleepMoodRecoveryPerMin         = 2.0
	PetWorkRewardCoins         int64   = 60
	PetWorkRewardGrowth                = 30.0
	PetStudyRewardGrowth               = 240.0
	PetFeedCost                int64   = 10
	PetBatheCost               int64   = 6
	PetSoakCost                int64   = 15
	PetStudyCost               int64   = 20
	PetDailyBonusCoins         int64   = 20
	PetWorkMinLevel                    = 4
	PetStudyMinLevel                   = 6
	PetSoakMinLevel                    = 2
	PetRetroCoinCap            float64 = 200
	PetLevelGrowthCoefficient          = 5000.0
)

// GetPetLevel 与源项目使用同一条平方成长曲线，避免等级在 Go/Vue 两端出现边界差异。
func GetPetLevel(growth float64) int {
	if math.IsNaN(growth) || math.IsInf(growth, 0) || growth < 0 {
		growth = 0
	}
	return int(math.Floor(math.Sqrt(growth/PetLevelGrowthCoefficient))) + 1
}

func GetGrowthForLevel(level int) float64 {
	if level < 1 {
		level = 1
	}
	delta := float64(level - 1)
	return PetLevelGrowthCoefficient * delta * delta
}

func GetLevelProgress(growth float64) float64 {
	level := GetPetLevel(growth)
	current := GetGrowthForLevel(level)
	next := GetGrowthForLevel(level + 1)
	if next <= current {
		return 0
	}
	progress := (growth - current) / (next - current)
	return clampPetNumber(progress, 0, 1)
}

func GetCombinedPetGrowth(state PetState, totalExp float64) float64 {
	if math.IsNaN(totalExp) || math.IsInf(totalExp, 0) || totalExp < 0 {
		totalExp = 0
	}
	return clampPetNumber(state.Growth, 0, math.MaxFloat64) + totalExp
}

// TickPet 只计算状态转移，不写数据库。跨越睡眠结束点时必须分两段结算，
// 否则离线唤醒会把整段清醒时间错误地按睡眠恢复心情。
func TickPet(state PetState, now int64) PetState {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	state = NormalizePetState(state, now)
	elapsed := now - state.LastTickAt
	if elapsed < 1000 {
		return state
	}

	if state.Sleeping && state.SleepEndsAt > 0 && now >= state.SleepEndsAt {
		slept := applyPetDecay(state, state.SleepEndsAt-state.LastTickAt)
		slept.LastTickAt = state.SleepEndsAt
		slept.Sleeping = false
		slept.SleepEndsAt = 0
		awake := applyPetDecay(slept, now-state.SleepEndsAt)
		awake.LastTickAt = now
		return awake
	}

	state = applyPetDecay(state, elapsed)
	state.LastTickAt = now
	return state
}

func applyPetDecay(state PetState, elapsedMs int64) PetState {
	minutes := math.Min(float64(elapsedMs), float64(24*time.Hour/time.Millisecond)) / float64(time.Minute/time.Millisecond)
	if minutes <= 0 {
		return state
	}

	restFactor := 1.0
	if state.Sleeping {
		restFactor = 0.4
	} else if state.AwayTask != nil {
		restFactor = 0.5
	}
	hunger := clampPetNumber(state.Hunger-0.8*restFactor*minutes, 0, 100)
	cleanliness := clampPetNumber(state.Cleanliness-0.5*restFactor*minutes, 0, 100)
	uncomfortable := hunger < 30 || cleanliness < 30
	var moodDelta float64
	if state.Sleeping {
		factor := 1.0
		if uncomfortable {
			factor = 0.4
		}
		moodDelta = PetSleepMoodRecoveryPerMin * factor * minutes
	} else if uncomfortable {
		moodDelta = -1.2 * minutes
	} else {
		moodDelta = 0.6 * minutes
	}

	state.Hunger = hunger
	state.Cleanliness = cleanliness
	state.Mood = clampPetNumber(state.Mood+moodDelta, 0, 100)
	return state
}

func PetFeed(state PetState) (PetState, PetActionResult) {
	state = NormalizePetState(state, state.LastTickAt)
	if state.AwayTask != nil {
		return state, petActionFailure(PetActionFailureBusy)
	}
	if state.Sleeping {
		return state, petActionFailure(PetActionFailureSleeping)
	}
	if state.Hunger >= 95 {
		return state, petActionFailure(PetActionFailureFull)
	}
	if state.Coins < PetFeedCost {
		return state, petActionFailure(PetActionFailureCoins)
	}
	state.Coins -= PetFeedCost
	state.Hunger = clampPetNumber(state.Hunger+35, 0, 100)
	state.Mood = clampPetNumber(state.Mood+2, 0, 100)
	return state, PetActionResult{OK: true}
}

func PetBathe(state PetState) (PetState, PetActionResult) {
	state = NormalizePetState(state, state.LastTickAt)
	if state.AwayTask != nil {
		return state, petActionFailure(PetActionFailureBusy)
	}
	if state.Sleeping {
		return state, petActionFailure(PetActionFailureSleeping)
	}
	if state.Cleanliness >= 95 {
		return state, petActionFailure(PetActionFailureClean)
	}
	if state.Coins < PetBatheCost {
		return state, petActionFailure(PetActionFailureCoins)
	}
	state.Coins -= PetBatheCost
	state.Cleanliness = clampPetNumber(state.Cleanliness+45, 0, 100)
	state.Mood = clampPetNumber(state.Mood+1, 0, 100)
	return state, PetActionResult{OK: true}
}

func PetSoak(state PetState, totalExp float64) (PetState, PetActionResult) {
	state = NormalizePetState(state, state.LastTickAt)
	if state.AwayTask != nil {
		return state, petActionFailure(PetActionFailureBusy)
	}
	if state.Sleeping {
		return state, petActionFailure(PetActionFailureSleeping)
	}
	if GetPetLevel(GetCombinedPetGrowth(state, totalExp)) < PetSoakMinLevel {
		return state, petActionFailure(PetActionFailureLevel)
	}
	if state.Coins < PetSoakCost {
		return state, petActionFailure(PetActionFailureCoins)
	}
	state.Coins -= PetSoakCost
	state.Cleanliness = clampPetNumber(state.Cleanliness+30, 0, 100)
	state.Mood = clampPetNumber(state.Mood+28, 0, 100)
	return state, PetActionResult{OK: true}
}

func PetPlay(state PetState) (PetState, PetActionResult) {
	state = NormalizePetState(state, state.LastTickAt)
	if state.AwayTask != nil {
		return state, petActionFailure(PetActionFailureBusy)
	}
	if state.Sleeping {
		return state, petActionFailure(PetActionFailureSleeping)
	}
	if state.Hunger < 10 {
		return state, petActionFailure(PetActionFailureHungry)
	}
	state.Mood = clampPetNumber(state.Mood+18, 0, 100)
	state.Hunger = clampPetNumber(state.Hunger-6, 0, 100)
	state.Cleanliness = clampPetNumber(state.Cleanliness-4, 0, 100)
	return state, PetActionResult{OK: true}
}

func PetToggleSleep(state PetState, now int64) PetState {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	state = TickPet(state, now)
	if state.AwayTask != nil {
		return state
	}
	if state.Sleeping {
		state.Sleeping = false
		state.SleepEndsAt = 0
		return state
	}
	state.Sleeping = true
	state.SleepEndsAt = now + PetSleepDuration.Milliseconds()
	return state
}

func PetStartWork(state PetState, totalExp float64, now int64) (PetState, PetActionResult) {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	state = NormalizePetState(state, now)
	if state.AwayTask != nil {
		return state, petActionFailure(PetActionFailureBusy)
	}
	if GetPetLevel(GetCombinedPetGrowth(state, totalExp)) < PetWorkMinLevel {
		return state, petActionFailure(PetActionFailureLevel)
	}
	if state.Hunger < 20 {
		return state, petActionFailure(PetActionFailureHungry)
	}
	state.Sleeping = false
	state.SleepEndsAt = 0
	state.AwayTask = &PetAwayTask{Kind: PetAwayWork, StartedAt: now, EndsAt: now + PetWorkDuration.Milliseconds()}
	return state, PetActionResult{OK: true}
}

func PetStartStudy(state PetState, totalExp float64, now int64) (PetState, PetActionResult) {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	state = NormalizePetState(state, now)
	if state.AwayTask != nil {
		return state, petActionFailure(PetActionFailureBusy)
	}
	if GetPetLevel(GetCombinedPetGrowth(state, totalExp)) < PetStudyMinLevel {
		return state, petActionFailure(PetActionFailureLevel)
	}
	if state.Coins < PetStudyCost {
		return state, petActionFailure(PetActionFailureCoins)
	}
	if state.Hunger < 20 {
		return state, petActionFailure(PetActionFailureHungry)
	}
	state.Sleeping = false
	state.SleepEndsAt = 0
	state.Coins -= PetStudyCost
	state.AwayTask = &PetAwayTask{Kind: PetAwayStudy, StartedAt: now, EndsAt: now + PetStudyDuration.Milliseconds()}
	return state, PetActionResult{OK: true}
}

// PetEndWorkEarly 只结算已经过去的衰减，不发放未完成的工作奖励。
func PetEndWorkEarly(state PetState, now int64) (PetState, bool) {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	if state.AwayTask == nil || state.AwayTask.Kind != PetAwayWork || now >= state.AwayTask.EndsAt {
		return state, false
	}
	state = TickPet(state, now)
	if state.AwayTask == nil || state.AwayTask.Kind != PetAwayWork {
		return state, false
	}
	state.AwayTask = nil
	return state, true
}

func PetResolveAwayTask(state PetState, now int64) (PetState, *PetAwayReward) {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	if state.AwayTask == nil || now < state.AwayTask.EndsAt {
		return state, nil
	}

	reward := &PetAwayReward{}
	if state.AwayTask.Kind == PetAwayWork {
		reward.Kind = PetAwayWork
		reward.Coins = PetWorkRewardCoins
		reward.Growth = PetWorkRewardGrowth
	} else if state.AwayTask.Kind == PetAwayStudy {
		reward.Kind = PetAwayStudy
		reward.Growth = PetStudyRewardGrowth
	} else {
		return state, nil
	}
	state.AwayTask = nil
	state.Coins += reward.Coins
	state.Growth += reward.Growth
	state.Hunger = clampPetNumber(state.Hunger-10, 0, 100)
	state.Cleanliness = clampPetNumber(state.Cleanliness-8, 0, 100)
	return state, reward
}

func PetPetted(state PetState) PetState {
	if state.Sleeping || state.AwayTask != nil {
		return state
	}
	state.Mood = clampPetNumber(state.Mood+3, 0, 100)
	return state
}

func PetMarkMilestone(state PetState, days int64) PetState {
	if days > state.LastMilestoneDays {
		state.LastMilestoneDays = days
	}
	return state
}

func PetApplyDreamEmotion(state PetState, emotion PetDreamEmotion) PetState {
	if !state.Sleeping {
		return state
	}
	delta := map[PetDreamEmotion]float64{
		PetDreamPleasant: 2,
		PetDreamCalm:     0,
		PetDreamTense:    -1,
		PetDreamAfraid:   -3,
	}[emotion]
	if !IsPetDreamEmotion(emotion) {
		return state
	}
	state.Mood = clampPetNumber(state.Mood+delta, 0, 100)
	return state
}

func PetLocalDateKey(now int64) string {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	return time.UnixMilli(now).Local().Format("2006-01-02")
}

func GetPetProactiveCountToday(state PetState, now int64) int64 {
	if state.ProactiveDate != PetLocalDateKey(now) {
		return 0
	}
	return state.ProactiveCount
}

func PetRecordProactive(state PetState, now int64) PetState {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	today := PetLocalDateKey(now)
	if state.ProactiveDate == today {
		state.ProactiveCount++
	} else {
		state.ProactiveDate = today
		state.ProactiveCount = 1
	}
	state.LastProactiveAt = now
	return state
}

func PetClaimDailyBonus(state PetState, now int64) (PetState, int64) {
	today := PetLocalDateKey(now)
	if state.LastDailyBonusDate == today {
		return state, 0
	}
	state.LastDailyBonusDate = today
	state.Coins += PetDailyBonusCoins
	return state, PetDailyBonusCoins
}

func PetCreditExpCoins(state PetState, totalExp float64) (PetState, float64) {
	if math.IsNaN(totalExp) || math.IsInf(totalExp, 0) || totalExp <= state.CoinCreditedExp {
		return state, 0
	}
	delta := totalExp - state.CoinCreditedExp
	credit := delta
	if state.CoinCreditedExp == 0 && credit > PetRetroCoinCap {
		credit = PetRetroCoinCap
	}
	state.Coins += int64(credit)
	state.CoinCreditedExp = totalExp
	return state, credit
}

func PetAddCoins(state PetState, amount int64) PetState {
	if amount > 0 {
		state.Coins += amount
	}
	return state
}

func petActionFailure(reason PetActionFailureReason) PetActionResult {
	return PetActionResult{Reason: reason}
}

func clampPetNumber(value, min, max float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return min
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
