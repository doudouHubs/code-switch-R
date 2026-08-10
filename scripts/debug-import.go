//go:build ignore

// +build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ccSwitchConfig struct {
	Claude ccProviderSection `json:"claude"`
	Codex  ccProviderSection `json:"codex"`
	MCP    ccMCPSection      `json:"mcp"`
}

type ccProviderSection struct {
	Providers map[string]ccProviderEntry `json:"providers"`
}

type ccProviderEntry struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	WebsiteURL string            `json:"websiteUrl"`
	Settings   ccProviderSetting `json:"settingsConfig"`
}

type ccProviderSetting struct {
	Env    map[string]string `json:"env"`
	Auth   map[string]string `json:"auth"`
	Config string            `json:"config"`
}

type ccMCPSection struct {
	Claude ccMCPPlatform `json:"claude"`
	Codex  ccMCPPlatform `json:"codex"`
}

type ccMCPPlatform struct {
	Servers map[string]json.RawMessage `json:"servers"`
}

func main() {
	fmt.Println("=== cc-switch 导入诊断 ===")
	fmt.Println()

	// 1. 检查 HOME 目录
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("os.UserHomeDir() 错误: %v\n", err)
		return
	}
	home = filepath.Clean(home)
	if !filepath.IsAbs(home) {
		fmt.Println("用户目录不是绝对路径，终止诊断以避免访问当前目录")
		return
	}
	fmt.Println("用户目录: [resolved]")
	fmt.Println()

	// 2. 检查配置文件路径
	configPath := filepath.Join(home, ".cc-switch", "config.json")
	fmt.Println("配置文件路径: ~/.cc-switch/config.json")

	// 3. 检查文件是否存在
	info, err := os.Stat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("文件不存在!\n")
		} else {
			fmt.Printf("stat 错误: %v\n", err)
		}
		return
	}
	fmt.Printf("文件存在, 大小: %d 字节\n", info.Size())
	fmt.Println()

	// 4. 读取文件
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Printf("读取文件错误: %v\n", err)
		return
	}
	fmt.Printf("读取成功, 内容长度: %d 字节\n", len(data))

	// 5. 解析 JSON
	var cfg ccSwitchConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("JSON 解析错误: %v\n", err)
		fmt.Println("原始配置内容已省略，避免在诊断日志中泄露凭据")
		return
	}
	fmt.Println("JSON 解析成功!")
	fmt.Println()

	// 6. 输出解析结果
	fmt.Println("=== Claude Providers ===")
	if len(cfg.Claude.Providers) == 0 {
		fmt.Println("(空)")
	}
	for key, entry := range cfg.Claude.Providers {
		fmt.Printf("  [%s]\n", key)
		fmt.Printf("    Name: %s\n", entry.Name)
		fmt.Printf("    ANTHROPIC_BASE_URL: %s\n", entry.Settings.Env["ANTHROPIC_BASE_URL"])
		fmt.Printf("    ANTHROPIC_AUTH_TOKEN: %s\n", maskKey(entry.Settings.Env["ANTHROPIC_AUTH_TOKEN"]))
	}
	fmt.Println()

	fmt.Println("=== Codex Providers ===")
	if len(cfg.Codex.Providers) == 0 {
		fmt.Println("(空)")
	}
	for key, entry := range cfg.Codex.Providers {
		fmt.Printf("  [%s]\n", key)
		fmt.Printf("    Name: %s\n", entry.Name)
		fmt.Printf("    OPENAI_API_KEY (env): %s\n", maskKey(entry.Settings.Env["OPENAI_API_KEY"]))
		fmt.Printf("    OPENAI_API_KEY (auth): %s\n", maskKey(entry.Settings.Auth["OPENAI_API_KEY"]))
		fmt.Printf("    Config (TOML): %d bytes [content redacted]\n", len(entry.Settings.Config))
	}
	fmt.Println()

	fmt.Println("=== MCP Claude Servers ===")
	fmt.Printf("  服务器数量: %d\n", len(cfg.MCP.Claude.Servers))
	for key := range cfg.MCP.Claude.Servers {
		fmt.Printf("    - %s\n", key)
	}
	fmt.Println()

	fmt.Println("=== MCP Codex Servers ===")
	fmt.Printf("  服务器数量: %d\n", len(cfg.MCP.Codex.Servers))
	for key := range cfg.MCP.Codex.Servers {
		fmt.Printf("    - %s\n", key)
	}
}

func maskKey(key string) string {
	if key == "" {
		return "(空)"
	}
	return "[redacted]"
}
