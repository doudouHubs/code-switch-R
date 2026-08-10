//go:build ignore

// 临时修复脚本：移除空的 supportedModels 和 modelMapping 字段
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Provider struct {
	ID              int               `json:"id"`
	Name            string            `json:"name"`
	APIURL          string            `json:"apiUrl"`
	APIKey          string            `json:"apiKey"`
	Enabled         bool              `json:"enabled"`
	Level           int               `json:"level,omitempty"`
	SupportedModels map[string]bool   `json:"supportedModels,omitempty"`
	ModelMapping    map[string]string `json:"modelMapping,omitempty"`
}

type Config struct {
	Providers []Provider `json:"providers"`
}

func main() {
	// 脚本会备份并覆盖用户配置，家目录无效时必须在写入前失败。
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("获取用户目录失败: %v\n", err)
		return
	}
	if !filepath.IsAbs(home) {
		fmt.Printf("获取用户目录失败: 用户目录必须是绝对路径，实际为 %q\n", home)
		return
	}
	files := []string{
		filepath.Join(home, ".code-switch", "claude-code.json"),
		filepath.Join(home, ".code-switch", "codex.json"),
	}

	for _, filePath := range files {
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			continue
		}

		fmt.Printf("处理文件: %s\n", filePath)

		// 读取文件
		data, err := os.ReadFile(filePath)
		if err != nil {
			fmt.Printf("  错误: 读取失败: %v\n", err)
			continue
		}

		// 解析 JSON
		var config Config
		if err := json.Unmarshal(data, &config); err != nil {
			fmt.Printf("  错误: 解析失败: %v\n", err)
			continue
		}

		// 清理空字段
		fixed := false
		for i := range config.Providers {
			p := &config.Providers[i]
			if p.SupportedModels != nil && len(p.SupportedModels) == 0 {
				p.SupportedModels = nil
				fixed = true
				fmt.Printf("  ✓ 清理 %s 的空 supportedModels\n", p.Name)
			}
			if p.ModelMapping != nil && len(p.ModelMapping) == 0 {
				p.ModelMapping = nil
				fixed = true
				fmt.Printf("  ✓ 清理 %s 的空 modelMapping\n", p.Name)
			}
		}

		if !fixed {
			fmt.Println("  ✓ 无需修复")
			continue
		}

		// 配置中可能含 API Key，备份必须与原配置一样仅当前用户可读；备份失败时不覆盖原文件。
		backupPath := filePath + ".bak"
		if err := os.WriteFile(backupPath, data, 0600); err != nil {
			fmt.Printf("  错误: 备份失败: %v\n", err)
			continue
		}
		fmt.Printf("  ✓ 已备份到: %s\n", backupPath)

		// 写入修复后的文件
		newData, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			fmt.Printf("  错误: 序列化失败: %v\n", err)
			continue
		}
		if err := os.WriteFile(filePath, newData, 0600); err != nil {
			fmt.Printf("  错误: 写入失败: %v\n", err)
			continue
		}

		fmt.Println("  ✓ 修复成功")
	}

	fmt.Println("\n完成！请重启应用。")
}
