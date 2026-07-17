package services

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

var errProjectManagerSessionFileNotFound = errors.New("未找到会话源文件")

type projectManagerConversationFile struct {
	SessionID string
	Path      string
	IsRollout bool
}

type projectManagerConversationTurn struct {
	User   SessionConversationItem
	Agents []SessionConversationItem
}

type projectManagerConversationPrunePlan struct {
	SessionID      string
	TargetIDs      map[string]struct{}
	TargetUserIDs  map[string]struct{}
	TargetAgentIDs map[string]struct{}
	Turns          []projectManagerConversationTurn
}

type projectManagerRolloutRecord struct {
	LineIndex   int
	Type        string
	Timestamp   int64
	PayloadType string
	Role        string
	Message     string
}

type projectManagerRolloutTurn struct {
	TurnID           string
	StartLineIndex   int
	EndLineIndex     int
	UserMessage      string
	AgentLineIndices []int
	Records          []projectManagerRolloutRecord
}

type projectManagerRolloutFile struct {
	SessionID string
	Path      string
	Lines     []string
	Turns     []projectManagerRolloutTurn
}

func projectManagerIsRolloutSessionPath(path string) bool {
	name := strings.ToLower(strings.TrimSpace(filepath.Base(path)))
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

func (s *ProjectManagerService) findProjectManagerSessionFileByID(sessionID string) (projectManagerConversationFile, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return projectManagerConversationFile{}, fmt.Errorf("会话 ID 不能为空")
	}

	sessionFiles, err := s.readProjectManagerSessionFiles(map[string]projectManagerSessionIndexEntry{})
	if err != nil {
		return projectManagerConversationFile{}, err
	}

	var bestPrimary *projectManagerSessionFileEntry
	var bestRollout *projectManagerSessionFileEntry
	for index := range sessionFiles {
		item := &sessionFiles[index]
		if strings.TrimSpace(item.SessionID) != sessionID {
			continue
		}

		if item.IsRollout {
			if bestRollout == nil {
				bestRollout = item
				continue
			}
			if item.UpdatedAt.After(bestRollout.UpdatedAt) {
				bestRollout = item
				continue
			}
			if item.UpdatedAt.Equal(bestRollout.UpdatedAt) && strings.ToLower(item.Path) < strings.ToLower(bestRollout.Path) {
				bestRollout = item
			}
			continue
		}

		if bestPrimary == nil {
			bestPrimary = item
			continue
		}
		if item.UpdatedAt.After(bestPrimary.UpdatedAt) {
			bestPrimary = item
			continue
		}
		if item.UpdatedAt.Equal(bestPrimary.UpdatedAt) && strings.ToLower(item.Path) < strings.ToLower(bestPrimary.Path) {
			bestPrimary = item
		}
	}

	// 真实历史里存在大量只有 rollout、没有主会话文件的老会话。
	// 这里必须优先主会话、其次 rollout 回退，不然详情页会把这批历史全判死刑。
	if bestPrimary != nil {
		return projectManagerConversationFile{
			SessionID: sessionID,
			Path:      bestPrimary.Path,
			IsRollout: false,
		}, nil
	}
	if bestRollout != nil {
		return projectManagerConversationFile{
			SessionID: sessionID,
			Path:      bestRollout.Path,
			IsRollout: true,
		}, nil
	}

	return projectManagerConversationFile{}, fmt.Errorf("%w: %s", errProjectManagerSessionFileNotFound, sessionID)
}

func (s *ProjectManagerService) findProjectManagerSessionFileByIDFast(
	sessionID string,
	cache projectManagerSnapshotCache,
) (projectManagerConversationFile, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return projectManagerConversationFile{}, fmt.Errorf("会话 ID 不能为空")
	}

	if file, ok := projectManagerSelectConversationSourceFromCache(sessionID, cache); ok {
		return file, nil
	}
	return s.findProjectManagerSessionFileByID(sessionID)
}

