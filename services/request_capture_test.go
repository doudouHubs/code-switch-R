package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAppSettings_DefaultEnableRequestCapture(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	settings := NewAppSettingsService(NewAutoStartService())
	got, err := settings.GetAppSettings()
	if err != nil {
		t.Fatalf("GetAppSettings 失败: %v", err)
	}
	if !got.EnableRequestCapture {
		t.Fatal("默认应开启 enable_request_capture")
	}
	if got.RequestCaptureDir != "" {
		t.Fatalf("默认 request_capture_dir = %q，期望空字符串", got.RequestCaptureDir)
	}
}

func TestAppSettings_SaveNormalizesRequestCaptureDir(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	settingsService := NewAppSettingsService(NewAutoStartService())
	settings, err := settingsService.GetAppSettings()
	if err != nil {
		t.Fatalf("GetAppSettings 失败: %v", err)
	}

	settings.RequestCaptureDir = `captures\requests`
	saved, err := settingsService.SaveAppSettings(settings)
	if err != nil {
		t.Fatalf("SaveAppSettings 失败: %v", err)
	}

	if !filepath.IsAbs(saved.RequestCaptureDir) {
		t.Fatalf("request_capture_dir = %q，期望绝对路径", saved.RequestCaptureDir)
	}
	if !strings.HasSuffix(saved.RequestCaptureDir, filepath.Join("captures", "requests")) {
		t.Fatalf("request_capture_dir = %q，期望以 captures\\requests 结尾", saved.RequestCaptureDir)
	}
}

func TestRequestCaptureService_CaptureWritesMinimalRecord(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	appSettings := NewAppSettingsService(NewAutoStartService())
	service := NewRequestCaptureService(appSettings)
	err := service.Capture(RequestCaptureContext{
		Platform: "claude",
		Method:   http.MethodPost,
		Endpoint: "/v1/messages",
		Query: map[string]string{
			"trace": "1",
		},
		Headers: map[string]string{
			"X-Session-Id": "sess-123",
		},
		Body: []byte(`{"project":"demo-project","model":"claude-sonnet-4","messages":[{"role":"user","content":"hello"}]}`),
	})
	if err != nil {
		t.Fatalf("Capture 失败: %v", err)
	}

	files := collectCaptureFiles(t, filepath.Join(tmpHome, ".code-switch", requestCaptureDirName))
	if len(files) != 1 {
		t.Fatalf("期望 1 个捕获文件，实际 %d", len(files))
	}

	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("读取捕获文件失败: %v", err)
	}

	var record RequestCaptureRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("解析捕获文件失败: %v", err)
	}

	if record.Platform != "claude" {
		t.Fatalf("platform = %q，期望 claude", record.Platform)
	}
	if record.ProjectID != "demo-project" {
		t.Fatalf("project_id = %q，期望 demo-project", record.ProjectID)
	}
	if record.SessionID != "sess-123" {
		t.Fatalf("session_id = %q，期望 sess-123", record.SessionID)
	}
	if record.Request.Endpoint != "/v1/messages" {
		t.Fatalf("endpoint = %q，期望 /v1/messages", record.Request.Endpoint)
	}
	if record.Request.Method != http.MethodPost {
		t.Fatalf("method = %q，期望 POST", record.Request.Method)
	}
	bodyMap, ok := record.Request.Body.(map[string]any)
	if !ok {
		t.Fatalf("body 应为 JSON 对象，实际 %#v", record.Request.Body)
	}
	if bodyMap["project"] != "demo-project" {
		t.Fatalf("body.project = %#v，期望 demo-project", bodyMap["project"])
	}
}

func TestRequestCaptureService_CaptureWritesToConfiguredDirectory(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	appSettings := NewAppSettingsService(NewAutoStartService())
	settings, err := appSettings.GetAppSettings()
	if err != nil {
		t.Fatalf("GetAppSettings 失败: %v", err)
	}
	settings.RequestCaptureDir = filepath.Join(tmpHome, "custom-captures")
	if _, err := appSettings.SaveAppSettings(settings); err != nil {
		t.Fatalf("SaveAppSettings 失败: %v", err)
	}

	service := NewRequestCaptureService(appSettings)
	err = service.Capture(RequestCaptureContext{
		Platform: "codex",
		Method:   http.MethodPost,
		Endpoint: "/responses",
		Headers: map[string]string{
			"Session-Id": "sess-custom",
		},
		Body: []byte(`{"project":"custom-project","model":"gpt-5-codex"}`),
	})
	if err != nil {
		t.Fatalf("Capture 失败: %v", err)
	}

	files := collectCaptureFiles(t, filepath.Join(tmpHome, "custom-captures"))
	if len(files) != 1 {
		t.Fatalf("期望自定义目录下 1 个捕获文件，实际 %d", len(files))
	}
}

