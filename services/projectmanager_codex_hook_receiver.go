package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ProjectManagerCodexHookArgument = "--codex-hook-event"
	projectManagerCodexStatusDir    = "project-manager-codex-status"
	projectManagerCodexEventDir     = "events"
	projectManagerCodexEventMaxSize = 2 << 20
)

type projectManagerCodexHookEvent struct {
	EventID                   string `json:"event_id"`
	HookEventName             string `json:"hook_event_name"`
	SessionID                 string `json:"session_id"`
	TurnID                    string `json:"turn_id,omitempty"`
	AgentID                   string `json:"agent_id,omitempty"`
	AgentType                 string `json:"agent_type,omitempty"`
	ToolName                  string `json:"tool_name,omitempty"`
	Cwd                       string `json:"cwd"`
	TranscriptPath            string `json:"transcript_path,omitempty"`
	PlanImplementationPending bool   `json:"plan_implementation_pending,omitempty"`
	CodexPID                  uint32 `json:"codex_pid,omitempty"`
	CodexStartedAt            string `json:"codex_started_at,omitempty"`
	ReceivedAt                int64  `json:"received_at"`
	ReceivedUnixNano          int64  `json:"received_unix_nano"`
}

type projectManagerCodexHookPayload struct {
	HookEventName        string `json:"hook_event_name"`
	SessionID            string `json:"session_id"`
	TurnID               string `json:"turn_id"`
	AgentID              string `json:"agent_id"`
	AgentType            string `json:"agent_type"`
	ToolName             string `json:"tool_name"`
	Cwd                  string `json:"cwd"`
	TranscriptPath       any    `json:"transcript_path"`
	PermissionMode       string `json:"permission_mode"`
	LastAssistantMessage any    `json:"last_assistant_message"`
}

func IsProjectManagerCodexHookInvocation(args []string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), ProjectManagerCodexHookArgument) {
			return true
		}
	}
	return false
}

// RunProjectManagerCodexHookReceiver 是独立的轻量入口，必须在 Wails 和数据库初始化前调用。
// Hook 属于 Codex 的同步主流程；这里只校验、补充进程身份并原子落盘，绝不能启动 GUI 或做网络请求。
func RunProjectManagerCodexHookReceiver(reader io.Reader) error {
	WriteRuntimeDiagnostic("hook-receiver-start", fmt.Sprintf("args=%q", os.Args[1:]))
	if reader == nil {
		return errors.New("Codex hook stdin 不能为空")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, projectManagerCodexEventMaxSize+1))
	if err != nil {
		return fmt.Errorf("读取 Codex hook 事件失败: %w", err)
	}
	if len(raw) == 0 || len(raw) > projectManagerCodexEventMaxSize {
		return errors.New("Codex hook 事件为空或超过大小限制")
	}

	var payload projectManagerCodexHookPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("解析 Codex hook 事件失败: %w", err)
	}
	payload.HookEventName = strings.TrimSpace(payload.HookEventName)
	payload.SessionID = strings.TrimSpace(payload.SessionID)
	if payload.HookEventName == "" || payload.SessionID == "" {
		return errors.New("Codex hook 事件缺少 hook_event_name 或 session_id")
	}

	now := time.Now().UTC()
	pid, startedAt, _ := resolveProjectManagerCodexAncestorProcess()
	event := projectManagerCodexHookEvent{
		EventID:                   uuid.NewString(),
		HookEventName:             payload.HookEventName,
		SessionID:                 payload.SessionID,
		TurnID:                    strings.TrimSpace(payload.TurnID),
		AgentID:                   strings.TrimSpace(payload.AgentID),
		AgentType:                 strings.TrimSpace(payload.AgentType),
		ToolName:                  strings.TrimSpace(payload.ToolName),
		Cwd:                       normalizeProjectManagerProjectPath(payload.Cwd),
		TranscriptPath:            projectManagerCodexNullablePath(payload.TranscriptPath),
		PlanImplementationPending: projectManagerCodexPlanImplementationPending(payload),
		CodexPID:                  pid,
		CodexStartedAt:            startedAt,
		ReceivedAt:                now.UnixMilli(),
		ReceivedUnixNano:          now.UnixNano(),
	}

	eventDir, err := projectManagerCodexEventRootPath()
	if err != nil {
		return err
	}
	fileName := fmt.Sprintf("%020d-%s.json", event.ReceivedUnixNano, event.EventID)
	path := filepath.Join(eventDir, fileName)
	if err := AtomicWriteJSON(path, event); err != nil {
		WriteRuntimeDiagnostic("hook-receiver-write-failed", fmt.Sprintf("path=%q err=%q", path, err.Error()))
		return err
	}
	WriteRuntimeDiagnostic("hook-receiver-written", fmt.Sprintf("event=%s path=%q", event.HookEventName, path))
	return nil
}

func projectManagerCodexPlanImplementationPending(payload projectManagerCodexHookPayload) bool {
	if !strings.EqualFold(strings.TrimSpace(payload.HookEventName), "stop") ||
		!strings.EqualFold(strings.TrimSpace(payload.PermissionMode), "plan") {
		return false
	}
	// 计划确认弹窗由 Codex TUI 在 Stop 后本地生成，不会再发送 request_user_input。
	// 这里只把 assistant 正文归约为状态位，避免计划内容写入 CodeSwitch 的事件文件。
	message, ok := payload.LastAssistantMessage.(string)
	return ok && strings.Contains(message, "<proposed_plan>")
}

func projectManagerCodexNullablePath(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		// 新旧 Codex 对 NullableString 的编码不同，只接受明确的字符串值，避免把对象格式化成脏路径。
		for _, key := range []string{"value", "some"} {
			if text, ok := typed[key].(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func projectManagerCodexStatusRootPath() (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, appSettingsDir, projectManagerCodexStatusDir), nil
}

func projectManagerCodexEventRootPath() (string, error) {
	root, err := projectManagerCodexStatusRootPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, projectManagerCodexEventDir), nil
}

func projectManagerCodexCurrentExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(executable)
}
