package services

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/daodao97/xgo/xrequest"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	codexReliableStreamFallbackContextKey = "code-switch.codex-reliable-stream-fallback"
	maxBufferedCodexStreamBytes           = 32 << 20
)

// configureCodexReliableStreamFallback 只为确实存在不同默认候选的项目自动回落启用缓冲。
// 流一旦写给客户端就不能再安全拼接另一家供应商，因此这里必须在首字节写出前完成终止事件验证。
func configureCodexReliableStreamFallback(
	c *gin.Context,
	kind string,
	isStream bool,
	preferredProviderID int64,
	autoFallback bool,
	active []Provider,
) {
	if c == nil || !strings.EqualFold(strings.TrimSpace(kind), "codex") || !isStream || preferredProviderID <= 0 || !autoFallback {
		return
	}

	hasPreferred := false
	hasDistinctFallback := false
	for _, provider := range active {
		if provider.ID == preferredProviderID {
			hasPreferred = true
			continue
		}
		hasDistinctFallback = true
	}
	if hasPreferred && hasDistinctFallback {
		c.Set(codexReliableStreamFallbackContextKey, true)
	}
}

func codexReliableStreamFallbackEnabled(c *gin.Context, kind string, isStream bool) bool {
	if c == nil || !isStream || !strings.EqualFold(strings.TrimSpace(kind), "codex") {
		return false
	}
	enabled, exists := c.Get(codexReliableStreamFallbackContextKey)
	return exists && enabled == true
}

func forwardBufferedCodexStream(
	c *gin.Context,
	provider Provider,
	resp *xrequest.Response,
	converter *OpenAIToAnthropicSSEConverter,
	requestLog *ReqeustLog,
) (bool, error) {
	buffered := newBufferedCodexResponseWriter(maxBufferedCodexStreamBytes)
	tracker := &codexStreamCompletionTracker{}
	hooks := []xrequest.ResponseHook{tracker.hook}
	if converter != nil {
		hooks = append(hooks, protocolConvertHook(converter, "codex", requestLog))
	} else {
		hooks = append(hooks, ReqeustLogHook(c, "codex", requestLog))
	}

	if _, err := resp.ToHttpResponseWriter(buffered, hooks...); err != nil {
		requestLog.HttpCode = http.StatusBadGateway
		return false, newProviderRelayUpstreamError(
			http.StatusBadGateway,
			fmt.Sprintf("Provider %s 流读取失败: %v", provider.Name, err),
		)
	}
	if err := tracker.validate(); err != nil {
		requestLog.HttpCode = http.StatusBadGateway
		return false, newProviderRelayUpstreamError(
			http.StatusBadGateway,
			fmt.Sprintf("Provider %s %v", provider.Name, err),
		)
	}
	if err := buffered.replay(c.Writer); err != nil {
		return false, fmt.Errorf("%w: 回放 Codex 完整流失败: %v", errClientAbort, err)
	}
	return true, nil
}

type codexStreamCompletionTracker struct {
	completed       bool
	terminalFailure string
}

func (t *codexStreamCompletionTracker) hook(data []byte) (bool, []byte) {
	payload := strings.TrimSpace(string(data))
	if strings.HasPrefix(payload, "data:") {
		payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
	}
	if payload == "" || payload == "[DONE]" || !gjson.Valid(payload) {
		return true, data
	}

	eventType := strings.TrimSpace(gjson.Get(payload, "type").String())
	switch eventType {
	case "response.completed":
		t.completed = true
	case "response.failed", "response.incomplete", "error":
		t.terminalFailure = firstNonEmptyString(
			gjson.Get(payload, "response.error.message").String(),
			gjson.Get(payload, "error.message").String(),
			eventType,
		)
	}
	if eventType == "" && strings.EqualFold(strings.TrimSpace(gjson.Get(payload, "status").String()), "completed") {
		t.completed = true
	}
	return true, data
}

func (t *codexStreamCompletionTracker) validate() error {
	if t.terminalFailure != "" {
		return fmt.Errorf("上游流终止失败: %s", t.terminalFailure)
	}
	if !t.completed {
		return fmt.Errorf("stream closed before response.completed")
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type bufferedCodexResponseWriter struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	maxBytes int
}

func newBufferedCodexResponseWriter(maxBytes int) *bufferedCodexResponseWriter {
	return &bufferedCodexResponseWriter{
		header:   make(http.Header),
		maxBytes: maxBytes,
	}
}

func (w *bufferedCodexResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedCodexResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *bufferedCodexResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.maxBytes > 0 && w.body.Len()+len(data) > w.maxBytes {
		return 0, fmt.Errorf("Codex 流缓冲超过 %d 字节上限", w.maxBytes)
	}
	return w.body.Write(data)
}

// Flush 只满足 http.Flusher；验证完成前绝不能把候选供应商的半截流写入真实客户端。
func (w *bufferedCodexResponseWriter) Flush() {}

func (w *bufferedCodexResponseWriter) replay(dst http.ResponseWriter) error {
	for key, values := range w.header {
		dst.Header()[key] = append([]string(nil), values...)
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	dst.WriteHeader(status)
	if _, err := dst.Write(w.body.Bytes()); err != nil {
		return err
	}
	if flusher, ok := dst.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
