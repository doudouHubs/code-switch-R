package services

import (
	"fmt"
	"strings"
)

// providerRelayUpstreamError 保留结构化状态码，让调度层能区分上游拒绝与网络故障。
// 错误文本继续沿用原格式，避免改变日志、接口响应和既有排障习惯。
type providerRelayUpstreamError struct {
	StatusCode int
	Detail     string
}

func (e *providerRelayUpstreamError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		return fmt.Sprintf("upstream status %d", e.StatusCode)
	}
	return fmt.Sprintf("upstream status %d: %s", e.StatusCode, detail)
}

func newProviderRelayUpstreamError(statusCode int, detail string) error {
	return &providerRelayUpstreamError{
		StatusCode: statusCode,
		Detail:     detail,
	}
}
