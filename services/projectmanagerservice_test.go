package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestProjectManagerCodexProviderBindingSurvivesRenameAndEnrichesSnapshot(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	projectDir := filepath.Join(home, "workspace", "provider-bound")
	sessionID := "019f3509-provider-bound"
	writeProviderFixture(t, "codex", []Provider{
		{ID: 11, Name: "Global", APIURL: "https://global.example.com", APIKey: "sk-global", Enabled: true},
		{ID: 22, Name: "Project Fast", APIURL: "https://project.example.com", APIKey: "sk-project", Enabled: true},
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Provider Bound", "2026-06-15T09:40:00Z")
	writeProjectManagerCodexSessionFixture(t, home, sessionID, projectDir, "绑定项目供应商", "2026-06-15T09:58:00Z", "2026-06-15T10:00:00Z")

	if err := service.SetProjectCodexProviderRouting(projectDir, 22, false); err != nil {
		t.Fatalf("SetProjectCodexProviderRouting 失败: %v", err)
	}
	if err := service.SaveProjectRunCommand(projectDir, "npm run dev"); err != nil {
		t.Fatalf("SaveProjectRunCommand 失败: %v", err)
	}
	if err := service.RenameProject(projectDir, "Provider Bound Alias"); err != nil {
		t.Fatalf("RenameProject 失败: %v", err)
	}

	store, err := service.store.load()
	if err != nil {
		t.Fatalf("读取 project manager store 失败: %v", err)
	}
	normalizedProject := normalizeProjectManagerProjectPath(projectDir)
	meta := store.Projects[normalizedProject]
	if meta.DisplayName != "Provider Bound Alias" {
		t.Fatalf("项目别名未保留，got=%q", meta.DisplayName)
	}
	if meta.CodexProviderID != 22 {
		t.Fatalf("项目 provider 绑定未保留，want=22 got=%d", meta.CodexProviderID)
	}
	if !meta.CodexProviderAutoFallbackDisabled {
		t.Fatalf("项目 provider auto=false 未保留")
	}
	if meta.RunCommand != "npm run dev" {
		t.Fatalf("项目运行指令未保留，want=%q got=%q", "npm run dev", meta.RunCommand)
	}

	snapshot, err := service.RefreshProjectIndex()
	if err != nil {
		t.Fatalf("RefreshProjectIndex 失败: %v", err)
	}
	if len(snapshot.Projects) != 1 {
		t.Fatalf("项目数不对，want=1 got=%d", len(snapshot.Projects))
	}
	project := snapshot.Projects[0]
	if project.CodexProviderID != 22 {
		t.Fatalf("快照 provider id 不对，want=22 got=%d", project.CodexProviderID)
	}
	if project.CodexProviderName != "Project Fast" {
		t.Fatalf("快照 provider 名称不对，want=%q got=%q", "Project Fast", project.CodexProviderName)
	}
	if project.CodexProviderAuto {
		t.Fatalf("快照 provider auto 不对，want=false got=true")
	}
	if project.RunCommand != "npm run dev" {
		t.Fatalf("快照运行指令不对，want=%q got=%q", "npm run dev", project.RunCommand)
	}

	if err := service.ClearProjectCodexProvider(projectDir); err != nil {
		t.Fatalf("ClearProjectCodexProvider 失败: %v", err)
	}
	store, err = service.store.load()
	if err != nil {
		t.Fatalf("重新读取 project manager store 失败: %v", err)
	}
	if got := store.Projects[normalizedProject].CodexProviderID; got != 0 {
		t.Fatalf("清除 provider 绑定失败，got=%d", got)
	}
	if got := store.Projects[normalizedProject].CodexProviderAutoFallbackDisabled; got {
		t.Fatalf("清除 provider 后不应保留 auto=false")
	}
	if got := store.Projects[normalizedProject].RunCommand; got != "npm run dev" {
		t.Fatalf("清除 provider 不应影响运行指令，got=%q", got)
	}
}

func TestProjectManagerRunCommandCanBeClearedWithoutDroppingProjectMeta(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	projectDir := filepath.Join(home, "workspace", "run-command-clear")
	writeProviderFixture(t, "codex", []Provider{
		{ID: 22, Name: "Project Fast", APIURL: "https://project.example.com", APIKey: "sk-project", Enabled: true},
	})

	if err := service.RenameProject(projectDir, "Run Command Alias"); err != nil {
		t.Fatalf("RenameProject 失败: %v", err)
	}
	if err := service.SetProjectCodexProviderRouting(projectDir, 22, false); err != nil {
		t.Fatalf("SetProjectCodexProviderRouting 失败: %v", err)
	}
	if err := service.SaveProjectRunCommand(projectDir, "pnpm dev"); err != nil {
		t.Fatalf("SaveProjectRunCommand 失败: %v", err)
	}
	if err := service.SaveProjectRunCommand(projectDir, "   "); err != nil {
		t.Fatalf("清空运行指令失败: %v", err)
	}

	store, err := service.store.load()
	if err != nil {
		t.Fatalf("读取 project manager store 失败: %v", err)
	}

	normalizedProject := normalizeProjectManagerProjectPath(projectDir)
	meta, ok := store.Projects[normalizedProject]
	if !ok {
		t.Fatalf("清空运行指令不应删除仍包含别名/provider 的项目 meta")
	}
	if meta.DisplayName != "Run Command Alias" {
		t.Fatalf("项目别名不应被清空，got=%q", meta.DisplayName)
	}
	if meta.CodexProviderID != 22 || !meta.CodexProviderAutoFallbackDisabled {
		t.Fatalf("项目 provider 配置不应被清空，got=%+v", meta)
	}
	if meta.RunCommand != "" {
		t.Fatalf("运行指令应被清空，got=%q", meta.RunCommand)
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

func TestProjectManagerGetSnapshotFallsBackToSessionMetaCwd(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-rollout-cwd-fallback-case"
	projectDir := filepath.Join(home, "workspace", "codex-rule-system")
	writeProjectManagerSessionIndex(t, home, sessionID, "Cwd Fallback Session", "2026-06-16T10:01:14Z")
	writeProjectManagerCodexSessionFixture(
		t,
		home,
		sessionID,
		projectDir,
		"只带 session_meta.cwd 的真实 Codex 历史会话也必须进入项目列表。",
		"2026-06-16T10:00:00Z",
		"2026-06-16T10:02:00Z",
	)

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot 失败: %v", err)
	}
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("会话数不对，want=1 got=%d", len(snapshot.Sessions))
	}

	session := snapshot.Sessions[0]
	normalizedProjectDir := normalizeProjectManagerProjectPath(projectDir)
	if session.ProjectPath != normalizedProjectDir {
		t.Fatalf("session_meta.cwd 应作为项目路径兜底，want=%q got=%q", normalizedProjectDir, session.ProjectPath)
	}
	if session.ProjectSourceHint != "cwd" {
		t.Fatalf("项目来源提示不对，want=%q got=%q", "cwd", session.ProjectSourceHint)
	}
	if len(snapshot.Projects) != 1 || snapshot.Projects[0].Path != normalizedProjectDir {
		t.Fatalf("项目列表未包含 cwd 兜底项目，projects=%+v", snapshot.Projects)
	}
}

func TestProjectManagerGetSnapshotTracksLatestUserMessage(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-latest-user-message-case"
	projectDir := filepath.Join(home, "workspace", "latest-user-message")
	writeProjectManagerSessionIndex(t, home, sessionID, "Latest User Message Session", "2026-06-16T10:03:00Z")
	writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一问保留为稳定摘要"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,ignored"},{"type":"input_text","text":"第二问才是卡片预览"}]}}`,
		`{"type":"response_item","timestamp":"2026-06-16T10:02:10Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"助手消息不能覆盖用户预览"}]}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:20Z","payload":{"type":"user_message","message":"   "}}`,
	})

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot 失败: %v", err)
	}
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("会话数不对，want=1 got=%d", len(snapshot.Sessions))
	}

	session := snapshot.Sessions[0]
	if session.Summary != "第一问保留为稳定摘要" {
		t.Fatalf("summary 应继续保留首条用户消息，got=%q", session.Summary)
	}
	if session.LatestUserMessage != "第二问才是卡片预览" {
		t.Fatalf("卡片预览应取最后一条有效用户消息，got=%q", session.LatestUserMessage)
	}

	// 用新 service 重新读取落盘快照，锁住 latest_user_message 的缓存序列化契约。
	cachedService := NewProjectManagerService()
	cachedSnapshot, err := cachedService.GetSnapshot()
	if err != nil {
		t.Fatalf("从缓存读取 GetSnapshot 失败: %v", err)
	}
	if len(cachedSnapshot.Sessions) != 1 || cachedSnapshot.Sessions[0].LatestUserMessage != "第二问才是卡片预览" {
		t.Fatalf("快照缓存丢失最新用户消息，sessions=%+v", cachedSnapshot.Sessions)
	}
}

func TestProjectManagerGetSnapshotKeepsLargeRolloutSession(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-large-rollout-case"
	projectDir := filepath.Join(home, "workspace", "large-rollout")
	writeProjectManagerSessionIndex(t, home, sessionID, "Large Rollout Session", "2026-06-16T10:01:14Z")

	// 真实 Codex rollout 会把长日志、工具输出或系统上下文写成单行 JSON。
	// 这个用例锁住“超长非关键行不应让整条会话被跳过”的扫描契约。
	largePayload := strings.Repeat("x", 2*1024*1024)
	writeProjectManagerCodexSessionFixture(
		t,
		home,
		sessionID,
		projectDir,
		"前面的 cwd 已经足够识别项目，后面的超长行不能把它冲掉。",
		"2026-06-16T10:00:00Z",
		"2026-06-16T10:02:00Z",
	)
	sessionPath := filepath.Join(home, ".codex", "sessions", "2026", "06", "15", "20260615-"+sessionID+".jsonl")
	if err := os.WriteFile(sessionPath, []byte(strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-06-16T10:00:00Z","payload":{"id":%q,"cwd":%q,"timestamp":"2026-06-16T10:00:00Z"}}`, sessionID, projectDir),
		`{"type":"response_item","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"function_call_output","output":"` + largePayload + `"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"超长 rollout 测试"}}`,
	}, "\n")), 0o644); err != nil {
		t.Fatalf("写入超长 rollout fixture 失败: %v", err)
	}

	snapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot 失败: %v", err)
	}

	normalizedProjectDir := normalizeProjectManagerProjectPath(projectDir)
	for _, session := range snapshot.Sessions {
		if session.ID == sessionID && session.ProjectPath == normalizedProjectDir {
			return
		}
	}
	t.Fatalf("超长 rollout 会话没有进入项目快照，sessions=%+v", snapshot.Sessions)
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

