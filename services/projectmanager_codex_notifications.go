package services

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	CodexHookNotificationWaitingApproval  = "waiting_approval"
	CodexHookNotificationWaitingUserInput = "waiting_user_input"
	CodexHookNotificationSystemError      = "system_error"
	CodexHookNotificationSessionEnded     = "session_ended"

	projectManagerCodexHookSourceLimit = 256
)

// CodexHookSource 是一个正在运行的 Codex thread/turn 的入口身份。
// Hook 进程只能携带 session/thread/turn 标识，状态服务通过这些标识把事件
// 归属到 Agent 管家或具体频道，不能按“最近项目”这种模糊规则猜来源。
type CodexHookSource struct {
	Source            string
	ProjectID         string
	ProjectPath       string
	ProjectName       string
	SessionName       string
	SessionID         string
	ThreadID          string
	TurnID            string
	ChannelInstanceID string
	ChannelChatID     string
}

// CodexHookSourceRegistrar 是 runtime 登记来源的最小接口。实现位于项目
// 管理器状态服务，PetCodexRuntime 不依赖具体的状态存储或频道包。
type CodexHookSourceRegistrar interface {
	RegisterCodexHookSource(CodexHookSource)
}

// CodexHookNotification 是 Hook 状态服务向下游发送的结构化通知。它只携带
// 事件上下文和有限长度的工具信息，不携带模型配置、认证信息或完整转录。
type CodexHookNotification struct {
	EventID           string
	Event             string
	HookEventName     string
	ProjectID         string
	ProjectPath       string
	ProjectName       string
	SessionID         string
	SessionName       string
	ThreadID          string
	TurnID            string
	OccurredAt        int64
	ToolName          string
	ToolInput         string
	ToolResponse      string
	Reason            string
	Error             string
	Managed           bool
	Source            string
	ChannelInstanceID string
	ChannelChatID     string
}

// CodexHookNotificationSink 是 Hook 通知的旁路出口。调用方应直接投递到
// 频道，不得把通知重新包装成 Agent 用户消息，否则会消耗模型并污染长会话。
type CodexHookNotificationSink func(context.Context, CodexHookNotification) error

type codexHookSourceRecord struct {
	source       CodexHookSource
	registeredAt int64
}

func normalizeCodexHookSource(source CodexHookSource) CodexHookSource {
	source.Source = strings.ToLower(strings.TrimSpace(source.Source))
	source.ProjectID = strings.TrimSpace(source.ProjectID)
	source.ProjectPath = normalizeProjectManagerProjectPath(source.ProjectPath)
	source.ProjectName = strings.TrimSpace(source.ProjectName)
	source.SessionName = strings.TrimSpace(source.SessionName)
	source.SessionID = strings.TrimSpace(source.SessionID)
	source.ThreadID = strings.TrimSpace(source.ThreadID)
	source.TurnID = strings.TrimSpace(source.TurnID)
	source.ChannelInstanceID = strings.TrimSpace(source.ChannelInstanceID)
	source.ChannelChatID = strings.TrimSpace(source.ChannelChatID)
	return source
}

func codexHookSourceKey(source CodexHookSource) string {
	return strings.Join([]string{
		source.Source,
		source.ProjectID,
		source.ProjectPath,
		source.SessionID,
		source.ThreadID,
		source.TurnID,
		source.ChannelInstanceID,
		source.ChannelChatID,
	}, "\x00")
}