func (s *ProjectManagerService) findProjectManagerRolloutFilesByID(sessionID string) ([]projectManagerConversationFile, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("会话 ID 不能为空")
	}

	sessionFiles, err := s.readProjectManagerSessionFiles(map[string]projectManagerSessionIndexEntry{})
	if err != nil {
		return nil, err
	}

	result := make([]projectManagerConversationFile, 0, 4)
	for _, item := range sessionFiles {
		if strings.TrimSpace(item.SessionID) != sessionID || !item.IsRollout {
			continue
		}
		result = append(result, projectManagerConversationFile{
			SessionID: sessionID,
			Path:      item.Path,
			IsRollout: true,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Path) < strings.ToLower(result[j].Path)
	})
	return result, nil
}

func projectManagerSelectConversationSourceFromCache(
	sessionID string,
	cache projectManagerSnapshotCache,
) (projectManagerConversationFile, bool) {
	if !cache.isUsable() || len(cache.SessionFiles) == 0 {
		return projectManagerConversationFile{}, false
	}

	var bestPrimary *projectManagerSessionFileEntry
	var bestRollout *projectManagerSessionFileEntry
	for _, cached := range cache.SessionFiles {
		entry := cached.Entry
		if strings.TrimSpace(entry.SessionID) != sessionID {
			continue
		}

		if entry.IsRollout {
			bestRollout = projectManagerSelectNewerSessionFileEntry(bestRollout, &entry)
			continue
		}
		bestPrimary = projectManagerSelectNewerSessionFileEntry(bestPrimary, &entry)
	}

	if bestPrimary != nil {
		return projectManagerConversationFile{
			SessionID: sessionID,
			Path:      bestPrimary.Path,
			IsRollout: false,
		}, true
	}
	if bestRollout != nil {
		return projectManagerConversationFile{
			SessionID: sessionID,
			Path:      bestRollout.Path,
			IsRollout: true,
		}, true
	}

	return projectManagerConversationFile{}, false
}

func projectManagerSelectNewerSessionFileEntry(
	current *projectManagerSessionFileEntry,
	candidate *projectManagerSessionFileEntry,
) *projectManagerSessionFileEntry {
	if candidate == nil {
		return current
	}
	if current == nil {
		return candidate
	}
	if candidate.UpdatedAt.After(current.UpdatedAt) {
		return candidate
	}
	if candidate.UpdatedAt.Equal(current.UpdatedAt) && strings.ToLower(candidate.Path) < strings.ToLower(current.Path) {
		return candidate
	}
	return current
}

func readProjectManagerSessionConversationItems(path string, sessionID string) ([]SessionConversationItem, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 4*1024)
	// 会话中可能夹着大块工具输出；详情和全文搜索共享这条解析链，
	// 必须与快照扫描保持同一上限，否则一个超长非消息行就会让整次项目搜索失败。
	scanner.Buffer(buf, 16*1024*1024)

	items := make([]SessionConversationItem, 0, 64)
	currentUserID := ""
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if gjson.Get(line, "type").String() != "event_msg" {
			continue
		}

		payloadType := gjson.Get(line, "payload.type").String()
		if payloadType != "user_message" && payloadType != "agent_message" {
			continue
		}

		content := strings.TrimSpace(gjson.Get(line, "payload.message").String())
		if content == "" {
			continue
		}

		itemID := buildProjectManagerConversationMessageID(sessionID, payloadType, lineNumber)
		item := SessionConversationItem{
			ID:         itemID,
			SessionID:  sessionID,
			Role:       projectManagerConversationRole(payloadType),
			Content:    content,
			Timestamp:  parseProjectManagerConversationTimestamp(gjson.Get(line, "timestamp").String()),
			SourceFile: path,
			SourceLine: lineNumber,
		}

		// 这里把“agent 属于哪个 user”在后端直接定死。
		// 前端后续无论做批量勾选、折叠还是剪枝，统一消费 reply_for，避免每个入口各自猜一套关联规则。
		if payloadType == "user_message" {
			currentUserID = itemID
		} else if currentUserID != "" {
			item.ReplyFor = currentUserID
		}

		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

