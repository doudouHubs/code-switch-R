package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ProjectSummary struct {
	ID                string `json:"id"`
	Path              string `json:"path"`
	SourceName        string `json:"source_name"`
	DisplayName       string `json:"display_name"`
	UpdatedAt         int64  `json:"updated_at"`
	SessionCount      int    `json:"session_count"`
	CodexProviderID   int64  `json:"codex_provider_id,omitempty"`
	CodexProviderName string `json:"codex_provider_name,omitempty"`
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
	Projects          []ProjectSummary `json:"projects"`
	Sessions          []SessionSummary `json:"sessions"`
	SnapshotUpdatedAt int64            `json:"snapshot_updated_at,omitempty"`
}

type SessionConversationItem struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	Timestamp  int64  `json:"timestamp"`
	ReplyFor   string `json:"reply_for"`
	SourceFile string `json:"source_file"`
	SourceLine int    `json:"source_line"`
}

type SessionConversationDetail struct {
	Session SessionSummary            `json:"session"`
	Items   []SessionConversationItem `json:"items"`
}

type ProjectManagerService struct {
	store              *projectManagerStoreService
	snapshotCache      *projectManagerSnapshotCacheService
	detailCache        *projectManagerConversationCacheService
	snapshotBuildMu    sync.Mutex
	warmRefreshMu      sync.Mutex
	warmRefreshRunning bool
	lastWarmRefreshAt  time.Time
}

func NewProjectManagerService() *ProjectManagerService {
	return &ProjectManagerService{
		store:         newProjectManagerStoreService(),
		snapshotCache: newProjectManagerSnapshotCacheService(),
		detailCache:   newProjectManagerConversationCacheService(),
	}
}

func (s *ProjectManagerService) GetSnapshot() (ProjectManagerSnapshot, error) {
	cache, err := s.loadProjectManagerSnapshotCache()
	if err == nil && cache.isUsable() && (len(cache.Snapshot.Projects) > 0 || len(cache.Snapshot.Sessions) > 0 || cache.Snapshot.SnapshotUpdatedAt > 0) {
		s.maybeScheduleProjectManagerWarmRefresh(cache.Snapshot)
		return cache.Snapshot, nil
	}

	return s.refreshProjectManagerSnapshotIncrementalWithFallback()
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
	return s.refreshProjectManagerSnapshotIncrementalWithFallback()
}

func (s *ProjectManagerService) RenameProject(projectPath string, displayName string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}
	if err := s.store.saveProjectDisplayName(projectPath, strings.TrimSpace(displayName)); err != nil {
		return err
	}
	s.invalidateProjectManagerSnapshotCache()
	return nil
}

func (s *ProjectManagerService) SetProjectCodexProvider(projectPath string, providerID int64) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}

	if providerID > 0 {
		providers, err := loadProviderSnapshot("codex")
		if err != nil {
			return fmt.Errorf("加载 Codex 供应商失败: %w", err)
		}
		if _, ok := findProviderByID(providers, providerID); !ok {
			return fmt.Errorf("未找到 Codex 供应商 ID: %d", providerID)
		}
	}

	if err := s.store.saveProjectCodexProviderID(projectPath, providerID); err != nil {
		return err
	}
	s.invalidateProjectManagerSnapshotCache()
	return nil
}

func (s *ProjectManagerService) ClearProjectCodexProvider(projectPath string) error {
	return s.SetProjectCodexProvider(projectPath, 0)
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
	s.invalidateProjectManagerSnapshotCache()
	s.invalidateProjectManagerConversationCache(sessionID)
	return nil
}

func (s *ProjectManagerService) DeleteProject(projectPath string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}

	snapshot, err := s.GetSnapshot()
	if err != nil {
		return err
	}

	targetSessions := make([]SessionSummary, 0, 8)
	for _, session := range snapshot.Sessions {
		if normalizeProjectManagerProjectPath(session.ProjectPath) != projectPath {
			continue
		}
		targetSessions = append(targetSessions, session)
	}

	var failed []string
	for _, session := range targetSessions {
		if err := s.DeleteSession(session.ID); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", session.ID, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("删除项目关联会话失败: %s", strings.Join(failed, "; "))
	}

	if err := s.store.deleteProject(projectPath); err != nil {
		return err
	}
	s.invalidateProjectManagerSnapshotCache()
	for _, session := range targetSessions {
		s.invalidateProjectManagerConversationCache(session.ID)
	}
	return nil
}