func TestProjectManagerGetSessionConversationDetailRolloutItemsIncludeTurnID(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-rollout-turn-case"
	writeProjectManagerSessionIndex(t, home, sessionID, "Rollout Turn Session", "2026-06-16T10:01:14Z")
	writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-00-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-rollout-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"task_complete","turn_id":"turn-rollout-1"}}`,
	})

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if len(detail.Items) != 2 {
		t.Fatalf("详情消息数不对，want=2 got=%d", len(detail.Items))
	}
	if detail.Items[0].TurnID != "turn-rollout-1" || detail.Items[1].TurnID != "turn-rollout-1" {
		t.Fatalf("rollout 消息未带 turn_id: %+v", detail.Items)
	}
}

func TestProjectManagerGetSessionConversationDetailAggregatesRolloutTurnUsage(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-rollout-usage-case"
	writeProjectManagerSessionIndex(t, home, sessionID, "Rollout Usage Session", "2026-06-16T10:01:14Z")
	writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-00-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-usage-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"统计这一轮"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":120},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":20,"reasoning_output_tokens":10,"total_tokens":120}}}}`,
		// 同一累计快照重复写入时不能把一次模型调用翻倍。
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":120},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":20,"reasoning_output_tokens":10,"total_tokens":120}}}}`,
		// 累计计数回退代表会话恢复后的新片段，必须继续累计本次调用。
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:05Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":40},"last_token_usage":{"input_tokens":30,"cached_input_tokens":5,"output_tokens":10,"reasoning_output_tokens":2,"total_tokens":40}}}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:08Z","payload":{"type":"agent_message","message":"统计完成"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"task_complete","turn_id":"turn-usage-1"}}`,
	})

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	usage := detail.Items[0].TurnUsage
	if usage == nil {
		t.Fatal("用户消息缺少逐轮用量")
	}
	if usage.InputTokens != 130 || usage.CachedInputTokens != 25 || usage.OutputTokens != 30 || usage.ReasoningOutputTokens != 12 {
		t.Fatalf("Token 明细累计不对: %+v", usage)
	}
	if usage.TotalTokens != 160 || usage.ModelCalls != 2 {
		t.Fatalf("总 Token 或模型调用数不对: %+v", usage)
	}
	if usage.DurationMS != 10_000 || !usage.Complete {
		t.Fatalf("轮次结束状态或耗时不对: %+v", usage)
	}
	if detail.Items[1].TurnUsage != nil {
		t.Fatalf("Agent 消息不应重复持有整轮用量: %+v", detail.Items[1])
	}
}

