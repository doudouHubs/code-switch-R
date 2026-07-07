package services

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daodao97/xgo/xdb"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ==================== ReplaceModelInRequestBody 测试 ====================

func TestReplaceModelInRequestBody(t *testing.T) {
	tests := []struct {
		name          string
		inputJSON     string
		newModel      string
		expectError   bool
		expectedModel string
	}{
		// 成功场景
		{
			name: "简单替换",
			inputJSON: `{
				"model": "claude-sonnet-4",
				"messages": [{"role": "user", "content": "Hello"}]
			}`,
			newModel:      "anthropic/claude-sonnet-4",
			expectError:   false,
			expectedModel: "anthropic/claude-sonnet-4",
		},
		{
			name: "复杂嵌套JSON",
			inputJSON: `{
				"model": "claude-opus-4",
				"messages": [
					{
						"role": "user",
						"content": "Test"
					}
				],
				"temperature": 0.7,
				"max_tokens": 1000,
				"metadata": {
					"user_id": "12345"
				}
			}`,
			newModel:      "gpt-4",
			expectError:   false,
			expectedModel: "gpt-4",
		},
		{
			name: "模型名包含特殊字符",
			inputJSON: `{
				"model": "claude-sonnet-4",
				"messages": []
			}`,
			newModel:      "anthropic/claude-3.5-sonnet@20241022",
			expectError:   false,
			expectedModel: "anthropic/claude-3.5-sonnet@20241022",
		},

		// 错误场景
		{
			name: "缺少model字段",
			inputJSON: `{
				"messages": [{"role": "user", "content": "Hello"}]
			}`,
			newModel:    "any-model",
			expectError: true,
		},
		{
			name: "空JSON",
			inputJSON: `{
			}`,
			newModel:    "any-model",
			expectError: true,
		},
		{
			name:        "无效JSON",
			inputJSON:   `{invalid json}`,
			newModel:    "any-model",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodyBytes := []byte(tt.inputJSON)
			result, err := ReplaceModelInRequestBody(bodyBytes, tt.newModel)

			// 检查错误预期
			if tt.expectError && err == nil {
				t.Errorf("期望返回错误，但没有错误")
			}
			if !tt.expectError && err != nil {
				t.Errorf("不期望错误，但返回了: %v", err)
			}

			// 如果不期望错误，验证结果
			if !tt.expectError {
				// 验证返回的JSON是否有效
				if !json.Valid(result) {
					t.Errorf("返回的JSON无效")
				}

				// 验证模型名是否正确替换
				actualModel := gjson.GetBytes(result, "model").String()
				if actualModel != tt.expectedModel {
					t.Errorf("替换后的模型名 = %q, 期望 %q", actualModel, tt.expectedModel)
				}

				// 验证其他字段未被修改
				if gjson.GetBytes(bodyBytes, "messages").Exists() {
					originalMessages := gjson.GetBytes(bodyBytes, "messages").Raw
					resultMessages := gjson.GetBytes(result, "messages").Raw
					if originalMessages != resultMessages {
						t.Errorf("messages 字段被意外修改")
					}
				}
			}
		})
	}
}

// ==================== 端到端场景测试 ====================

