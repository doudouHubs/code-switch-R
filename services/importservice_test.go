package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigImportContractExcludesRetiredMCPFields 锁定对外导入合同只承载供应商状态,
// 防止后续生成绑定时把已经退役的 MCP 模块字段重新带回前端。
func TestConfigImportContractExcludesRetiredMCPFields(t *testing.T) {
	statusJSON, err := json.Marshal(ConfigImportStatus{
		ConfigExists:         true,
		PendingProviders:     true,
		PendingProviderCount: 2,
	})
	if err != nil {
		t.Fatalf("序列化导入状态失败: %v", err)
	}
	resultJSON, err := json.Marshal(ConfigImportResult{ImportedProviders: 2})
	if err != nil {
		t.Fatalf("序列化导入结果失败: %v", err)
	}

	for name, payload := range map[string][]byte{"status": statusJSON, "result": resultJSON} {
		if strings.Contains(strings.ToLower(string(payload)), "mcp") {
			t.Fatalf("%s 合同仍包含已退役 MCP 字段: %s", name, payload)
		}
	}
}

// TestLoadLegacyConfigIgnoresRetiredMCPSection 验证旧配置中的 MCP 段会被安全忽略,
// 同时供应商数据仍可读取；模块退役不能破坏既有 cc-switch 配置的迁移能力。
func TestLoadLegacyConfigIgnoresRetiredMCPSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	payload := `{
		"claude": {
			"providers": {
				"legacy-provider": {
					"id": "legacy-provider",
					"name": "Legacy Provider",
					"settingsConfig": {
						"env": {"ANTHROPIC_BASE_URL": "https://example.com"}
					}
				}
			}
		},
		"mcp": {
			"claude": {
				"servers": {"legacy-server": {"name": "Legacy MCP"}}
			}
		}
	}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	cfg, exists, err := loadCcSwitchConfigFromPath(path)
	if err != nil {
		t.Fatalf("读取旧配置失败: %v", err)
	}
	if !exists || cfg == nil {
		t.Fatal("旧配置应被识别并读取")
	}
	provider, ok := cfg.Claude.Providers["legacy-provider"]
	if !ok {
		t.Fatal("退役 MCP 模块后仍应保留供应商配置")
	}
	if provider.Name != "Legacy Provider" {
		t.Fatalf("供应商名称读取错误: %q", provider.Name)
	}
}
