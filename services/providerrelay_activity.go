package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/daodao97/xgo/xrequest"
	"github.com/gin-gonic/gin"
)

const providerRelayActivityContextKey = "codeswitch.provider-relay.activity"

// SetActivityEmitter 注入桌宠活动旁路；relay 本身不依赖宠物状态，未注入时保持原有行为。
func (prs *ProviderRelayService) SetActivityEmitter(emitter PetActivityEmitter) {
	if prs == nil {
		return
	}
	prs.activityEmitter = emitter
}

// beginProviderRelayActivity 把一次外部 relay 请求绑定到单个 activity owner。
// provider 重试、降级和协议转换都复用这个 owner，避免一次用户请求在桌宠端闪烁多次。
func (prs *ProviderRelayService) beginProviderRelayActivity(c *gin.Context) {
	if prs == nil || c == nil || prs.activityEmitter == nil {
		return
	}
	activity := newPetActivityRequest(
		prs.activityEmitter,
		PetActivitySourceRelay,
		newPetActivityRequestID("relay"),
		"",
	)
	c.Set(providerRelayActivityContextKey, activity)
}

// finishProviderRelayActivity 必须由最外层 HTTP handler 延迟调用；如果把 defer
// 放在 begin 内部，函数一返回就会发送 completed，provider 降级期间前端会错误地
// 退出工作态，而后续真实输出又会被当成迟到事件丢弃。
func (prs *ProviderRelayService) finishProviderRelayActivity(c *gin.Context) {
	activity := providerRelayActivityFromContext(c)
	if activity == nil {
		return
	}

	phase := PetActivityCompleted
	if request := c.Request; request != nil && request.Context() != nil {
		if errors.Is(request.Context().Err(), context.Canceled) {
			phase = PetActivityCancelled
		} else if request.Context().Err() != nil {
			phase = PetActivityFailed
		}
	}
	if c.Writer != nil && c.Writer.Status() >= 400 {
		phase = PetActivityFailed
	}
	activity.Finish(phase)
}

func providerRelayActivityFromContext(c *gin.Context) *petActivityRequest {
	if c == nil {
		return nil
	}
	value, exists := c.Get(providerRelayActivityContextKey)
	if !exists {
		return nil
	}
	activity, _ := value.(*petActivityRequest)
	return activity
}

// providerRelayActivityHook 在响应复制前观察原始上游 payload；它不修改数据，
// 因此不会影响现有协议转换、日志计费和客户端流式传输。
func providerRelayActivityHook(activity *petActivityRequest) xrequest.ResponseHook {
	return func(data []byte) (bool, []byte) {
		observeProviderRelayOutput(activity, data)
		return true, data
	}
}

func providerRelayResponseHooks(
	c *gin.Context,
	kind string,
	requestLog *ReqeustLog,
	isStream bool,
	converter *OpenAIToAnthropicSSEConverter,
) []xrequest.ResponseHook {
	hooks := make([]xrequest.ResponseHook, 0, 3)
	if activity := providerRelayActivityFromContext(c); activity != nil {
		hooks = append(hooks, providerRelayActivityHook(activity))
	}
	if isStream && converter != nil {
		hooks = append(hooks, protocolConvertHook(converter, kind, requestLog))
	} else {
		hooks = append(hooks, ReqeustLogHook(c, kind, requestLog))
	}
	return hooks
}

func observeProviderRelayOutput(activity *petActivityRequest, data []byte) {
	if activity == nil || len(data) == 0 {
		return
	}
	for _, payload := range providerRelayJSONPayloads(data) {
		if providerRelayPayloadHasOutput(payload) {
			activity.Output()
			return
		}
	}
}

func providerRelayJSONPayloads(data []byte) [][]byte {
	var payloads [][]byte
	seenSSEData := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		seenSSEData = true
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" || !json.Valid([]byte(payload)) {
			continue
		}
		payloads = append(payloads, []byte(payload))
	}
	if !seenSSEData && json.Valid(petActivityBytesTrimSpace(data)) {
		payloads = append(payloads, petActivityBytesTrimSpace(data))
	}
	return payloads
}

func petActivityBytesTrimSpace(data []byte) []byte {
	return []byte(strings.TrimSpace(string(data)))
}

// providerRelayPayloadHasOutput 使用白名单递归识别模型输出字段。
// 不能简单扫描所有字符串，否则 usage、model、error 或请求回显也会把宠物误判为工作。
func providerRelayPayloadHasOutput(payload []byte) bool {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return false
	}
	return providerRelayOutputValue(value, false)
}

func providerRelayOutputValue(value any, textAllowed bool) bool {
	switch typed := value.(type) {
	case string:
		return textAllowed && strings.TrimSpace(typed) != ""
	case []any:
		for _, item := range typed {
			if providerRelayOutputValue(item, textAllowed) {
				return true
			}
		}
		return false
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "text", "output_text", "completion", "arguments", "delta":
				if providerRelayOutputValue(child, true) {
					return true
				}
			case "content":
				if providerRelayOutputValue(child, true) {
					return true
				}
			case "parts", "choices", "candidates", "output", "message", "response", "item", "tool_calls", "function":
				if providerRelayOutputValue(child, false) {
					return true
				}
			}
		}
	}
	return false
}