func TestModelMappingEndToEnd(t *testing.T) {
	// 模拟真实场景：用户请求 claude-sonnet-4，需要映射到 OpenRouter 的格式
	provider := Provider{
		Name: "OpenRouter",
		SupportedModels: map[string]bool{
			"anthropic/claude-sonnet-4":   true,
			"anthropic/claude-opus-4":     true,
			"openai/gpt-4":                true,
			"google/gemini-pro":           true,
			"meta-llama/llama-3.1-405b":   true,
			"anthropic/claude-3.5-sonnet": true,
			"anthropic/claude-3.5-haiku":  true,
		},
		ModelMapping: map[string]string{
			"claude-*": "anthropic/claude-*",
			"gpt-*":    "openai/gpt-*",
			"gemini-*": "google/gemini-*",
			"llama-*":  "meta-llama/llama-*",
		},
	}

	scenarios := []struct {
		requestedModel string
		shouldSupport  bool
		effectiveModel string
	}{
		// 通配符映射场景
		{"claude-sonnet-4", true, "anthropic/claude-sonnet-4"},
		{"claude-opus-4", true, "anthropic/claude-opus-4"},
		{"claude-3.5-sonnet", true, "anthropic/claude-3.5-sonnet"},
		{"gpt-4", true, "openai/gpt-4"},
		{"gpt-4-turbo", true, "openai/gpt-4-turbo"},
		{"gemini-pro", true, "google/gemini-pro"},
		{"llama-3.1-405b", true, "meta-llama/llama-3.1-405b"},

		// 不支持的模型
		{"deepseek-v3", false, "deepseek-v3"},
		{"qwen-max", false, "qwen-max"},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.requestedModel, func(t *testing.T) {
			// 1. 检查是否支持
			supported := provider.IsModelSupported(scenario.requestedModel)
			if supported != scenario.shouldSupport {
				t.Errorf("IsModelSupported(%q) = %v, 期望 %v",
					scenario.requestedModel, supported, scenario.shouldSupport)
			}

			// 2. 获取有效模型名
			effectiveModel := provider.GetEffectiveModel(scenario.requestedModel)
			if effectiveModel != scenario.effectiveModel {
				t.Errorf("GetEffectiveModel(%q) = %q, 期望 %q",
					scenario.requestedModel, effectiveModel, scenario.effectiveModel)
			}

			// 3. 如果支持，测试请求体替换
			if scenario.shouldSupport {
				requestBody := `{"model": "` + scenario.requestedModel + `", "messages": []}`
				result, err := ReplaceModelInRequestBody([]byte(requestBody), effectiveModel)
				if err != nil {
					t.Fatalf("ReplaceModelInRequestBody 失败: %v", err)
				}

				actualModel := gjson.GetBytes(result, "model").String()
				if actualModel != scenario.effectiveModel {
					t.Errorf("请求体中的模型 = %q, 期望 %q", actualModel, scenario.effectiveModel)
				}
			}
		})
	}
}

// ==================== 配置验证集成测试 ====================

func TestProviderConfigValidation(t *testing.T) {
	// 场景 1：完美配置
	validProvider := Provider{
		Name: "ValidProvider",
		SupportedModels: map[string]bool{
			"anthropic/claude-sonnet-4": true,
			"anthropic/claude-opus-4":   true,
		},
		ModelMapping: map[string]string{
			"claude-sonnet-4": "anthropic/claude-sonnet-4",
			"claude-opus-4":   "anthropic/claude-opus-4",
		},
	}

	errors := validProvider.ValidateConfiguration()
	if len(errors) != 0 {
		t.Errorf("完美配置不应有错误，但返回了: %v", errors)
	}

	// 场景 2：错误配置 - 映射目标不存在
	invalidProvider := Provider{
		Name: "InvalidProvider",
		SupportedModels: map[string]bool{
			"model-a": true,
		},
		ModelMapping: map[string]string{
			"external": "non-existent-model",
		},
	}

	errors = invalidProvider.ValidateConfiguration()
	if len(errors) == 0 {
		t.Errorf("错误配置应该返回验证错误")
	}

	// 场景 3：通配符配置
	wildcardProvider := Provider{
		Name: "WildcardProvider",
		SupportedModels: map[string]bool{
			"anthropic/claude-*": true,
			"openai/gpt-*":       true,
		},
		ModelMapping: map[string]string{
			"claude-*": "anthropic/claude-*",
			"gpt-*":    "openai/gpt-*",
		},
	}

	errors = wildcardProvider.ValidateConfiguration()
	if len(errors) != 0 {
		t.Errorf("通配符配置不应有错误，但返回了: %v", errors)
	}
}

func TestCodexProjectPreferredProviderRoutesFirst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := setupRenameTestEnv(t)

	projectDir := filepath.Join(home, "workspace", "preferred")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}

	hits := map[string]int{}
	globalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["global"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"global"}`))
	}))
	defer globalServer.Close()
	projectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["project"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"project"}`))
	}))
	defer projectServer.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{
		{ID: 1, Name: "Global", APIURL: globalServer.URL, APIKey: "sk-global", Enabled: true, Level: 1},
		{ID: 2, Name: "Project", APIURL: projectServer.URL, APIKey: "sk-project", Enabled: true, Level: 10},
	}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	projectManager := NewProjectManagerService()
	if err := projectManager.SetProjectCodexProvider(projectDir, 2); err != nil {
		t.Fatalf("SetProjectCodexProvider 失败: %v", err)
	}

	router := gin.New()
	newTestRelayService(providerService).registerRoutes(router)

	body := []byte(`{"model":"gpt-5-codex","stream":false}`)
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	metadata, err := json.Marshal(codexTurnMetadata{
		SessionID: "session-preferred",
		Workspaces: map[string]json.RawMessage{
			projectDir: json.RawMessage(`{}`),
		},
	})
	if err != nil {
		t.Fatalf("构造 Codex turn metadata 失败: %v", err)
	}
	req.Header.Set("X-Codex-Turn-Metadata", string(metadata))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，got=%d body=%s", w.Code, w.Body.String())
	}
	if hits["project"] != 1 {
		t.Fatalf("项目首选 provider 未命中，hits=%v", hits)
	}
	if hits["global"] != 0 {
		t.Fatalf("首选 provider 成功时不应回落全局 provider，hits=%v", hits)
	}
	if got := gjson.Get(w.Body.String(), "provider").String(); got != "project" {
		t.Fatalf("响应 provider 不对，want=project got=%q", got)
	}
}

