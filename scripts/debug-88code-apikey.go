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
	"strings"
	"time"
)

// 测试带隐藏字符的 API Key 对 88code 的影响
// Author: Half open flowers

func main() {
	baseKey := os.Getenv("CODESWITCH_88CODE_API_KEY")
	if baseKey == "" {
		fmt.Fprintln(os.Stderr, "CODESWITCH_88CODE_API_KEY is required")
		os.Exit(2)
	}
	apiURL := "https://m.88code.org/api/v1/messages"

	reqBody := map[string]interface{}{
		"model":      "claude-haiku-4-5-20251001",
		"max_tokens": 1,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	fmt.Println("================================================================")
	fmt.Println("测试带隐藏字符的 API Key")
	fmt.Println("================================================================")

	tests := []struct {
		name   string
		apiKey string
	}{
		{"正常 API Key", baseKey},
		{"尾部空格", baseKey + " "},
		{"尾部多个空格", baseKey + "   "},
		{"前部空格", " " + baseKey},
		{"前后空格", " " + baseKey + " "},
		{"尾部换行符 \\n", baseKey + "\n"},
		{"尾部换行符 \\r\\n", baseKey + "\r\n"},
		{"尾部制表符 \\t", baseKey + "\t"},
		{"零宽空格 \\u200B", baseKey + "\u200B"},
		{"BOM 字符 \\uFEFF", baseKey + "\uFEFF"},
		{"前部 BOM", "\uFEFF" + baseKey},
		{"修改 key 末尾字符", baseKey[:len(baseKey)-1] + "9"},
		{"完全错误的 key", "invalid-api-key"},
	}

	client := &http.Client{Timeout: 15 * time.Second}

	for _, test := range tests {
		fmt.Printf("\n--- %s ---\n", test.name)
		fmt.Printf("Key 长度: %d (原始: %d)\n", len(test.apiKey), len(baseKey))

		req, _ := http.NewRequest("POST", apiURL, bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", test.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start)

		if err != nil {
			fmt.Printf("❌ 网络错误: %v\n", err)
			continue
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			fmt.Printf("✅ HTTP %d (%v)\n", resp.StatusCode, latency.Round(time.Millisecond))
		} else {
			fmt.Printf("❌ HTTP %d (%v)\n", resp.StatusCode, latency.Round(time.Millisecond))
			// 检查错误类型
			var errResp map[string]interface{}
			if json.Unmarshal(body, &errResp) == nil {
				if errObj, ok := errResp["error"].(map[string]interface{}); ok {
					fmt.Printf("   type: %v\n", errObj["type"])
					message := fmt.Sprint(errObj["message"])
					fmt.Printf("   message: %s\n", redactSecrets(message, baseKey, test.apiKey))
				}
			} else {
				fmt.Printf("   响应: %s\n", redactSecrets(string(body), baseKey, test.apiKey))
			}
		}
	}
}

// redactSecrets 防止服务端在错误响应中回显请求凭据。
func redactSecrets(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "***REDACTED***")
		}
	}
	return value
}