func TestRequestCaptureService_DisabledSkipsWrite(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	appSettings := NewAppSettingsService(NewAutoStartService())
	settings, err := appSettings.GetAppSettings()
	if err != nil {
		t.Fatalf("GetAppSettings 失败: %v", err)
	}
	settings.EnableRequestCapture = false
	if _, err := appSettings.SaveAppSettings(settings); err != nil {
		t.Fatalf("SaveAppSettings 失败: %v", err)
	}

	service := NewRequestCaptureService(appSettings)
	if err := service.Capture(RequestCaptureContext{
		Platform: "claude",
		Method:   http.MethodPost,
		Endpoint: "/v1/messages",
		Body:     []byte(`{"project":"demo"}`),
	}); err != nil {
		t.Fatalf("Capture 失败: %v", err)
	}

	files := collectCaptureFiles(t, filepath.Join(tmpHome, ".code-switch", requestCaptureDirName))
	if len(files) != 0 {
		t.Fatalf("关闭开关后不应写文件，实际 %d 个", len(files))
	}
}

func TestRequestCaptureService_CaptureFallsBackToCodexSessionProject(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOME", tmpHome)

	projectDir := filepath.Join(tmpHome, "workspace", "capture-fallback")
	sessionID := "sess-fallback-codex"
	writeProjectManagerRolloutFixtureWithWorkspaceRoots(
		t,
		tmpHome,
		sessionID,
		"rollout-2026-06-16T10-00-04-"+sessionID+".jsonl",
		`C:\Users\X1`,
		[]string{projectDir},
		[]string{
			`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"capture 回退测试"}}`,
		},
	)

	appSettings := NewAppSettingsService(NewAutoStartService())
	service := NewRequestCaptureService(appSettings)
	err := service.Capture(RequestCaptureContext{
		Platform: "codex",
		Method:   http.MethodPost,
		Endpoint: "/responses",
		Headers: map[string]string{
			"Session-Id": sessionID,
		},
		Body: []byte(`{"model":"gpt-5-codex","input":[{"role":"user","content":"hello"}]}`),
	})
	if err != nil {
		t.Fatalf("Capture 失败: %v", err)
	}

	files := collectCaptureFiles(t, filepath.Join(tmpHome, ".code-switch", requestCaptureDirName))
	if len(files) != 1 {
		t.Fatalf("期望 1 个捕获文件，实际 %d", len(files))
	}

	var record RequestCaptureRecord
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("读取捕获文件失败: %v", err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("解析捕获文件失败: %v", err)
	}
	if record.ProjectID != normalizeProjectManagerProjectPath(projectDir) {
		t.Fatalf("project_id = %q，期望 %q", record.ProjectID, normalizeProjectManagerProjectPath(projectDir))
	}
}

func TestRequestCaptureService_CaptureMigratesUnknownProjectDirectory(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOME", tmpHome)

	projectDir := filepath.Join(tmpHome, "workspace", "capture-migrate")
	sessionID := "sess-migrate-codex"
	writeProjectManagerRolloutFixtureWithWorkspaceRoots(
		t,
		tmpHome,
		sessionID,
		"rollout-2026-06-16T10-00-05-"+sessionID+".jsonl",
		`C:\Users\X1`,
		[]string{projectDir},
		[]string{
			`{"type":"event_msg","timestamp":"2026-06-16T10:01:01Z","payload":{"type":"user_message","message":"capture 迁移测试"}}`,
		},
	)

	baseDir := filepath.Join(tmpHome, ".code-switch", requestCaptureDirName, "codex", "unknown-project", sessionID)
	if err := AtomicWriteJSON(filepath.Join(baseDir, "old.json"), RequestCaptureRecord{
		Platform:  "codex",
		ProjectID: unknownProjectCaptureID,
		SessionID: sessionID,
	}); err != nil {
		t.Fatalf("写入旧 unknown capture 失败: %v", err)
	}

	appSettings := NewAppSettingsService(NewAutoStartService())
	service := NewRequestCaptureService(appSettings)
	err := service.Capture(RequestCaptureContext{
		Platform: "codex",
		Method:   http.MethodPost,
		Endpoint: "/responses",
		Headers: map[string]string{
			"Session-Id": sessionID,
		},
		Body: []byte(`{"model":"gpt-5-codex","input":[{"role":"user","content":"hello again"}]}`),
	})
	if err != nil {
		t.Fatalf("Capture 失败: %v", err)
	}

	targetDir := filepath.Join(tmpHome, ".code-switch", requestCaptureDirName, "codex", sanitizeCapturePathSegment(normalizeProjectManagerProjectPath(projectDir), unknownProjectCaptureID), sessionID)
	files := collectCaptureFiles(t, targetDir)
	if len(files) != 2 {
		t.Fatalf("迁移后目标目录应有 2 个 capture，实际 %d", len(files))
	}
	if FileExists(baseDir) {
		t.Fatalf("unknown-project 原目录应被迁空移除: %s", baseDir)
	}
}