func (s *projectManagerCodexStatusService) registerCodexHookSource(source CodexHookSource) {
	source = normalizeCodexHookSource(source)
	if source.Source != AgentConversationSourceManager && source.Source != AgentConversationSourceChannel {
		return
	}
	if source.SessionID == "" && source.ThreadID == "" && source.TurnID == "" {
		return
	}

	// 共享 thread 可能先后由 Agent 管家和频道使用；SessionEnd 等事件没有
	// turn_id，只能用来源登记顺序消除同分歧义。毫秒精度会让连续入口落在同一
	// 时间戳，改用纳秒后“最近入口”才是确定性的。
	now := time.Now().UnixNano()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hookSources == nil {
		s.hookSources = make(map[string]codexHookSourceRecord)
	}
	key := codexHookSourceKey(source)
	s.hookSources[key] = codexHookSourceRecord{source: source, registeredAt: now}
	for len(s.hookSources) > projectManagerCodexHookSourceLimit {
		oldestKey := ""
		oldestAt := int64(0)
		for candidateKey, record := range s.hookSources {
			if oldestKey == "" || record.registeredAt < oldestAt {
				oldestKey = candidateKey
				oldestAt = record.registeredAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.hookSources, oldestKey)
	}
}

func codexHookSourceMatchScore(event projectManagerCodexHookEvent, source CodexHookSource) int {
	if source.ProjectPath != "" && event.Cwd != "" && !projectManagerProjectPathsEqual(source.ProjectPath, event.Cwd) {
		return 0
	}
	if source.ProjectID != "" && event.ProjectID != "" && !strings.EqualFold(source.ProjectID, event.ProjectID) {
		return 0
	}
	if source.TurnID != "" && event.TurnID != "" && source.TurnID != event.TurnID {
		return 0
	}

	score := 0
	if source.TurnID != "" && event.TurnID != "" && source.TurnID == event.TurnID {
		score += 100
	}
	if source.SessionID != "" && event.SessionID != "" && source.SessionID == event.SessionID {
		score += 90
	}
	if source.ThreadID != "" {
		switch {
		case source.ThreadID == event.ThreadID:
			score += 80
		case source.ThreadID == event.SessionID:
			// Codex Hook 当前版本把 thread 身份暴露为 session_id；保留
			// event.ThreadID 分支兼容未来 payload，不把 PID 当作唯一依据。
			score += 75
		}
	}
	if source.SessionID == "" && source.ThreadID == "" && source.TurnID == "" {
		return 0
	}
	if score == 0 {
		return 0
	}
	return score
}

func (s *projectManagerCodexStatusService) matchCodexHookSource(event projectManagerCodexHookEvent) (CodexHookSource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched CodexHookSource
	bestScore := 0
	bestRegisteredAt := int64(0)
	for _, record := range s.hookSources {
		score := codexHookSourceMatchScore(event, record.source)
		if score == 0 || score < bestScore || (score == bestScore && record.registeredAt <= bestRegisteredAt) {
			continue
		}
		matched = record.source
		bestScore = score
		bestRegisteredAt = record.registeredAt
	}
	return matched, bestScore > 0
}

func codexHookNotificationType(event projectManagerCodexHookEvent) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(event.HookEventName)) {
	case "permissionrequest":
		return CodexHookNotificationWaitingApproval, true
	case "pretooluse":
		if strings.EqualFold(strings.TrimSpace(event.ToolName), "request_user_input") {
			return CodexHookNotificationWaitingUserInput, true
		}
	case "posttooluse":
		if projectManagerCodexHookEventIndicatesError(event) {
			return CodexHookNotificationSystemError, true
		}
	case "stop":
		if projectManagerCodexHookEventIndicatesError(event) {
			return CodexHookNotificationSystemError, true
		}
	case "sessionend":
		return CodexHookNotificationSessionEnded, true
	case "systemerror", "error":
		return CodexHookNotificationSystemError, true
	}
	return "", false
}

func projectManagerCodexHookEventIndicatesError(event projectManagerCodexHookEvent) bool {
	if strings.TrimSpace(event.Error) != "" {
		return true
	}

	switch strings.ToLower(strings.TrimSpace(event.HookEventName)) {
	case "posttooluse":
		return projectManagerCodexHookToolResponseIndicatesError(event.ToolResponse)
	case "stop", "systemerror", "error":
		return projectManagerCodexHookFailureReason(event.Reason)
	default:
		return false
	}
}