func TestCodexProjectPreferredProviderRoutesFirstWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := setupRenameTestEnv(t)

	projectDir := filepath.Join(home, "workspace", "disabled-preferred")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}

	hits := map[string]int{}
	globalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["global"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"global"}`))
	}))
	defer globalServer.Close()
	projectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["project"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"project"}`))
	}))
	defer projectServer.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{
		{ID: 1, Name: "Global", APIURL: globalServer.URL, APIKey: "sk-global", Enabled: true, Level: 1},
		// 项目级首选允许不在首页启用，避免它被纳入其他项目的全局轮询。
		{ID: 2, Name: "Project", APIURL: projectServer.URL, APIKey: "sk-project", Enabled: false, Level: 10},
	}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	projectManager := NewProjectManagerService()
	if err := projectManager.SetProjectCodexProvider(projectDir, 2); err != nil {
		t.Fatalf("SetProjectCodexProvider 失败: %v", err)
	}

	router := gin.New()
	newTestRelayService(providerService).registerRoutes(router)

	body := []byte(`{"model":"gpt-5-codex","stream":false}`)
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-Root-Path", projectDir)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，got=%d body=%s", w.Code, w.Body.String())
	}
	if hits["project"] != 1 {
		t.Fatalf("未启用的项目首选 provider 应该被命中，hits=%v", hits)
	}
	if hits["global"] != 0 {
		t.Fatalf("未启用项目首选成功时不应回落全局 provider，hits=%v", hits)
	}
	if got := gjson.Get(w.Body.String(), "provider").String(); got != "project" {
		t.Fatalf("响应 provider 不对，want=project got=%q", got)
	}
}

func TestCodexProjectPreferredProviderDoesNotFallbackWhenAutoDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := setupRenameTestEnv(t)

	projectDir := filepath.Join(home, "workspace", "no-auto-fallback")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}

	hits := map[string]int{}
	globalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["global"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"global"}`))
	}))
	defer globalServer.Close()
	projectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["project"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"project failed"}`))
	}))
	defer projectServer.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{
		{ID: 1, Name: "Global", APIURL: globalServer.URL, APIKey: "sk-global", Enabled: true, Level: 1},
		{ID: 2, Name: "Project", APIURL: projectServer.URL, APIKey: "sk-project", Enabled: true, Level: 10},
	}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	projectManager := NewProjectManagerService()
	if err := projectManager.SetProjectCodexProviderRouting(projectDir, 2, false); err != nil {
		t.Fatalf("SetProjectCodexProviderRouting 失败: %v", err)
	}

	router := gin.New()
	newTestRelayService(providerService).registerRoutes(router)

	body := []byte(`{"model":"gpt-5-codex","stream":false}`)
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-Root-Path", projectDir)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("关闭 auto 后应返回首选 provider 失败，got=%d body=%s", w.Code, w.Body.String())
	}
	if hits["project"] != 1 {
		t.Fatalf("关闭 auto 后应只调用项目首选 provider 一次，hits=%v", hits)
	}
	if hits["global"] != 0 {
		t.Fatalf("关闭 auto 后不应回落全局 provider，hits=%v", hits)
	}
}

