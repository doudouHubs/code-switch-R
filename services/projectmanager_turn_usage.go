package services

import "github.com/tidwall/gjson"

// projectManagerRolloutUsageAccumulator 追踪 rollout 内累计快照。
// token_count 可能被重复写入；total_token_usage 不变时跳过，避免把同一次模型调用翻倍。
type projectManagerRolloutUsageAccumulator struct {
	hasTotal bool
	total    int64
}

func newProjectManagerRolloutUsageAccumulator() *projectManagerRolloutUsageAccumulator {
	return &projectManagerRolloutUsageAccumulator{}
}

func (a *projectManagerRolloutUsageAccumulator) add(line string, turn *projectManagerRolloutTurn) {
	if turn == nil {
		return
	}

	lastUsage := gjson.Get(line, "payload.info.last_token_usage")
	if !lastUsage.Exists() {
		// 旧日志只有 token_count 总数，无法拆出一次调用的真实增量，宁可不展示也不瞎算。
		return
	}

	cumulative := gjson.Get(line, "payload.info.total_token_usage.total_tokens")
	if cumulative.Exists() {
		currentTotal := cumulative.Int()
		if a.hasTotal && currentTotal == a.total {
			return
		}
		// 会话压缩、恢复或日志拼接后累计计数可能回退；回退不是重复事件，
		// 此时以本次 last_token_usage 继续累计，保证已发生的调用不丢失。
		a.total = currentTotal
		a.hasTotal = true
	}

	if turn.Usage == nil {
		turn.Usage = &SessionConversationTurnUsage{}
	}

	input := lastUsage.Get("input_tokens").Int()
	output := lastUsage.Get("output_tokens").Int()
	total := lastUsage.Get("total_tokens").Int()
	if total == 0 {
		total = input + output
	}

	turn.Usage.InputTokens += input
	turn.Usage.CachedInputTokens += lastUsage.Get("cached_input_tokens").Int()
	turn.Usage.OutputTokens += output
	turn.Usage.ReasoningOutputTokens += lastUsage.Get("reasoning_output_tokens").Int()
	turn.Usage.TotalTokens += total
	turn.Usage.ModelCalls++
}

func projectManagerFinalizeRolloutTurnUsage(turn *projectManagerRolloutTurn) {
	if turn == nil || turn.Usage == nil || turn.Usage.ModelCalls == 0 {
		return
	}

	endAt := turn.CompletedAt
	if endAt == 0 {
		endAt = turn.LastActivityAt
	}
	if turn.StartedAt > 0 && endAt >= turn.StartedAt {
		turn.Usage.DurationMS = endAt - turn.StartedAt
	}
	turn.Usage.Complete = turn.CompletedAt > 0
}

func projectManagerCloneTurnUsage(usage *SessionConversationTurnUsage) *SessionConversationTurnUsage {
	if usage == nil || usage.ModelCalls == 0 {
		return nil
	}

	copy := *usage
	return &copy
}
