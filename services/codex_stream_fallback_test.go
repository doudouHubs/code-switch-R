package services

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCodexIncompletePreferredStreamFallsBackBeforeWritingClientResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupRenameTestEnv(t)

	var preferredHits atomic.Int32
	preferredUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		preferredHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_preferred\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial-from-preferred\"}\n\n"))
		// 模拟截图中的故障：HTTP 200 且已经输出部分内容，但连接在 response.completed 前关闭。
	}))
	defer preferredUpstream.Close()

	var defaultHits atomic.Int32
	defaultUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defaultHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_default\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"complete-from-default\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_default\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer defaultUpstream.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{
		{ID: 1, Name: "Default", APIURL: defaultUpstream.URL, APIKey: "sk-default", Enabled: true, Level: 1},
		{ID: 2, Name: "Preferred", APIURL: preferredUpstream.URL, APIKey: "sk-preferred", Enabled: false, Level: 10},
	}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	projectDir := filepath.Join(t.TempDir(), "stream-fallback-project")
	if err := NewProjectManagerService().SetProjectCodexProviderRouting(projectDir, 2, true); err != nil {
		t.Fatalf("保存项目首选 provider 失败: %v", err)
	}

	router := gin.New()
	newTestRelayService(providerService).registerRoutes(router)
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-Root-Path", projectDir)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("首选流提前断开后默认供应商应接管，got=%d body=%s", w.Code, w.Body.String())
	}
	if preferredHits.Load() != 1 || defaultHits.Load() != 1 {
		t.Fatalf("应各尝试首选和默认供应商一次，preferred=%d default=%d", preferredHits.Load(), defaultHits.Load())
	}
	if strings.Contains(w.Body.String(), "partial-from-preferred") {
		t.Fatalf("首选供应商的半截流不应写入客户端: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "complete-from-default") || !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("客户端未收到默认供应商的完整流: %s", w.Body.String())
	}
}
