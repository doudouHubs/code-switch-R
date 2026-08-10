//go:build ignore

// +build ignore

package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
)

// 测试 88_ 开头的 API Key 在 HTTP header 中的实际传输情况
// Author: Half open flowers

func main() {
	apiKey := os.Getenv("CODESWITCH_88CODE_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "CODESWITCH_88CODE_API_KEY is required")
		os.Exit(2)
	}
	url := "https://m.88code.org/api/v1/messages"
	body := []byte(`{"max_tokens":1,"messages":[{"content":"hi","role":"user"}],"model":"claude-haiku-4-5-20251001"}`)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	// 请求转储会包含认证头；克隆后遮蔽凭据仍能验证其他传输字段，避免日志二次泄露。
	dumpRequest := req.Clone(req.Context())
	dumpRequest.Header.Set("x-api-key", "[redacted]")
	dump, _ := httputil.DumpRequestOut(dumpRequest, false)
	fmt.Println("=== 实际发送的 HTTP 请求头 ===")
	fmt.Println(string(dump))

	// 检查 header 值
	fmt.Println("=== Header 值检查 ===")
	fmt.Printf("API Key 长度: %d\n", len(apiKey))
	fmt.Printf("Header 中的值长度: %d\n", len(req.Header.Get("x-api-key")))

	// 检查是否相等
	if apiKey == req.Header.Get("x-api-key") {
		fmt.Println("✅ Header 值与原始值完全相同")
	} else {
		fmt.Println("❌ Header 值与原始值不同！")
		// 只报告不一致的位置，避免异常场景把原始凭据写进终端日志。
		for i := 0; i < len(apiKey); i++ {
			h := req.Header.Get("x-api-key")
			if i < len(h) && apiKey[i] != h[i] {
				fmt.Printf("  位置 %d 的字节不一致\n", i)
			}
		}
	}
}