func readProjectManagerRolloutConversationItems(path string, sessionID string) ([]SessionConversationItem, error) {
	parsed, err := parseProjectManagerRolloutFile(path, sessionID)
	if err != nil {
		return nil, err
	}

	items := make([]SessionConversationItem, 0, 64)
	currentUserID := ""
	for _, turn := range parsed.Turns {
		for _, record := range turn.Records {
			if record.Type != "event_msg" {
				continue
			}
			if record.PayloadType != "user_message" && record.PayloadType != "agent_message" {
				continue
			}
			content := strings.TrimSpace(record.Message)
			if content == "" {
				continue
			}

			itemID := buildProjectManagerConversationMessageID(sessionID, record.PayloadType, record.LineIndex+1)
			item := SessionConversationItem{
				ID:         itemID,
				SessionID:  sessionID,
				Role:       projectManagerConversationRole(record.PayloadType),
				Content:    content,
				Timestamp:  record.Timestamp,
				SourceFile: path,
				SourceLine: record.LineIndex + 1,
				TurnID:     strings.TrimSpace(turn.TurnID),
			}
			if record.PayloadType == "user_message" {
				currentUserID = itemID
			} else if currentUserID != "" {
				item.ReplyFor = currentUserID
			}
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *ProjectManagerService) hydrateProjectManagerConversationTurnIDsFromRollouts(sessionID string, items []SessionConversationItem) error {
	if len(items) == 0 {
		return nil
	}

	rolloutFiles, err := s.findProjectManagerRolloutFilesByID(sessionID)
	if err != nil {
		return err
	}

	var failed []string
	for _, rolloutFile := range rolloutFiles {
		parsed, err := parseProjectManagerRolloutFile(rolloutFile.Path, sessionID)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", rolloutFile.Path, err))
			continue
		}

		// 主会话文件没有稳定 turn_id，但 rollout 有 task_started.turn_id。
		// 这里按用户消息顺序和规范化内容回填，目的只是让原生 thread/fork 能精确截断；
		// 匹配不上就保持空值，后续 fork 入口 fail-fast，避免凭行号硬猜 turn。
		if applyProjectManagerConversationTurnIDsFromRollout(items, parsed) > 0 {
			return nil
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("解析 rollout 失败: %s", strings.Join(failed, "; "))
	}
	return nil
}

func applyProjectManagerConversationTurnIDsFromRollout(items []SessionConversationItem, rollout projectManagerRolloutFile) int {
	if len(items) == 0 || len(rollout.Turns) == 0 {
		return 0
	}

	itemIndexByID := make(map[string]int, len(items))
	for index, item := range items {
		itemIndexByID[item.ID] = index
	}

	turns, _ := buildProjectManagerConversationTurns(items)
	applied := 0
	rolloutTurnIndex := 0
	for _, conversationTurn := range turns {
		matchIndex := projectManagerMatchRolloutTurnIndex(rollout.Turns, conversationTurn.User.Content, rolloutTurnIndex)
		if matchIndex < 0 {
			continue
		}
		rolloutTurnIndex = matchIndex + 1

		turnID := strings.TrimSpace(rollout.Turns[matchIndex].TurnID)
		if turnID == "" {
			continue
		}

		if itemIndex, ok := itemIndexByID[conversationTurn.User.ID]; ok {
			items[itemIndex].TurnID = turnID
			applied++
		}
		for _, agent := range conversationTurn.Agents {
			if itemIndex, ok := itemIndexByID[agent.ID]; ok {
				items[itemIndex].TurnID = turnID
				applied++
			}
		}
	}
	return applied
}

func buildProjectManagerConversationTurns(items []SessionConversationItem) ([]projectManagerConversationTurn, map[string]int) {
	turns := make([]projectManagerConversationTurn, 0, 16)
	turnIndexByUserID := make(map[string]int, 16)

	for _, item := range items {
		if item.Role == "user" {
			turns = append(turns, projectManagerConversationTurn{
				User:   item,
				Agents: make([]SessionConversationItem, 0, 4),
			})
			turnIndexByUserID[item.ID] = len(turns) - 1
			continue
		}

		if item.Role != "agent" {
			continue
		}

		if item.ReplyFor != "" {
			if turnIndex, ok := turnIndexByUserID[item.ReplyFor]; ok {
				turns[turnIndex].Agents = append(turns[turnIndex].Agents, item)
				continue
			}
		}
		if len(turns) > 0 {
			turns[len(turns)-1].Agents = append(turns[len(turns)-1].Agents, item)
		}
	}

	return turns, turnIndexByUserID
}

func buildProjectManagerConversationForkTurnID(items []SessionConversationItem, messageIDs []string) (string, error) {
	itemIndexByID := make(map[string]int, len(items))
	for index, item := range items {
		itemIndexByID[item.ID] = index
	}

	latestSelectedIndex := -1
	for _, messageID := range messageIDs {
		trimmed := strings.TrimSpace(messageID)
		if trimmed == "" {
			continue
		}
		index, ok := itemIndexByID[trimmed]
		if !ok {
			return "", fmt.Errorf("消息不存在或已变化: %s", trimmed)
		}
		if index > latestSelectedIndex {
			latestSelectedIndex = index
		}
	}

	if latestSelectedIndex < 0 {
		return "", fmt.Errorf("没有可 fork 的消息")
	}

	turnID := strings.TrimSpace(items[latestSelectedIndex].TurnID)
	if turnID == "" {
		return "", fmt.Errorf("所选消息缺少 turn_id，无法使用 Codex 原生 fork")
	}
	return turnID, nil
}

func buildProjectManagerConversationPrunePlan(sessionID string, items []SessionConversationItem, messageIDs []string) (projectManagerConversationPrunePlan, error) {
	validIDs := make(map[string]SessionConversationItem, len(items))
	for _, item := range items {
		validIDs[item.ID] = item
	}
	turns, turnIndexByUserID := buildProjectManagerConversationTurns(items)

	targetIDs := make(map[string]struct{}, len(messageIDs))
	targetUserIDs := make(map[string]struct{}, len(messageIDs))
	targetAgentIDs := make(map[string]struct{}, len(messageIDs))

	for _, messageID := range messageIDs {
		trimmed := strings.TrimSpace(messageID)
		if trimmed == "" {
			continue
		}
		item, ok := validIDs[trimmed]
		if !ok {
			return projectManagerConversationPrunePlan{}, fmt.Errorf("消息不存在或已变化: %s", trimmed)
		}

		targetIDs[trimmed] = struct{}{}
		if item.Role == "user" {
			targetUserIDs[trimmed] = struct{}{}
			if turnIndex, ok := turnIndexByUserID[trimmed]; ok {
				// 需求上“删用户问题”就是删掉这轮问答，后端这里顺手把 reply 链补齐，
				// 防止前端哪天漏传关联 agent ID 时，主文件和 rollout 修剪语义跑偏。
				for _, agent := range turns[turnIndex].Agents {
					targetIDs[agent.ID] = struct{}{}
					targetAgentIDs[agent.ID] = struct{}{}
				}
			}
			continue
		}

		if item.Role == "agent" {
			targetAgentIDs[trimmed] = struct{}{}
		}
	}

	if len(targetIDs) == 0 {
		return projectManagerConversationPrunePlan{}, fmt.Errorf("没有可删除的消息")
	}

	return projectManagerConversationPrunePlan{
		SessionID:      sessionID,
		TargetIDs:      targetIDs,
		TargetUserIDs:  targetUserIDs,
		TargetAgentIDs: targetAgentIDs,
		Turns:          turns,
	}, nil
}

func pruneProjectManagerSessionConversationFile(path string, sessionID string, targetIDs map[string]struct{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))

	for index, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			kept = append(kept, rawLine)
			continue
		}

		if gjson.Get(trimmed, "type").String() != "event_msg" {
			kept = append(kept, rawLine)
			continue
		}

		payloadType := gjson.Get(trimmed, "payload.type").String()
		if payloadType != "user_message" && payloadType != "agent_message" {
			kept = append(kept, rawLine)
			continue
		}

		messageID := buildProjectManagerConversationMessageID(sessionID, payloadType, index+1)
		if _, shouldDelete := targetIDs[messageID]; shouldDelete {
			continue
		}

		kept = append(kept, rawLine)
	}

	// 主会话文件仍然只按用户可见消息做过滤，不去重排别的事件，
	// 这样对 Codex 原始历史的副作用最小，出了问题也更容易人工排查。
	return AtomicWriteText(path, strings.Join(kept, "\n"))
}

