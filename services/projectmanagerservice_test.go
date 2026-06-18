package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupProjectManagerTestHome(t *testing.T) string {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	return tmpHome
}

func mustParseProjectManagerRFC3339Time(t *testing.T, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("解析测试时间失败: %v", err)
	}
	return parsed
}

func writeProjectManagerSessionIndex(t *testing.T, home string, sessionID string, threadName string, updatedAt string) string {
	t.Helper()

	path := filepath.Join(home, ".codex", "session_index.jsonl")
	line := fmt.Sprintf(`{"id":%q,"thread_name":%q,"updated_at":%q}`, sessionID, threadName, updatedAt)
	if err := AtomicWriteText(path, line); err != nil {
		t.Fatalf("写入 session_index.jsonl 失败: %v", err)
	}
	return path
}

func writeProjectManagerCodexSessionFixture(
	t *testing.T,
	home string,
	sessionID string,
	projectDir string,
	summary string,
	metaTimestamp string,
	eventTimestamp string,
) string {
	t.Helper()

	path := filepath.Join(home, ".codex", "sessions", "2026", "06", "15", "20260615-"+sessionID+".jsonl")
	lines := []string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":%q,"payload":{"id":%q,"cwd":%q,"timestamp":%q}}`, metaTimestamp, sessionID, projectDir, metaTimestamp),
		fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"user_message","message":%q}}`, eventTimestamp, summary),
	}
	if err := AtomicWriteText(path, strings.Join(lines, "\n")); err != nil {
		t.Fatalf("写入 Codex session fixture 失败: %v", err)
	}
	return path
}

func writeProjectManagerConversationFixture(
	t *testing.T,
	home string,
	sessionID string,
	projectDir string,
	lines []string,
) string {
	t.Helper()

	path := filepath.Join(home, ".codex", "sessions", "2026", "06", "16", "20260616-"+sessionID+".jsonl")
	baseLines := []string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-06-16T10:00:00Z","payload":{"id":%q,"cwd":%q,"timestamp":"2026-06-16T10:00:00Z"}}`, sessionID, projectDir),
	}
	baseLines = append(baseLines, lines...)
	if err := AtomicWriteText(path, strings.Join(baseLines, "\n")); err != nil {
		t.Fatalf("写入 conversation fixture 失败: %v", err)
	}
	return path
}

func writeProjectManagerRolloutFixture(
	t *testing.T,
	home string,
	sessionID string,
	fileName string,
	lines []string,
) string {
	t.Helper()

	path := filepath.Join(home, ".codex", "sessions", "2026", "06", "16", fileName)
	baseLines := []string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-06-16T10:00:00Z","payload":{"id":%q,"cwd":"C:\\workspace\\rollout","timestamp":"2026-06-16T10:00:00Z"}}`, sessionID),
	}
	baseLines = append(baseLines, lines...)
	if err := AtomicWriteText(path, strings.Join(baseLines, "\n")); err != nil {
		t.Fatalf("写入 rollout fixture 失败: %v", err)
	}
	return path
}

func writeProjectManagerRolloutFixtureWithWorkspaceRoots(
	t *testing.T,
	home string,
	sessionID string,
	fileName string,
	cwd string,
	workspaceRoots []string,
	lines []string,
) string {
	t.Helper()

	rootsJSON, err := json.Marshal(workspaceRoots)
	if err != nil {
		t.Fatalf("序列化 workspace_roots 失败: %v", err)
	}

	path := filepath.Join(home, ".codex", "sessions", "2026", "06", "16", fileName)
	baseLines := []string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-06-16T10:00:00Z","payload":{"id":%q,"cwd":%q,"timestamp":"2026-06-16T10:00:00Z"}}`, sessionID, cwd),
		fmt.Sprintf(`{"type":"turn_context","timestamp":"2026-06-16T10:00:01Z","payload":{"turn_id":"turn-workspace-root","cwd":%q,"workspace_roots":%s}}`, cwd, string(rootsJSON)),
	}
	baseLines = append(baseLines, lines...)
	if err := AtomicWriteText(path, strings.Join(baseLines, "\n")); err != nil {
		t.Fatalf("写入带 workspace_roots 的 rollout fixture 失败: %v", err)
	}
	return path
}

