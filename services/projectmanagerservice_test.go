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
