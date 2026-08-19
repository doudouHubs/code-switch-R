//go:build windows

package services

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	runtimeForegroundUser32        = windows.NewLazySystemDLL("user32.dll")
	runtimeGetWindowTextProc       = runtimeForegroundUser32.NewProc("GetWindowTextW")
	runtimeGetClassNameProc        = runtimeForegroundUser32.NewProc("GetClassNameW")
	runtimeSetWinEventHookProc     = runtimeForegroundUser32.NewProc("SetWinEventHook")
)

const (
	runtimeForegroundPollInterval    = 100 * time.Millisecond
	runtimeEventSystemForeground     = 0x0003
	runtimeWinEventOutOfContext      = 0x0000
)

var runtimeForegroundWinEventOnce sync.Once

// StartRuntimeForegroundMonitor 只观察前台 HWND 的变化，不执行任何激活或置顶操作。
// 目标是把“用户看到 WT 闪烁”拆成可验证的窗口切换序列，区分桌宠/Wails、WT 自身和
// 其他进程；只有 HWND 或所属 PID 变化时才落日志，避免 100ms 轮询把日志写爆。
func StartRuntimeForegroundMonitor() func() {
	stop := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		ownPID := uint32(os.Getpid())
		ownExecutable, _ := os.Executable()
		previousKey := ""
		WriteRuntimeDiagnostic("foreground-monitor-start", fmt.Sprintf("own_pid=%d own_exe=%q", ownPID, ownExecutable))

		ticker := time.NewTicker(runtimeForegroundPollInterval)
		defer ticker.Stop()
		for {
			key, details := runtimeForegroundSnapshot(ownPID)
			if key != previousKey {
				previousKey = key
				WriteRuntimeDiagnostic("foreground-window-changed", details)
			}

			select {
			case <-ticker.C:
			case <-stop:
				WriteRuntimeDiagnostic("foreground-monitor-stop")
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() { close(stop) })
	}
}

// StartRuntimeForegroundEventMonitor 必须由带消息循环的 Wails 主线程调用。
// 轮询只能看到 100ms 采样点，短暂的 CodeSwitch -> WT 前台切换会被直接漏掉；
// Win32 的 EVENT_SYSTEM_FOREGROUND 能保留每一次系统前台变更，专用于分辨
// "WT 自己抢前台" 和 "应用短暂抢焦点后又回到 WT" 两条完全不同的因果链。
// 这里只订阅事件并写诊断，绝不调用任何 Show、Focus、SetForegroundWindow 或终端 API。
func StartRuntimeForegroundEventMonitor() {
	runtimeForegroundWinEventOnce.Do(func() {
		ownPID := uint32(os.Getpid())
		previousKey := ""
		var previousTick uint32
		callback := windows.NewCallback(func(
			_ uintptr,
			event uintptr,
			hwnd uintptr,
			idObject uintptr,
			idChild uintptr,
			eventThread uintptr,
			eventTick uintptr,
		) uintptr {
			if uint32(event) != runtimeEventSystemForeground {
				return 0
			}

			// 回调运行在 Wails 的消息循环线程。直接按回调携带的 HWND 取快照，
			// 不能再次调用 GetForegroundWindow，否则连续切换时会把 A 的事件误记成 B。
			key, details := runtimeForegroundSnapshotForWindow(windows.HWND(hwnd), ownPID)
			currentTick := uint32(eventTick)
			elapsedSincePrevious := uint32(0)
			if previousKey != "" {
				// eventTick 是系统启动后的 32 位毫秒计数；无符号减法天然覆盖约 49 天的回绕。
				elapsedSincePrevious = currentTick - previousTick
			}
			previousKeyForEvent := previousKey
			previousKey = key
			previousTick = currentTick
			WriteRuntimeDiagnostic("foreground-window-event", fmt.Sprintf(
				"source=win-event event_tick_ms=%d previous=%q since_previous_ms=%d object=%d child=%d event_thread=%d %s",
				currentTick,
				previousKeyForEvent,
				elapsedSincePrevious,
				int32(idObject),
				int32(idChild),
				uint32(eventThread),
				details,
			))
			return 0
		})

		hook, _, callErr := runtimeSetWinEventHookProc.Call(
			uintptr(runtimeEventSystemForeground),
			uintptr(runtimeEventSystemForeground),
			0,
			callback,
			0,
			0,
			uintptr(runtimeWinEventOutOfContext),
		)
		if hook == 0 {
			// 事件钩子注册失败时仍保留原有轮询监控，保证诊断能力不会因为系统策略退化为无日志。
			WriteRuntimeDiagnostic("foreground-monitor-event-hook-failed", fmt.Sprintf("err=%v", callErr))
			return
		}

		// WinEvent hook 的生命周期与 GUI 进程一致；进程退出时 Windows 会自动释放句柄。
		// 不在这里手工 Unhook，避免 Wails 关闭消息循环后跨线程反注册造成二次异常。
		WriteRuntimeDiagnostic("foreground-monitor-event-hook-ready", fmt.Sprintf("hook=%#x own_pid=%d", hook, ownPID))
	})
}

func runtimeForegroundSnapshot(ownPID uint32) (string, string) {
	return runtimeForegroundSnapshotForWindow(windows.GetForegroundWindow(), ownPID)
}

func runtimeForegroundSnapshotForWindow(hwnd windows.HWND, ownPID uint32) (string, string) {
	if hwnd == 0 {
		return "0", "hwnd=0 pid=0 own=false title=\"\" class=\"\" exe=\"\""
	}

	var pid uint32
	_, _ = windows.GetWindowThreadProcessId(hwnd, &pid)
	title := runtimeReadWindowText(runtimeGetWindowTextProc, hwnd)
	className := runtimeReadWindowText(runtimeGetClassNameProc, hwnd)
	executable := runtimeProcessExecutable(pid)
	key := fmt.Sprintf("%#x/%d", uintptr(hwnd), pid)
	details := fmt.Sprintf(
		"hwnd=%#x pid=%d own=%t title=%q class=%q exe=%q",
		uintptr(hwnd),
		pid,
		pid == ownPID,
		title,
		className,
		executable,
	)
	return key, details
}

func runtimeReadWindowText(proc *windows.LazyProc, hwnd windows.HWND) string {
	buffer := make([]uint16, 512)
	result, _, _ := proc.Call(
		uintptr(hwnd),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if result == 0 {
		return ""
	}
	return strings.TrimSpace(windows.UTF16ToString(buffer[:result]))
}

func runtimeProcessExecutable(pid uint32) string {
	if pid == 0 {
		return ""
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)

	buffer := make([]uint16, 1024)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return ""
	}
	return strings.TrimSpace(windows.UTF16ToString(buffer[:size]))
}