func writeProjectManagerCaptureFixture(
	t *testing.T,
	home string,
	sessionID string,
	projectDir string,
	summary string,
	windowID string,
	capturedAt string,
) string {
	t.Helper()

	record := map[string]any{
		"captured_at": capturedAt,
		"project_id":  projectDir,
		"session_id":  sessionID,
		"request": map[string]any{
			"headers": map[string]any{
				"X-Codex-Window-Id": windowID,
			},
			"body": map[string]any{
				"tool_args": fmt.Sprintf(`{"workspace":{"project_root_path":%q}}`, projectDir),
				"input": []any{
					map[string]any{
						"content": []any{
							map[string]any{
								"text": summary,
							},
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("序列化 capture fixture 失败: %v", err)
	}

	path := filepath.Join(home, appSettingsDir, requestCaptureDirName, "codex", "fixtures", sessionID, "capture.json")
	if err := AtomicWriteBytes(path, data); err != nil {
		t.Fatalf("写入 capture fixture 失败: %v", err)
	}
	return path
}

func writeProjectManagerUnknownCaptureFixture(
	t *testing.T,
	home string,
	sessionID string,
	summary string,
	windowID string,
	capturedAt string,
) string {
	t.Helper()

	record := map[string]any{
		"captured_at": capturedAt,
		"project_id":  unknownProjectCaptureID,
		"session_id":  sessionID,
		"request": map[string]any{
			"headers": map[string]any{
				"X-Codex-Window-Id": windowID,
			},
			"body": map[string]any{
				"input": []any{
					map[string]any{
						"content": []any{
							map[string]any{
								"text": summary,
							},
						},
					},
				},
			},
		},
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("序列化 unknown capture fixture 失败: %v", err)
	}

	path := filepath.Join(home, appSettingsDir, requestCaptureDirName, "codex", "unknown-project", sessionID, "capture.json")
	if err := AtomicWriteBytes(path, data); err != nil {
		t.Fatalf("写入 unknown capture fixture 失败: %v", err)
	}
	return path
}

func TestProjectManagerRenamePersistsAliases(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	projectDir := filepath.Join(home, "workspace", "alpha")
	sessionID := "019ecab9-rename-case"
	indexPath := writeProjectManagerSessionIndex(
		t,
		home,
		sessionID,
		"Old Session",
		"2026-06-15T10:01:14.1126438Z",
	)

	if err := service.RenameProject(projectDir, "Alpha Alias"); err != nil {
		t.Fatalf("RenameProject 失败: %v", err)
	}
	if err := service.RenameSession(sessionID, "Session Alias"); err != nil {
		t.Fatalf("RenameSession 失败: %v", err)
	}

	store, err := service.store.load()
	if err != nil {
		t.Fatalf("读取 project manager store 失败: %v", err)
	}

	normalizedProject := normalizeProjectManagerProjectPath(projectDir)
	if got := store.Projects[normalizedProject].DisplayName; got != "Alpha Alias" {
		t.Fatalf("项目别名未落盘，got=%q", got)
	}
	if got := store.Sessions[sessionID].DisplayName; got != "Session Alias" {
		t.Fatalf("会话别名未落盘，got=%q", got)
	}

	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("读取 session_index.jsonl 失败: %v", err)
	}
	if !strings.Contains(string(data), `"thread_name":"Session Alias"`) {
		t.Fatalf("session_index.jsonl 未写回新标题: %s", string(data))
	}
}

func TestProjectManagerGetSnapshotAggregatesCodexArtifacts(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	projectDir := filepath.Join(home, "workspace", "nebula")
	sessionID := "019ecab9-snapshot-case"
	sessionTitle := "Nebula Planning"
	codexSummary := "新增一个项目管理模块，先把历史会话接进来。"
	captureSummary := "这条概要只有在 session 文件拿不到摘要时才该兜底。"
	metaTimestamp := "2026-06-15T09:58:00Z"
	eventTimestamp := "2026-06-15T10:00:00Z"
	capturedAt := "2026-06-15T10:05:00Z"
	windowID := "window-123:9"

	writeProjectManagerSessionIndex(t, home, sessionID, sessionTitle, "2026-06-15T09:40:00Z")
	writeProjectManagerCodexSessionFixture(t, home, sessionID, projectDir, codexSummary, metaTimestamp, eventTimestamp)
	capturePath := writeProjectManagerCaptureFixture(t, home, sessionID, projectDir, captureSummary, windowID, capturedAt)

	if err := service.RenameProject(projectDir, "Nebula Studio"); err != nil {
		t.Fatalf("RenameProject 失败: %v", err)
	}

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot 失败: %v", err)
	}

	if len(snapshot.Projects) != 1 {
		t.Fatalf("项目数不对，want=1 got=%d", len(snapshot.Projects))
	}
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("会话数不对，want=1 got=%d", len(snapshot.Sessions))
	}

	project := snapshot.Projects[0]
	normalizedProject := normalizeProjectManagerProjectPath(projectDir)
	if project.Path != normalizedProject {
		t.Fatalf("项目路径不对，want=%q got=%q", normalizedProject, project.Path)
	}
	if project.DisplayName != "Nebula Studio" {
		t.Fatalf("项目显示名不对，want=%q got=%q", "Nebula Studio", project.DisplayName)
	}
	if project.SourceName != filepath.Base(normalizedProject) {
		t.Fatalf("项目原始名不对，want=%q got=%q", filepath.Base(normalizedProject), project.SourceName)
	}
	if project.SessionCount != 1 {
		t.Fatalf("项目会话数不对，want=1 got=%d", project.SessionCount)
	}

	session := snapshot.Sessions[0]
	if session.ID != sessionID {
		t.Fatalf("会话 ID 不对，want=%q got=%q", sessionID, session.ID)
	}
	if session.ProjectPath != normalizedProject {
		t.Fatalf("会话项目路径不对，want=%q got=%q", normalizedProject, session.ProjectPath)
	}
	if session.ProjectName != "Nebula Studio" {
		t.Fatalf("会话项目名不对，want=%q got=%q", "Nebula Studio", session.ProjectName)
	}
	if session.DisplayName != sessionTitle {
		t.Fatalf("会话显示名不对，want=%q got=%q", sessionTitle, session.DisplayName)
	}
	if session.SourceName != sessionTitle {
		t.Fatalf("会话原始名不对，want=%q got=%q", sessionTitle, session.SourceName)
	}
	if session.Summary != projectManagerTrimSummary(codexSummary) {
		t.Fatalf("会话摘要来源不对，want=%q got=%q", projectManagerTrimSummary(codexSummary), session.Summary)
	}
	if session.WindowID != windowID {
		t.Fatalf("窗口 ID 不对，want=%q got=%q", windowID, session.WindowID)
	}
	if session.Cwd != projectDir {
		t.Fatalf("cwd 不对，want=%q got=%q", projectDir, session.Cwd)
	}
	if session.LastCapturePath != capturePath {
		t.Fatalf("最后 capture 路径不对，want=%q got=%q", capturePath, session.LastCapturePath)
	}
	if session.ProjectSourceHint != "project_root_path" {
		t.Fatalf("项目来源提示不对，want=%q got=%q", "project_root_path", session.ProjectSourceHint)
	}

	expectedUpdatedAt, err := time.Parse(time.RFC3339Nano, capturedAt)
	if err != nil {
		t.Fatalf("解析 capturedAt 失败: %v", err)
	}
	if session.UpdatedAt != expectedUpdatedAt.UnixMilli() {
		t.Fatalf("更新时间不对，want=%d got=%d", expectedUpdatedAt.UnixMilli(), session.UpdatedAt)
	}

	projectSessions, err := service.ListProjectSessions(projectDir)
	if err != nil {
		t.Fatalf("ListProjectSessions 失败: %v", err)
	}
	if len(projectSessions) != 1 || projectSessions[0].ID != sessionID {
		t.Fatalf("项目会话筛选不对，sessions=%+v", projectSessions)
	}
}

func TestProjectManagerGetSnapshotIncludesSessionsMissingFromIndex(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	indexOnlySessionID := "019ecab9-indexed-case"
	indexOnlyProjectDir := filepath.Join(home, "workspace", "indexed")
	indexOnlySummary := "这个会话在索引里也在 session 文件里。"

	writeProjectManagerSessionIndex(
		t,
		home,
		indexOnlySessionID,
		"Indexed Session",
		"2026-06-15T10:01:14.1126438Z",
	)
	writeProjectManagerCodexSessionFixture(
		t,
		home,
		indexOnlySessionID,
		indexOnlyProjectDir,
		indexOnlySummary,
		"2026-06-15T10:00:00Z",
		"2026-06-15T10:02:00Z",
	)

	historyOnlySessionID := "019ecab9-history-case"
	historyOnlyProjectDir := filepath.Join(home, "workspace", "history-only")
	historyOnlySummary := "这个会话只存在于 .codex/sessions，不该被漏掉。"

	writeProjectManagerCodexSessionFixture(
		t,
		home,
		historyOnlySessionID,
		historyOnlyProjectDir,
		historyOnlySummary,
		"2026-06-14T09:00:00Z",
		"2026-06-14T09:05:00Z",
	)

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot 失败: %v", err)
	}

	if len(snapshot.Sessions) != 2 {
		t.Fatalf("会话数不对，want=2 got=%d", len(snapshot.Sessions))
	}
	if len(snapshot.Projects) != 2 {
		t.Fatalf("项目数不对，want=2 got=%d", len(snapshot.Projects))
	}

	foundHistoryOnly := false
	for _, session := range snapshot.Sessions {
		if session.ID != historyOnlySessionID {
			continue
		}
		foundHistoryOnly = true
		if session.ProjectPath != normalizeProjectManagerProjectPath(historyOnlyProjectDir) {
			t.Fatalf("历史会话项目路径不对，got=%q", session.ProjectPath)
		}
		if session.Summary != projectManagerTrimSummary(historyOnlySummary) {
			t.Fatalf("历史会话摘要不对，got=%q", session.Summary)
		}
		if session.DisplayName != historyOnlySessionID {
			t.Fatalf("历史会话默认显示名应退回 sessionID，got=%q", session.DisplayName)
		}
	}
	if !foundHistoryOnly {
		t.Fatal("只存在于 .codex/sessions 的历史会话没有被纳入快照")
	}
}

func TestProjectManagerGetSnapshotPrefersRolloutWorkspaceRoots(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-rollout-workspace-roots-case"
	projectDir := filepath.Join(home, "workspace", "from-workspace-roots")
	writeProjectManagerSessionIndex(t, home, sessionID, "Workspace Roots Session", "2026-06-16T10:01:14Z")
	writeProjectManagerRolloutFixtureWithWorkspaceRoots(
		t,
		home,
		sessionID,
		"rollout-2026-06-16T10-00-04-"+sessionID+".jsonl",
		`C:\Users\X1`,
		[]string{projectDir},
		[]string{
			`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"rollout 工作区根测试"}}`,
		},
	)
	writeProjectManagerUnknownCaptureFixture(t, home, sessionID, "capture 是 unknown", "window-rollout-root", "2026-06-16T10:05:00Z")

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot 失败: %v", err)
	}

	if len(snapshot.Sessions) != 1 {
		t.Fatalf("会话数不对，want=1 got=%d", len(snapshot.Sessions))
	}

	session := snapshot.Sessions[0]
	if session.ProjectPath != normalizeProjectManagerProjectPath(projectDir) {
		t.Fatalf("应优先使用 rollout workspace_roots，want=%q got=%q", normalizeProjectManagerProjectPath(projectDir), session.ProjectPath)
	}
	if session.ProjectSourceHint != "workspace_roots" {
		t.Fatalf("项目来源提示不对，want=%q got=%q", "workspace_roots", session.ProjectSourceHint)
	}
	if session.Cwd != `C:\Users\X1` {
		t.Fatalf("cwd 应保留原始家目录，got=%q", session.Cwd)
	}
}