func TestProviderRelay_CapturesOncePerIncomingRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok","type":"message","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstreamServer.Close()

	providerService := NewProviderService()
	err := providerService.SaveProviders("claude", []Provider{{
		ID:      1,
		Name:    "CaptureProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "test-api-key",
		Enabled: true,
		Level:   1,
	}})
	if err != nil {
		t.Fatalf("SaveProviders 失败: %v", err)
	}

	relayService := newTestRelayService(providerService)
	router := gin.New()
	relayService.registerRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages?debug=1", strings.NewReader(`{"project":"relay-project","session_id":"relay-session","model":"claude-sonnet-4","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体: %s", w.Code, w.Body.String())
	}

	files := collectCaptureFiles(t, filepath.Join(tmpHome, ".code-switch", requestCaptureDirName))
	if len(files) != 1 {
		t.Fatalf("期望仅写 1 个捕获文件，实际 %d", len(files))
	}
}

func TestDetectCaptureScope_UsesCodexHeadersAndMetadata(t *testing.T) {
	projectID, sessionID := DetectCaptureScope(map[string]string{
		"Session-Id":            "sess-header",
		"Thread-Id":             "thread-header",
		"X-Client-Request-Id":   "client-request",
		"X-Codex-Turn-Metadata": `{"session_id":"sess-meta","thread_id":"thread-meta","workspaces":{"F:\\GitlabProjects\\code-switch-R":{"has_changes":true}}}`,
	}, []byte(`{"model":"gpt-5-codex"}`))

	if projectID != `F:\GitlabProjects\code-switch-R` {
		t.Fatalf("project_id = %q，期望 F:\\GitlabProjects\\code-switch-R", projectID)
	}
	if sessionID != "sess-header" {
		t.Fatalf("session_id = %q，期望 sess-header", sessionID)
	}
}

func TestDetectCaptureScope_UsesCodexMetadataSessionWhenHeadersMissing(t *testing.T) {
	projectID, sessionID := DetectCaptureScope(map[string]string{
		"X-Codex-Turn-Metadata": `{"session_id":"sess-meta","thread_id":"thread-meta","workspaces":{"F:\\GitlabProjects\\code-switch-R":{"has_changes":true}}}`,
	}, []byte(`{"model":"gpt-5-codex"}`))

	if projectID != `F:\GitlabProjects\code-switch-R` {
		t.Fatalf("project_id = %q，期望 F:\\GitlabProjects\\code-switch-R", projectID)
	}
	if sessionID != "sess-meta" {
		t.Fatalf("session_id = %q，期望 sess-meta", sessionID)
	}
}

func TestDetectCaptureScope_UsesProjectRootPathAndWorkdir(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "project_root_path",
			body: `{"tool":{"arguments":{"project_root_path":"F:\\GitlabProjects\\code-switch-R"}}}`,
			want: `F:\GitlabProjects\code-switch-R`,
		},
		{
			name: "workdir",
			body: `{"tool":{"arguments":"{\"workdir\":\"F:\\\\GitlabProjects\\\\code-switch-R\"}"}}`,
			want: `F:\GitlabProjects\code-switch-R`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID, sessionID := DetectCaptureScope(nil, []byte(tt.body))
			if projectID != tt.want {
				t.Fatalf("project_id = %q，期望 %q", projectID, tt.want)
			}
			if sessionID != unknownSessionCaptureID {
				t.Fatalf("session_id = %q，期望 %q", sessionID, unknownSessionCaptureID)
			}
		})
	}
}

func TestDetectCaptureScope_UsesEnvironmentContextTextFallback(t *testing.T) {
	body := `{"input":[{"content":[{"type":"input_text","text":"<environment_context><cwd>F:\\GitlabProjects\\code-switch-R</cwd><workspace_roots><root>F:\\GitlabProjects\\code-switch-R</root></workspace_roots></environment_context>"}]}]}`
	projectID, sessionID := DetectCaptureScope(nil, []byte(body))

	if projectID != `F:\GitlabProjects\code-switch-R` {
		t.Fatalf("project_id = %q，期望 F:\\GitlabProjects\\code-switch-R", projectID)
	}
	if sessionID != unknownSessionCaptureID {
		t.Fatalf("session_id = %q，期望 %q", sessionID, unknownSessionCaptureID)
	}
}

func TestDetectCaptureScope_FallsBackToUnknown(t *testing.T) {
	projectID, sessionID := DetectCaptureScope(nil, []byte(`{"model":"gpt-5-codex","input":[{"role":"user","content":"hello"}]}`))

	if projectID != unknownProjectCaptureID {
		t.Fatalf("project_id = %q，期望 %q", projectID, unknownProjectCaptureID)
	}
	if sessionID != unknownSessionCaptureID {
		t.Fatalf("session_id = %q，期望 %q", sessionID, unknownSessionCaptureID)
	}
}

func collectCaptureFiles(t *testing.T, root string) []string {
	t.Helper()

	files := make([]string, 0)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".json" {
			files = append(files, path)
		}
		return nil
	})
	return files
}
