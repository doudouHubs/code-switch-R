package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

const codexPortabilityRetryContextKey = "code-switch.codex-portability-retry"

// forwardRequest 保留原始请求作为主路径，只在旧会话状态被新供应商以 5xx 拒绝时恢复一次。
// 一次性门禁挂在 gin 请求上下文上，确保多 provider 与硬锁重试都不会放大成重试风暴。
func (prs *ProviderRelayService) forwardRequest(
	c *gin.Context,
	kind string,
	provider Provider,
	endpoint string,
	query map[string]string,
	clientHeaders map[string]string,
	bodyBytes []byte,
	isStream bool,
	model string,
) (bool, error) {
	attemptBody := bodyBytes
	if codexPortableModeEnabled(c, kind) {
		portableBody, changed, portableErr := makeCodexRequestProviderPortable(bodyBytes)
		if portableErr != nil {
			return false, fmt.Errorf("Codex 可移植模式转换请求失败: %w", portableErr)
		}
		if changed {
			attemptBody = portableBody
			fmt.Printf("[CodexPortability] Provider %s 使用无供应商状态的回落请求\n", provider.Name)
		}
	}

	ok, err := prs.forwardRequestOnce(
		c,
		kind,
		provider,
		endpoint,
		query,
		clientHeaders,
		attemptBody,
		isStream,
		model,
	)
	if ok || !shouldRetryCodexRequestWithoutProviderState(c, kind, err) {
		return ok, err
	}

	portableBody, changed, portableErr := makeCodexRequestProviderPortable(bodyBytes)
	if portableErr != nil {
		fmt.Printf("[CodexPortability] 旧会话状态转换失败，保留原始上游错误: %v\n", portableErr)
		return false, err
	}
	if !changed {
		return false, err
	}

	// 先落门禁再发请求。即使兼容重试仍失败，后续 provider 调度也不能重复触发同一恢复分支。
	c.Set(codexPortabilityRetryContextKey, true)
	fmt.Printf("[CodexPortability] Provider %s 拒绝旧会话状态，使用无供应商状态的请求重试一次\n", provider.Name)
	return prs.forwardRequestOnce(
		c,
		kind,
		provider,
		endpoint,
		query,
		clientHeaders,
		portableBody,
		isStream,
		model,
	)
}

func codexPortableModeEnabled(c *gin.Context, kind string) bool {
	if c == nil || !strings.EqualFold(strings.TrimSpace(kind), "codex") {
		return false
	}
	enabled, exists := c.Get(codexPortabilityRetryContextKey)
	return exists && enabled == true
}

func shouldRetryCodexRequestWithoutProviderState(c *gin.Context, kind string, err error) bool {
	if c == nil || !strings.EqualFold(strings.TrimSpace(kind), "codex") || err == nil {
		return false
	}
	if codexPortableModeEnabled(c, kind) {
		return false
	}

	var upstreamErr *providerRelayUpstreamError
	if !errors.As(err, &upstreamErr) {
		return false
	}
	return upstreamErr.StatusCode >= 500 && upstreamErr.StatusCode < 600
}

// makeCodexRequestProviderPortable 把 Responses API 的服务端状态引用还原成可跨供应商输入。
// 用户消息、工具调用和 call_id 都保留；只移除必须由原供应商解析的 response/item 引用与加密推理项。
func makeCodexRequestProviderPortable(body []byte) ([]byte, bool, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, fmt.Errorf("解析 Codex 请求失败: %w", err)
	}

	changed := false
	for _, key := range []string{"previous_response_id", "conversation"} {
		if _, exists := payload[key]; exists {
			delete(payload, key)
			changed = true
		}
	}

	rawInput, hasInput := payload["input"]
	if hasInput {
		portableInput, inputChanged, err := makeCodexInputProviderPortable(rawInput)
		if err != nil {
			return nil, false, err
		}
		if inputChanged {
			payload["input"] = portableInput
			changed = true
		}
	}

	if !changed {
		return body, false, nil
	}
	portableBody, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("编码无供应商状态的 Codex 请求失败: %w", err)
	}
	return portableBody, true, nil
}

func makeCodexInputProviderPortable(rawInput json.RawMessage) (json.RawMessage, bool, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(rawInput, &items); err != nil {
		// Responses API 也允许字符串 input；它不携带 response item 状态，保持原值即可。
		return rawInput, false, nil
	}

	changed := false
	portableItems := make([]json.RawMessage, 0, len(items))
	for _, rawItem := range items {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return nil, false, fmt.Errorf("解析 Codex input item 失败: %w", err)
		}

		var itemType string
		_ = json.Unmarshal(item["type"], &itemType)
		switch strings.ToLower(strings.TrimSpace(itemType)) {
		case "reasoning", "item_reference":
			// reasoning 的 encrypted_content 和 item_reference 都只能由生成它们的供应商解释。
			changed = true
			continue
		}

		if _, exists := item["id"]; exists {
			// 顶层 item id 是供应商分配的 response item 标识；call_id 是工具调用配对键，必须保留。
			delete(item, "id")
			changed = true
			portableItem, err := json.Marshal(item)
			if err != nil {
				return nil, false, fmt.Errorf("编码 Codex input item 失败: %w", err)
			}
			portableItems = append(portableItems, portableItem)
			continue
		}

		portableItems = append(portableItems, rawItem)
	}

	if !changed {
		return rawInput, false, nil
	}
	portableInput, err := json.Marshal(portableItems)
	if err != nil {
		return nil, false, fmt.Errorf("编码无供应商状态的 Codex input 失败: %w", err)
	}
	return portableInput, true, nil
}
