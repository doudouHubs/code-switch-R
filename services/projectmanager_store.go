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
	DisplayName string `json:"display_name"`
}

type projectManagerSessionMeta struct {
	DisplayName string `json:"display_name"`
	Summary     string `json:"summary"`
	WindowID    string `json:"window_id"`
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
	if trimmed == "" {
		delete(store.Projects, key)
	} else {
		store.Projects[key] = projectManagerProjectMeta{DisplayName: trimmed}
	}

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

	if meta.DisplayName == "" && meta.Summary == "" && meta.WindowID == "" {
		delete(store.Sessions, sessionID)
	} else {
		store.Sessions[sessionID] = meta
	}

	return s.saveLocked(store)
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