func TestExtractProjectManagerProjectRootFromValue(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "workspace", "delta")

	t.Run("nested json string", func(t *testing.T) {
		value := map[string]any{
			"tool_args": fmt.Sprintf(`{"workspace":{"project_root_path":%q}}`, projectDir),
		}

		got, source := extractProjectManagerProjectRootFromValue(value, 0)
		if got != projectDir || source != "project_root_path" {
			t.Fatalf("递归 JSON 提取失败，got=%q source=%q", got, source)
		}
	})

	t.Run("xml cwd tag", func(t *testing.T) {
		value := map[string]any{
			"prompt": "<cwd>" + projectDir + "</cwd>",
		}

		got, source := extractProjectManagerProjectRootFromValue(value, 0)
		if got != projectDir || source != "cwd" {
			t.Fatalf("XML cwd 提取失败，got=%q source=%q", got, source)
		}
	})
}

func TestProjectManagerGetSessionConversationDetail(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-conversation-case"
	projectDir := filepath.Join(home, "workspace", "detail")
	writeProjectManagerSessionIndex(t, home, sessionID, "Detail Session", "2026-06-16T10:01:14Z")
	writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:20Z","payload":{"type":"agent_message","message":"第一答补充"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:01:25Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"这条不该进详情"}]}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:10Z","payload":{"type":"agent_message","message":"第二答"}}`,
	})

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}

	if detail.Session.ID != sessionID {
		t.Fatalf("会话 ID 不对，got=%q", detail.Session.ID)
	}
	if len(detail.Items) != 5 {
		t.Fatalf("详情消息数不对，want=5 got=%d", len(detail.Items))
	}

	if detail.Items[0].Role != "user" || detail.Items[0].Content != "第一问" {
		t.Fatalf("第一条消息不对，got=%+v", detail.Items[0])
	}
	if detail.Items[1].Role != "agent" || detail.Items[1].ReplyFor != detail.Items[0].ID {
		t.Fatalf("第一条回答归属关系不对，got=%+v", detail.Items[1])
	}
	if detail.Items[2].ReplyFor != detail.Items[0].ID {
		t.Fatalf("补充回答也应归属第一问，got=%+v", detail.Items[2])
	}
	if detail.Items[4].ReplyFor != detail.Items[3].ID {
		t.Fatalf("第二轮回答归属关系不对，got=%+v", detail.Items[4])
	}
}

func TestProjectManagerConversationSourceSkipsRollout(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-primary-source-case"
	projectDir := filepath.Join(home, "workspace", "primary-source")
	primaryPath := writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"主会话问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"主会话回答"}}`,
	})
	writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-00-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-primary-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"rollout 里的问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"agent_message","message":"rollout 里的回答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"task_complete","turn_id":"turn-primary-1"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Primary Source Session", "2026-06-16T10:01:14Z")

	source, err := service.findProjectManagerSessionFileByID(sessionID)
	if err != nil {
		t.Fatalf("findProjectManagerSessionFileByID 失败: %v", err)
	}
	if source.Path != primaryPath {
		t.Fatalf("主会话源不该误选 rollout，want=%q got=%q", primaryPath, source.Path)
	}

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if len(detail.Items) != 2 || detail.Items[0].Content != "主会话问题" || detail.Items[1].Content != "主会话回答" {
		t.Fatalf("详情不该从 rollout 读取，got=%+v", detail.Items)
	}
}