func TestProjectManagerGetSessionConversationDetailMarksUnfinishedTurnUsagePartial(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-rollout-partial-usage-case"
	writeProjectManagerSessionIndex(t, home, sessionID, "Partial Usage Session", "2026-06-16T10:01:14Z")
	writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-00-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-partial-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"还没结束"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:05Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":40},"last_token_usage":{"input_tokens":30,"output_tokens":10,"total_tokens":40}}}}`,
	})

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	usage := detail.Items[0].TurnUsage
	if usage == nil {
		t.Fatal("未完成轮次仍应返回已累计用量")
	}
	if usage.TotalTokens != 40 || usage.ModelCalls != 1 || usage.DurationMS != 5_000 || usage.Complete {
		t.Fatalf("未完成轮次统计不对: %+v", usage)
	}
}

func TestProjectManagerConversationDetailCacheTracksRolloutUsage(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-primary-usage-cache-case"
	projectDir := filepath.Join(home, "workspace", "primary-usage-cache")
	writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"主会话问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:05Z","payload":{"type":"agent_message","message":"主会话回答"}}`,
	})
	rolloutName := "rollout-2026-06-16T10-00-00-" + sessionID + ".jsonl"
	writeProjectManagerRolloutFixture(t, home, sessionID, rolloutName, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-primary-usage"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"主会话问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":10},"last_token_usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:05Z","payload":{"type":"task_complete","turn_id":"turn-primary-usage"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Primary Usage Cache Session", "2026-06-16T10:02:14Z")

	first, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("首次 GetSessionConversationDetail 失败: %v", err)
	}
	if first.Items[0].TurnUsage == nil || first.Items[0].TurnUsage.TotalTokens != 10 {
		t.Fatalf("首次 rollout 用量不对: %+v", first.Items[0])
	}

	writeProjectManagerRolloutFixture(t, home, sessionID, rolloutName, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-primary-usage"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"主会话问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":10},"last_token_usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:04Z","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":30},"last_token_usage":{"input_tokens":16,"output_tokens":4,"total_tokens":20}}}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:05Z","payload":{"type":"task_complete","turn_id":"turn-primary-usage"}}`,
	})

	second, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("更新 rollout 后 GetSessionConversationDetail 失败: %v", err)
	}
	if second.Items[0].TurnUsage == nil || second.Items[0].TurnUsage.TotalTokens != 30 || second.Items[0].TurnUsage.ModelCalls != 2 {
		t.Fatalf("rollout 变化后详情缓存没有刷新: %+v", second.Items[0])
	}
}

