package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProjectManagerCodexHookGroupMarksEventsAsyncExceptSessionEnd(t *testing.T) {
	events := []string{
		"SessionStart",
		"UserPromptSubmit",
		"Stop",
		"PermissionRequest",
		"PreToolUse",
		"PostToolUse",
		"SubagentStart",
		"SubagentStop",
		"SessionEnd",
	}

	for _, eventName := range events {
		group := buildProjectManagerCodexHookGroup(eventName, `codeswitch --codex-hook-event`)
		hooks, ok := group["hooks"].([]any)
		if !ok || len(hooks) != 1 {
			t.Fatalf("%s hooks 结构异常: %#v", eventName, group["hooks"])
		}
		handler, ok := hooks[0].(map[string]any)
		if !ok {
			t.Fatalf("%s handler 结构异常: %#v", eventName, hooks[0])
		}
		async, ok := handler["async"].(bool)
		if eventName == "SessionEnd" {
			if ok {
				t.Fatalf("%s 不应标记为 async: %#v", eventName, handler)
			}
		} else if !ok || !async {
			t.Fatalf("%s 未标记为 async: %#v", eventName, handler)
		}
	}
}

func TestMergeProjectManagerCodexHooksPreservesCustomHandlers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	root := map[string]any{
		"description": "user hooks",
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "custom-tool"},
					map[string]any{"type": "command", "command": "old --codex-hook-event", "async": false},
				}},
			},
		},
	}
	writeHookTestJSON(t, path, root)

	if err := mergeProjectManagerCodexHooks(path, `codeswitch --codex-hook-event`, true); err != nil {
		t.Fatalf("mergeProjectManagerCodexHooks 失败: %v", err)
	}

	merged := readHookTestJSON(t, path)
	encoded, _ := json.Marshal(merged)
	text := string(encoded)
	if !strings.Contains(text, "custom-tool") {
		t.Fatalf("用户自定义 Hook 被覆盖: %s", text)
	}
	if strings.Contains(text, "old --codex-hook-event") {
		t.Fatalf("旧的 CodeSwitch Hook 未被替换: %s", text)
	}

	hooks := merged["hooks"].(map[string]any)
	for _, eventName := range []string{"SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest", "PreToolUse", "PostToolUse", "SessionEnd", "SubagentStart", "SubagentStop"} {
		if _, ok := hooks[eventName]; !ok {
			t.Fatalf("缺少事件 %s", eventName)
		}
	}
	assertCodeSwitchHookAsyncCompatibility(t, hooks)
}

func TestUninstallProjectManagerCodexHooksPreservesUserHooks(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	path := filepath.Join(codexHome, "hooks.json")
	root := map[string]any{
		"description": "user hooks",
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "custom-tool"},
				}},
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "codeswitch --codex-hook-event", "async": true},
				}},
			},
			"SessionStart": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "codeswitch --codex-hook-event", "async": true},
				}},
			},
		},
	}
	writeHookTestJSON(t, path, root)

	if _, err := uninstallProjectManagerCodexHooks(); err != nil {
		t.Fatalf("uninstallProjectManagerCodexHooks 失败: %v", err)
	}

	remaining := readHookTestJSON(t, path)
	encoded, _ := json.Marshal(remaining)
	text := string(encoded)
	if strings.Contains(text, projectManagerCodexHookCommandMarker) {
		t.Fatalf("CodeSwitch Hook 未清理: %s", text)
	}
	if !strings.Contains(text, "custom-tool") {
		t.Fatalf("用户自定义 Hook 被误删: %s", text)
	}
	hooks := remaining["hooks"].(map[string]any)
	if _, ok := hooks["SessionStart"]; ok {
		t.Fatalf("只包含 CodeSwitch Hook 的事件未被删除: %#v", hooks["SessionStart"])
	}
}

func TestUninstallProjectManagerCodexHooksFailsOnCorruptConfig(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	path := filepath.Join(codexHome, "hooks.json")
	original := []byte(`{"hooks":`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("写入损坏配置失败: %v", err)
	}

	if _, err := uninstallProjectManagerCodexHooks(); err == nil {
		t.Fatal("损坏的 hooks.json 应该返回错误")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取损坏配置失败: %v", err)
	}
	if string(current) != string(original) {
		t.Fatalf("损坏配置被覆盖: %q", current)
	}
}

func TestDefaultAppSettingsEnablesCodexHook(t *testing.T) {
	settings := (&AppSettingsService{}).defaultSettings()
	if !settings.EnableCodexHook {
		t.Fatal("Codex Hook 默认应开启")
	}
}

func TestSaveAppSettingsPersistsAndAppliesCodexHook(t *testing.T) {
	applier := &recordingCodexHookApplier{}
	service := &AppSettingsService{
		path:                    filepath.Join(t.TempDir(), "app.json"),
		codexHookRuntimeApplier: applier,
	}

	settings := service.defaultSettings()
	settings.EnableCodexHook = false
	if _, err := service.SaveAppSettings(settings); err != nil {
		t.Fatalf("SaveAppSettings 失败: %v", err)
	}
	if len(applier.enabled) != 1 || applier.enabled[0] {
		t.Fatalf("运行时 applier 调用异常: %#v", applier.enabled)
	}
	persisted, err := service.GetAppSettings()
	if err != nil {
		t.Fatalf("GetAppSettings 失败: %v", err)
	}
	if persisted.EnableCodexHook {
		t.Fatal("Codex Hook 关闭状态未持久化")
	}
}

type recordingCodexHookApplier struct {
	enabled []bool
}

func (a *recordingCodexHookApplier) ApplyCodexHookEnabled(enabled bool) error {
	a.enabled = append(a.enabled, enabled)
	return nil
}

func writeHookTestJSON(t *testing.T, path string, root map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatalf("序列化 Hook 配置失败: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("写入 Hook 配置失败: %v", err)
	}
}

func readHookTestJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 Hook 配置失败: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("解析 Hook 配置失败: %v", err)
	}
	return root
}

func assertCodeSwitchHookAsyncCompatibility(t *testing.T, hooks map[string]any) {
	t.Helper()
	for eventName, rawGroups := range hooks {
		groups, ok := rawGroups.([]any)
		if !ok {
			continue
		}
		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				continue
			}
			handlers, ok := group["hooks"].([]any)
			if !ok {
				continue
			}
			for _, rawHandler := range handlers {
				handler, ok := rawHandler.(map[string]any)
				command, _ := handler["command"].(string)
				if !ok || !strings.Contains(command, projectManagerCodexHookCommandMarker) {
					continue
				}
				async, ok := handler["async"].(bool)
				if eventName == "SessionEnd" {
					if ok {
						t.Fatalf("事件 %s 不应标记为 async: %#v", eventName, handler)
					}
				} else if !ok || !async {
					t.Fatalf("事件 %s 的 CodeSwitch Hook 未异步: %#v", eventName, handler)
				}
			}
		}
	}
}
