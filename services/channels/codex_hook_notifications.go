package channels

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeswitch/services"
)

const (
	codexHookNotificationMaxTextLength   = 12000
	codexHookNotificationDetailMaxLength = 240

	// Weixin 官方聊天会把段落中的单个 LF 当作普通空白；使用空行分隔才能
	// 稳定触发客户端换行。通知字段数量受控，空行带来的高度增长是可接受的。
	codexHookNotificationLineBreak = "\n\n"
)

type codexHookDeliveryTarget struct {
	instance ChannelInstance
	chatID   string
	route    string
}

// DeliverCodexHookNotification 是 Hook 通知进入频道的唯一出口。
// Agent 管家来源在上游状态服务已经被过滤，这里仍保留防线，避免未来新增
// 调用方绕过状态服务后把内部 Hook 重新投递给模型或频道。
func (r *AgentRuntime) DeliverCodexHookNotification(ctx context.Context, notification services.CodexHookNotification) error {
	if r == nil || r.store == nil || r.manager == nil {
		return errors.New("channel hook delivery is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	notification = normalizeCodexHookNotification(notification)
	if notification.EventID == "" {
		return errors.New("Codex Hook notification event id is required")
	}
	if notification.Source == services.AgentConversationSourceManager {
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-filtered", notification, "", "", "", "manager_source")
		return nil
	}

	// 共享 runtime 登记了频道入口时，实例和聊天 ID 是精确路由；不能再按项目
	// 广播，否则同一 Hook 会在源频道和广播频道各出现一份。
	if notification.Source == services.AgentConversationSourceChannel {
		instanceID := strings.TrimSpace(notification.ChannelInstanceID)
		chatID := strings.TrimSpace(notification.ChannelChatID)
		if instanceID != "" && chatID != "" {
			instance, found, err := r.store.GetInstance(instanceID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("Hook channel instance %q not found", instanceID)
			}
			if !codexHookProjectMatches(ctx, r, instance, notification) {
				return errors.New("Hook channel project does not match the registered source")
			}
			return r.deliverCodexHookNotificationToChannel(ctx, instance, chatID, notification, "source_channel")
		}
		if notification.Managed {
			// CodeSwitch 管理的 runtime 没有精确频道身份时，禁止退化成模糊广播。
			writeCodexHookDeliveryDiagnostic("codex-hook-delivery-filtered", notification, instanceID, chatID, "", "managed_channel_target_missing")
			return nil
		}
	}
	if notification.Managed {
		// managed 进程只能来自本应用登记过的 Agent/频道来源；未匹配事件不猜目标。
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-filtered", notification, "", "", "", "managed_source_unmatched")
		return nil
	}

	targets, err := r.resolveExternalCodexHookTargets(ctx, notification)
	if err != nil {
		return err
	}
	var firstErr error
	for _, target := range targets {
		if err := r.deliverCodexHookNotificationToChannel(ctx, target.instance, target.chatID, notification, target.route); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *AgentRuntime) resolveExternalCodexHookTargets(ctx context.Context, notification services.CodexHookNotification) ([]codexHookDeliveryTarget, error) {
	instances, err := r.store.ListInstances()
	if err != nil {
		return nil, err
	}
	targets := make([]codexHookDeliveryTarget, 0)
	matchedProject := false
	for _, instance := range instances {
		if !instance.Enabled || !codexHookProjectMatches(ctx, r, instance, notification) {
			continue
		}
		matchedProject = true
		chatID := codexHookConfiguredChatID(instance)
		route := "configured_broadcast_chat"
		if chatID == "" {
			route = "latest_project_session"
			// 外部 Codex 没有 CodeSwitch 入口身份，无法做精确 thread 路由。
			// 当前频道模型是一实例对应一个聊天入口，因此使用该项目最近活跃的
			// 已知会话即可完成无额外配置的通知投递，同时不把状态广播给旧项目会话。
			chatID, err = r.latestCodexHookSessionChatID(instance)
			if err != nil {
				return nil, err
			}
			if chatID == "" {
				writeCodexHookDeliveryDiagnostic("codex-hook-delivery-no-target", notification, instance.ID, "", route, "no_project_chat_session")
				continue
			}
		}
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-target", notification, instance.ID, chatID, route, "")
		targets = append(targets, codexHookDeliveryTarget{instance: instance, chatID: chatID, route: route})
	}
	if len(targets) == 0 && matchedProject {
		return nil, errors.New("no channel chat session is available for Codex Hook notification")
	}
	return targets, nil
}

func codexHookConfiguredChatID(instance ChannelInstance) string {
	for _, key := range []string{"broadcastChatId", "broadcastChatID", "broadcast_chat_id"} {
		if value := strings.TrimSpace(instance.Config[key]); value != "" {
			return value
		}
	}
	return ""
}

func (r *AgentRuntime) latestCodexHookSessionChatID(instance ChannelInstance) (string, error) {
	sessions, err := r.store.ListSessions(instance.ID)
	if err != nil {
		return "", err
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.ChatID) == "" || !codexHookSessionProjectMatches(instance, session) {
			continue
		}
		return strings.TrimSpace(session.ChatID), nil
	}
	return "", nil
}

func codexHookSessionProjectMatches(instance ChannelInstance, session ChannelSession) bool {
	if instance.ProjectID == nil {
		return false
	}
	boundProjectID := strings.TrimSpace(*instance.ProjectID)
	sessionProjectID := strings.TrimSpace(session.ProjectID)
	if boundProjectID == "" || sessionProjectID == "" {
		return false
	}
	return strings.EqualFold(boundProjectID, sessionProjectID) || codexHookPathsEqual(boundProjectID, sessionProjectID)
}

func normalizeCodexHookNotification(notification services.CodexHookNotification) services.CodexHookNotification {
	notification.EventID = strings.TrimSpace(notification.EventID)
	notification.Event = strings.ToLower(strings.TrimSpace(notification.Event))
	notification.HookEventName = strings.TrimSpace(notification.HookEventName)
	notification.ProjectID = strings.TrimSpace(notification.ProjectID)
	notification.ProjectPath = strings.TrimSpace(notification.ProjectPath)
	notification.ProjectName = strings.TrimSpace(notification.ProjectName)
	notification.SessionID = strings.TrimSpace(notification.SessionID)
	notification.SessionName = strings.TrimSpace(notification.SessionName)
	notification.ThreadID = strings.TrimSpace(notification.ThreadID)
	notification.TurnID = strings.TrimSpace(notification.TurnID)
	notification.ToolName = strings.TrimSpace(notification.ToolName)
	notification.ToolInput = strings.TrimSpace(notification.ToolInput)
	notification.ToolResponse = strings.TrimSpace(notification.ToolResponse)
	notification.Reason = strings.TrimSpace(notification.Reason)
	notification.Error = strings.TrimSpace(notification.Error)
	notification.Source = strings.ToLower(strings.TrimSpace(notification.Source))
	notification.ChannelInstanceID = strings.TrimSpace(notification.ChannelInstanceID)
	notification.ChannelChatID = strings.TrimSpace(notification.ChannelChatID)
	return notification
}

func codexHookProjectMatches(ctx context.Context, r *AgentRuntime, instance ChannelInstance, notification services.CodexHookNotification) bool {
	if instance.ProjectID == nil {
		return false
	}
	projectID := strings.TrimSpace(*instance.ProjectID)
	if projectID == "" {
		return false
	}
	if notification.ProjectID != "" && strings.EqualFold(projectID, notification.ProjectID) {
		return true
	}
	if codexHookPathsEqual(projectID, notification.ProjectPath) {
		return true
	}
	if r != nil && r.projectResolve != nil && notification.ProjectPath != "" {
		workspace, err := r.resolveProjectWorkspace(ctx, projectID)
		if err == nil && codexHookPathsEqual(workspace, notification.ProjectPath) {
			return true
		}
	}
	return false
}

func codexHookPathsEqual(left, right string) bool {
	left = normalizeCodexHookPath(left)
	right = normalizeCodexHookPath(right)
	if left == "" || right == "" {
		return false
	}
	// 频道项目 ID 在 Windows 上可能保存为不同大小写的路径；其它平台保持
	// 路径比较的大小写语义，避免把两个大小写敏感项目错误合并。
	if os.PathSeparator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func normalizeCodexHookPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if absolute, err := filepath.Abs(value); err == nil {
		value = absolute
	}
	return filepath.Clean(value)
}

func (r *AgentRuntime) deliverCodexHookNotificationToChannel(ctx context.Context, instance ChannelInstance, chatID string, notification services.CodexHookNotification, route string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return errors.New("Hook channel chat id is empty")
	}
	content := formatCodexHookNotification(notification)
	if content == "" {
		return errors.New("Hook notification content is empty")
	}

	// 这里把“查询幂等键、平台发送、历史落库”串行化。数据库唯一索引仍是
	// 最终事实源，内存锁只负责阻止同一进程内两个异步 sink 同时撞到平台。
	r.hookDeliveryMu.Lock()
	defer r.hookDeliveryMu.Unlock()
	writeCodexHookDeliveryDiagnostic("codex-hook-delivery-attempt", notification, instance.ID, chatID, route, "")
	seen, err := r.store.HasMessageExternalID(instance.ID, notification.EventID)
	if err != nil {
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-failed", notification, instance.ID, chatID, route, err.Error())
		return err
	}
	if seen {
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-skipped", notification, instance.ID, chatID, route, "duplicate_event")
		return nil
	}
	if err := r.ensureCodexHookSession(ctx, instance, chatID, notification); err != nil {
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-failed", notification, instance.ID, chatID, route, err.Error())
		return err
	}
	if err := r.manager.restoreMessageContextFromHistory(instance.ID, chatID); err != nil {
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-failed", notification, instance.ID, chatID, route, "restore_context: "+err.Error())
		return fmt.Errorf("restore Hook channel context: %w", err)
	}
	messageID, err := r.manager.SendMessage(ctx, instance.ID, chatID, content)
	if err != nil {
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-failed", notification, instance.ID, chatID, route, err.Error())
		return fmt.Errorf("send Codex Hook notification: %w", err)
	}
	if err := appendChannelOutboundMessage(r.store, r.eventSink, instance, chatID, content, notification.EventID); err != nil {
		writeCodexHookDeliveryDiagnostic("codex-hook-delivery-failed", notification, instance.ID, chatID, route, "persist: "+err.Error())
		return fmt.Errorf("persist Codex Hook notification: %w", err)
	}
	writeCodexHookDeliveryDiagnostic("codex-hook-delivery-sent", notification, instance.ID, chatID, route, fmt.Sprintf("message_id=%q", messageID))
	_ = messageID
	return nil
}

func writeCodexHookDeliveryDiagnostic(event string, notification services.CodexHookNotification, instanceID, chatID, route, reason string) {
	details := []string{
		"component=channel-codex-hook",
		fmt.Sprintf("event_id=%q", notification.EventID),
		fmt.Sprintf("notification_event=%q", notification.Event),
		fmt.Sprintf("hook_event=%q", notification.HookEventName),
		fmt.Sprintf("source=%q", notification.Source),
		fmt.Sprintf("managed=%t", notification.Managed),
		fmt.Sprintf("project_id=%q", notification.ProjectID),
		fmt.Sprintf("instance_id=%q", strings.TrimSpace(instanceID)),
		fmt.Sprintf("chat_id=%q", strings.TrimSpace(chatID)),
		fmt.Sprintf("route=%q", strings.TrimSpace(route)),
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		details = append(details, fmt.Sprintf("reason=%q", reason))
	}
	services.WriteRuntimeDiagnosticAsync(event, details...)
}

func (r *AgentRuntime) ensureCodexHookSession(ctx context.Context, instance ChannelInstance, chatID string, notification services.CodexHookNotification) error {
	if _, found, err := r.store.GetSession(instance.ID, chatID); err != nil {
		return err
	} else if found {
		return nil
	}
	if instance.ProjectID == nil || strings.TrimSpace(*instance.ProjectID) == "" {
		return errors.New("Hook channel project binding is missing")
	}
	projectID := strings.TrimSpace(*instance.ProjectID)
	workspace := strings.TrimSpace(notification.ProjectPath)
	if workspace != "" {
		if normalized, err := normalizeWorkspace(ctx, workspace); err == nil {
			workspace = normalized
		} else {
			workspace = ""
		}
	}
	if workspace == "" {
		var err error
		workspace, err = r.resolveProjectWorkspace(ctx, projectID)
		if err != nil {
			return fmt.Errorf("resolve Hook channel workspace: %w", err)
		}
	}
	_, err := r.ensureSession(instance, ChannelMessage{
		ChatID:     chatID,
		SenderName: "CodeSwitch Hook",
	}, workspace)
	return err
}

func formatCodexHookNotification(notification services.CodexHookNotification) string {
	eventLabel := codexHookNotificationLabel(notification.Event)
	eventName := eventLabel
	if notification.HookEventName != "" {
		eventName += " · " + notification.HookEventName
	}
	projectName := firstNonEmpty(notification.ProjectName, filepath.Base(notification.ProjectPath), "未命名项目")
	sessionName := firstNonEmpty(notification.SessionName, notification.SessionID, "Codex session")
	occurredAt := notification.OccurredAt
	if occurredAt <= 0 {
		occurredAt = time.Now().UnixMilli()
	}

	lines := []string{
		"【Codex Hook｜" + eventLabel + "】 项目：" + projectName,
		"会话：" + sessionName,
		"事件：" + eventName,
		"时间：" + time.UnixMilli(occurredAt).Local().Format("2006-01-02 15:04:05"),
	}
	if notification.ToolName != "" {
		lines = append(lines, "工具："+notification.ToolName)
	}
	if detailLabel, detail := codexHookNotificationDetail(notification); detail != "" {
		lines = append(lines, detailLabel+detail)
	}
	return truncateCodexHookNotificationText(strings.Join(lines, codexHookNotificationLineBreak))
}

func codexHookNotificationDetail(notification services.CodexHookNotification) (string, string) {
	switch strings.ToLower(strings.TrimSpace(notification.Event)) {
	case services.CodexHookNotificationWaitingApproval:
		return "请求：", compactCodexHookDetail(firstNonEmpty(notification.Reason, compactCodexHookInput(notification.ToolInput)))
	case services.CodexHookNotificationWaitingUserInput:
		return "问题：", compactCodexHookDetail(firstNonEmpty(notification.Reason, compactCodexHookInput(notification.ToolInput)))
	case services.CodexHookNotificationSystemError:
		return "错误：", compactCodexHookDetail(firstNonEmpty(notification.Error, notification.Reason, notification.ToolResponse))
	case services.CodexHookNotificationSessionEnded:
		return "原因：", compactCodexHookDetail(notification.Reason)
	default:
		return "详情：", compactCodexHookDetail(firstNonEmpty(notification.Reason, notification.Error, compactCodexHookInput(notification.ToolInput)))
	}
}

func compactCodexHookInput(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(value), &object); err == nil {
		if questions := compactCodexHookQuestions(object["questions"]); questions != "" {
			return questions
		}
		// 工具输入优先显示用户真正关心的动作或提示，不把协议字段原样转发。
		for _, key := range []string{"command", "prompt", "question", "message", "description", "reason"} {
			if detail := compactCodexHookJSONValue(object[key]); detail != "" {
				return detail
			}
		}
		return ""
	}
	return compactCodexHookText(value)
}

