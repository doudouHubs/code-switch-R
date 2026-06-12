package services

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestCodexSettingsService_GetModelInstructionsFile_MissingConfigReturnsEmpty(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	service := NewCodexSettingsService("127.0.0.1:18100")
	got, err := service.GetModelInstructionsFile()
	if err != nil {
		t.Fatalf("GetModelInstructionsFile 失败: %v", err)
	}
	if got != "" {
		t.Fatalf("model_instructions_file = %q，期望空字符串", got)
	}
}

func TestCodexSettingsService_SetModelInstructionsFile_WritesNormalizedAbsolutePath(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	service := NewCodexSettingsService("127.0.0.1:18100")
	if err := service.SetModelInstructionsFile(`instructions\custom.md`); err != nil {
		t.Fatalf("SetModelInstructionsFile 失败: %v", err)
	}

	configPath := filepath.Join(tmpHome, ".codex", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取 config.toml 失败: %v", err)
	}

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("解析 config.toml 失败: %v", err)
	}

	value, ok := raw["model_instructions_file"].(string)
	if !ok {
		t.Fatalf("model_instructions_file 类型错误: %#v", raw["model_instructions_file"])
	}
	if !filepath.IsAbs(value) {
		t.Fatalf("model_instructions_file = %q，期望绝对路径", value)
	}
	if !hasPathSuffix(value, filepath.Join("instructions", "custom.md")) {
		t.Fatalf("model_instructions_file = %q，期望以 instructions\\custom.md 结尾", value)
	}

	got, err := service.GetModelInstructionsFile()
	if err != nil {
		t.Fatalf("GetModelInstructionsFile 失败: %v", err)
	}
	if got != value {
		t.Fatalf("GetModelInstructionsFile = %q，期望 %q", got, value)
	}
}

func TestCodexSettingsService_SetModelInstructionsFile_EmptyRemovesField(t *testing.T) {
	tmpHome := setupRenameTestEnv(t)
	t.Setenv("USERPROFILE", tmpHome)

	configDir := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("创建 .codex 目录失败: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := []byte("model = \"gpt-5-codex\"\nmodel_instructions_file = \"C:/temp/instructions.md\"\n")
	if err := os.WriteFile(configPath, initial, 0o644); err != nil {
		t.Fatalf("写入初始 config.toml 失败: %v", err)
	}

	service := NewCodexSettingsService("127.0.0.1:18100")
	if err := service.SetModelInstructionsFile(""); err != nil {
		t.Fatalf("SetModelInstructionsFile 清空失败: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取 config.toml 失败: %v", err)
	}

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("解析 config.toml 失败: %v", err)
	}
	if _, exists := raw["model_instructions_file"]; exists {
		t.Fatalf("model_instructions_file 仍存在: %#v", raw["model_instructions_file"])
	}
	if raw["model"] != "gpt-5-codex" {
		t.Fatalf("其他字段被意外改坏，model = %#v", raw["model"])
	}
}

func hasPathSuffix(fullPath string, suffix string) bool {
	cleanFull := filepath.Clean(fullPath)
	cleanSuffix := filepath.Clean(suffix)
	return len(cleanFull) >= len(cleanSuffix) && cleanFull[len(cleanFull)-len(cleanSuffix):] == cleanSuffix
}
