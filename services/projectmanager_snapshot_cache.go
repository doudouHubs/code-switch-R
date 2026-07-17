package services

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	projectManagerSnapshotCacheFile    = "project-manager-snapshot-cache.json"
	projectManagerSnapshotCacheVersion = 4
	projectManagerWarmRefreshMinGap    = 12 * time.Second
)

type projectManagerSnapshotCache struct {
	Version             int                                        `json:"version"`
	Snapshot            ProjectManagerSnapshot                     `json:"snapshot"`
	SessionIndexMeta    projectManagerTrackedFile                  `json:"session_index_meta"`
	SessionIndexEntries []projectManagerSessionIndexEntry          `json:"session_index_entries"`
	SessionFiles        map[string]projectManagerCachedSessionFile `json:"session_files"`
	CaptureFiles        map[string]projectManagerCachedCaptureFile `json:"capture_files"`
}

type projectManagerTrackedFile struct {
	Size             int64 `json:"size"`
	ModTimeUnixMilli int64 `json:"mod_time_unix_milli"`
}

type projectManagerCachedSessionFile struct {
	Signature projectManagerTrackedFile      `json:"signature"`
	Entry     projectManagerSessionFileEntry `json:"entry"`
}

type projectManagerCaptureFileEntry struct {
	SessionID     string    `json:"session_id"`
	Path          string    `json:"path"`
	CapturedAt    time.Time `json:"captured_at"`
	WindowID      string    `json:"window_id"`
	ProjectPath   string    `json:"project_path"`
	ProjectSource string    `json:"project_source"`
	ProjectID     string    `json:"project_id"`
	Summary       string    `json:"summary"`
}

type projectManagerCachedCaptureFile struct {
	Signature projectManagerTrackedFile      `json:"signature"`
	Entry     projectManagerCaptureFileEntry `json:"entry"`
}

type projectManagerSnapshotCacheService struct {
	path string
	mu   sync.Mutex
}

func newProjectManagerSnapshotCacheService() *projectManagerSnapshotCacheService {
	home, err := getUserHomeDir()
	if err != nil {
		home = "."
	}
	return &projectManagerSnapshotCacheService{
		path: filepath.Join(home, appSettingsDir, projectManagerSnapshotCacheFile),
	}
}

func (s *projectManagerSnapshotCacheService) load() (projectManagerSnapshotCache, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *projectManagerSnapshotCacheService) save(cache projectManagerSnapshotCache) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(cache)
}

func (s *projectManagerSnapshotCacheService) invalidate() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *projectManagerSnapshotCacheService) loadLocked() (projectManagerSnapshotCache, error) {
	cache := newProjectManagerSnapshotCache()
	if s == nil || strings.TrimSpace(s.path) == "" {
		return cache, nil
	}
	if err := ReadJSONFile(s.path, &cache); err != nil {
		if os.IsNotExist(err) {
			return newProjectManagerSnapshotCache(), nil
		}
		return projectManagerSnapshotCache{}, err
	}
	return normalizeProjectManagerSnapshotCache(cache), nil
}

func (s *projectManagerSnapshotCacheService) saveLocked(cache projectManagerSnapshotCache) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	return AtomicWriteJSON(s.path, normalizeProjectManagerSnapshotCache(cache))
}

func newProjectManagerSnapshotCache() projectManagerSnapshotCache {
	return projectManagerSnapshotCache{
		Version:      projectManagerSnapshotCacheVersion,
		SessionFiles: map[string]projectManagerCachedSessionFile{},
		CaptureFiles: map[string]projectManagerCachedCaptureFile{},
	}
}

func normalizeProjectManagerSnapshotCache(cache projectManagerSnapshotCache) projectManagerSnapshotCache {
	if cache.Version == 0 {
		cache.Version = projectManagerSnapshotCacheVersion
	}
	if cache.SessionFiles == nil {
		cache.SessionFiles = map[string]projectManagerCachedSessionFile{}
	}
	if cache.CaptureFiles == nil {
		cache.CaptureFiles = map[string]projectManagerCachedCaptureFile{}
	}
	return cache
}