func compactCodexHookDetail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(value), &object); err == nil {
		for _, key := range []string{"error", "error_message", "errorMessage", "message", "reason", "output", "status"} {
			if detail := compactCodexHookJSONValue(object[key]); detail != "" {
				return truncateCodexHookDetail(detail)
			}
		}
		return ""
	}
	return truncateCodexHookDetail(compactCodexHookText(value))
}

func compactCodexHookQuestions(value any) string {
	questions, ok := value.([]any)
	if !ok || len(questions) == 0 {
		return ""
	}
	question, ok := questions[0].(map[string]any)
	if !ok {
		return ""
	}
	text := compactCodexHookJSONValue(question["question"])
	if text == "" {
		text = compactCodexHookJSONValue(question["prompt"])
	}
	if text == "" {
		text = compactCodexHookJSONValue(question["description"])
	}
	options, _ := question["options"].([]any)
	labels := make([]string, 0, len(options))
	for _, rawOption := range options {
		option, ok := rawOption.(map[string]any)
		if !ok {
			continue
		}
		if label := compactCodexHookJSONValue(option["label"]); label != "" {
			labels = append(labels, label)
		}
	}
	if len(labels) > 0 {
		text = strings.TrimSpace(text) + "（选项：" + strings.Join(labels, "、") + "）"
	}
	return text
}

func compactCodexHookJSONValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return compactCodexHookText(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return compactCodexHookText(string(encoded))
	}
}

func compactCodexHookText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func truncateCodexHookDetail(value string) string {
	return truncateCodexHookText(value, codexHookNotificationDetailMaxLength)
}

func codexHookNotificationLabel(event string) string {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case services.CodexHookNotificationWaitingApproval:
		return "等待授权"
	case services.CodexHookNotificationWaitingUserInput:
		return "等待用户输入"
	case services.CodexHookNotificationSystemError:
		return "系统错误"
	case services.CodexHookNotificationSessionEnded:
		return "会话结束"
	default:
		return firstNonEmpty(event, "Codex Hook 事件")
	}
}

func truncateCodexHookNotificationText(value string) string {
	return truncateCodexHookText(value, codexHookNotificationMaxTextLength)
}

func truncateCodexHookText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len([]rune(value)) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "..."
}

// HasMessageExternalID 只做幂等查询，不暴露 SQL 给 runtime；外部 ID 是平台
// webhook 与 Codex Hook 事件的统一去重事实源。
func (s *Store) HasMessageExternalID(instanceID, externalID string) (bool, error) {
	instanceID = strings.TrimSpace(instanceID)
	externalID = strings.TrimSpace(externalID)
	if s == nil || s.db == nil {
		return false, errors.New("channel store is unavailable")
	}
	if instanceID == "" || externalID == "" {
		return false, errors.New("message idempotency key is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var marker int
	err := s.db.QueryRow(`SELECT 1 FROM channel_messages WHERE instance_id=? AND external_id=? LIMIT 1`, instanceID, externalID).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return marker == 1, nil
}