func (s *ProjectManagerService) pruneProjectManagerRolloutFiles(sessionID string, plan projectManagerConversationPrunePlan) error {
	rolloutFiles, err := s.findProjectManagerRolloutFilesByID(sessionID)
	if err != nil {
		return err
	}
	if len(rolloutFiles) == 0 {
		return nil
	}

	var failed []string
	for _, rolloutFile := range rolloutFiles {
		if err := pruneProjectManagerRolloutFile(rolloutFile.Path, sessionID, plan); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", rolloutFile.Path, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("同步修剪 rollout 失败: %s", strings.Join(failed, "; "))
	}
	return nil
}

func pruneProjectManagerRolloutFile(path string, sessionID string, plan projectManagerConversationPrunePlan) error {
	parsed, err := parseProjectManagerRolloutFile(path, sessionID)
	if err != nil {
		return err
	}
	if len(parsed.Turns) == 0 {
		return nil
	}

	targetLines := make(map[int]struct{}, 32)
	rolloutTurnIndex := 0

	for _, conversationTurn := range plan.Turns {
		_, deleteWholeTurn := plan.TargetUserIDs[conversationTurn.User.ID]
		selectedAgentIndexes := projectManagerSelectedAgentIndexes(conversationTurn, plan.TargetAgentIDs)
		if !deleteWholeTurn && len(selectedAgentIndexes) == 0 {
			continue
		}

		matchIndex := projectManagerMatchRolloutTurnIndex(parsed.Turns, conversationTurn.User.Content, rolloutTurnIndex)
		if matchIndex < 0 {
			log.Printf("[ProjectManager] rollout 未匹配到对应用户轮次，已跳过 session=%s file=%s user=%q", sessionID, path, conversationTurn.User.Content)
			continue
		}
		rolloutTurnIndex = matchIndex + 1
		turn := parsed.Turns[matchIndex]

		if deleteWholeTurn {
			for lineIndex := turn.StartLineIndex; lineIndex <= turn.EndLineIndex; lineIndex++ {
				targetLines[lineIndex] = struct{}{}
			}
			continue
		}

		for _, agentIndex := range selectedAgentIndexes {
			if agentIndex >= len(turn.AgentLineIndices) {
				log.Printf("[ProjectManager] rollout agent 链路数量不足，已跳过 session=%s file=%s user=%q agent_index=%d", sessionID, path, conversationTurn.User.Content, agentIndex)
				continue
			}

			startLine := turn.AgentLineIndices[agentIndex]
			endExclusive := turn.EndLineIndex
			if agentIndex+1 < len(turn.AgentLineIndices) {
				endExclusive = turn.AgentLineIndices[agentIndex+1]
			}
			if endExclusive < startLine {
				continue
			}

			// 这里故意不反向吞掉前面的 reasoning / function_call。
			// 原因是这些前置事件没有稳定 turn 内子标识，倒着吸附很容易把上一个 agent 链也带走。
			// 当前策略从 agent_message 往后裁到下一个 agent 或 task_complete 前，风险最可控。
			for lineIndex := startLine; lineIndex < endExclusive; lineIndex++ {
				targetLines[lineIndex] = struct{}{}
			}
		}
	}

	if len(targetLines) == 0 {
		return nil
	}

	kept := make([]string, 0, len(parsed.Lines))
	for lineIndex, rawLine := range parsed.Lines {
		if _, shouldDelete := targetLines[lineIndex]; shouldDelete {
			continue
		}
		kept = append(kept, rawLine)
	}
	return AtomicWriteText(path, strings.Join(kept, "\n"))
}

