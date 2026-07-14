package services

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const projectManagerStoreFile = "project-manager.json"

type projectManagerStore struct {
	Projects map[string]projectManagerProjectMeta `json:"projects"`
	Sessions map[string]projectManagerSessionMeta `json:"sessions"`
}

type projectManagerProjectMeta struct {
	DisplayName                       string `json:"display_name,omitempty"`
	RunCommand                        string `json:"run_command,omitempty"`
	CodexProviderID                   int64  `json:"codex_provider_id,omitempty"`
	CodexProviderAutoFallbackDisabled bool   `json:"codex_provider_auto_fallback_disabled,omitempty"`
}

type projectManagerSessionMeta struct {
	DisplayName string `json:"display_name"`
	Summary     string `json:"summary"`
	WindowID    string `json:"window_id"`
	Hidden      bool   `json:"hidden,omitempty"`
}

type projectManagerStoreService struct {
	path string
	mu   sync.Mutex
}

func newProjectManagerStoreService() *projectManagerStoreService {
	home, err := getUserHomeDir()
	if err != nil {
		home = "."
	}
	return &projectManagerStoreService{
		path: filepath.Join(home, appSettingsDir, projectManagerStoreFile),
	}
}

func (s *projectManagerStoreService) load() (projectManagerStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *projectManagerStoreService) saveProjectDisplayName(projectPath string, displayName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := normalizeProjectManagerProjectPath(projectPath)
	if key == "" {
		return errors.New("项目路径不能为空")
	}

	trimmed := strings.TrimSpace(displayName)
	meta := store.Projects[key]
	// 项目元数据由别名、运行指令和 Codex provider 绑定共同组成。
	// 修改别名时必须保留同一项目的其它配置，否则一次重命名就会误删项目级事实源。
	meta.DisplayName = trimmed
	if trimmed == "" {
		if projectManagerProjectMetaIsEmpty(meta) {
			delete(store.Projects, key)
		} else {
			store.Projects[key] = meta
		}
	} else {
		store.Projects[key] = meta
	}

	return s.saveLocked(store)
}

func (s *projectManagerStoreService) saveProjectRunCommand(projectPath string, command string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := normalizeProjectManagerProjectPath(projectPath)
	if key == "" {
		return errors.New("项目路径不能为空")
	}

	meta := store.Projects[key]
	// 运行指令是项目级配置，和别名/provider 共用同一条 meta 记录。
	// 这里只更新 run_command，避免用户编辑指令时把已有别名或路由策略“顺手清空”。
	meta.RunCommand = strings.TrimSpace(command)
	if projectManagerProjectMetaIsEmpty(meta) {
		delete(store.Projects, key)
	} else {
		store.Projects[key] = meta
	}

	return s.saveLocked(store)
}

func (s *projectManagerStoreService) saveProjectCodexProviderID(projectPath string, providerID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := normalizeProjectManagerProjectPath(projectPath)
	if key == "" {
		return errors.New("项目路径不能为空")
	}

	meta := store.Projects[key]
	if providerID < 0 {
		providerID = 0
	}
	meta.CodexProviderID = providerID
	if providerID <= 0 {
		meta.CodexProviderAutoFallbackDisabled = false
	}
	if projectManagerProjectMetaIsEmpty(meta) {
		delete(store.Projects, key)
	} else {
		store.Projects[key] = meta
	}

	return s.saveLocked(store)
}

func (s *projectManagerStoreService) saveProjectCodexProviderRouting(projectPath string, providerID int64, autoFallback bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := normalizeProjectManagerProjectPath(projectPath)
	if key == "" {
		return errors.New("项目路径不能为空")
	}

	meta := store.Projects[key]
	if providerID < 0 {
		providerID = 0
	}
	meta.CodexProviderID = providerID
	// 默认值必须保持自动回落，避免老项目因为新增字段突然变成硬锁。
	// 只有用户显式关闭 auto 时才写 disabled=true；清回默认路由时同步移除该策略。
	meta.CodexProviderAutoFallbackDisabled = providerID > 0 && !autoFallback
	if projectManagerProjectMetaIsEmpty(meta) {
		delete(store.Projects, key)
	} else {
		store.Projects[key] = meta
	}

	return s.saveLocked(store)
}

func (s *projectManagerStoreService) deleteProject(projectPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := normalizeProjectManagerProjectPath(projectPath)
	if key == "" {
		return errors.New("项目路径不能为空")
	}

	delete(store.Projects, key)
	return s.saveLocked(store)
}

func (s *projectManagerStoreService) saveSessionMetadata(sessionID string, mutate func(*projectManagerSessionMeta) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("会话 ID 不能为空")
	}

	meta := store.Sessions[sessionID]
	if !mutate(&meta) {
		return nil
	}

	if !meta.Hidden && meta.DisplayName == "" && meta.Summary == "" && meta.WindowID == "" {
		delete(store.Sessions, sessionID)
	} else {
		store.Sessions[sessionID] = meta
	}

	return s.saveLocked(store)
}

func (s *projectManagerStoreService) deleteSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("会话 ID 不能为空")
	}

	delete(store.Sessions, sessionID)
	return s.saveLocked(store)
}

func (s *projectManagerStoreService) hideSession(sessionID string) error {
	return s.saveSessionMetadata(sessionID, func(meta *projectManagerSessionMeta) bool {
		changed := meta.DisplayName != "" || meta.Summary != "" || meta.WindowID != "" || !meta.Hidden
		meta.DisplayName = ""
		meta.Summary = ""
		meta.WindowID = ""
		meta.Hidden = true
		return changed
	})
}

func projectManagerSessionIsHidden(store projectManagerStore, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	meta, ok := store.Sessions[sessionID]
	return ok && meta.Hidden
}

func projectManagerProjectMetaIsEmpty(meta projectManagerProjectMeta) bool {
	return strings.TrimSpace(meta.DisplayName) == "" &&
		strings.TrimSpace(meta.RunCommand) == "" &&
		meta.CodexProviderID <= 0 &&
		!meta.CodexProviderAutoFallbackDisabled
}

func (s *projectManagerStoreService) loadLocked() (projectManagerStore, error) {
	store := projectManagerStore{
		Projects: map[string]projectManagerProjectMeta{},
		Sessions: map[string]projectManagerSessionMeta{},
	}

	if s == nil || strings.TrimSpace(s.path) == "" {
		return store, nil
	}

	if err := ReadJSONFile(s.path, &store); err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return store, err
	}

	if store.Projects == nil {
		store.Projects = map[string]projectManagerProjectMeta{}
	}
	if store.Sessions == nil {
		store.Sessions = map[string]projectManagerSessionMeta{}
	}

	return store, nil
}

func (s *projectManagerStoreService) saveLocked(store projectManagerStore) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	if store.Projects == nil {
		store.Projects = map[string]projectManagerProjectMeta{}
	}
	if store.Sessions == nil {
		store.Sessions = map[string]projectManagerSessionMeta{}
	}
	return AtomicWriteJSON(s.path, store)
}

func normalizeProjectManagerProjectPath(projectPath string) string {
	trimmed := strings.TrimSpace(projectPath)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if abs, err := filepath.Abs(cleaned); err == nil {
		cleaned = filepath.Clean(abs)
	}
	return cleaned
}

func listProjectManagerStoreKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
