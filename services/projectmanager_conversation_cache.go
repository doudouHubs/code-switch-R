package services

import (
	"os"
	"strings"
	"sync"
)

type projectManagerConversationCacheEntry struct {
	Signature projectManagerTrackedFile
	Detail    SessionConversationDetail
}

type projectManagerConversationCacheService struct {
	mu      sync.RWMutex
	entries map[string]projectManagerConversationCacheEntry
}

func newProjectManagerConversationCacheService() *projectManagerConversationCacheService {
	return &projectManagerConversationCacheService{
		entries: make(map[string]projectManagerConversationCacheEntry),
	}
}

func (s *projectManagerConversationCacheService) load(sessionID string) (projectManagerConversationCacheEntry, bool) {
	if s == nil {
		return projectManagerConversationCacheEntry{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[strings.TrimSpace(sessionID)]
	return entry, ok
}

func (s *projectManagerConversationCacheService) save(sessionID string, entry projectManagerConversationCacheEntry) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[strings.TrimSpace(sessionID)] = entry
}

func (s *projectManagerConversationCacheService) delete(sessionID string) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, strings.TrimSpace(sessionID))
}

func (s *ProjectManagerService) loadProjectManagerConversationDetailCache(
	session SessionSummary,
	file projectManagerConversationFile,
) (SessionConversationDetail, bool, error) {
	sessionID := strings.TrimSpace(session.ID)
	if sessionID == "" || s.detailCache == nil {
		return SessionConversationDetail{}, false, nil
	}

	cached, ok := s.detailCache.load(sessionID)
	if !ok {
		return SessionConversationDetail{}, false, nil
	}

	info, err := os.Stat(file.Path)
	if err != nil {
		if os.IsNotExist(err) {
			s.invalidateProjectManagerConversationCache(sessionID)
			return SessionConversationDetail{}, false, nil
		}
		return SessionConversationDetail{}, false, err
	}

	signature := projectManagerFileSignature(info)
	if !projectManagerFileSignatureEquals(cached.Signature, signature) {
		s.invalidateProjectManagerConversationCache(sessionID)
		return SessionConversationDetail{}, false, nil
	}

	detail := cached.Detail
	detail.Session = session
	return detail, true, nil
}

func (s *ProjectManagerService) saveProjectManagerConversationDetailCache(
	file projectManagerConversationFile,
	detail SessionConversationDetail,
) {
	sessionID := strings.TrimSpace(detail.Session.ID)
	if sessionID == "" || s.detailCache == nil {
		return
	}

	info, err := os.Stat(file.Path)
	if err != nil {
		return
	}

	s.detailCache.save(sessionID, projectManagerConversationCacheEntry{
		Signature: projectManagerFileSignature(info),
		Detail:    detail,
	})
}
