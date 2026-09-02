package services

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	RunCommand        string `json:"run_command,omitempty"`
	UpdatedAt         int64  `json:"updated_at"`
	SessionCount      int    `json:"session_count"`
	CodexProviderID   int64  `json:"codex_provider_id,omitempty"`
	CodexProviderName string `json:"codex_provider_name,omitempty"`
	CodexProviderAuto bool   `json:"codex_provider_auto"`
}

type SessionSummary struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	ProjectPath       string `json:"project_path"`
	ProjectName       string `json:"project_name"`
	SourceName        string `json:"source_name"`
	DisplayName       string `json:"display_name"`
	Summary           string `json:"summary"`
	LatestUserMessage string `json:"latest_user_message"`
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
	ID         string                        `json:"id"`
	SessionID  string                        `json:"session_id"`
	Role       string                        `json:"role"`
	Content    string                        `json:"content"`
	Timestamp  int64                         `json:"timestamp"`
	ReplyFor   string                        `json:"reply_for"`
	TurnID     string                        `json:"turn_id"`
	TurnUsage  *SessionConversationTurnUsage `json:"turn_usage,omitempty"`
	SourceFile string                        `json:"source_file"`
	SourceLine int                           `json:"source_line"`
}

// SessionConversationTurnUsage 表示一次用户提问从开始到结束的全部模型调用用量。
// CachedInputTokens 已包含在 InputTokens 中，ReasoningOutputTokens 已包含在 OutputTokens 中，
// 因此 TotalTokens 不能再把这两个明细重复相加。
type SessionConversationTurnUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
	ModelCalls            int   `json:"model_calls"`
	DurationMS            int64 `json:"duration_ms"`
	Complete              bool  `json:"complete"`
}

type SessionConversationDetail struct {
	Session SessionSummary            `json:"session"`
	Items   []SessionConversationItem `json:"items"`
}

type SessionConversationPruneResult struct {
	SessionID    string `json:"session_id"`
	DeletedTurns int    `json:"deleted_turns"`
	DeletedItems int    `json:"deleted_items"`
	Error        string `json:"error,omitempty"`
}

type SessionConversationBatchPruneResult struct {
	RangeKey          string                           `json:"range_key"`
	CutoffAt          int64                            `json:"cutoff_at"`
	Results           []SessionConversationPruneResult `json:"results"`
	TotalDeletedTurns int                              `json:"total_deleted_turns"`
	TotalDeletedItems int                              `json:"total_deleted_items"`
}

type ProjectManagerService struct {
	store                   *projectManagerStoreService
	snapshotCache           *projectManagerSnapshotCacheService
	detailCache             *projectManagerConversationCacheService
	conversationSearchCache *projectManagerConversationSearchCacheService
	codexStatus             *projectManagerCodexStatusService
	petAgentModelReader     PetAgentModelReader
	snapshotBuildMu         sync.Mutex
	conversationSearchMu    sync.Mutex
	warmRefreshMu           sync.Mutex
	warmRefreshRunning      bool
	lastWarmRefreshAt       time.Time
}

const (
	projectManagerSessionDeleteRangeAll        = "all"
	projectManagerSessionDeleteRangeOneWeek    = "one_week"
	projectManagerSessionDeleteRangeThreeWeeks = "three_weeks"
	projectManagerSessionDeleteRangeOneMonth   = "one_month"
)

func NewProjectManagerService() *ProjectManagerService {
	return newProjectManagerService(nil)
}

// NewProjectManagerServiceWithPetAgentModelReader 注入宠物模型读取器。
// 无参构造仍然保留给独立调用方和旧测试；应用入口必须注入 PetDAO，才能让
// AI-Commit 的模型选择和宠物 Agent 设置保持同一个事实源。
func NewProjectManagerServiceWithPetAgentModelReader(reader PetAgentModelReader) *ProjectManagerService {
	return newProjectManagerService(reader)
}

func newProjectManagerService(reader PetAgentModelReader) *ProjectManagerService {
	return &ProjectManagerService{
		store:                   newProjectManagerStoreService(),
		snapshotCache:           newProjectManagerSnapshotCacheService(),
		detailCache:             newProjectManagerConversationCacheService(),
		conversationSearchCache: newProjectManagerConversationSearchCacheService(),
		codexStatus:             newProjectManagerCodexStatusService(),
		petAgentModelReader:     reader,
	}
}