func (s *ProjectManagerService) DeleteSession(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("会话 ID 不能为空")
	}

	sessionFile, err := s.findProjectManagerSessionFileByID(sessionID)
	hasSessionFile := err == nil
	if err != nil && !strings.Contains(err.Error(), "未找到会话源文件") {
		return err
	}

	if hasSessionFile {
		if err := os.Remove(sessionFile.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除会话文件失败: %w", err)
		}
	}
	if err := s.removeCodexSessionIndexEntry(sessionID); err != nil {
		return err
	}
	// 这里明确不清 request capture；删除只作用于真实会话文件与项目管理本地状态。
	// 为了防止 capture 兜底把已删会话重新扫回来，本地 store 要留下 hidden tombstone。
	if err := s.store.hideSession(sessionID); err != nil {
		return err
	}
	if err := removeProjectManagerSessionRuntime(sessionID); err != nil {
		return err
	}
	s.invalidateProjectManagerSnapshotCache()
	s.invalidateProjectManagerConversationCache(sessionID)
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

func (s *ProjectManagerService) OpenSessionTerminalWithSession(session SessionSummary) error {
	session = sanitizeProjectManagerSessionSummaryForTerminal(session)
	if session.ID == "" {
		return errors.New("会话 ID 不能为空")
	}

	// 打开终端是高频即时操作，不能每次都回去全量重扫 .codex 历史。
	// 当前前端已经持有列表里的会话摘要，这里优先直接复用，只有关键目录信息缺失时才退回慢路径补查。
	if session.ProjectPath != "" || session.Cwd != "" {
		return s.openProjectManagerSessionTerminal(session)
	}

	return s.OpenSessionTerminal(session.ID)
}

func (s *ProjectManagerService) OpenProjectFolder(projectPath string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}
	return OpenInExplorer(projectPath)
}

func (s *ProjectManagerService) OpenProjectTerminal(projectPath string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}
	return s.openProjectManagerProjectTerminal(projectPath)
}

func (s *ProjectManagerService) RunProjectAICommit(projectPath string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}
	return s.runProjectManagerAICommit(projectPath)
}

func (s *ProjectManagerService) GetSessionConversationDetail(sessionID string) (SessionConversationDetail, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionConversationDetail{}, errors.New("会话 ID 不能为空")
	}

	snapshot, snapshotCache, err := s.loadProjectManagerSnapshotWithCache()
	if err != nil {
		return SessionConversationDetail{}, err
	}

	session, err := projectManagerFindSession(snapshot.Sessions, sessionID)
	if err != nil {
		return SessionConversationDetail{}, err
	}

	sessionFile, err := s.findProjectManagerSessionFileByIDFast(sessionID, snapshotCache)
	if err != nil {
		return SessionConversationDetail{}, err
	}

	if cachedDetail, ok, err := s.loadProjectManagerConversationDetailCache(session, sessionFile); err != nil {
		return SessionConversationDetail{}, err
	} else if ok {
		return cachedDetail, nil
	}

	items, err := s.readProjectManagerConversationItems(sessionFile)
	if err != nil {
		return SessionConversationDetail{}, err
	}

	detail := SessionConversationDetail{
		Session: session,
		Items:   items,
	}
	s.saveProjectManagerConversationDetailCache(sessionFile, detail)
	return detail, nil
}