func TestProjectManagerGetSessionConversationDetailHydratesPrimaryTurnIDsFromRollout(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-primary-turn-case"
	projectDir := filepath.Join(home, "workspace", "primary-turn")
	primaryPath := writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:10Z","payload":{"type":"agent_message","message":"第二答"}}`,
	})
	writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-00-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-primary-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"task_complete","turn_id":"turn-primary-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"task_started","turn_id":"turn-primary-2"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:01Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:02Z","payload":{"type":"agent_message","message":"第二答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:03Z","payload":{"type":"task_complete","turn_id":"turn-primary-2"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Primary Turn Session", "2026-06-16T10:02:14Z")

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if len(detail.Items) != 4 {
		t.Fatalf("详情消息数不对，want=4 got=%d", len(detail.Items))
	}
	if detail.Items[0].SourceFile != primaryPath {
		t.Fatalf("详情仍应优先展示主会话源，got=%q", detail.Items[0].SourceFile)
	}
	if detail.Items[0].TurnID != "turn-primary-1" || detail.Items[1].TurnID != "turn-primary-1" {
		t.Fatalf("第一轮 turn_id 回填失败: %+v", detail.Items[:2])
	}
	if detail.Items[2].TurnID != "turn-primary-2" || detail.Items[3].TurnID != "turn-primary-2" {
		t.Fatalf("第二轮 turn_id 回填失败: %+v", detail.Items[2:])
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

func TestProjectManagerGetSessionConversationDetailUsesSnapshotCacheFastPath(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-detail-fast-path-case"
	projectDir := filepath.Join(home, "workspace", "detail-fast-path")
	sessionPath := writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"缓存快路径问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"缓存快路径回答"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Fast Path Session", "2026-06-16T10:01:14Z")

	if _, err := service.GetSnapshot(); err != nil {
		t.Fatalf("预热 snapshot 失败: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(home, ".codex", "sessions")); err != nil {
		t.Fatalf("删除原始 sessions 目录失败: %v", err)
	}
	if err := AtomicWriteText(sessionPath, strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-06-16T10:00:00Z","payload":{"id":%q,"cwd":%q,"timestamp":"2026-06-16T10:00:00Z"}}`, sessionID, projectDir),
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"缓存快路径问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"缓存快路径回答"}}`,
	}, "\n")); err != nil {
		t.Fatalf("重建单会话文件失败: %v", err)
	}

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if len(detail.Items) != 2 {
		t.Fatalf("详情消息数不对，want=2 got=%d", len(detail.Items))
	}
}

func TestProjectManagerGetSessionConversationDetailReusesDetailCacheUntilSignatureChanges(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-detail-cache-case"
	projectDir := filepath.Join(home, "workspace", "detail-cache")
	sessionPath := writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一次问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一次回答"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Detail Cache Session", "2026-06-16T10:01:14Z")

	first, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("首次 GetSessionConversationDetail 失败: %v", err)
	}

	if err := AtomicWriteText(sessionPath, strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-06-16T10:00:00Z","payload":{"id":%q,"cwd":%q,"timestamp":"2026-06-16T10:00:00Z"}}`, sessionID, projectDir),
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第二次问题"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第二次回答"}}`,
	}, "\n")); err != nil {
		t.Fatalf("改写会话文件失败: %v", err)
	}

	second, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("第二次 GetSessionConversationDetail 失败: %v", err)
	}
	if reflect.DeepEqual(first.Items, second.Items) {
		t.Fatalf("文件签名变化后详情不该继续命中旧缓存，first=%+v second=%+v", first.Items, second.Items)
	}
	if second.Items[0].Content != "第二次问题" || second.Items[1].Content != "第二次回答" {
		t.Fatalf("更新后的详情内容不对，got=%+v", second.Items)
	}
}

func TestProjectManagerForkSessionConversationUsesLatestSelectedTurn(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	originalFork := projectManagerForkSessionWithAppServer
	originalOpen := projectManagerOpenForkedSessionTerminal
	defer func() {
		projectManagerForkSessionWithAppServer = originalFork
		projectManagerOpenForkedSessionTerminal = originalOpen
	}()

	var forkSourceSessionID string
	var forkLastTurnID string
	projectManagerForkSessionWithAppServer = func(sessionID string, lastTurnID string) (string, error) {
		forkSourceSessionID = sessionID
		forkLastTurnID = lastTurnID
		return "019ecab9-forked-session", nil
	}
	var openedSession SessionSummary
	projectManagerOpenForkedSessionTerminal = func(_ *ProjectManagerService, session SessionSummary) error {
		openedSession = session
		return nil
	}

	sessionID := "019ecab9-fork-source-case"
	projectDir := filepath.Join(home, "workspace", "fork-source")
	writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:10Z","payload":{"type":"agent_message","message":"第二答"}}`,
	})
	writeProjectManagerRolloutFixture(t, home, sessionID, "rollout-2026-06-16T10-00-00-"+sessionID+".jsonl", []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"task_started","turn_id":"turn-fork-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:02Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:03Z","payload":{"type":"task_complete","turn_id":"turn-fork-1"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"task_started","turn_id":"turn-fork-2"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:01Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:02Z","payload":{"type":"agent_message","message":"第二答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:03Z","payload":{"type":"task_complete","turn_id":"turn-fork-2"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Fork Source Session", "2026-06-16T10:02:14Z")

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	forked, err := service.ForkSessionConversation(sessionID, []string{detail.Items[0].ID, detail.Items[3].ID})
	if err != nil {
		t.Fatalf("ForkSessionConversation 失败: %v", err)
	}

	if forkSourceSessionID != sessionID {
		t.Fatalf("fork 源会话不对，want=%q got=%q", sessionID, forkSourceSessionID)
	}
	if forkLastTurnID != "turn-fork-2" {
		t.Fatalf("多选 fork 应使用最晚选中轮次，got=%q", forkLastTurnID)
	}
	if forked.ID != "019ecab9-forked-session" || openedSession.ID != forked.ID {
		t.Fatalf("fork 后应打开新会话，forked=%+v opened=%+v", forked, openedSession)
	}
	if openedSession.ProjectPath != projectDir {
		t.Fatalf("fork 会话应继承项目路径，got=%q", openedSession.ProjectPath)
	}
}

