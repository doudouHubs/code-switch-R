package services

import (
	"fmt"
	"strings"
	"testing"
)

func TestProjectManagerSearchProjectSessionConversations(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	projectDir := normalizeProjectManagerProjectPath(home + `\workspace\project-a`)
	otherProjectDir := normalizeProjectManagerProjectPath(home + `\workspace\project-b`)
	contentSessionID := "019ecab9-content-search-case"
	metadataSessionID := "019ecab9-metadata-keyword-case"
	otherProjectSessionID := "019ecab9-other-project-case"

	writeProjectManagerConversationFixture(t, home, contentSessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一条 alpha 命中"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"agent-only-secret"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"这是最新的 ALPHA 命中"}}`,
	})
	writeProjectManagerConversationFixture(t, home, metadataSessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"普通会话内容"}}`,
	})
	writeProjectManagerConversationFixture(t, home, otherProjectSessionID, otherProjectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"另一个项目也有 alpha"}}`,
	})

	results, err := service.SearchProjectSessionConversations(projectDir, "alpha")
	if err != nil {
		t.Fatalf("SearchProjectSessionConversations 失败: %v", err)
	}
	if len(results) != 1 || results[0].SessionID != contentSessionID {
		t.Fatalf("正文搜索结果应限制在当前项目，got=%+v", results)
	}
	if results[0].MatchedContent != "这是最新的 ALPHA 命中" {
		t.Fatalf("同一会话应返回最新用户命中，got=%q", results[0].MatchedContent)
	}

	agentOnlyResults, err := service.SearchProjectSessionConversations(projectDir, "agent-only-secret")
	if err != nil {
		t.Fatalf("Agent-only 搜索失败: %v", err)
	}
	if len(agentOnlyResults) != 0 {
		t.Fatalf("项目全文搜索不应匹配 Agent 消息，got=%+v", agentOnlyResults)
	}

	metadataResults, err := service.SearchProjectSessionConversations(projectDir, "metadata-keyword")
	if err != nil {
		t.Fatalf("元数据搜索失败: %v", err)
	}
	if len(metadataResults) != 1 || metadataResults[0].SessionID != metadataSessionID {
		t.Fatalf("元数据匹配未进入结果集，got=%+v", metadataResults)
	}
	if metadataResults[0].MatchedContent != "" {
		t.Fatalf("仅元数据命中时不应覆盖会话摘要，got=%q", metadataResults[0].MatchedContent)
	}

	emptyResults, err := service.SearchProjectSessionConversations(projectDir, "   ")
	if err != nil || len(emptyResults) != 0 {
		t.Fatalf("空查询应直接返回空结果，results=%+v err=%v", emptyResults, err)
	}
}

func TestProjectManagerSearchProjectSessionConversationsSupportsRolloutOnlySession(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()
	sessionID := "019ecab9-rollout-search-case"
	projectDir := normalizeProjectManagerProjectPath(`C:\workspace\rollout`)

	writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-00-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-search-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"rollout-only-search-keyword"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"agent_message","message":"rollout 回答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"task_complete","turn_id":"turn-search-1"}}`,
	})

	results, err := service.SearchProjectSessionConversations(projectDir, "rollout-only-search-keyword")
	if err != nil {
		t.Fatalf("rollout-only 搜索失败: %v", err)
	}
	if len(results) != 1 || results[0].SessionID != sessionID || results[0].MatchedContent != "rollout-only-search-keyword" {
		t.Fatalf("rollout-only 搜索结果不对，got=%+v", results)
	}
}

func TestProjectManagerSearchProjectSessionConversationsInvalidatesChangedFile(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()
	projectDir := normalizeProjectManagerProjectPath(home + `\workspace\cache-search`)
	sessionID := "019ecab9-search-cache-case"
	sessionPath := writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"稳定摘要"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"cache-old-only"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:03:00Z","payload":{"type":"user_message","message":"稳定最新消息"}}`,
	})

	first, err := service.SearchProjectSessionConversations(projectDir, "cache-old-only")
	if err != nil || len(first) != 1 {
		t.Fatalf("首次缓存搜索失败，results=%+v err=%v", first, err)
	}

	updatedLines := []string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-06-16T10:00:00Z","payload":{"id":%q,"cwd":%q,"timestamp":"2026-06-16T10:00:00Z"}}`, sessionID, projectDir),
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"稳定摘要"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"cache-new-content-longer"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:03:00Z","payload":{"type":"user_message","message":"稳定最新消息"}}`,
	}
	if err := AtomicWriteText(sessionPath, strings.Join(updatedLines, "\n")); err != nil {
		t.Fatalf("更新会话搜索 fixture 失败: %v", err)
	}

	stale, err := service.SearchProjectSessionConversations(projectDir, "cache-old-only")
	if err != nil {
		t.Fatalf("缓存失效后的旧关键词搜索失败: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("源文件变化后不应继续返回旧语料，got=%+v", stale)
	}

	updated, err := service.SearchProjectSessionConversations(projectDir, "cache-new-content-longer")
	if err != nil || len(updated) != 1 || updated[0].MatchedContent != "cache-new-content-longer" {
		t.Fatalf("源文件变化后未读取新语料，results=%+v err=%v", updated, err)
	}
}

func TestBuildProjectManagerConversationSearchExcerptCentersKeyword(t *testing.T) {
	content := strings.Repeat("前", 90) + "Needle" + strings.Repeat("后", 90)
	excerpt := buildProjectManagerConversationSearchExcerpt(content, "needle")

	if !strings.HasPrefix(excerpt, "...") || !strings.HasSuffix(excerpt, "...") {
		t.Fatalf("长消息片段应标记前后截断，got=%q", excerpt)
	}
	if !strings.Contains(excerpt, "Needle") {
		t.Fatalf("截取片段必须包含原始大小写的关键词，got=%q", excerpt)
	}
	if got := len([]rune(excerpt)); got > projectManagerSearchExcerptRunes+6 {
		t.Fatalf("搜索片段超过目标长度，got=%d excerpt=%q", got, excerpt)
	}
}
