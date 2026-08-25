package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const (
	channelHTTPTimeout = 20 * time.Second
	channelMaxHTTPBody = 4 << 20
)

func channelHTTPClient() *http.Client {
	return &http.Client{Timeout: channelHTTPTimeout}
}

func doJSON(ctx context.Context, client *http.Client, method, endpoint string, headers map[string]string, requestBody any, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode channel request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build channel request: %w", err)
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if client == nil {
		client = channelHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, channelMaxHTTPBody+1))
	if err != nil {
		return err
	}
	if len(data) > channelMaxHTTPBody {
		return fmt.Errorf("channel response exceeds %d bytes", channelMaxHTTPBody)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// 只返回状态和有限的错误摘要，避免把平台返回的 token 或完整请求内容送进 UI。
		return fmt.Errorf("channel HTTP request failed: %s", response.Status)
	}
	if responseBody == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, responseBody); err != nil {
		return fmt.Errorf("decode channel response: %w", err)
	}
	return nil
}

func doBytes(ctx context.Context, client *http.Client, method, endpoint string, headers map[string]string, body []byte) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if client == nil {
		client = channelHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, channelMaxHTTPBody+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > channelMaxHTTPBody {
		return nil, "", fmt.Errorf("channel media response exceeds %d bytes", channelMaxHTTPBody)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("channel media request failed: %s", response.Status)
	}
	mediaType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if parsed, _, parseErr := mime.ParseMediaType(mediaType); parseErr == nil {
		mediaType = parsed
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return data, mediaType, nil
}

func stringMap(values ...string) map[string]string {
	result := make(map[string]string, len(values)/2)
	for index := 0; index+1 < len(values); index += 2 {
		result[values[index]] = values[index+1]
	}
	return result
}
