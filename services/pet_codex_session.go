package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const PetCodexPlanProtocolVersion = 1

// PetCodexSession 是宠物与 Codex thread 的最小持久化映射。
// workspace/persona/protocol 是 thread 的创建条件；任一条件变化都必须创建新 thread，
// 否则旧对话会在错误项目或旧 persona 下继续执行，形成跨项目/跨角色污染。
type PetCodexSession struct {
	PetID              string `json:"petId"`
	ThreadID           string `json:"threadId"`
	Workspace          string `json:"workspace"`
	PersonaFingerprint string `json:"personaFingerprint"`
	ToolFingerprint    string `json:"toolFingerprint,omitempty"`
	ProtocolVersion    int    `json:"protocolVersion"`
	UpdatedAt          int64  `json:"updatedAt"`
}

// AgentCodexSession 是项目级 Agent 与 Codex thread 的持久化映射。
// projectId 才是桌宠管家和频道共同认可的会话身份；频道 instance/chat 只负责
// 当前消息的投递上下文，不能再各自占有一条 Codex thread。
type AgentCodexSession struct {
	ProjectID          string `json:"projectId"`
	ThreadID           string `json:"threadId"`
	Workspace          string `json:"workspace"`
	PersonaFingerprint string `json:"personaFingerprint"`
	ToolFingerprint    string `json:"toolFingerprint,omitempty"`
	ProtocolVersion    int    `json:"protocolVersion"`
	UpdatedAt          int64  `json:"updatedAt"`
}

// AgentCodexSessionRepository 是项目级会话的最小持久化边界。旧的
// PetCodexSessionRepository 继续服务兼容数据，但主流程不再把它当成项目事实源。
type AgentCodexSessionRepository interface {
	LoadAgentCodexSession(context.Context, string) (*AgentCodexSession, error)
	SaveAgentCodexSession(context.Context, AgentCodexSession) error
}

func (d *PetDAO) LoadCodexSession(ctx context.Context, petID string) (*PetCodexSession, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	petID = normalizePetID(petID)
	var session PetCodexSession
	err := d.db.QueryRowContext(petContext(ctx), `
		SELECT pet_id, thread_id, workspace, persona_fingerprint, tool_fingerprint, protocol_version, updated_at
		FROM pet_codex_session WHERE pet_id = ?
	`, petID).Scan(
		&session.PetID,
		&session.ThreadID,
		&session.Workspace,
		&session.PersonaFingerprint,
		&session.ToolFingerprint,
		&session.ProtocolVersion,
		&session.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load pet Codex session: %w", err)
	}
	return &session, nil
}

func (d *PetDAO) SaveCodexSession(ctx context.Context, session PetCodexSession) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	session.PetID = normalizePetID(session.PetID)
	session.ThreadID = strings.TrimSpace(session.ThreadID)
	session.Workspace = strings.TrimSpace(session.Workspace)
	session.PersonaFingerprint = strings.TrimSpace(session.PersonaFingerprint)
	session.ToolFingerprint = strings.TrimSpace(session.ToolFingerprint)
	if session.ThreadID == "" || session.Workspace == "" || session.PersonaFingerprint == "" {
		return errors.New("pet Codex session metadata is incomplete")
	}
	if session.ProtocolVersion <= 0 {
		return errors.New("pet Codex session protocol version is invalid")
	}
	if session.UpdatedAt <= 0 {
		session.UpdatedAt = currentPetTimestamp()
	}
	_, err := d.db.ExecContext(petContext(ctx), `
		INSERT INTO pet_codex_session
			(pet_id, thread_id, workspace, persona_fingerprint, tool_fingerprint, protocol_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pet_id) DO UPDATE SET
			thread_id = excluded.thread_id,
			workspace = excluded.workspace,
			persona_fingerprint = excluded.persona_fingerprint,
			tool_fingerprint = excluded.tool_fingerprint,
			protocol_version = excluded.protocol_version,
			updated_at = excluded.updated_at
	`, session.PetID, session.ThreadID, session.Workspace, session.PersonaFingerprint, session.ToolFingerprint, session.ProtocolVersion, session.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save pet Codex session: %w", err)
	}
	return nil
}

func (d *PetDAO) DeleteCodexSession(ctx context.Context, petID string) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(petContext(ctx), `DELETE FROM pet_codex_session WHERE pet_id = ?`, normalizePetID(petID)); err != nil {
		return fmt.Errorf("delete pet Codex session: %w", err)
	}
	return nil
}

// LoadAgentCodexSession 只按项目读取共享 thread。不存在时返回 nil，交由 runtime
// 决定是否执行一次旧 pet session 的兼容迁移，避免 DAO 隐式猜测项目绑定关系。
func (d *PetDAO) LoadAgentCodexSession(ctx context.Context, projectID string) (*AgentCodexSession, error) {
	if err := d.ensureSchema(); err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("agent Codex session project id is required")
	}
	var session AgentCodexSession
	err := d.db.QueryRowContext(petContext(ctx), `
		SELECT project_id, thread_id, workspace, persona_fingerprint, tool_fingerprint, protocol_version, updated_at
		FROM agent_codex_session WHERE project_id = ?
	`, projectID).Scan(
		&session.ProjectID,
		&session.ThreadID,
		&session.Workspace,
		&session.PersonaFingerprint,
		&session.ToolFingerprint,
		&session.ProtocolVersion,
		&session.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load agent Codex session: %w", err)
	}
	return &session, nil
}

func (d *PetDAO) SaveAgentCodexSession(ctx context.Context, session AgentCodexSession) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	session.ProjectID = strings.TrimSpace(session.ProjectID)
	session.ThreadID = strings.TrimSpace(session.ThreadID)
	session.Workspace = strings.TrimSpace(session.Workspace)
	session.PersonaFingerprint = strings.TrimSpace(session.PersonaFingerprint)
	session.ToolFingerprint = strings.TrimSpace(session.ToolFingerprint)
	if session.ProjectID == "" || session.ThreadID == "" || session.Workspace == "" || session.PersonaFingerprint == "" {
		return errors.New("agent Codex session metadata is incomplete")
	}
	if session.ProtocolVersion <= 0 {
		return errors.New("agent Codex session protocol version is invalid")
	}
	if session.UpdatedAt <= 0 {
		session.UpdatedAt = currentPetTimestamp()
	}
	_, err := d.db.ExecContext(petContext(ctx), `
		INSERT INTO agent_codex_session
			(project_id, thread_id, workspace, persona_fingerprint, tool_fingerprint, protocol_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			thread_id = excluded.thread_id,
			workspace = excluded.workspace,
			persona_fingerprint = excluded.persona_fingerprint,
			tool_fingerprint = excluded.tool_fingerprint,
			protocol_version = excluded.protocol_version,
			updated_at = excluded.updated_at
	`, session.ProjectID, session.ThreadID, session.Workspace, session.PersonaFingerprint, session.ToolFingerprint, session.ProtocolVersion, session.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save agent Codex session: %w", err)
	}
	return nil
}

func (d *PetDAO) DeleteAgentCodexSession(ctx context.Context, projectID string) error {
	if err := d.ensureSchema(); err != nil {
		return err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("agent Codex session project id is required")
	}
	if _, err := d.db.ExecContext(petContext(ctx), `DELETE FROM agent_codex_session WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("delete agent Codex session: %w", err)
	}
	return nil
}