func TestProjectManagerGetSessionConversationDetailFallsBackToRolloutOnly(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-rollout-only-detail-case"
	projectDir := filepath.Join(home, "workspace", "rollout-only-detail")
	writeProjectManagerSessionIndex(t, home, sessionID, "Rollout Only Detail Session", "2026-06-16T10:01:14Z")
	writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-03-"+sessionID+".jsonl", []string{
		fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-rollout-only-1"}}`),
		fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"只有 rollout 的问题"}}`),
		fmt.Sprintf(`{"type":"response_item","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"reasoning"}}`),
		fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"agent_message","message":"只有 rollout 的回答"}}`),
		fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-06-16T10:01:05Z","payload":{"type":"task_complete","turn_id":"turn-rollout-only-1"}}`),
	})
	writeProjectManagerCaptureFixture(t, home, sessionID, projectDir, "只有 rollout 的问题", "window-rollout-only", "2026-06-16T10:05:00Z")

	source, err := service.findProjectManagerSessionFileByID(sessionID)
	if err != nil {
		t.Fatalf("findProjectManagerSessionFileByID 失败: %v", err)
	}
	if !source.IsRollout {
		t.Fatalf("rollout only 会话应回退到 rollout 源，got=%+v", source)
	}

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if len(detail.Items) != 2 {
		t.Fatalf("rollout only 详情消息数不对，want=2 got=%d", len(detail.Items))
	}
	if detail.Items[0].Content != "只有 rollout 的问题" || detail.Items[1].Content != "只有 rollout 的回答" {
		t.Fatalf("rollout only 详情内容不对，got=%+v", detail.Items)
	}
	if detail.Items[1].ReplyFor != detail.Items[0].ID {
		t.Fatalf("rollout only 回答归属关系不对，got=%+v", detail.Items[1])
	}
	if detail.Items[0].Timestamp != mustParseProjectManagerRFC3339Time(t, "2026-06-16T10:01:01Z").UnixMilli() {
		t.Fatalf("rollout only 用户时间戳不对，got=%d", detail.Items[0].Timestamp)
	}
	if detail.Items[1].Timestamp != mustParseProjectManagerRFC3339Time(t, "2026-06-16T10:01:03Z").UnixMilli() {
		t.Fatalf("rollout only Agent 时间戳不对，got=%d", detail.Items[1].Timestamp)
	}
}