func TestCodexProjectPreferredProviderFallsBackWhenModelUnsupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := setupRenameTestEnv(t)

	projectDir := filepath.Join(home, "workspace", "fallback")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}

	hits := map[string]int{}
	globalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["global"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"global"}`))
	}))
	defer globalServer.Close()
	projectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["project"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"project"}`))
	}))
	defer projectServer.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{
		{ID: 1, Name: "Global", APIURL: globalServer.URL, APIKey: "sk-global", Enabled: true, Level: 1},
		{ID: 2, Name: "Project", APIURL: projectServer.URL, APIKey: "sk-project", Enabled: true, Level: 10, SupportedModels: map[string]bool{"project-only": true}},
	}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	projectManager := NewProjectManagerService()
	if err := projectManager.SetProjectCodexProvider(projectDir, 2); err != nil {
		t.Fatalf("SetProjectCodexProvider 失败: %v", err)
	}

	router := gin.New()
	newTestRelayService(providerService).registerRoutes(router)

	body := []byte(`{"model":"gpt-5-codex","stream":false}`)
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-Root-Path", projectDir)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，got=%d body=%s", w.Code, w.Body.String())
	}
	if hits["project"] != 0 {
		t.Fatalf("不支持模型的项目首选 provider 不应被调用，hits=%v", hits)
	}
	if hits["global"] != 1 {
		t.Fatalf("应回落到全局 provider，hits=%v", hits)
	}
	if got := gjson.Get(w.Body.String(), "provider").String(); got != "global" {
		t.Fatalf("响应 provider 不对，want=global got=%q", got)
	}
}

func TestCodexProjectPreferredProviderFallsBackWhenBlacklisted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	home := setupRenameTestEnv(t)
	if err := ensureBlacklistTables(); err != nil {
		t.Fatalf("初始化黑名单表失败: %v", err)
	}

	projectDir := filepath.Join(home, "workspace", "blacklisted")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}

	hits := map[string]int{}
	globalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["global"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"global"}`))
	}))
	defer globalServer.Close()
	projectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits["project"]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"provider":"project"}`))
	}))
	defer projectServer.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{
		{ID: 1, Name: "Global", APIURL: globalServer.URL, APIKey: "sk-global", Enabled: true, Level: 1},
		{ID: 2, Name: "Project", APIURL: projectServer.URL, APIKey: "sk-project", Enabled: true, Level: 10},
	}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	db, err := xdb.DB("default")
	if err != nil {
		t.Fatalf("获取数据库失败: %v", err)
	}
	if _, err := db.Exec(`UPDATE app_settings SET value = 'true' WHERE key = 'enable_blacklist'`); err != nil {
		t.Fatalf("开启黑名单失败: %v", err)
	}
	_, err = db.Exec(
		`INSERT INTO provider_blacklist (platform, provider_name, failure_count, blacklisted_at, blacklisted_until) VALUES (?, ?, ?, ?, ?)`,
		"codex",
		"Project",
		3,
		time.Now().UTC(),
		time.Now().Add(time.Hour).UTC(),
	)
	if err != nil {
		t.Fatalf("写入黑名单 fixture 失败: %v", err)
	}

	projectManager := NewProjectManagerService()
	if err := projectManager.SetProjectCodexProvider(projectDir, 2); err != nil {
		t.Fatalf("SetProjectCodexProvider 失败: %v", err)
	}

	router := gin.New()
	newTestRelayService(providerService).registerRoutes(router)

	body := []byte(`{"model":"gpt-5-codex","stream":false}`)
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-Root-Path", projectDir)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望状态码 200，got=%d body=%s", w.Code, w.Body.String())
	}
	if hits["project"] != 0 {
		t.Fatalf("被黑名单拦截的项目首选 provider 不应被调用，hits=%v", hits)
	}
	if hits["global"] != 1 {
		t.Fatalf("应继续回落到全局 provider，hits=%v", hits)
	}
	if got := gjson.Get(w.Body.String(), "provider").String(); got != "global" {
		t.Fatalf("响应 provider 不对，want=global got=%q", got)
	}
}

// ==================== 性能测试 ====================

func BenchmarkIsModelSupported(b *testing.B) {
	provider := Provider{
		SupportedModels: map[string]bool{
			"claude-sonnet-4": true,
			"claude-opus-4":   true,
			"gpt-4":           true,
			"gpt-4-turbo":     true,
		},
		ModelMapping: map[string]string{
			"claude-*": "anthropic/claude-*",
			"gpt-*":    "openai/gpt-*",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = provider.IsModelSupported("claude-sonnet-4")
	}
}

func BenchmarkGetEffectiveModel(b *testing.B) {
	provider := Provider{
		ModelMapping: map[string]string{
			"claude-*": "anthropic/claude-*",
			"gpt-*":    "openai/gpt-*",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = provider.GetEffectiveModel("claude-sonnet-4")
	}
}

func BenchmarkReplaceModelInRequestBody(b *testing.B) {
	bodyBytes := []byte(`{
		"model": "claude-sonnet-4",
		"messages": [{"role": "user", "content": "Hello"}],
		"temperature": 0.7,
		"max_tokens": 1000
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ReplaceModelInRequestBody(bodyBytes, "anthropic/claude-sonnet-4")
	}
}
