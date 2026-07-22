package services

const projectManagerCodexStatusEventName = "project-manager:codex-status"

type CodexRuntimeState string

const (
	CodexRuntimeNotLoaded        CodexRuntimeState = "not_loaded"
	CodexRuntimeIdle             CodexRuntimeState = "idle"
	CodexRuntimeActive           CodexRuntimeState = "active"
	CodexRuntimeWaitingApproval  CodexRuntimeState = "waiting_approval"
	CodexRuntimeWaitingUserInput CodexRuntimeState = "waiting_user_input"
	CodexRuntimeSystemError      CodexRuntimeState = "system_error"
)

type CodexStatusMonitorInfo struct {
	Installed           bool   `json:"installed"`
	CodexVersion        string `json:"codex_version,omitempty"`
	AgentHooksSupported bool   `json:"agent_hooks_supported"`
	Error               string `json:"error,omitempty"`
}

type CodexSessionRuntimeStatus struct {
	SessionID       string            `json:"session_id"`
	TurnID          string            `json:"turn_id,omitempty"`
	ProjectPath     string            `json:"project_path"`
	State           CodexRuntimeState `json:"state"`
	TurnStatus      string            `json:"turn_status,omitempty"`
	ActiveAgents    int               `json:"active_agents"`
	AgentSupported  bool              `json:"agent_supported"`
	Monitored       bool              `json:"monitored"`
	UpdatedAt       int64             `json:"updated_at"`
	LastEvent       string            `json:"last_event,omitempty"`
	LastError       string            `json:"last_error,omitempty"`
	CodexPID        uint32            `json:"-"`
	CodexStartedAt  string            `json:"-"`
	TranscriptPath  string            `json:"-"`
	LastEventNano   int64             `json:"-"`
	TranscriptMTime int64             `json:"-"`
	activeAgentIDs  map[string]struct{}
}

type CodexProjectRuntimeStatus struct {
	ProjectPath        string            `json:"project_path"`
	State              CodexRuntimeState `json:"state"`
	ActiveSessions     int               `json:"active_sessions"`
	WaitingSessions    int               `json:"waiting_sessions"`
	ErrorSessions      int               `json:"error_sessions"`
	LatestSessionID    string            `json:"latest_session_id,omitempty"`
	LatestSessionState CodexRuntimeState `json:"latest_session_state,omitempty"`
	UpdatedAt          int64             `json:"updated_at"`
}

type CodexRuntimeStatusSnapshot struct {
	Monitor   CodexStatusMonitorInfo      `json:"monitor"`
	Sessions  []CodexSessionRuntimeStatus `json:"sessions"`
	Projects  []CodexProjectRuntimeStatus `json:"projects"`
	UpdatedAt int64                       `json:"updated_at"`
}