// loadProjectManagerAICommitModel 在每次 AI-Commit 点击时读取 default 宠物的最新模型。
// reader 未注入时沿用旧的 commit-fast 默认模型，兼容不依赖宠物数据库的调用方；
// 正常应用入口会始终注入 PetDAO。
func (s *ProjectManagerService) loadProjectManagerAICommitModel() (PetAgentModelReference, error) {
	if s == nil || s.petAgentModelReader == nil {
		return PetAgentModelReference{}, nil
	}

	reference, err := s.petAgentModelReader.LoadAgentModelReference(context.Background(), DefaultPetID)
	if err != nil {
		return PetAgentModelReference{}, fmt.Errorf("读取 default 宠物 Agent 模型失败: %w", err)
	}
	return reference, nil
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
		if projectManagerProjectPathsEqual(session.ProjectPath, projectPath) {
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
	return s.SetProjectCodexProviderRouting(projectPath, providerID, true)
}

func (s *ProjectManagerService) SetProjectCodexProviderRouting(projectPath string, providerID int64, autoFallback bool) error {
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

	if err := s.store.saveProjectCodexProviderRouting(projectPath, providerID, autoFallback); err != nil {
		return err
	}
	s.invalidateProjectManagerSnapshotCache()
	return nil
}

func (s *ProjectManagerService) ClearProjectCodexProvider(projectPath string) error {
	return s.SetProjectCodexProviderRouting(projectPath, 0, true)
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
		if !projectManagerProjectPathsEqual(session.ProjectPath, projectPath) {
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
	if err != nil && !errors.Is(err, errProjectManagerSessionFileNotFound) {
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

func (s *ProjectManagerService) SaveProjectRunCommand(projectPath string, command string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}
	if err := s.store.saveProjectRunCommand(projectPath, command); err != nil {
		return err
	}
	s.invalidateProjectManagerSnapshotCache()
	return nil
}

func (s *ProjectManagerService) RunProjectCommand(projectPath string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}

	projectInfo, err := os.Stat(projectPath)
	if err != nil || !projectInfo.IsDir() {
		return errors.New("项目路径不存在或不是目录")
	}

	store, err := s.store.load()
	if err != nil {
		return err
	}
	command := ""
	if meta, ok := projectManagerProjectMetaFromStore(store, projectPath); ok {
		command = strings.TrimSpace(meta.RunCommand)
	}
	if command == "" {
		return errors.New("项目运行指令未配置")
	}

	if err := s.runProjectManagerProjectCommand(projectPath, command); err != nil {
		return fmt.Errorf("启动项目运行指令失败: %w", err)
	}
	return nil
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

	if err := s.applyProjectManagerConversationPrune(sessionID, sessionFile, prunePlan); err != nil {
		return SessionConversationDetail{}, err
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

func (s *ProjectManagerService) applyProjectManagerConversationPrune(
	sessionID string,
	sessionFile projectManagerConversationFile,
	prunePlan projectManagerConversationPrunePlan,
) error {
	if sessionFile.IsRollout {
		return pruneProjectManagerRolloutFile(sessionFile.Path, sessionID, prunePlan)
	}
	if err := pruneProjectManagerSessionConversationFile(sessionFile.Path, sessionID, prunePlan.TargetIDs); err != nil {
		return err
	}
	return s.pruneProjectManagerRolloutFiles(sessionID, prunePlan)
}

func projectManagerSessionDeleteRangeDuration(rangeKey string) (time.Duration, error) {
	switch strings.TrimSpace(rangeKey) {
	case projectManagerSessionDeleteRangeOneWeek:
		return 7 * 24 * time.Hour, nil
	case projectManagerSessionDeleteRangeThreeWeeks:
		return 21 * 24 * time.Hour, nil
	case projectManagerSessionDeleteRangeOneMonth:
		return 30 * 24 * time.Hour, nil
	case projectManagerSessionDeleteRangeAll:
		return 0, errors.New("全部范围必须使用 DeleteSession")
	default:
		return 0, fmt.Errorf("不支持的会话删除时间范围: %s", strings.TrimSpace(rangeKey))
	}
}

func (s *ProjectManagerService) pruneSessionConversationBefore(
	sessionID string,
	cutoffAt int64,
) (int, int, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, 0, errors.New("会话 ID 不能为空")
	}
	if cutoffAt <= 0 {
		return 0, 0, errors.New("时间阈值无效")
	}

	snapshot, snapshotCache, err := s.loadProjectManagerSnapshotWithCache()
	if err != nil {
		return 0, 0, err
	}
	if _, err := projectManagerFindSession(snapshot.Sessions, sessionID); err != nil {
		return 0, 0, err
	}

	sessionFile, err := s.findProjectManagerSessionFileByIDFast(sessionID, snapshotCache)
	if err != nil {
		return 0, 0, err
	}
	currentItems, err := s.readProjectManagerConversationItems(sessionFile)
	if err != nil {
		return 0, 0, err
	}

	prunePlan, matched, err := buildProjectManagerConversationPrunePlanBefore(sessionID, currentItems, cutoffAt)
	if err != nil {
		return 0, 0, err
	}
	if !matched || len(prunePlan.TargetIDs) == 0 {
		// 没有命中旧轮次时不写文件，避免仅仅因为批量清理而改变文件时间或历史格式。
		return 0, 0, nil
	}

	if err := s.applyProjectManagerConversationPrune(sessionID, sessionFile, prunePlan); err != nil {
		return 0, 0, err
	}

	s.invalidateProjectManagerConversationCache(sessionID)
	s.invalidateProjectManagerSnapshotCache()
	return len(prunePlan.TargetUserIDs), len(prunePlan.TargetIDs), nil
}

func (s *ProjectManagerService) PruneSessionConversationsByRange(
	sessionIDs []string,
	rangeKey string,
) (SessionConversationBatchPruneResult, error) {
	rangeKey = strings.TrimSpace(rangeKey)
	duration, err := projectManagerSessionDeleteRangeDuration(rangeKey)
	if err != nil {
		return SessionConversationBatchPruneResult{}, err
	}

	normalizedIDs := make([]string, 0, len(sessionIDs))
	seenIDs := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, seen := seenIDs[sessionID]; seen {
			continue
		}
		seenIDs[sessionID] = struct{}{}
		normalizedIDs = append(normalizedIDs, sessionID)
	}
	if len(normalizedIDs) == 0 {
		return SessionConversationBatchPruneResult{}, errors.New("至少选择一个会话")
	}

	cutoffAt := time.Now().Add(-duration).UnixMilli()
	result := SessionConversationBatchPruneResult{
		RangeKey: rangeKey,
		CutoffAt: cutoffAt,
		Results:  make([]SessionConversationPruneResult, 0, len(normalizedIDs)),
	}
	for _, sessionID := range normalizedIDs {
		item := SessionConversationPruneResult{SessionID: sessionID}
		deletedTurns, deletedItems, pruneErr := s.pruneSessionConversationBefore(sessionID, cutoffAt)
		if pruneErr != nil {
			// 单个会话失败只记录在结果中，批量任务继续处理其余会话。
			item.Error = pruneErr.Error()
		} else {
			item.DeletedTurns = deletedTurns
			item.DeletedItems = deletedItems
			result.TotalDeletedTurns += deletedTurns
			result.TotalDeletedItems += deletedItems
		}
		result.Results = append(result.Results, item)
	}
	return result, nil
}

func (s *ProjectManagerService) ForkSessionConversation(sessionID string, messageIDs []string) (SessionSummary, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionSummary{}, errors.New("会话 ID 不能为空")
	}
	if len(messageIDs) == 0 {
		return SessionSummary{}, errors.New("至少选择一条消息")
	}

	snapshot, snapshotCache, err := s.loadProjectManagerSnapshotWithCache()
	if err != nil {
		return SessionSummary{}, err
	}

	session, err := projectManagerFindSession(snapshot.Sessions, sessionID)
	if err != nil {
		return SessionSummary{}, err
	}

	sessionFile, err := s.findProjectManagerSessionFileByIDFast(sessionID, snapshotCache)
	if err != nil {
		return SessionSummary{}, err
	}

	currentItems, err := s.readProjectManagerConversationItems(sessionFile)
	if err != nil {
		return SessionSummary{}, err
	}

	lastTurnID, err := buildProjectManagerConversationForkTurnID(currentItems, messageIDs)
	if err != nil {
		return SessionSummary{}, err
	}

	forkedSessionID, err := projectManagerForkSessionWithAppServer(sessionID, lastTurnID)
	if err != nil {
		return SessionSummary{}, err
	}

	forkedSession := session
	forkedSession.ID = forkedSessionID
	forkedSession.SourceName = strings.TrimSpace(session.SourceName)
	if strings.TrimSpace(forkedSession.SourceName) == "" {
		forkedSession.SourceName = forkedSessionID
	}
	forkedSession.DisplayName = strings.TrimSpace(session.DisplayName)
	if forkedSession.DisplayName == "" {
		forkedSession.DisplayName = forkedSession.SourceName
	}
	forkedSession.UpdatedAt = time.Now().UnixMilli()

	s.invalidateProjectManagerSnapshotCache()
	s.invalidateProjectManagerConversationCache(forkedSessionID)

	if err := projectManagerOpenForkedSessionTerminal(s, forkedSession); err != nil {
		return forkedSession, fmt.Errorf("fork 已创建但打开终端失败: %w", err)
	}

	return forkedSession, nil
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
	items, err := readProjectManagerSessionConversationItems(file.Path, file.SessionID)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateProjectManagerConversationTurnIDsFromRollouts(file.SessionID, items); err != nil {
		log.Printf("[ProjectManager] 回填会话 turn_id 失败，fork 精确截断将不可用 session=%s err=%v", file.SessionID, err)
	}
	return items, nil
}

func (s *ProjectManagerService) invalidateProjectManagerConversationCache(sessionID string) {
	if s.detailCache != nil {
		s.detailCache.delete(sessionID)
	}
	if s.conversationSearchCache != nil {
		s.conversationSearchCache.delete(sessionID)
	}
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