func (cache projectManagerSnapshotCache) isUsable() bool {
	return cache.Version == projectManagerSnapshotCacheVersion
}

func projectManagerFileSignature(info os.FileInfo) projectManagerTrackedFile {
	return projectManagerTrackedFile{
		Size:             info.Size(),
		ModTimeUnixMilli: info.ModTime().UnixMilli(),
	}
}

func projectManagerFileSignatureEquals(left, right projectManagerTrackedFile) bool {
	return left.Size == right.Size && left.ModTimeUnixMilli == right.ModTimeUnixMilli
}

func normalizeProjectManagerTrackedPath(path string) string {
	return normalizeProjectManagerProjectPath(path)
}

func (s *ProjectManagerService) buildProjectManagerSnapshotCache(previous *projectManagerSnapshotCache) (projectManagerSnapshotCache, error) {
	store, err := s.store.load()
	if err != nil {
		return projectManagerSnapshotCache{}, err
	}

	cache := newProjectManagerSnapshotCache()
	if previous != nil && previous.isUsable() {
		cache = normalizeProjectManagerSnapshotCache(*previous)
	}

	indexEntries, indexMeta, err := s.readProjectManagerSessionIndexCached(cache)
	if err != nil {
		return projectManagerSnapshotCache{}, err
	}
	indexByID := make(map[string]projectManagerSessionIndexEntry, len(indexEntries))
	for _, entry := range indexEntries {
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		indexByID[entry.ID] = entry
	}

	sessionFiles, sessionFileCache, err := s.readProjectManagerSessionFilesIncremental(cache, indexByID)
	if err != nil {
		return projectManagerSnapshotCache{}, err
	}
	captureFiles, captureFileCache, err := s.readProjectManagerCaptureFilesIncremental(cache)
	if err != nil {
		return projectManagerSnapshotCache{}, err
	}

	aggregate := s.buildProjectManagerAggregateFromEntries(store, sessionFiles, captureFiles, indexByID)
	snapshot := ProjectManagerSnapshot{
		Projects:          aggregate.Projects,
		Sessions:          aggregate.Sessions,
		SnapshotUpdatedAt: time.Now().UnixMilli(),
	}

	return normalizeProjectManagerSnapshotCache(projectManagerSnapshotCache{
		Version:             projectManagerSnapshotCacheVersion,
		Snapshot:            snapshot,
		SessionIndexMeta:    indexMeta,
		SessionIndexEntries: indexEntries,
		SessionFiles:        sessionFileCache,
		CaptureFiles:        captureFileCache,
	}), nil
}

func (s *ProjectManagerService) buildProjectManagerAggregateFromEntries(
	store projectManagerStore,
	sessionFiles []projectManagerSessionFileEntry,
	captureFiles []projectManagerCaptureFileEntry,
	indexByID map[string]projectManagerSessionIndexEntry,
) projectManagerAggregate {
	sessions := make(map[string]*projectManagerSessionState, len(sessionFiles))
	_ = s.enrichProjectManagerSessionsFromCodexSessions(sessions, store, sessionFiles, indexByID)
	s.enrichProjectManagerSessionsFromCaptureEntries(sessions, store, captureFiles)
	projects := s.groupProjectManagerProjects(sessions, store)

	return projectManagerAggregate{
		Projects: buildProjectManagerProjectSummaries(projects),
		Sessions: buildProjectManagerSessionSummaries(projects),
	}
}

func (s *ProjectManagerService) readProjectManagerSessionIndexCached(
	cache projectManagerSnapshotCache,
) ([]projectManagerSessionIndexEntry, projectManagerTrackedFile, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return nil, projectManagerTrackedFile{}, err
	}

	path := filepath.Join(home, ".codex", "session_index.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []projectManagerSessionIndexEntry{}, projectManagerTrackedFile{}, nil
		}
		return nil, projectManagerTrackedFile{}, err
	}

	signature := projectManagerFileSignature(info)
	if cache.isUsable() && projectManagerFileSignatureEquals(cache.SessionIndexMeta, signature) {
		return cache.SessionIndexEntries, signature, nil
	}

	entries, err := s.readProjectManagerSessionIndex()
	if err != nil {
		return nil, projectManagerTrackedFile{}, err
	}
	return entries, signature, nil
}

