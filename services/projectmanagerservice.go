package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ProjectSummary struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	SourceName   string `json:"source_name"`
	DisplayName  string `json:"display_name"`
	UpdatedAt    int64  `json:"updated_at"`
	SessionCount int    `json:"session_count"`
}

type SessionSummary struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	ProjectPath       string `json:"project_path"`
	ProjectName       string `json:"project_name"`
	SourceName        string `json:"source_name"`
	DisplayName       string `json:"display_name"`
	Summary           string `json:"summary"`
	UpdatedAt         int64  `json:"updated_at"`
	WindowID          string `json:"window_id"`
	Cwd               string `json:"cwd"`
	LastCapturePath   string `json:"last_capture_path"`
	ProjectSourceHint string `json:"project_source_hint"`
}

type ProjectManagerSnapshot struct {
	Projects []ProjectSummary `json:"projects"`
	Sessions []SessionSummary `json:"sessions"`
}

type ProjectManagerService struct {
	store *projectManagerStoreService
}

func NewProjectManagerService() *ProjectManagerService {
	return &ProjectManagerService{
		store: newProjectManagerStoreService(),
	}
}

func (s *ProjectManagerService) GetSnapshot() (ProjectManagerSnapshot, error) {
	aggregate, err := s.scanProjectManagerData()
	if err != nil {
		return ProjectManagerSnapshot{}, err
	}
	return ProjectManagerSnapshot{
		Projects: aggregate.Projects,
		Sessions: aggregate.Sessions,
	}, nil
}

func (s *ProjectManagerService) ListProjects() ([]ProjectSummary, error) {
	snapshot, err := s.GetSnapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.Projects, nil
}

func (s *ProjectManagerService) ListRecentSessions() ([]SessionSummary, error) {
	snapshot, err := s.GetSnapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.Sessions, nil
}

func (s *ProjectManagerService) ListProjectSessions(projectPath string) ([]SessionSummary, error) {
	snapshot, err := s.GetSnapshot()
	if err != nil {
		return nil, err
	}

	projectPath = normalizeProjectManagerProjectPath(projectPath)
	result := make([]SessionSummary, 0, 32)
	for _, session := range snapshot.Sessions {
		if normalizeProjectManagerProjectPath(session.ProjectPath) == projectPath {
			result = append(result, session)
		}
	}
	return result, nil
}

func (s *ProjectManagerService) RefreshProjectIndex() (ProjectManagerSnapshot, error) {
	return s.GetSnapshot()
}

func (s *ProjectManagerService) RenameProject(projectPath string, displayName string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}
	return s.store.saveProjectDisplayName(projectPath, strings.TrimSpace(displayName))
}

func (s *ProjectManagerService) RenameSession(sessionID string, displayName string) error {
	sessionID = strings.TrimSpace(sessionID)
	displayName = strings.TrimSpace(displayName)
	if sessionID == "" {
		return errors.New("会话 ID 不能为空")
	}
	if displayName == "" {
		return errors.New("会话名称不能为空")
	}

	// 第一步先写本地覆盖名，保证前端立即可见；第二步再尽量回写 Codex 源索引。
	if err := s.store.saveSessionMetadata(sessionID, func(meta *projectManagerSessionMeta) bool {
		if meta.DisplayName == displayName {
			return false
		}
		meta.DisplayName = displayName
		return true
	}); err != nil {
		return err
	}

	if err := s.tryRenameCodexSessionIndex(sessionID, displayName); err != nil {
		return fmt.Errorf("已保存本地会话别名，但回写 Codex 会话标题失败: %w", err)
	}
	return nil
}

func (s *ProjectManagerService) OpenSessionTerminal(sessionID string) error {
	snapshot, err := s.GetSnapshot()
	if err != nil {
		return err
	}

	session, err := projectManagerFindSession(snapshot.Sessions, strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}

	return s.openProjectManagerSessionTerminal(session)
}

func (s *ProjectManagerService) OpenProjectFolder(projectPath string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}
	return OpenInExplorer(projectPath)
}

func (s *ProjectManagerService) tryRenameCodexSessionIndex(sessionID string, displayName string) error {
	home, err := getUserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".codex", "session_index.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	changed := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if gjson.Get(trimmed, "id").String() != sessionID {
			continue
		}
		next, err := sjson.Set(trimmed, "thread_name", displayName)
		if err != nil {
			return err
		}
		lines[index] = next
		changed = true
		break
	}
	if !changed {
		return fmt.Errorf("session_index.jsonl 中未找到会话 %s", sessionID)
	}

	return AtomicWriteText(path, strings.Join(lines, "\n"))
}
