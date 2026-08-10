package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newTestRelayService 按当前构造函数签名装配 ProviderRelayService（geminiService 传 nil，addr 留空）。
func newTestRelayService(providerService *ProviderService) *ProviderRelayService {
	appSettings := NewAppSettingsService(NewAutoStartService())
	notificationService := NewNotificationService(appSettings)
	blacklistService := NewBlacklistService(NewSettingsService(), notificationService)
	return NewProviderRelayService(providerService, nil, blacklistService, notificationService, appSettings, "")
}

// TestModelsHandler 测试 /v1/models 端点处理器
func TestModelsHandler(t *testing.T) {
	// 设置测试环境
	gin.SetMode(gin.TestMode)

	// 隔离配置目录与 DB，避免污染真实 ~/.code-switch 配置
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	// 创建模拟的上游服务器
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求方法
		if r.Method != "GET" {
			t.Errorf("期望 GET 请求，收到 %s", r.Method)
		}

		// 验证路径
		if r.URL.Path != "/v1/models" {
			t.Errorf("期望路径 /v1/models，收到 %s", r.URL.Path)
		}

		// 验证 Authorization 头
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			t.Error("缺少 Authorization 头")
		}
		if authHeader != "Bearer test-api-key" {
			t.Errorf("Authorization 头不正确，期望 'Bearer test-api-key'，收到 '%s'", authHeader)
		}

		// 返回模拟的模型列表
		response := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"id":       "claude-sonnet-4",
					"object":   "model",
					"created":  1234567890,
					"owned_by": "anthropic",
				},
				{
					"id":       "claude-opus-4",
					"object":   "model",
					"created":  1234567890,
					"owned_by": "anthropic",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer upstreamServer.Close()

	// 创建测试用的 ProviderService
	providerService := NewProviderService()

	// 创建测试用的 provider（使用模拟服务器的 URL）
	testProvider := Provider{
		ID:      1,
		Name:    "TestProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "test-api-key",
		Enabled: true,
		Level:   1,
	}

	// 保存 provider 配置
	err := providerService.SaveProviders("claude", []Provider{testProvider})
	if err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	// 创建 ProviderRelayService
	relayService := newTestRelayService(providerService)

	// 创建测试路由
	router := gin.New()
	relayService.registerRoutes(router)

	// 创建测试请求
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	// 执行请求
	router.ServeHTTP(w, req)

	// 验证响应状态码
	if w.Code != http.StatusOK {
		t.Errorf("期望状态码 %d，收到 %d", http.StatusOK, w.Code)
		t.Logf("响应体: %s", w.Body.String())
	}

	// 验证响应内容类型
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("期望 Content-Type 为 'application/json'，收到 '%s'", contentType)
	}

	// 验证响应体可以解析为 JSON
	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("响应体不是有效的 JSON: %v", err)
		t.Logf("响应体: %s", w.Body.String())
	}

	// 验证响应包含 data 字段
	if _, ok := response["data"]; !ok {
		t.Error("响应缺少 'data' 字段")
	}
}

