//go:build ignore

// +build ignore

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// 测试 88code 的各种请求变体
// Author: Half open flowers

func main() {
	// 诊断脚本不读取用户配置文件，避免把本机路径和密钥带入开源复现日志。
	apiKey := strings.TrimSpace(os.Getenv("CODESWITCH_88CODE_API_KEY"))
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "CODESWITCH_88CODE_API_KEY is required")
		return
	}
	apiURL := strings.TrimSpace(os.Getenv("CODESWITCH_88CODE_API_URL"))
	if apiURL == "" {
		apiURL = "https://m.88code.org"
	}

	fmt.Println("================================================================")
	fmt.Println("88code 请求变体测试")
	fmt.Println("================================================================")
	fmt.Println("API Key: [redacted]")
	fmt.Println()

	client := &http.Client{Timeout: 30 * time.Second}

	// 尝试不同的 URL 变体
	urls := []string{
		strings.TrimSuffix(apiURL, "/") + "/v1/messages",
		"https://m.88code.org/v1/messages",
		"https://88code.org/api/v1/messages",
		"https://api.88code.org/v1/messages",
	}

	// 尝试不同的请求体格式
	bodies := []struct {
		name string
		body string
	}{
		{
			name: "标准 Anthropic 格式",
			body: `{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "使用 claude-3-haiku",
			body: `{"model":"claude-3-haiku-20240307","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "使用 claude-3-5-sonnet",
			body: `{"model":"claude-3-5-sonnet-20241022","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "添加 stream: false",
			body: `{"model":"claude-haiku-4-5-20251001","max_tokens":1,"stream":false,"messages":[{"role":"user","content":"hi"}]}`,
		},
	}

	// 只测试第一个 URL，但尝试不同的请求体
	url := urls[0]
	fmt.Printf("测试 URL: %s\n\n", url)

	for _, b := range bodies {
		fmt.Printf("--- %s ---\n", b.name)

		req, _ := http.NewRequest("POST", url, bytes.NewBufferString(b.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start)

		if err != nil {
			fmt.Printf("错误: %v\n\n", err)
			continue
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		respStr := string(respBody)
		respStr = redactSecrets(respStr, apiKey)

		if len(respStr) > 200 {
			respStr = respStr[:200] + "..."
		}

		fmt.Printf("HTTP %d (%v): %s\n\n", resp.StatusCode, latency.Round(time.Millisecond), respStr)
	}

	// 测试不同的 URL
	fmt.Println("================================================================")
	fmt.Println("测试不同的 URL")
	fmt.Println("================================================================")

	body := `{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

	for _, url := range urls {
		fmt.Printf("\n--- %s ---\n", url)

		req, _ := http.NewRequest("POST", url, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start)

		if err != nil {
			fmt.Printf("错误: %v\n", err)
			continue
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		respStr := string(respBody)
		respStr = redactSecrets(respStr, apiKey)

		if len(respStr) > 200 {
			respStr = respStr[:200] + "..."
		}

		fmt.Printf("HTTP %d (%v): %s\n", resp.StatusCode, latency.Round(time.Millisecond), respStr)
	}

	// 测试 API Key 去掉 88_ 前缀
	fmt.Println("\n================================================================")
	fmt.Println("测试 API Key 变体")
	fmt.Println("================================================================")

	keyVariants := []struct {
		name string
		key  string
	}{
		{"原始 Key", apiKey},
		{"去掉 88_ 前缀", strings.TrimPrefix(apiKey, "88_")},
		{"添加 sk- 前缀", "sk-" + strings.TrimPrefix(apiKey, "88_")},
	}

	for _, kv := range keyVariants {
		fmt.Printf("\n--- %s ---\n", kv.name)
		fmt.Println("Key: [redacted]")

		req, _ := http.NewRequest("POST", urls[0], bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", kv.key)
		req.Header.Set("anthropic-version", "2023-06-01")

		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start)

		if err != nil {
			fmt.Printf("错误: %v\n", err)
			continue
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		respStr := string(respBody)
		respStr = redactSecrets(respStr, apiKey, kv.key)

		if len(respStr) > 200 {
			respStr = respStr[:200] + "..."
		}

		fmt.Printf("HTTP %d (%v): %s\n", resp.StatusCode, latency.Round(time.Millisecond), respStr)
	}
}

// 服务端错误响应可能回显认证头，写入控制台前必须替换所有本轮使用的密钥变体。
func redactSecrets(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}
