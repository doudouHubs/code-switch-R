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
	EventID          string `json:"event_id"`
	HookEventName    string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id,omitempty"`
	AgentID          string `json:"agent_id,omitempty"`
	AgentType        string `json:"agent_type,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	Cwd              string `json:"cwd"`
	TranscriptPath   string `json:"transcript_path,omitempty"`
	CodexPID         uint32 `json:"codex_pid,omitempty"`
	CodexStartedAt   string `json:"codex_started_at,omitempty"`
	ReceivedAt       int64  `json:"received_at"`
	ReceivedUnixNano int64  `json:"received_unix_nano"`
}

type projectManagerCodexHookPayload struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TurnID         string `json:"turn_id"`
	AgentID        string `json:"agent_id"`
	AgentType      string `json:"agent_type"`
	ToolName       string `json:"tool_name"`
	Cwd            string `json:"cwd"`
	TranscriptPath any    `json:"transcript_path"`
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
		EventID:          uuid.NewString(),
		HookEventName:    payload.HookEventName,
		SessionID:        payload.SessionID,
		TurnID:           strings.TrimSpace(payload.TurnID),
		AgentID:          strings.TrimSpace(payload.AgentID),
		AgentType:        strings.TrimSpace(payload.AgentType),
		ToolName:         strings.TrimSpace(payload.ToolName),
		Cwd:              normalizeProjectManagerProjectPath(payload.Cwd),
		TranscriptPath:   projectManagerCodexNullablePath(payload.TranscriptPath),
		CodexPID:         pid,
		CodexStartedAt:   startedAt,
		ReceivedAt:       now.UnixMilli(),
		ReceivedUnixNano: now.UnixNano(),
	}

	eventDir, err := projectManagerCodexEventRootPath()
	if err != nil {
		return err
	}
	fileName := fmt.Sprintf("%020d-%s.json", event.ReceivedUnixNano, event.EventID)
	return AtomicWriteJSON(filepath.Join(eventDir, fileName), event)
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
