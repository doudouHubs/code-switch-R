package services

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCodexOldSessionRetriesWithoutProviderBoundState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupRenameTestEnv(t)

	var (
		mu       sync.Mutex
		requests [][]byte
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取上游请求失败: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		mu.Lock()
		requests = append(requests, append([]byte(nil), body...))
		mu.Unlock()

		if codexTestRequestHasProviderBoundState(body) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"foreign response state"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_portable","output":[]}`))
	}))
	defer upstream.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{{
		ID:      1,
		Name:    "Default",
		APIURL:  upstream.URL,
		APIKey:  "sk-default",
		Enabled: true,
		Level:   1,
	}}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	router := gin.New()
	newTestRelayService(providerService).registerRoutes(router)

	body := codexOldSessionTestBody()
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("旧会话状态不兼容时应通过无状态重试恢复，got=%d body=%s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("兼容恢复必须只增加一次请求，got=%d", len(requests))
	}
	if !codexTestRequestHasProviderBoundState(requests[0]) {
		t.Fatal("第一次请求必须保留原始会话状态")
	}
	if codexTestRequestHasProviderBoundState(requests[1]) {
		t.Fatalf("兼容重试仍携带供应商私有状态: %s", requests[1])
	}
	if !bytes.Contains(requests[1], []byte(`"call_id":"call_keep"`)) {
		t.Fatalf("兼容重试误删了工具调用关联 ID: %s", requests[1])
	}
	if bytes.Contains(requests[1], []byte(`"namespace"`)) {
		t.Fatalf("兼容重试仍携带旧供应商的 custom tool namespace: %s", requests[1])
	}
}

