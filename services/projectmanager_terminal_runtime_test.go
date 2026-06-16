package services

import (
	"path/filepath"
	"testing"
)

func TestProjectManagerSessionRuntimeCarriesPrivateClosureMetadata(t *testing.T) {
	runtime := projectManagerSessionRuntime{
		SessionID:      "session-001",
		ShellPID:       123,
		ShellStartedAt: "2026-06-16T07:08:52.8213389Z",
		LaunchSource:   projectManagerRuntimeLaunchSource,
		WindowID:       "codeswitch-project-session-001",
		TabTitle:       "[PM]session-001|Alpha",
		TabIndex:       0,
	}

	if runtime.SessionID != "session-001" {
		t.Fatalf("session id 丢了: %+v", runtime)
	}
	if runtime.LaunchSource != projectManagerRuntimeLaunchSource {
		t.Fatalf("launch source 不对: %+v", runtime)
	}
	if runtime.WindowID != "codeswitch-project-session-001" {
		t.Fatalf("window id 不对: %+v", runtime)
	}
	if runtime.TabTitle != "[PM]session-001|Alpha" {
		t.Fatalf("tab title 不对: %+v", runtime)
	}
	if runtime.TabIndex != 0 {
		t.Fatalf("tab index 不对: %+v", runtime)
	}
}

func TestLoadProjectManagerSessionRuntimeIfExistsReturnsFalseWhenMissing(t *testing.T) {
	setupProjectManagerTestHome(t)

	runtime, exists, err := loadProjectManagerSessionRuntimeIfExists("missing-session")
	if err != nil {
		t.Fatalf("读取不存在 runtime 不该报错: %v", err)
	}
	if exists {
		t.Fatalf("不存在 runtime 不该标成 exists=true: %+v", runtime)
	}
}

func TestLoadProjectManagerSessionRuntimeIfExistsReturnsRuntimeWhenPresent(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	sessionID := "session-001"
	runtimePath := filepath.Join(home, ".code-switch", "project-manager-runtime", "session-001.json")
	content := `{"session_id":"session-001","shell_pid":321,"launch_source":"project-manager","window_id":"codeswitch-project-001","tab_title":"[PM]session-001|Alpha","tab_index":2}`

	if err := AtomicWriteText(runtimePath, content); err != nil {
		t.Fatalf("写入 runtime fixture 失败: %v", err)
	}

	runtime, exists, err := loadProjectManagerSessionRuntimeIfExists(sessionID)
	if err != nil {
		t.Fatalf("读取已存在 runtime 失败: %v", err)
	}
	if !exists {
		t.Fatalf("已存在 runtime 却返回 exists=false")
	}
	if runtime.SessionID != sessionID || runtime.ShellPID != 321 || runtime.TabIndex != 2 {
		t.Fatalf("读取的 runtime 不对: %+v", runtime)
	}
}

func TestLoadProjectManagerSessionRuntimeIfExistsRejectsBlankSessionID(t *testing.T) {
	_, _, err := loadProjectManagerSessionRuntimeIfExists("   ")
	if err == nil {
		t.Fatalf("空 session id 应该直接报参数错误，got=%v", err)
	}
}