func TestProjectManagerPruneSessionConversation(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-prune-case"
	projectDir := filepath.Join(home, "workspace", "prune")
	sessionPath := writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:20Z","payload":{"type":"agent_message","message":"第一答补充"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:10Z","payload":{"type":"agent_message","message":"第二答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:20Z","payload":{"type":"token_count","info":{"total_tokens":1}}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Prune Session", "2026-06-16T10:01:14Z")

	before, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if len(before.Items) != 5 {
		t.Fatalf("初始消息数不对，got=%d", len(before.Items))
	}

	pruned, err := service.PruneSessionConversation(sessionID, []string{before.Items[0].ID, before.Items[1].ID, before.Items[4].ID})
	if err != nil {
		t.Fatalf("PruneSessionConversation 失败: %v", err)
	}
	if len(pruned.Items) != 1 {
		t.Fatalf("剪枝后消息数不对，want=1 got=%d", len(pruned.Items))
	}
	if pruned.Items[0].Content != "第二问" {
		t.Fatalf("剪枝后剩余消息不对，got=%+v", pruned.Items)
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("读取会话文件失败: %v", err)
	}
	text := string(data)
	if strings.Contains(text, `"message":"第一问"`) || strings.Contains(text, `"message":"第一答"`) || strings.Contains(text, `"message":"第二答"`) {
		t.Fatalf("剪枝目标仍残留在文件中: %s", text)
	}
	if strings.Contains(text, `"message":"第一答补充"`) {
		t.Fatalf("删用户后 reply 链未联动清理: %s", text)
	}
	if !strings.Contains(text, `"token_count"`) {
		t.Fatalf("非目标消息或其他事件被误删: %s", text)
	}
}

func TestProjectManagerPruneSessionConversationAlsoPrunesRolloutWholeTurn(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-rollout-whole-turn-case"
	projectDir := filepath.Join(home, "workspace", "rollout-whole-turn")
	writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:10Z","payload":{"type":"agent_message","message":"第二答"}}`,
	})
	rolloutPath := writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-01-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-rollout-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"reasoning"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:01:04Z","payload":{"type":"message","role":"assistant"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:05Z","payload":{"type":"task_complete","turn_id":"turn-rollout-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"task_started","turn_id":"turn-rollout-2"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:01Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:03Z","payload":{"type":"agent_message","message":"第二答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:05Z","payload":{"type":"task_complete","turn_id":"turn-rollout-2"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Rollout Whole Turn Session", "2026-06-16T10:01:14Z")

	before, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if _, err := service.PruneSessionConversation(sessionID, []string{before.Items[0].ID}); err != nil {
		t.Fatalf("PruneSessionConversation 失败: %v", err)
	}

	data, err := os.ReadFile(rolloutPath)
	if err != nil {
		t.Fatalf("读取 rollout 文件失败: %v", err)
	}
	text := string(data)
	if strings.Contains(text, `"turn_id":"turn-rollout-1"`) || strings.Contains(text, `"message":"第一问"`) || strings.Contains(text, `"message":"第一答"`) {
		t.Fatalf("第一轮 rollout 没删干净: %s", text)
	}
	if !strings.Contains(text, `"turn_id":"turn-rollout-2"`) || !strings.Contains(text, `"message":"第二问"`) || !strings.Contains(text, `"message":"第二答"`) {
		t.Fatalf("第二轮 rollout 被误删: %s", text)
	}
}

func TestProjectManagerPruneSessionConversationAlsoPrunesRolloutAgentChain(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-rollout-agent-chain-case"
	projectDir := filepath.Join(home, "workspace", "rollout-agent-chain")
	writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:20Z","payload":{"type":"agent_message","message":"第一答补充"}}`,
	})
	rolloutPath := writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-02-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-rollout-agent-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"reasoning"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:01:04Z","payload":{"type":"message","role":"assistant"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:01:05Z","payload":{"type":"function_call","name":"tool-a"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:01:06Z","payload":{"type":"function_call_output","call_id":"call-a"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:07Z","payload":{"type":"agent_message","message":"第一答补充"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:01:08Z","payload":{"type":"message","role":"assistant"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:09Z","payload":{"type":"task_complete","turn_id":"turn-rollout-agent-1"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Rollout Agent Chain Session", "2026-06-16T10:01:14Z")

	before, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if _, err := service.PruneSessionConversation(sessionID, []string{before.Items[1].ID}); err != nil {
		t.Fatalf("PruneSessionConversation 失败: %v", err)
	}

	data, err := os.ReadFile(rolloutPath)
	if err != nil {
		t.Fatalf("读取 rollout 文件失败: %v", err)
	}
	text := string(data)
	if strings.Contains(text, `"message":"第一答"`) || strings.Contains(text, `"tool-a"`) || strings.Contains(text, `"call-a"`) {
		t.Fatalf("命中的 agent 链路未删干净: %s", text)
	}
	if !strings.Contains(text, `"message":"第一问"`) || !strings.Contains(text, `"message":"第一答补充"`) || !strings.Contains(text, `"turn_id":"turn-rollout-agent-1"`) {
		t.Fatalf("同轮其他内容被误删: %s", text)
	}
}

func TestProjectManagerDeleteSessionKeepsCaptureHidden(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-delete-session-case"
	projectDir := filepath.Join(home, "workspace", "delete-session")
	sessionPath := writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"准备删除"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Delete Session", "2026-06-16T10:01:14Z")
	capturePath := writeProjectManagerCaptureFixture(t, home, sessionID, projectDir, "capture 还在", "window-x", "2026-06-16T10:05:00Z")

	if err := service.DeleteSession(sessionID); err != nil {
		t.Fatalf("DeleteSession 失败: %v", err)
	}
	if FileExists(sessionPath) {
		t.Fatalf("会话文件未删除: %s", sessionPath)
	}
	if !FileExists(capturePath) {
		t.Fatalf("capture 不该被清理: %s", capturePath)
	}

	store, err := service.store.load()
	if err != nil {
		t.Fatalf("读取 store 失败: %v", err)
	}
	meta, ok := store.Sessions[sessionID]
	if !ok || !meta.Hidden {
		t.Fatalf("已删会话未写入 hidden tombstone: %+v", meta)
	}

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot 失败: %v", err)
	}
	if len(snapshot.Sessions) != 0 {
		t.Fatalf("已删会话不应继续出现在 snapshot 中: %+v", snapshot.Sessions)
	}
}

func TestProjectManagerDeleteProjectRemovesAllSessions(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	projectDir := filepath.Join(home, "workspace", "delete-project")
	sessionA := "019ecab9-delete-project-a"
	sessionB := "019ecab9-delete-project-b"
	pathA := writeProjectManagerConversationFixture(t, home, sessionA, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"A"}}`,
	})
	pathB := writeProjectManagerConversationFixture(t, home, sessionB, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"B"}}`,
	})
	indexPath := filepath.Join(home, ".codex", "session_index.jsonl")
	if err := AtomicWriteText(indexPath, strings.Join([]string{
		fmt.Sprintf(`{"id":%q,"thread_name":"Session A","updated_at":"2026-06-16T10:01:14Z"}`, sessionA),
		fmt.Sprintf(`{"id":%q,"thread_name":"Session B","updated_at":"2026-06-16T10:02:14Z"}`, sessionB),
	}, "\n")); err != nil {
		t.Fatalf("写入 session_index 失败: %v", err)
	}
	writeProjectManagerCaptureFixture(t, home, sessionA, projectDir, "capture-a", "window-a", "2026-06-16T10:06:00Z")
	writeProjectManagerCaptureFixture(t, home, sessionB, projectDir, "capture-b", "window-b", "2026-06-16T10:07:00Z")

	if err := service.RenameProject(projectDir, "Delete Project Alias"); err != nil {
		t.Fatalf("RenameProject 失败: %v", err)
	}
	if err := service.DeleteProject(projectDir); err != nil {
		t.Fatalf("DeleteProject 失败: %v", err)
	}
	if FileExists(pathA) || FileExists(pathB) {
		t.Fatalf("项目下会话文件未删干净: %v %v", FileExists(pathA), FileExists(pathB))
	}

	store, err := service.store.load()
	if err != nil {
		t.Fatalf("读取 store 失败: %v", err)
	}
	if _, ok := store.Projects[normalizeProjectManagerProjectPath(projectDir)]; ok {
		t.Fatalf("项目别名未清理: %+v", store.Projects)
	}
	for _, sessionID := range []string{sessionA, sessionB} {
		meta, ok := store.Sessions[sessionID]
		if !ok || !meta.Hidden {
			t.Fatalf("会话 %s 未写入 hidden tombstone: %+v", sessionID, meta)
		}
	}

	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("读取 session_index 失败: %v", err)
	}
	text := string(data)
	if strings.Contains(text, sessionA) || strings.Contains(text, sessionB) {
		t.Fatalf("session_index 仍残留已删会话: %s", text)
	}

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot 失败: %v", err)
	}
	if len(snapshot.Projects) != 0 || len(snapshot.Sessions) != 0 {
		t.Fatalf("项目删除后 snapshot 不应残留: %+v %+v", snapshot.Projects, snapshot.Sessions)
	}
}