func projectManagerCodexHookToolResponseIndicatesError(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if gjson.Valid(value) {
		result := gjson.Parse(value)
		for _, key := range []string{"error", "errorMessage", "error_message", "isError", "is_error"} {
			field := result.Get(key)
			if !field.Exists() {
				continue
			}
			if (field.Type == gjson.True || field.Type == gjson.False) && field.Bool() {
				return true
			}
			if field.Type == gjson.String && strings.TrimSpace(field.String()) != "" {
				return true
			}
			if field.Type == gjson.JSON && strings.TrimSpace(field.Raw) != "{}" && strings.TrimSpace(field.Raw) != "null" {
				return true
			}
		}
		if success := result.Get("success"); success.Exists() && (success.Type == gjson.True || success.Type == gjson.False) && !success.Bool() {
			return true
		}
		status := strings.ToLower(strings.TrimSpace(result.Get("status").String()))
		return status == "error" || status == "failed" || status == "failure"
	}
	return projectManagerCodexHookFailureReason(value)
}

func projectManagerCodexHookFailureReason(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, marker := range []string{"error", "failed", "failure", "exception"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func projectManagerCodexHookErrorText(event projectManagerCodexHookEvent) string {
	if value := strings.TrimSpace(event.Error); value != "" {
		return value
	}
	if value := strings.TrimSpace(event.Reason); value != "" && projectManagerCodexHookFailureReason(value) {
		return value
	}
	if value := strings.TrimSpace(event.ToolResponse); value != "" && projectManagerCodexHookToolResponseIndicatesError(value) {
		return value
	}
	return "Codex Hook reported a system error"
}

func (s *projectManagerCodexStatusService) buildCodexHookNotification(event projectManagerCodexHookEvent) *CodexHookNotification {
	notification, _ := s.buildCodexHookNotificationWithReason(event)
	return notification
}

func (s *projectManagerCodexStatusService) buildCodexHookNotificationWithReason(event projectManagerCodexHookEvent) (*CodexHookNotification, string) {
	notificationType, ok := codexHookNotificationType(event)
	if !ok {
		return nil, "unsupported_event"
	}
	source, matched := s.matchCodexHookSource(event)
	if matched && source.Source == AgentConversationSourceManager {
		// Agent 管家与频道共用 thread，但管家的 Hook 永远不能穿过频道出口。
		return nil, "manager_source"
	}
	if event.Managed && !matched {
		// CodeSwitch 自己管理的进程必须有明确来源；宁可丢弃也不能按项目
		// 猜一个频道，避免旧来源或其它入口被误转发。
		return nil, "managed_source_unmatched"
	}

	projectPath := normalizeProjectManagerProjectPath(firstNonEmptyProjectManagerString(source.ProjectPath, event.Cwd))
	projectName := firstNonEmptyProjectManagerString(source.ProjectName, filepath.Base(projectPath))
	sessionName := firstNonEmptyProjectManagerString(source.SessionName, event.SessionID, "Codex session")
	threadID := firstNonEmptyProjectManagerString(event.ThreadID, source.ThreadID)
	turnID := firstNonEmptyProjectManagerString(event.TurnID, source.TurnID)
	projectID := firstNonEmptyProjectManagerString(source.ProjectID, event.ProjectID, projectPath)
	errorText := strings.TrimSpace(event.Error)
	if notificationType == CodexHookNotificationSystemError && errorText == "" {
		errorText = projectManagerCodexHookErrorText(event)
	}

	return &CodexHookNotification{
		EventID:           event.EventID,
		Event:             notificationType,
		HookEventName:     event.HookEventName,
		ProjectID:         projectID,
		ProjectPath:       projectPath,
		ProjectName:       projectName,
		SessionID:         event.SessionID,
		SessionName:       sessionName,
		ThreadID:          threadID,
		TurnID:            turnID,
		OccurredAt:        event.ReceivedAt,
		ToolName:          event.ToolName,
		ToolInput:         event.ToolInput,
		ToolResponse:      event.ToolResponse,
		Reason:            event.Reason,
		Error:             errorText,
		Managed:           event.Managed,
		Source:            source.Source,
		ChannelInstanceID: source.ChannelInstanceID,
		ChannelChatID:     source.ChannelChatID,
	}, ""
}

func firstNonEmptyProjectManagerString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