func TestProjectManagerForkSessionConversationFailsWithoutTurnID(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	originalFork := projectManagerForkSessionWithAppServer
	defer func() {
		projectManagerForkSessionWithAppServer = originalFork
	}()
	called := false
	projectManagerForkSessionWithAppServer = func(sessionID string, lastTurnID string) (string, error) {
		called = true
		return "should-not-be-used", nil
	}

	sessionID := "019ecab9-fork-missing-turn"
	projectDir := filepath.Join(home, "workspace", "fork-missing-turn")
	writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"没有 rollout 的问题"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Missing Turn Session", "2026-06-16T10:01:14Z")

	detail, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("GetSessionConversationDetail 失败: %v", err)
	}
	if len(detail.Items) != 1 {
		t.Fatalf("详情消息数不对，want=1 got=%d", len(detail.Items))
	}

	_, err = service.ForkSessionConversation(sessionID, []string{detail.Items[0].ID})
	if err == nil || !strings.Contains(err.Error(), "turn_id") {
		t.Fatalf("缺少 turn_id 应 fail-fast，got=%v", err)
	}
	if called {
		t.Fatal("缺少 turn_id 时不应调用 Codex app-server fork")
	}
}

func TestProjectManagerPruneSessionConversationInvalidatesDetailCache(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	sessionID := "019ecab9-prune-cache-case"
	projectDir := filepath.Join(home, "workspace", "prune-cache")
	writeProjectManagerConversationFixture(t, home, sessionID, projectDir, []string{
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:00Z","payload":{"type":"user_message","message":"第一问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:01:10Z","payload":{"type":"agent_message","message":"第一答"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:00Z","payload":{"type":"user_message","message":"第二问"}}`,
		`{"type":"event_msg","timestamp":"2026-06-16T10:02:10Z","payload":{"type":"agent_message","message":"第二答"}}`,
	})
	writeProjectManagerSessionIndex(t, home, sessionID, "Prune Cache Session", "2026-06-16T10:01:14Z")

	before, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("首次 GetSessionConversationDetail 失败: %v", err)
	}
	if len(before.Items) != 4 {
		t.Fatalf("初始消息数不对，got=%d", len(before.Items))
	}

	pruned, err := service.PruneSessionConversation(sessionID, []string{before.Items[0].ID})
	if err != nil {
		t.Fatalf("PruneSessionConversation 失败: %v", err)
	}
	if len(pruned.Items) != 2 {
		t.Fatalf("剪枝后消息数不对，want=2 got=%d", len(pruned.Items))
	}

	after, err := service.GetSessionConversationDetail(sessionID)
	if err != nil {
		t.Fatalf("剪枝后再次读取详情失败: %v", err)
	}
	if len(after.Items) != 2 || after.Items[0].Content != "第二问" || after.Items[1].Content != "第二答" {
		t.Fatalf("剪枝后详情缓存未正确失效，got=%+v", after.Items)
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

func TestOpenProjectTerminalRejectsEmptyProjectPath(t *testing.T) {
	service := NewProjectManagerService()

	err := service.OpenProjectTerminal("   ")
	if err == nil || !strings.Contains(err.Error(), "项目路径不能为空") {
		t.Fatalf("期望空路径报错，got=%v", err)
	}
}

func TestRunProjectCommandRejectsMissingRunCommand(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()
	projectDir := filepath.Join(home, "workspace", "missing-run-command")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}

	err := service.RunProjectCommand(projectDir)
	if err == nil || !strings.Contains(err.Error(), "项目运行指令未配置") {
		t.Fatalf("期望未配置运行指令报错，got=%v", err)
	}
}

func TestBuildProjectManagerPowerShellResumeCommandUsesDangerousBypassFlag(t *testing.T) {
	command := buildProjectManagerPowerShellResumeCommand("session-123")

	if !strings.Contains(command, "& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox resume 'session-123'") {
		t.Fatalf("resume 命令未附带危险权限参数，got=%q", command)
	}
}

func TestBuildProjectManagerProjectTerminalPowerShellCommandUsesDangerousBypassFlag(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "workspace", "alpha")
	command := buildProjectManagerProjectTerminalPowerShellCommand(projectPath)

	if !strings.Contains(command, "& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("项目终端命令未附带危险权限参数，got=%q", command)
	}
	if strings.Contains(command, " resume ") {
		t.Fatalf("项目终端命令不该混入 resume，got=%q", command)
	}
}

func TestBuildProjectManagerAICommitPowerShellCommandUsesDangerousBypassFlag(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "workspace", "commit-fast")
	command := buildProjectManagerAICommitPowerShellCommand(projectPath)

	if !strings.Contains(command, "& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox -p commit-fast exec --ephemeral '$commit commit本地文件'") {
		t.Fatalf("AI-Commit 命令未按预期附带危险权限参数，got=%q", command)
	}
	if strings.Contains(command, "codex exec -p commit-fast") {
		t.Fatalf("profile 被错误地下沉到 exec 子命令层，got=%q", command)
	}
}

func TestProjectManagerGetSnapshotWritesAndReusesSnapshotCache(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	projectDir := filepath.Join(home, "workspace", "cache-reuse")
	sessionID := "019ecab9-cache-reuse-case"
	writeProjectManagerSessionIndex(t, home, sessionID, "Cache Reuse Session", "2026-06-18T10:01:14Z")
	writeProjectManagerCodexSessionFixture(
		t,
		home,
		sessionID,
		projectDir,
		"第一次扫描应该落缓存。",
		"2026-06-18T10:00:00Z",
		"2026-06-18T10:02:00Z",
	)

	firstSnapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("首次 GetSnapshot 失败: %v", err)
	}
	if firstSnapshot.SnapshotUpdatedAt == 0 {
		t.Fatalf("首次快照缺少 snapshot_updated_at: %+v", firstSnapshot)
	}

	cachePath := filepath.Join(home, appSettingsDir, projectManagerSnapshotCacheFile)
	if !FileExists(cachePath) {
		t.Fatalf("首次快照后未生成 snapshot cache: %s", cachePath)
	}

	if err := os.RemoveAll(filepath.Join(home, ".codex", "sessions")); err != nil {
		t.Fatalf("删除 session 目录失败: %v", err)
	}
	if err := os.Remove(filepath.Join(home, ".codex", "session_index.jsonl")); err != nil {
		t.Fatalf("删除 session_index 失败: %v", err)
	}

	secondSnapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("复用缓存的 GetSnapshot 失败: %v", err)
	}
	if len(secondSnapshot.Sessions) != 1 || secondSnapshot.Sessions[0].ID != sessionID {
		t.Fatalf("复用缓存失败，sessions=%+v", secondSnapshot.Sessions)
	}
	if secondSnapshot.SnapshotUpdatedAt != firstSnapshot.SnapshotUpdatedAt {
		t.Fatalf("缓存命中时 snapshot 时间不应变化，first=%d second=%d", firstSnapshot.SnapshotUpdatedAt, secondSnapshot.SnapshotUpdatedAt)
	}
}

func TestProjectManagerSnapshotCacheRejectsOldVersion(t *testing.T) {
	cache := newProjectManagerSnapshotCache()
	cache.Version = projectManagerSnapshotCacheVersion - 1
	cache.Snapshot = ProjectManagerSnapshot{
		Sessions: []SessionSummary{
			{ID: "stale-session", ProjectPath: unknownProjectCaptureID},
		},
		SnapshotUpdatedAt: 1,
	}

	if cache.isUsable() {
		t.Fatalf("旧版本 snapshot cache 不应继续可用，version=%d current=%d", cache.Version, projectManagerSnapshotCacheVersion)
	}
}

func TestProjectManagerRefreshProjectIndexPicksUpNewSessionIncrementally(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()

	projectA := filepath.Join(home, "workspace", "incremental-a")
	projectB := filepath.Join(home, "workspace", "incremental-b")
	sessionA := "019ecab9-incremental-a"
	sessionB := "019ecab9-incremental-b"

	writeProjectManagerSessionIndex(t, home, sessionA, "Incremental A", "2026-06-18T10:01:14Z")
	writeProjectManagerCodexSessionFixture(
		t,
		home,
		sessionA,
		projectA,
		"A 先落到缓存里。",
		"2026-06-18T10:00:00Z",
		"2026-06-18T10:02:00Z",
	)

	initialSnapshot, err := service.GetSnapshot()
	if err != nil {
		t.Fatalf("首次 GetSnapshot 失败: %v", err)
	}
	if len(initialSnapshot.Sessions) != 1 || initialSnapshot.Sessions[0].ID != sessionA {
		t.Fatalf("首次快照不对: %+v", initialSnapshot.Sessions)
	}

	indexPath := filepath.Join(home, ".codex", "session_index.jsonl")
	if err := AtomicWriteText(indexPath, strings.Join([]string{
		fmt.Sprintf(`{"id":%q,"thread_name":"Incremental A","updated_at":"2026-06-18T10:01:14Z"}`, sessionA),
		fmt.Sprintf(`{"id":%q,"thread_name":"Incremental B","updated_at":"2026-06-18T10:05:14Z"}`, sessionB),
	}, "\n")); err != nil {
		t.Fatalf("追加第二条 session_index 失败: %v", err)
	}
	writeProjectManagerCodexSessionFixture(
		t,
		home,
		sessionB,
		projectB,
		"B 后续通过增量刷新补进来。",
		"2026-06-18T10:04:00Z",
		"2026-06-18T10:06:00Z",
	)

	refreshed, err := service.RefreshProjectIndex()
	if err != nil {
		t.Fatalf("RefreshProjectIndex 失败: %v", err)
	}
	if len(refreshed.Sessions) != 2 {
		t.Fatalf("增量刷新后会话数不对，want=2 got=%d", len(refreshed.Sessions))
	}

	foundA := false
	foundB := false
	for _, session := range refreshed.Sessions {
		switch session.ID {
		case sessionA:
			foundA = true
		case sessionB:
			foundB = true
			if session.ProjectPath != normalizeProjectManagerProjectPath(projectB) {
				t.Fatalf("增量刷新的第二个会话项目路径不对，got=%q", session.ProjectPath)
			}
		}
	}
	if !foundA || !foundB {
		t.Fatalf("增量刷新未正确返回两条会话: %+v", refreshed.Sessions)
	}
}
