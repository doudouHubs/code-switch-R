//go:build !windows

package services

// StartRuntimeForegroundMonitor 非 Windows 平台没有这条 Windows 焦点诊断路径。
func StartRuntimeForegroundMonitor() func() {
	return func() {}
}

// StartRuntimeForegroundEventMonitor 仅 Windows 需要 WinEvent 前台变更订阅。
func StartRuntimeForegroundEventMonitor() {}