func (s *ProjectManagerService) readProjectManagerSessionFilesIncremental(
	cache projectManagerSnapshotCache,
	indexByID map[string]projectManagerSessionIndexEntry,
) ([]projectManagerSessionFileEntry, map[string]projectManagerCachedSessionFile, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return nil, nil, err
	}

	baseDir := filepath.Join(home, ".codex", "sessions")
	inventory, err := listProjectManagerFileInventory(baseDir, ".jsonl")
	if err != nil {
		return nil, nil, err
	}

	nextCache := make(map[string]projectManagerCachedSessionFile, len(inventory))
	result := make([]projectManagerSessionFileEntry, 0, len(inventory))
	keys := listProjectManagerTrackedFileKeys(inventory)
	for _, key := range keys {
		signature := inventory[key]
		cached, ok := cache.SessionFiles[key]
		entry := projectManagerSessionFileEntry{}
		if ok && projectManagerFileSignatureEquals(cached.Signature, signature) {
			entry = cached.Entry
		} else {
			parsed, parseErr := scanProjectManagerCodexSessionFile(key)
			if parseErr != nil {
				// 历史扫描阶段必须容忍坏文件；否则某个半截 rollout 就能把整个首页加载拖死。
				continue
			}
			if parsed.SessionID == "" {
				parsed.SessionID = extractProjectManagerSessionIDFromFileName(filepath.Base(key))
			}
			if parsed.SessionID == "" {
				continue
			}
			entry = parsed
		}

		if indexEntry, ok := indexByID[entry.SessionID]; ok {
			entry.ThreadName = strings.TrimSpace(indexEntry.ThreadName)
		} else {
			entry.ThreadName = ""
		}
		entry.Path = key

		nextCache[key] = projectManagerCachedSessionFile{
			Signature: signature,
			Entry:     entry,
		}
		result = append(result, entry)
	}

	return result, nextCache, nil
}

func (s *ProjectManagerService) readProjectManagerCaptureFilesIncremental(
	cache projectManagerSnapshotCache,
) ([]projectManagerCaptureFileEntry, map[string]projectManagerCachedCaptureFile, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return nil, nil, err
	}

	root := filepath.Join(home, appSettingsDir, requestCaptureDirName, "codex")
	inventory, err := listProjectManagerFileInventory(root, ".json")
	if err != nil {
		return nil, nil, err
	}

	nextCache := make(map[string]projectManagerCachedCaptureFile, len(inventory))
	result := make([]projectManagerCaptureFileEntry, 0, len(inventory))
	keys := listProjectManagerTrackedFileKeys(inventory)
	for _, key := range keys {
		signature := inventory[key]
		cached, ok := cache.CaptureFiles[key]
		entry := projectManagerCaptureFileEntry{}
		if ok && projectManagerFileSignatureEquals(cached.Signature, signature) {
			entry = cached.Entry
		} else {
			parsed, parseErr := parseProjectManagerCaptureFile(key)
			if parseErr != nil {
				continue
			}
			if strings.TrimSpace(parsed.SessionID) == "" {
				continue
			}
			entry = parsed
		}

		entry.Path = key
		nextCache[key] = projectManagerCachedCaptureFile{
			Signature: signature,
			Entry:     entry,
		}
		result = append(result, entry)
	}

	return result, nextCache, nil
}

