package services

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

const (
	projectManagerSearchExcerptRunes       = 120
	projectManagerSearchExcerptPrefixRunes = 40
)

type ProjectSessionSearchResult struct {
	SessionID      string `json:"session_id"`
	MatchedContent string `json:"matched_content"`
}

type projectManagerConversationSearchDocument struct {
	Content string
}

type projectManagerConversationSearchCacheEntry struct {
	Signature projectManagerTrackedFile
	Documents []projectManagerConversationSearchDocument
}

type projectManagerConversationSearchCacheService struct {
	mu      sync.RWMutex
	entries map[string]projectManagerConversationSearchCacheEntry
}

func newProjectManagerConversationSearchCacheService() *projectManagerConversationSearchCacheService {
	return &projectManagerConversationSearchCacheService{
		entries: make(map[string]projectManagerConversationSearchCacheEntry),
	}
}

func (s *projectManagerConversationSearchCacheService) load(
	sessionID string,
	signature projectManagerTrackedFile,
) ([]projectManagerConversationSearchDocument, bool) {
	if s == nil {
		return nil, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[strings.TrimSpace(sessionID)]
	if !ok || !projectManagerFileSignatureEquals(entry.Signature, signature) {
		return nil, false
	}
	return entry.Documents, true
}

func (s *projectManagerConversationSearchCacheService) save(
	sessionID string,
	entry projectManagerConversationSearchCacheEntry,
) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[strings.TrimSpace(sessionID)] = entry
}

func (s *projectManagerConversationSearchCacheService) delete(sessionID string) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, strings.TrimSpace(sessionID))
}

func (s *ProjectManagerService) SearchProjectSessionConversations(
	projectPath string,
	query string,
) ([]ProjectSessionSearchResult, error) {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return nil, errors.New("项目路径不能为空")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []ProjectSessionSearchResult{}, nil
	}

	// 冷缓存构建串行化，避免快速连续输入时多次并发扫描同一批历史文件。
	// 首次完成后后续查询只在内存语料上匹配，等待成本会迅速降下来。
	s.conversationSearchMu.Lock()
	defer s.conversationSearchMu.Unlock()

	snapshot, snapshotCache, err := s.loadProjectManagerSnapshotWithCache()
	if err != nil {
		return nil, err
	}

	normalizedQuery := strings.ToLower(query)
	results := make([]ProjectSessionSearchResult, 0, 16)
	for _, session := range snapshot.Sessions {
		if !projectManagerProjectPathsEqual(session.ProjectPath, projectPath) {
			continue
		}

		metadataMatched := projectManagerSessionMetadataMatches(session, normalizedQuery)
		matchedContent, err := s.findProjectManagerSessionLatestUserMatch(session, snapshotCache, query, normalizedQuery)
		if err != nil {
			return nil, fmt.Errorf("搜索会话 %s 失败: %w", session.ID, err)
		}
		if !metadataMatched && matchedContent == "" {
			continue
		}

		results = append(results, ProjectSessionSearchResult{
			SessionID:      session.ID,
			MatchedContent: matchedContent,
		})
	}

	return results, nil
}

func projectManagerSessionMetadataMatches(session SessionSummary, normalizedQuery string) bool {
	fields := []string{
		session.DisplayName,
		session.SourceName,
		session.ProjectName,
		session.ProjectPath,
		session.LatestUserMessage,
		session.Summary,
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), normalizedQuery) {
			return true
		}
	}
	return false
}

func (s *ProjectManagerService) findProjectManagerSessionLatestUserMatch(
	session SessionSummary,
	snapshotCache projectManagerSnapshotCache,
	query string,
	normalizedQuery string,
) (string, error) {
	file, err := s.findProjectManagerSessionFileByIDFast(session.ID, snapshotCache)
	if err != nil {
		// capture-only 历史卡片没有可搜索的详情源，但仍然可以靠元数据参与结果集。
		if errors.Is(err, errProjectManagerSessionFileNotFound) {
			return "", nil
		}
		return "", err
	}

	documents, err := s.loadProjectManagerConversationSearchDocuments(file)
	if err != nil {
		return "", err
	}
	for index := len(documents) - 1; index >= 0; index-- {
		content := documents[index].Content
		if strings.Contains(strings.ToLower(content), normalizedQuery) {
			return buildProjectManagerConversationSearchExcerpt(content, query), nil
		}
	}
	return "", nil
}

func (s *ProjectManagerService) loadProjectManagerConversationSearchDocuments(
	file projectManagerConversationFile,
) ([]projectManagerConversationSearchDocument, error) {
	info, err := os.Stat(file.Path)
	if err != nil {
		return nil, err
	}
	signature := projectManagerFileSignature(info)
	if documents, ok := s.conversationSearchCache.load(file.SessionID, signature); ok {
		return documents, nil
	}

	var items []SessionConversationItem
	if file.IsRollout {
		items, err = readProjectManagerRolloutConversationItems(file.Path, file.SessionID)
	} else {
		// 全文搜索不需要 fork 用的 rollout turn_id 回填，直接复用主文件消息解析即可，
		// 避免每个会话额外遍历全部 rollout 文件形成平方级磁盘扫描。
		items, err = readProjectManagerSessionConversationItems(file.Path, file.SessionID)
	}
	if err != nil {
		return nil, err
	}

	documents := make([]projectManagerConversationSearchDocument, 0, len(items))
	for _, item := range items {
		if item.Role != "user" || strings.TrimSpace(item.Content) == "" {
			continue
		}
		documents = append(documents, projectManagerConversationSearchDocument{
			Content: item.Content,
		})
	}
	s.conversationSearchCache.save(file.SessionID, projectManagerConversationSearchCacheEntry{
		Signature: signature,
		Documents: documents,
	})
	return documents, nil
}

func buildProjectManagerConversationSearchExcerpt(content string, query string) string {
	sourceRunes := []rune(content)
	queryRunes := []rune(query)
	normalizedSourceRunes := []rune(strings.ToLower(content))
	normalizedQueryRunes := []rune(strings.ToLower(query))
	matchStart := indexProjectManagerRunes(normalizedSourceRunes, normalizedQueryRunes)
	if matchStart < 0 {
		return content
	}

	windowSize := projectManagerSearchExcerptRunes
	if len(queryRunes) > windowSize {
		windowSize = len(queryRunes)
	}
	start := matchStart - projectManagerSearchExcerptPrefixRunes
	if start < 0 {
		start = 0
	}
	end := start + windowSize
	matchEnd := matchStart + len(queryRunes)
	if end < matchEnd {
		end = matchEnd
	}
	if end > len(sourceRunes) {
		end = len(sourceRunes)
		start = end - windowSize
		if start < 0 {
			start = 0
		}
	}

	excerpt := string(sourceRunes[start:end])
	if start > 0 {
		excerpt = "..." + excerpt
	}
	if end < len(sourceRunes) {
		excerpt += "..."
	}
	return excerpt
}

func indexProjectManagerRunes(source []rune, target []rune) int {
	if len(target) == 0 || len(target) > len(source) {
		return -1
	}
	for start := 0; start <= len(source)-len(target); start++ {
		matched := true
		for offset := range target {
			if source[start+offset] != target[offset] {
				matched = false
				break
			}
		}
		if matched {
			return start
		}
	}
	return -1
}