func parseProjectManagerRolloutFile(path string, expectedSessionID string) (projectManagerRolloutFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return projectManagerRolloutFile{}, err
	}

	lines := strings.Split(string(data), "\n")
	result := projectManagerRolloutFile{
		Path:  path,
		Lines: lines,
		Turns: make([]projectManagerRolloutTurn, 0, 8),
	}

	currentTurnIndex := -1
	for lineIndex, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}

		lineType := gjson.Get(trimmed, "type").String()
		payloadType := gjson.Get(trimmed, "payload.type").String()
		if lineType == "session_meta" && result.SessionID == "" {
			result.SessionID = strings.TrimSpace(gjson.Get(trimmed, "payload.id").String())
		}

		if lineType == "event_msg" && payloadType == "task_started" {
			turnID := strings.TrimSpace(gjson.Get(trimmed, "payload.turn_id").String())
			result.Turns = append(result.Turns, projectManagerRolloutTurn{
				TurnID:           turnID,
				StartLineIndex:   lineIndex,
				EndLineIndex:     len(lines) - 1,
				AgentLineIndices: make([]int, 0, 4),
				Records:          make([]projectManagerRolloutRecord, 0, 16),
			})
			currentTurnIndex = len(result.Turns) - 1
		}

		if currentTurnIndex < 0 {
			continue
		}

		record := projectManagerRolloutRecord{
			LineIndex:   lineIndex,
			Type:        lineType,
			Timestamp:   parseProjectManagerConversationTimestamp(gjson.Get(trimmed, "timestamp").String()),
			PayloadType: payloadType,
			Role:        strings.TrimSpace(gjson.Get(trimmed, "payload.role").String()),
			Message:     strings.TrimSpace(gjson.Get(trimmed, "payload.message").String()),
		}
		turn := &result.Turns[currentTurnIndex]
		turn.Records = append(turn.Records, record)

		if lineType == "event_msg" && payloadType == "user_message" && turn.UserMessage == "" {
			turn.UserMessage = record.Message
		}
		if lineType == "event_msg" && payloadType == "agent_message" {
			turn.AgentLineIndices = append(turn.AgentLineIndices, lineIndex)
		}
		if lineType == "event_msg" && payloadType == "task_complete" {
			turn.EndLineIndex = lineIndex
			currentTurnIndex = -1
		}
	}

	if result.SessionID == "" {
		result.SessionID = extractProjectManagerSessionIDFromFileName(filepath.Base(path))
	}
	if expectedSessionID != "" && strings.TrimSpace(result.SessionID) != strings.TrimSpace(expectedSessionID) {
		return projectManagerRolloutFile{}, fmt.Errorf("会话 ID 不匹配: expected=%s actual=%s", expectedSessionID, result.SessionID)
	}

	return result, nil
}

