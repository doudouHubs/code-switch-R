//go:build ignore

// +build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 对比测试多个供应商，验证请求逻辑是否正确
// Author: Half open flowers

type Provider struct {
	Name    string `json:"name"`
	APIURL  string `json:"apiUrl"`
	APIKey  string `json:"apiKey"`
	Enabled bool   `json:"enabled"`
}

type Config struct {
	Providers []Provider `json:"providers"`
}

func main() {
	// 诊断脚本读取本机配置前先校验家目录，避免异常环境把路径退化到当前目录。
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取用户目录失败: %v\n", err)
		return
	}
	home = filepath.Clean(home)
	if !filepath.IsAbs(home) {
		fmt.Fprintln(os.Stderr, "用户目录必须是绝对路径")
		return
	}
	configPath := filepath.Join(home, ".code-switch", "claude-code.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Printf("读取配置失败: %v\n", err)
		return
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		fmt.Printf("解析配置失败: %v\n", err)
		return
	}

	fmt.Println("================================================================")
	fmt.Println("多供应商对比测试")
	fmt.Println("================================================================")
	fmt.Println("使用相同的请求格式测试所有启用的供应商")
	fmt.Println("如果其他供应商成功但 88code 失败，说明 88code 的 Key 有问题")
	fmt.Println()

	client := &http.Client{Timeout: 30 * time.Second}
	body := `{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

	for _, p := range config.Providers {
		if !p.Enabled || p.APIKey == "" || p.APIURL == "" {
			continue
		}

		url := strings.TrimSuffix(p.APIURL, "/") + "/v1/messages"
		fmt.Printf("--- %s ---\n", p.Name)
		fmt.Printf("URL: %s\n", url)
		fmt.Println("Key: [redacted]")

		req, _ := http.NewRequest("POST", url, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", p.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start)

		if err != nil {
			fmt.Printf("❌ 网络错误: %v\n\n", err)
			continue
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		respStr := string(respBody)
		respStr = strings.ReplaceAll(respStr, p.APIKey, "[redacted]")

		// 判断是否成功
		isSuccess := resp.StatusCode == 200 &&
			strings.Contains(respStr, `"content"`) &&
			!strings.Contains(respStr, `"error"`)

		if isSuccess {
			fmt.Printf("✅ HTTP %d (%v) - 成功!\n", resp.StatusCode, latency.Round(time.Millisecond))
		} else {
			fmt.Printf("❌ HTTP %d (%v)\n", resp.StatusCode, latency.Round(time.Millisecond))
		}

		if len(respStr) > 150 {
			respStr = respStr[:150] + "..."
		}
		fmt.Printf("响应: %s\n\n", respStr)
	}

	fmt.Println("================================================================")
	fmt.Println("结论")
	fmt.Println("================================================================")
	fmt.Println("如果其他供应商成功但 88code 失败:")
	fmt.Println("  → 88code 的 API Key 无效/过期，需要联系 88code 服务商")
	fmt.Println()
	fmt.Println("如果所有供应商都失败:")
	fmt.Println("  → 请求格式或网络有问题")
}
