package services

import (
	"bytes"
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
	ProjectID                 string `json:"project_id,omitempty"`
	ThreadID                  string `json:"thread_id,omitempty"`
	TurnID                    string `json:"turn_id,omitempty"`
	AgentID                   string `json:"agent_id,omitempty"`
	AgentType                 string `json:"agent_type,omitempty"`
	ToolName                  string `json:"tool_name,omitempty"`
	Cwd                       string `json:"cwd"`
	TranscriptPath            string `json:"transcript_path,omitempty"`
	Reason                    string `json:"reason,omitempty"`
	Error                     string `json:"error,omitempty"`
	ToolInput                 string `json:"tool_input,omitempty"`
	ToolResponse              string `json:"tool_response,omitempty"`
	PlanImplementationPending bool   `json:"plan_implementation_pending,omitempty"`
	Managed                   bool   `json:"managed,omitempty"`
	CodexPID                  uint32 `json:"codex_pid,omitempty"`
	CodexStartedAt            string `json:"codex_started_at,omitempty"`
	ReceivedAt                int64  `json:"received_at"`
	ReceivedUnixNano          int64  `json:"received_unix_nano"`
}

type projectManagerCodexHookPayload struct {
	HookEventName        string
	SessionID            string
	ProjectID            string
	ThreadID             string
	TurnID               string
	AgentID              string
	AgentType            string
	ToolName             string
	Cwd                  string
	TranscriptPath       string
	PermissionMode       string
	Reason               string
	Error                string
	ToolInput            string
	ToolResponse         string
	LastAssistantMessage string
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

	payload, err := decodeProjectManagerCodexHookPayload(raw)
	if err != nil {
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
		ProjectID:                 payload.ProjectID,
		ThreadID:                  payload.ThreadID,
		TurnID:                    strings.TrimSpace(payload.TurnID),
		AgentID:                   strings.TrimSpace(payload.AgentID),
		AgentType:                 strings.TrimSpace(payload.AgentType),
		ToolName:                  strings.TrimSpace(payload.ToolName),
		Cwd:                       normalizeProjectManagerProjectPath(payload.Cwd),
		TranscriptPath:            projectManagerCodexNullablePath(payload.TranscriptPath),
		Reason:                    payload.Reason,
		Error:                     payload.Error,
		ToolInput:                 payload.ToolInput,
		ToolResponse:              payload.ToolResponse,
		PlanImplementationPending: projectManagerCodexPlanImplementationPending(payload),
		Managed:                   projectManagerCodexHookProcessManaged(),
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

func decodeProjectManagerCodexHookPayload(raw []byte) (projectManagerCodexHookPayload, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return projectManagerCodexHookPayload{}, err
	}
	return projectManagerCodexHookPayload{
		HookEventName:        projectManagerCodexHookString(fields, "hook_event_name", "hookEventName"),
		SessionID:            projectManagerCodexHookString(fields, "session_id", "sessionId"),
		ProjectID:            projectManagerCodexHookString(fields, "project_id", "projectId"),
		ThreadID:             projectManagerCodexHookString(fields, "thread_id", "threadId"),
		TurnID:               projectManagerCodexHookString(fields, "turn_id", "turnId"),
		AgentID:              projectManagerCodexHookString(fields, "agent_id", "agentId"),
		AgentType:            projectManagerCodexHookString(fields, "agent_type", "agentType"),
		ToolName:             projectManagerCodexHookString(fields, "tool_name", "toolName", "tool"),
		Cwd:                  projectManagerCodexHookString(fields, "cwd", "working_directory", "workingDirectory"),
		TranscriptPath:       projectManagerCodexHookString(fields, "transcript_path", "transcriptPath"),
		PermissionMode:       projectManagerCodexHookString(fields, "permission_mode", "permissionMode"),
		Reason:               projectManagerCodexHookString(fields, "reason", "stop_reason", "stopReason"),
		Error:                projectManagerCodexHookValueText(fields, "error", "error_message", "errorMessage"),
		ToolInput:            projectManagerCodexHookValueText(fields, "tool_input", "toolInput"),
		ToolResponse:         projectManagerCodexHookValueText(fields, "tool_response", "toolResponse"),
		LastAssistantMessage: projectManagerCodexHookValueText(fields, "last_assistant_message", "lastAssistantMessage"),
	}, nil
}

func projectManagerCodexHookString(fields map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

func projectManagerCodexHookValueText(fields map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := fields[key]
		if !ok || strings.TrimSpace(string(raw)) == "" || strings.EqualFold(strings.TrimSpace(string(raw)), "null") {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return truncateProjectManagerCodexHookText(value)
		}
		var compacted bytes.Buffer
		if json.Compact(&compacted, raw) == nil {
			return truncateProjectManagerCodexHookText(compacted.String())
		}
	}
	return ""
}

func truncateProjectManagerCodexHookText(value string) string {
	value = strings.TrimSpace(value)
	const maxRunes = 4096
	if len([]rune(value)) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes]) + "..."
}

func projectManagerCodexHookProcessManaged() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("CODESWITCH_CODEX_APP_SERVER_MANAGED")))
	return value == "1" || value == "true" || value == "yes"
}

func projectManagerCodexPlanImplementationPending(payload projectManagerCodexHookPayload) bool {
	if !strings.EqualFold(strings.TrimSpace(payload.HookEventName), "stop") ||
		!strings.EqualFold(strings.TrimSpace(payload.PermissionMode), "plan") {
		return false
	}
	// 计划确认弹窗由 Codex TUI 在 Stop 后本地生成，不会再发送 request_user_input。
	// 这里只把 assistant 正文归约为状态位，避免计划内容写入 CodeSwitch 的事件文件。
	return strings.Contains(payload.LastAssistantMessage, "<proposed_plan>")
}

func projectManagerCodexNullablePath(value string) string {
	return strings.TrimSpace(value)
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