func projectManagerSelectedAgentIndexes(turn projectManagerConversationTurn, targetAgentIDs map[string]struct{}) []int {
	result := make([]int, 0, len(turn.Agents))
	for index, agent := range turn.Agents {
		if _, ok := targetAgentIDs[agent.ID]; ok {
			result = append(result, index)
		}
	}
	return result
}

func projectManagerMatchRolloutTurnIndex(turns []projectManagerRolloutTurn, userContent string, startIndex int) int {
	normalizedUser := normalizeProjectManagerConversationText(userContent)
	if normalizedUser == "" {
		return -1
	}

	for index := startIndex; index < len(turns); index++ {
		if normalizeProjectManagerConversationText(turns[index].UserMessage) == normalizedUser {
			return index
		}
	}
	return -1
}

func normalizeProjectManagerConversationText(value string) string {
	fields := strings.Fields(strings.TrimSpace(strings.ToLower(value)))
	return strings.Join(fields, " ")
}

func buildProjectManagerConversationMessageID(sessionID string, payloadType string, lineNumber int) string {
	return fmt.Sprintf("%s:%s:%d", strings.TrimSpace(sessionID), projectManagerConversationRole(payloadType), lineNumber)
}

func projectManagerConversationRole(payloadType string) string {
	if strings.TrimSpace(payloadType) == "user_message" {
		return "user"
	}
	return "agent"
}

func parseProjectManagerConversationTimestamp(value string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}
