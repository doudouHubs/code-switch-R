package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectProjectManagerCodexProjectPathPrefersHeaderToBody(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "header-project")
	bodyProject := filepath.Join(t.TempDir(), "body-project")
	body, err := json.Marshal(map[string]string{"cwd": bodyProject})
	if err != nil {
		t.Fatalf("构造请求体失败: %v", err)
	}

	got := detectProjectManagerCodexProjectPath(map[string]string{
		"X-Project-Root-Path": projectDir,
	}, body)
	if !projectManagerProjectPathsEqual(got, projectDir) {
		t.Fatalf("权威项目 header 必须压过正文 cwd，want=%q got=%q", projectDir, got)
	}
}

func TestDetectProjectManagerCodexProjectPathPrefersMetadataToBody(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "metadata-project")
	bodyProject := filepath.Join(t.TempDir(), "body-project")
	metadata, err := json.Marshal(codexTurnMetadata{
		SessionID: "metadata-session",
		Workspaces: map[string]json.RawMessage{
			projectDir: json.RawMessage(`{}`),
		},
	})
	if err != nil {
		t.Fatalf("构造 turn metadata 失败: %v", err)
	}
	body, err := json.Marshal(map[string]string{"project": bodyProject})
	if err != nil {
		t.Fatalf("构造请求体失败: %v", err)
	}

	got := detectProjectManagerCodexProjectPath(map[string]string{
		"X-Codex-Turn-Metadata": string(metadata),
	}, body)
	if !projectManagerProjectPathsEqual(got, projectDir) {
		t.Fatalf("Codex turn metadata 必须压过正文 project，want=%q got=%q", projectDir, got)
	}
}

func TestDetectProjectManagerCodexProjectPathIgnoresUntrustedBody(t *testing.T) {
	bodyProject := filepath.Join(t.TempDir(), "body-only-project")
	body, err := json.Marshal(map[string]string{"cwd": bodyProject})
	if err != nil {
		t.Fatalf("构造请求体失败: %v", err)
	}

	if got := detectProjectManagerCodexProjectPath(nil, body); got != "" {
		t.Fatalf("仅正文提供项目时必须走默认路由，got=%q", got)
	}
}

func TestDetectProjectManagerCodexProjectPathFallsBackToSessionFile(t *testing.T) {
	home := setupRenameTestEnv(t)
	projectDir := filepath.Join(home, "workspace", "session-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}

	sessionID := "session-routing-fallback"
	sessionDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "15")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("创建 Codex session 目录失败: %v", err)
	}
	sessionMeta, err := json.Marshal(map[string]any{
		"timestamp": "2026-07-15T08:00:00Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id":        sessionID,
			"cwd":       projectDir,
			"timestamp": "2026-07-15T08:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("构造 session_meta 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "rollout.jsonl"), append(sessionMeta, '\n'), 0o644); err != nil {
		t.Fatalf("写入 Codex session fixture 失败: %v", err)
	}

	got := detectProjectManagerCodexProjectPath(map[string]string{
		"X-Session-ID": sessionID,
	}, []byte(`{"cwd":"C:\\untrusted-body"}`))
	if !projectManagerProjectPathsEqual(got, projectDir) {
		t.Fatalf("缺少项目 header 时应通过 session 反查项目，want=%q got=%q", projectDir, got)
	}
}

func TestProjectManagerCodexProviderLookupIgnoresWindowsPathCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 路径大小写语义测试")
	}

	projectDir := filepath.Join(t.TempDir(), "MixedCaseProject")
	store := projectManagerStore{
		Projects: map[string]projectManagerProjectMeta{
			projectDir: {CodexProviderID: 42},
		},
	}

	if got := projectManagerCodexProviderIDFromStore(store, strings.ToLower(projectDir)); got != 42 {
		t.Fatalf("Windows 路径大小写变化后绑定应保持命中，want=42 got=%d", got)
	}
}

func BenchmarkProjectManagerCodexProviderRoutingForRequest(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("USERPROFILE", home)

	projectDir := filepath.Join(home, "workspace", "routing-benchmark")
	storeService := newProjectManagerStoreService()
	if err := storeService.saveProjectCodexProviderRouting(projectDir, 42, true); err != nil {
		b.Fatalf("保存项目 provider fixture 失败: %v", err)
	}
	headers := map[string]string{"X-Project-Root-Path": projectDir}
	body := []byte(`{"model":"gpt-5-codex"}`)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(parallel *testing.PB) {
		for parallel.Next() {
			routing := projectManagerCodexProviderRoutingForRequest(headers, body)
			if routing.ProviderID != 42 || !routing.AutoFallback {
				b.Fatalf("项目路由结果错误: %#v", routing)
			}
		}
	})
}