func listProjectManagerFileInventory(root string, extension string) (map[string]projectManagerTrackedFile, error) {
	result := make(map[string]projectManagerTrackedFile)
	if !FileExists(root) {
		return result, nil
	}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), extension) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		result[normalizeProjectManagerTrackedPath(path)] = projectManagerFileSignature(info)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func listProjectManagerTrackedFileKeys(items map[string]projectManagerTrackedFile) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func scanProjectManagerCodexSessionFile(path string) (projectManagerSessionFileEntry, error) {
	sessionID, cwd, projectPath, projectSource, summary, latestUserMessage, latestUserMessageAt, updatedAt, err := scanProjectManagerCodexSessionFileDetails(path)
	if err != nil {
		return projectManagerSessionFileEntry{}, err
	}

	return projectManagerSessionFileEntry{
		SessionID:           sessionID,
		Path:                normalizeProjectManagerTrackedPath(path),
		Cwd:                 cwd,
		ProjectPath:         projectPath,
		ProjectSource:       projectSource,
		Summary:             summary,
		LatestUserMessage:   latestUserMessage,
		LatestUserMessageAt: latestUserMessageAt,
		UpdatedAt:           updatedAt,
		IsRollout:           projectManagerIsRolloutSessionPath(path),
	}, nil
}

func parseProjectManagerCaptureFile(path string) (projectManagerCaptureFileEntry, error) {
	record, err := readProjectManagerCapture(path)
	if err != nil {
		return projectManagerCaptureFileEntry{}, err
	}

	capturedAt, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(record.CapturedAt))
	if parseErr != nil {
		capturedAt = time.Time{}
	}

	entry := projectManagerCaptureFileEntry{
		SessionID:  strings.TrimSpace(record.SessionID),
		Path:       normalizeProjectManagerTrackedPath(path),
		CapturedAt: capturedAt,
		ProjectID:  strings.TrimSpace(record.ProjectID),
	}
	entry.WindowID = extractProjectManagerWindowID(record.Raw)
	entry.Summary = extractProjectManagerFirstUserMessage(record.Raw)

	if projectRoot, source := extractProjectManagerProjectRoot(record.Raw); projectRoot != "" {
		entry.ProjectPath = normalizeProjectManagerProjectPath(projectRoot)
		entry.ProjectSource = source
	} else if entry.ProjectID != "" && entry.ProjectID != unknownProjectCaptureID {
		entry.ProjectPath = normalizeProjectManagerProjectPath(entry.ProjectID)
		entry.ProjectSource = "capture.project_id"
	}

	return entry, nil
}

func (s *ProjectManagerService) enrichProjectManagerSessionsFromCaptureEntries(
	sessions map[string]*projectManagerSessionState,
	store projectManagerStore,
	entries []projectManagerCaptureFileEntry,
) {
	for _, entry := range entries {
		sessionID := strings.TrimSpace(entry.SessionID)
		if sessionID == "" || projectManagerSessionIsHidden(store, sessionID) {
			continue
		}

		state, ok := sessions[sessionID]
		if !ok {
			state = &projectManagerSessionState{
				SessionID:   sessionID,
				SourceName:  sessionID,
				DisplayName: sessionID,
			}
			if meta, metaOK := store.Sessions[sessionID]; metaOK {
				if trimmed := strings.TrimSpace(meta.DisplayName); trimmed != "" {
					state.DisplayName = trimmed
				}
				if trimmed := strings.TrimSpace(meta.Summary); trimmed != "" {
					state.Summary = trimmed
				}
				if trimmed := strings.TrimSpace(meta.WindowID); trimmed != "" {
					state.WindowID = trimmed
				}
			}
			sessions[sessionID] = state
		}

		isNewerCapture := entry.CapturedAt.After(state.UpdatedAt)
		if isNewerCapture {
			state.UpdatedAt = entry.CapturedAt
		}

		if windowID := strings.TrimSpace(entry.WindowID); windowID != "" {
			state.WindowID = windowID
			_ = s.store.saveSessionMetadata(sessionID, func(meta *projectManagerSessionMeta) bool {
				if strings.TrimSpace(meta.WindowID) == windowID {
					return false
				}
				meta.WindowID = windowID
				return true
			})
		}

		if summary := strings.TrimSpace(entry.Summary); state.Summary == "" && summary != "" {
			state.Summary = summary
			_ = s.store.saveSessionMetadata(sessionID, func(meta *projectManagerSessionMeta) bool {
				if strings.TrimSpace(meta.Summary) == summary {
					return false
				}
				meta.Summary = summary
				return true
			})
		}
		if latestUserMessage := strings.TrimSpace(entry.Summary); latestUserMessage != "" && (state.LatestUserMessage == "" || entry.CapturedAt.After(state.LatestUserMessageAt)) {
			// capture 是没有 Codex JSONL 时的历史兜底来源；只有更新的捕获记录
			// 才能覆盖已有预览，避免旧 capture 把真实会话末条消息顶掉。
			state.LatestUserMessage = latestUserMessage
			state.LatestUserMessageAt = entry.CapturedAt
		}

		if normalized := normalizeProjectManagerProjectPath(entry.ProjectPath); normalized != "" {
			state.ProjectPath = normalized
			state.ProjectSource = strings.TrimSpace(entry.ProjectSource)
			if state.Cwd == "" && state.ProjectSource == "cwd" {
				state.Cwd = normalized
			}
		}

		if state.LastCapturePath == "" || isNewerCapture {
			state.LastCapturePath = entry.Path
		}
	}
}