func (s *ProjectManagerService) PruneSessionConversation(sessionID string, messageIDs []string) (SessionConversationDetail, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionConversationDetail{}, errors.New("会话 ID 不能为空")
	}
	if len(messageIDs) == 0 {
		return SessionConversationDetail{}, errors.New("至少选择一条消息")
	}

	snapshot, snapshotCache, err := s.loadProjectManagerSnapshotWithCache()
	if err != nil {
		return SessionConversationDetail{}, err
	}

	sessionFile, err := s.findProjectManagerSessionFileByIDFast(sessionID, snapshotCache)
	if err != nil {
		return SessionConversationDetail{}, err
	}

	currentItems, err := s.readProjectManagerConversationItems(sessionFile)
	if err != nil {
		return SessionConversationDetail{}, err
	}

	prunePlan, err := buildProjectManagerConversationPrunePlan(sessionID, currentItems, messageIDs)
	if err != nil {
		return SessionConversationDetail{}, err
	}

	if sessionFile.IsRollout {
		if err := pruneProjectManagerRolloutFile(sessionFile.Path, sessionID, prunePlan); err != nil {
			return SessionConversationDetail{}, err
		}
	} else {
		if err := pruneProjectManagerSessionConversationFile(sessionFile.Path, sessionID, prunePlan.TargetIDs); err != nil {
			return SessionConversationDetail{}, err
		}
		if err := s.pruneProjectManagerRolloutFiles(sessionID, prunePlan); err != nil {
			return SessionConversationDetail{}, err
		}
	}

	s.invalidateProjectManagerConversationCache(sessionID)

	// 剪枝后优先复用当前已取到的 snapshot；详情重新走一次读取链，
	// 这样能复用最新源文件选择逻辑，同时避免旧缓存消息回魂。
	if _, findErr := projectManagerFindSession(snapshot.Sessions, sessionID); findErr != nil {
		return SessionConversationDetail{}, findErr
	}
	detail, err := s.GetSessionConversationDetail(sessionID)
	if err == nil {
		s.invalidateProjectManagerSnapshotCache()
	}
	return detail, err
}

func (s *ProjectManagerService) loadProjectManagerSnapshotWithCache() (ProjectManagerSnapshot, projectManagerSnapshotCache, error) {
	snapshot, err := s.GetSnapshot()
	if err != nil {
		return ProjectManagerSnapshot{}, projectManagerSnapshotCache{}, err
	}

	cache, cacheErr := s.loadProjectManagerSnapshotCache()
	if cacheErr != nil || !cache.isUsable() {
		return snapshot, newProjectManagerSnapshotCache(), nil
	}

	return snapshot, cache, nil
}

func (s *ProjectManagerService) readProjectManagerConversationItems(file projectManagerConversationFile) ([]SessionConversationItem, error) {
	if file.IsRollout {
		return readProjectManagerRolloutConversationItems(file.Path, file.SessionID)
	}
	return readProjectManagerSessionConversationItems(file.Path, file.SessionID)
}

func (s *ProjectManagerService) invalidateProjectManagerConversationCache(sessionID string) {
	if s.detailCache == nil {
		return
	}
	s.detailCache.delete(sessionID)
}

func (s *ProjectManagerService) removeCodexSessionIndexEntry(sessionID string) error {
	home, err := getUserHomeDir()
	if err != nil {
		return err
	}

	path := filepath.Join(home, ".codex", "session_index.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if gjson.Get(trimmed, "id").String() == sessionID {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return nil
	}
	if err := AtomicWriteText(path, strings.Join(kept, "\n")); err != nil {
		return err
	}
	s.invalidateProjectManagerSnapshotCache()
	return nil
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

	if err := AtomicWriteText(path, strings.Join(lines, "\n")); err != nil {
		return err
	}
	s.invalidateProjectManagerSnapshotCache()
	return nil
}

func sanitizeProjectManagerSessionSummaryForTerminal(session SessionSummary) SessionSummary {
	session.ID = strings.TrimSpace(session.ID)
	session.ProjectID = strings.TrimSpace(session.ProjectID)
	session.ProjectPath = normalizeProjectManagerProjectPath(session.ProjectPath)
	session.ProjectName = strings.TrimSpace(session.ProjectName)
	session.SourceName = strings.TrimSpace(session.SourceName)
	session.DisplayName = strings.TrimSpace(session.DisplayName)
	session.Summary = strings.TrimSpace(session.Summary)
	session.WindowID = strings.TrimSpace(session.WindowID)
	session.Cwd = normalizeProjectManagerProjectPath(session.Cwd)
	session.LastCapturePath = strings.TrimSpace(session.LastCapturePath)
	session.ProjectSourceHint = strings.TrimSpace(session.ProjectSourceHint)
	return session
}
