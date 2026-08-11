package services

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// overrideUpdateSourceForTest 将真实更新地址替换为本地测试服务，避免回归测试依赖外部网络。
func overrideUpdateSourceForTest(t *testing.T, server *httptest.Server) {
	t.Helper()

	previousLatestURL := latestJSONURL
	previousAPIURL := githubAPIURL
	previousHTTPClient := updateHTTPClient

	latestJSONURL = server.URL + "/latest.json"
	githubAPIURL = server.URL + "/releases/latest"
	updateHTTPClient = server.Client()

	t.Cleanup(func() {
		latestJSONURL = previousLatestURL
		githubAPIURL = previousAPIURL
		updateHTTPClient = previousHTTPClient
	})
}

func TestCheckUpdateTreatsMissingSourcesAsNoUpdate(t *testing.T) {
	latestCalled := false
	apiCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, _ *http.Request) {
		latestCalled = true
		http.NotFound(w, nil)
	})
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		apiCalled = true
		http.NotFound(w, nil)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	overrideUpdateSourceForTest(t, server)

	service := &UpdateService{
		state:          StateIdle,
		currentVersion: "v1.0.0",
	}

	info, err := service.CheckUpdate()
	if err != nil {
		t.Fatalf("双 404 应按无更新处理，实际返回错误: %v", err)
	}
	if info != nil {
		t.Fatalf("双 404 应返回 nil 更新信息，实际为 %+v", info)
	}
	if service.state != StateIdle {
		t.Fatalf("双 404 后状态应为 idle，实际为 %s", service.state)
	}
	if !latestCalled || !apiCalled {
		t.Fatalf("应依次请求 latest.json 和 GitHub API，latest=%t api=%t", latestCalled, apiCalled)
	}
}

func TestDoCheckUpdateFallsBackToGitHubAPI(t *testing.T) {
	service := &UpdateService{
		currentVersion: "v1.0.0",
		cachedPolicy:   string(PolicyPortable),
	}
	assetName := service.getAssetName("v1.0.1")
	if assetName == "" {
		t.Skip("当前平台没有 GitHub API 资产映射")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		payload := map[string]any{
			"tag_name":     "v1.0.1",
			"published_at": "2026-08-11T00:00:00Z",
			"body":         "fallback release",
			"assets": []map[string]any{
				{
					"name":                 assetName,
					"browser_download_url": "https://github.com/doudouHubs/code-switch-cli/releases/download/v1.0.1/" + assetName,
					"size":                 123,
				},
			},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Fatalf("写入 GitHub API 测试响应失败: %v", err)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	overrideUpdateSourceForTest(t, server)

	info, err := service.doCheckUpdate()
	if err != nil {
		t.Fatalf("latest.json 失败后应回退 GitHub API，实际错误: %v", err)
	}
	if info == nil || info.Version != "v1.0.1" {
		t.Fatalf("GitHub API 回退结果不正确: %+v", info)
	}
	if !strings.HasSuffix(info.DownloadURL, "/"+assetName) || info.Size != 123 {
		t.Fatalf("GitHub API 资产信息未正确解析: %+v", info)
	}
}

func TestDoCheckUpdatePreservesRealSourceErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	overrideUpdateSourceForTest(t, server)

	service := &UpdateService{currentVersion: "v1.0.0"}
	_, err := service.doCheckUpdate()
	if err == nil {
		t.Fatal("GitHub API 返回 500 时应保留错误")
	}
	if errors.Is(err, errUpdateSourceNotFound) {
		t.Fatalf("500 不应被归类为更新源不存在: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("错误应包含真实 HTTP 状态码，实际为: %v", err)
	}
}