func (s *ProjectManagerService) loadProjectManagerSnapshotCache() (projectManagerSnapshotCache, error) {
	if s.snapshotCache == nil {
		return newProjectManagerSnapshotCache(), nil
	}
	return s.snapshotCache.load()
}

func (s *ProjectManagerService) saveProjectManagerSnapshotCache(cache projectManagerSnapshotCache) error {
	if s.snapshotCache == nil {
		return nil
	}
	return s.snapshotCache.save(cache)
}

func (s *ProjectManagerService) invalidateProjectManagerSnapshotCache() {
	if s.snapshotCache == nil {
		return
	}
	if err := s.snapshotCache.invalidate(); err != nil {
		log.Printf("[ProjectManager] 清理 snapshot cache 失败: %v", err)
	}
}

func (s *ProjectManagerService) refreshProjectManagerSnapshotIncrementalWithFallback() (ProjectManagerSnapshot, error) {
	s.snapshotBuildMu.Lock()
	defer s.snapshotBuildMu.Unlock()

	cache, err := s.loadProjectManagerSnapshotCache()
	if err != nil {
		cache = newProjectManagerSnapshotCache()
	}

	// 优先复用已有文件级索引做增量刷新；只有缓存损坏或增量失败时才退回全量重建。
	nextCache, err := s.buildProjectManagerSnapshotCache(&cache)
	if err != nil || !nextCache.isUsable() {
		nextCache, err = s.buildProjectManagerSnapshotCache(nil)
		if err != nil {
			return ProjectManagerSnapshot{}, err
		}
	}

	if err := s.saveProjectManagerSnapshotCache(nextCache); err != nil {
		return ProjectManagerSnapshot{}, err
	}
	return nextCache.Snapshot, nil
}

func (s *ProjectManagerService) maybeScheduleProjectManagerWarmRefresh(snapshot ProjectManagerSnapshot) {
	if snapshot.SnapshotUpdatedAt == 0 {
		return
	}
	if time.Since(time.UnixMilli(snapshot.SnapshotUpdatedAt)) < projectManagerWarmRefreshMinGap {
		return
	}

	s.warmRefreshMu.Lock()
	if s.warmRefreshRunning || time.Since(s.lastWarmRefreshAt) < projectManagerWarmRefreshMinGap {
		s.warmRefreshMu.Unlock()
		return
	}
	s.warmRefreshRunning = true
	s.lastWarmRefreshAt = time.Now()
	s.warmRefreshMu.Unlock()

	go func() {
		defer func() {
			s.warmRefreshMu.Lock()
			s.warmRefreshRunning = false
			s.warmRefreshMu.Unlock()
		}()

		if _, err := s.refreshProjectManagerSnapshotIncrementalWithFallback(); err != nil {
			log.Printf("[ProjectManager] 静默增量刷新失败: %v", err)
		}
	}()
}