func TestCodexOldSessionFallsBackToDefaultWithPortableState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupRenameTestEnv(t)

	var (
		mu                sync.Mutex
		preferredRequests [][]byte
		defaultRequests   [][]byte
	)
	preferredUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		preferredRequests = append(preferredRequests, append([]byte(nil), body...))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"preferred unavailable"}`))
	}))
	defer preferredUpstream.Close()

	defaultUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defaultRequests = append(defaultRequests, append([]byte(nil), body...))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if codexTestRequestHasProviderBoundState(body) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"default received foreign state"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_default","output":[]}`))
	}))
	defer defaultUpstream.Close()

	providerService := NewProviderService()
	if err := providerService.SaveProviders("codex", []Provider{
		{ID: 1, Name: "Default", APIURL: defaultUpstream.URL, APIKey: "sk-default", Enabled: true, Level: 1},
		{ID: 2, Name: "Preferred", APIURL: preferredUpstream.URL, APIKey: "sk-preferred", Enabled: false, Level: 10},
	}); err != nil {
		t.Fatalf("保存 provider 配置失败: %v", err)
	}

	projectDir := filepath.Join(t.TempDir(), "portable-fallback-project")
	if err := NewProjectManagerService().SetProjectCodexProviderRouting(projectDir, 2, true); err != nil {
		t.Fatalf("保存项目首选 provider 失败: %v", err)
	}

	router := gin.New()
	newTestRelayService(providerService).registerRoutes(router)
	req := httptest.NewRequest(http.MethodPost, "/responses", bytes.NewReader(codexOldSessionTestBody()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-Root-Path", projectDir)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("首选持续失败后默认路由应使用可移植请求接管，got=%d body=%s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(preferredRequests) != 2 {
		t.Fatalf("首选供应商应收到原始请求和一次兼容重试，got=%d", len(preferredRequests))
	}
	if len(defaultRequests) != 1 {
		t.Fatalf("默认供应商只应收到一次请求，got=%d", len(defaultRequests))
	}
	if codexTestRequestHasProviderBoundState(defaultRequests[0]) {
		t.Fatalf("默认供应商收到的回落请求仍携带首选供应商状态: %s", defaultRequests[0])
	}
}

func TestMakeCodexRequestProviderPortableRemovesOnlyProviderState(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"previous_response_id":"resp_foreign",
		"conversation":{"id":"conv_foreign"},
		"input":[
			{"type":"item_reference","id":"item_foreign"},
			{"type":"reasoning","id":"rs_foreign","encrypted_content":"ciphertext"},
			{"type":"message","id":"msg_foreign","role":"user","content":[{"type":"input_text","text":"continue"}]},
			{"type":"function_call","id":"fc_foreign","call_id":"call_keep","name":"shell","arguments":"{}"},
			{"type":"function_call_output","id":"fco_foreign","call_id":"call_keep","output":"ok"},
			{"type":"custom_tool_call","call_id":"custom_keep","name":"exec","namespace":"functions","status":"completed","input":"{}"},
			{"type":"custom_tool_call_output","call_id":"custom_keep","output":"ok"}
		]
	}`)

	got, changed, err := makeCodexRequestProviderPortable(body)
	if err != nil {
		t.Fatalf("转换旧会话请求失败: %v", err)
	}
	if !changed {
		t.Fatal("包含供应商状态的请求必须被转换")
	}
	if codexTestRequestHasProviderBoundState(got) {
		t.Fatalf("转换后仍携带供应商状态: %s", got)
	}
	if !bytes.Contains(got, []byte(`"call_id":"call_keep"`)) || !bytes.Contains(got, []byte(`"text":"continue"`)) {
		t.Fatalf("转换误删了用户内容或工具调用关联: %s", got)
	}
	if !bytes.Contains(got, []byte(`"call_id":"custom_keep"`)) || bytes.Contains(got, []byte(`"namespace"`)) {
		t.Fatalf("转换必须保留 custom tool 配对并移除供应商 namespace: %s", got)
	}
}

func TestMakeCodexRequestProviderPortableLeavesFreshRequestUntouched(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)

	got, changed, err := makeCodexRequestProviderPortable(body)
	if err != nil {
		t.Fatalf("检查新会话请求失败: %v", err)
	}
	if changed {
		t.Fatalf("新会话请求不应触发状态转换: %s", got)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("新会话请求必须原样返回，want=%s got=%s", body, got)
	}
}

func TestCodexPortabilityRetryOnlyHandlesUpstream5xxOnce(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if shouldRetryCodexRequestWithoutProviderState(c, "codex", newProviderRelayUpstreamError(http.StatusBadRequest, "bad request")) {
		t.Fatal("上游 4xx 是客户端契约错误，不应触发去状态重试")
	}
	if !shouldRetryCodexRequestWithoutProviderState(c, "codex", newProviderRelayUpstreamError(http.StatusServiceUnavailable, "unavailable")) {
		t.Fatal("上游 5xx 应允许一次去状态重试")
	}

	c.Set(codexPortabilityRetryContextKey, true)
	if shouldRetryCodexRequestWithoutProviderState(c, "codex", newProviderRelayUpstreamError(http.StatusServiceUnavailable, "unavailable")) {
		t.Fatal("同一客户端请求不能重复触发去状态重试")
	}
}

func codexOldSessionTestBody() []byte {
	return []byte(`{
		"model":"gpt-5.6-sol",
		"stream":false,
		"previous_response_id":"resp_foreign",
		"input":[
			{"type":"reasoning","id":"rs_foreign","encrypted_content":"ciphertext","summary":[]},
			{"type":"message","id":"msg_foreign","role":"user","content":[{"type":"input_text","text":"continue"}]},
			{"type":"function_call","id":"fc_foreign","call_id":"call_keep","name":"shell","arguments":"{}"},
			{"type":"function_call_output","id":"fco_foreign","call_id":"call_keep","output":"ok"},
			{"type":"custom_tool_call","call_id":"custom_keep","name":"exec","namespace":"functions","status":"completed","input":"{}"},
			{"type":"custom_tool_call_output","call_id":"custom_keep","output":"ok"}
		]
	}`)
}

func codexTestRequestHasProviderBoundState(body []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	if _, ok := payload["previous_response_id"]; ok {
		return true
	}
	if _, ok := payload["conversation"]; ok {
		return true
	}

	input, _ := payload["input"].([]any)
	for _, rawItem := range input {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		if _, ok := item["id"]; ok {
			return true
		}
		if item["type"] == "reasoning" || item["type"] == "item_reference" {
			return true
		}
		if item["type"] == "custom_tool_call" {
			if _, ok := item["namespace"]; ok {
				return true
			}
		}
	}
	return false
}