func TestOpenCoWorkModelsHandlerUsesCodexProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("期望 GET 请求，收到 %s", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("期望路径 /v1/models，收到 %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer codex-api-key" {
			t.Errorf("Authorization 头不正确，期望 Bearer codex-api-key，收到 %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.6-sol","object":"model"}]}`))
	}))
	defer upstreamServer.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{{
		ID:      1,
		Name:    "CodexProvider",
		APIURL:  upstreamServer.URL + "/v1",
		APIKey:  "codex-api-key",
		Enabled: true,
		Level:   1,
	}}); err != nil {
		t.Fatalf("保存 Codex provider 配置失败: %v", err)
	}

	relayService := newTestRelayService(providerService)
	router := gin.New()
	relayService.registerRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("OpenCoWork 模型发现应成功，got=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("响应体不是有效 JSON: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].ID != "gpt-5.6-sol" {
		t.Fatalf("模型列表错误: %s", w.Body.String())
	}
}

func TestOpenCoWorkModelsRouteDoesNotReplaceLegacyRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer claude-api-key":
			if r.URL.Path != "/v1/models" {
				t.Errorf("Claude 请求路径错误: %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"claude-legacy","object":"model"}]}`))
		case "Bearer codex-api-key":
			if r.URL.Path != "/v1/models" {
				t.Errorf("Codex 请求路径错误: %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.6-sol","object":"model"}]}`))
		default:
			t.Errorf("未知 Authorization 头: %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer upstreamServer.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("claude", []Provider{{
		ID:      1,
		Name:    "ClaudeProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "claude-api-key",
		Enabled: true,
		Level:   1,
	}}); err != nil {
		t.Fatalf("保存 Claude provider 配置失败: %v", err)
	}
	if err := providerService.SaveProviders("codex", []Provider{{
		ID:      2,
		Name:    "CodexProvider",
		APIURL:  upstreamServer.URL + "/v1",
		APIKey:  "codex-api-key",
		Enabled: true,
		Level:   1,
	}}); err != nil {
		t.Fatalf("保存 Codex provider 配置失败: %v", err)
	}

	relayService := newTestRelayService(providerService)
	router := gin.New()
	relayService.registerRoutes(router)

	for _, tt := range []struct {
		path        string
		expectedID  string
		description string
	}{
		{path: "/v1/models", expectedID: "claude-legacy", description: "legacy Claude route"},
		{path: "/models", expectedID: "gpt-5.6-sol", description: "OpenCoWork Codex route"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应成功，got=%d body=%s", tt.description, w.Code, w.Body.String())
		}
		var response struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s 响应体不是有效 JSON: %v", tt.description, err)
		}
		if len(response.Data) != 1 || response.Data[0].ID != tt.expectedID {
			t.Fatalf("%s 返回错误模型列表: %s", tt.description, w.Body.String())
		}
	}
}

func TestModelsURL(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{base: "https://api.example.com", want: "https://api.example.com/v1/models"},
		{base: "https://api.example.com/", want: "https://api.example.com/v1/models"},
		{base: "https://api.example.com/v1", want: "https://api.example.com/v1/models"},
		{base: "https://api.example.com/v1/", want: "https://api.example.com/v1/models"},
	}
	for _, tt := range tests {
		if got := modelsURL(tt.base); got != tt.want {
			t.Errorf("modelsURL(%q) = %q，期望 %q", tt.base, got, tt.want)
		}
	}
}

// TestCustomModelsHandler 测试自定义 CLI 工具的 /v1/models 端点
func TestCustomModelsHandler(t *testing.T) {
	// 设置测试环境
	gin.SetMode(gin.TestMode)

	// 隔离配置目录与 DB，避免污染真实 ~/.code-switch 配置
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	// 创建模拟的上游服务器
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求方法
		if r.Method != "GET" {
			t.Errorf("期望 GET 请求，收到 %s", r.Method)
		}

		// 验证路径
		if r.URL.Path != "/v1/models" {
			t.Errorf("期望路径 /v1/models，收到 %s", r.URL.Path)
		}

		// 验证 Authorization 头
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer custom-api-key" {
			t.Errorf("Authorization 头不正确，期望 'Bearer custom-api-key'，收到 '%s'", authHeader)
		}

		// 返回模拟的模型列表
		response := map[string]interface{}{
			"object": "list",
			"data": []map[string]interface{}{
				{
					"id":      "custom-model-1",
					"object":  "model",
					"created": 1234567890,
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}))
	defer upstreamServer.Close()

	// 创建测试用的 ProviderService
	providerService := NewProviderService()

	// 创建测试用的 provider（使用模拟服务器的 URL）
	testProvider := Provider{
		ID:      1,
		Name:    "CustomTestProvider",
		APIURL:  upstreamServer.URL,
		APIKey:  "custom-api-key",
		Enabled: true,
		Level:   1,
	}

	// 保存 provider 配置（使用自定义 CLI 工具的 kind）
	toolId := "mytool"
	kind := "custom:" + toolId
	err := providerService.SaveProviders(kind, []Provider{testProvider})
	if err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	// 创建 ProviderRelayService
	relayService := newTestRelayService(providerService)

	// 创建测试路由
	router := gin.New()
	relayService.registerRoutes(router)

	// 创建测试请求
	req := httptest.NewRequest("GET", "/custom/mytool/v1/models", nil)
	w := httptest.NewRecorder()

	// 执行请求
	router.ServeHTTP(w, req)

	// 验证响应状态码
	if w.Code != http.StatusOK {
		t.Errorf("期望状态码 %d，收到 %d", http.StatusOK, w.Code)
		t.Logf("响应体: %s", w.Body.String())
	}

	// 验证响应内容类型
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("期望 Content-Type 为 'application/json'，收到 '%s'", contentType)
	}

	// 验证响应体可以解析为 JSON
	var response map[string]interface{}
	err = json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("响应体不是有效的 JSON: %v", err)
		t.Logf("响应体: %s", w.Body.String())
	}

	// 验证响应包含 data 字段
	if _, ok := response["data"]; !ok {
		t.Error("响应缺少 'data' 字段")
	}
}

// TestModelsHandler_NoProviders 测试没有可用 provider 的情况
func TestModelsHandler_NoProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 隔离配置目录与 DB，避免污染真实 ~/.code-switch 配置
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	// 创建空的 ProviderService
	providerService := NewProviderService()

	// 创建 ProviderRelayService（没有配置任何 provider）
	relayService := newTestRelayService(providerService)

	// 创建测试路由
	router := gin.New()
	relayService.registerRoutes(router)

	// 创建测试请求
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	// 执行请求
	router.ServeHTTP(w, req)

	// 验证响应状态码应该是 404（没有可用的 provider）
	if w.Code != http.StatusNotFound {
		t.Errorf("期望状态码 %d，收到 %d", http.StatusNotFound, w.Code)
	}

	// 验证响应包含错误信息
	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Errorf("响应体不是有效的 JSON: %v", err)
	}

	if _, ok := response["error"]; !ok {
		t.Error("响应缺少 'error' 字段")
	}
}
