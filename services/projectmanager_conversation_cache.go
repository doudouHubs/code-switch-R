package services

import (
	"os"
	"strings"
	"sync"
)

type projectManagerConversationCacheEntry struct {
	Signatures map[string]projectManagerTrackedFile
	Detail     SessionConversationDetail
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

	signatures, err := s.projectManagerConversationDetailSignatures(file)
	if err != nil {
		if os.IsNotExist(err) {
			s.invalidateProjectManagerConversationCache(sessionID)
			return SessionConversationDetail{}, false, nil
		}
		return SessionConversationDetail{}, false, err
	}

	if !projectManagerConversationSignaturesEqual(cached.Signatures, signatures) {
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

	signatures, err := s.projectManagerConversationDetailSignatures(file)
	if err != nil {
		return
	}

	s.detailCache.save(sessionID, projectManagerConversationCacheEntry{
		Signatures: signatures,
		Detail:     detail,
	})
}

func (s *ProjectManagerService) projectManagerConversationDetailSignatures(
	file projectManagerConversationFile,
) (map[string]projectManagerTrackedFile, error) {
	paths := map[string]struct{}{
		normalizeProjectManagerTrackedPath(file.Path): {},
	}

	// 主会话详情会从 rollout 回填 turn_id 和用量，因此 rollout 变化也必须使缓存失效。
	if !file.IsRollout {
		rolloutFiles, err := s.findProjectManagerRolloutFilesByID(file.SessionID)
		if err != nil {
			return nil, err
		}
		for _, rolloutFile := range rolloutFiles {
			paths[normalizeProjectManagerTrackedPath(rolloutFile.Path)] = struct{}{}
		}
	}

	signatures := make(map[string]projectManagerTrackedFile, len(paths))
	for path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		signatures[path] = projectManagerFileSignature(info)
	}
	return signatures, nil
}

func projectManagerConversationSignaturesEqual(
	left map[string]projectManagerTrackedFile,
	right map[string]projectManagerTrackedFile,
) bool {
	if len(left) != len(right) {
		return false
	}
	for path, leftSignature := range left {
		rightSignature, ok := right[path]
		if !ok || !projectManagerFileSignatureEquals(leftSignature, rightSignature) {
			return false
		}
	}
	return true
}