func TestSanitizeProjectManagerSessionSummaryForTerminal(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "workspace", "alpha")
	cwdDir := filepath.Join(t.TempDir(), "workspace", "beta")

	sanitized := sanitizeProjectManagerSessionSummaryForTerminal(SessionSummary{
		ID:                "  session-001  ",
		ProjectID:         "  project-001  ",
		ProjectPath:       projectDir + `\..\alpha`,
		ProjectName:       "  Alpha  ",
		SourceName:        "  Source  ",
		DisplayName:       "  Display  ",
		Summary:           "  Summary  ",
		WindowID:          "  win-001  ",
		Cwd:               cwdDir + `\..\beta`,
		LastCapturePath:   "  capture.json  ",
		ProjectSourceHint: "  workspace_roots  ",
	})

	if sanitized.ID != "session-001" {
		t.Fatalf("ID 清理失败，got=%q", sanitized.ID)
	}
	if sanitized.ProjectID != "project-001" {
		t.Fatalf("ProjectID 清理失败，got=%q", sanitized.ProjectID)
	}
	if sanitized.ProjectPath != normalizeProjectManagerProjectPath(projectDir) {
		t.Fatalf("ProjectPath 清理失败，got=%q", sanitized.ProjectPath)
	}
	if sanitized.Cwd != normalizeProjectManagerProjectPath(cwdDir) {
		t.Fatalf("Cwd 清理失败，got=%q", sanitized.Cwd)
	}
	if sanitized.DisplayName != "Display" || sanitized.ProjectName != "Alpha" || sanitized.SourceName != "Source" {
		t.Fatalf("字符串 trim 失败，got=%+v", sanitized)
	}
}

func TestRunProjectAICommitRejectsEmptyProjectPath(t *testing.T) {
	service := NewProjectManagerService()

	err := service.RunProjectAICommit("   ")
	if err == nil || !strings.Contains(err.Error(), "项目路径不能为空") {
		t.Fatalf("期望空路径报错，got=%v", err)
	}
}
