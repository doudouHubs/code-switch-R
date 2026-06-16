//go:build windows

package services

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	errProjectManagerRuntimeInactive = errors.New("project manager session runtime inactive")

	projectManagerUser32DLL            = windows.NewLazySystemDLL("user32.dll")
	projectManagerGetWindowTextProc    = projectManagerUser32DLL.NewProc("GetWindowTextW")
	projectManagerShowWindowAsyncProc  = projectManagerUser32DLL.NewProc("ShowWindowAsync")
	projectManagerSetForegroundWndProc = projectManagerUser32DLL.NewProc("SetForegroundWindow")
	projectManagerBringWindowToTopProc = projectManagerUser32DLL.NewProc("BringWindowToTop")
	projectManagerSetWindowPosProc     = projectManagerUser32DLL.NewProc("SetWindowPos")
	projectManagerGetForegroundWndProc = projectManagerUser32DLL.NewProc("GetForegroundWindow")
	projectManagerSnapshotProcesses    = snapshotProjectManagerProcesses
)

const (
	projectManagerWindowRestoreCommand = 9
	projectManagerSWPNoSize            = 0x0001
	projectManagerSWPNoMove            = 0x0002
	projectManagerSWPShowWindow        = 0x0040
)

var (
	projectManagerHWNDTopMost   = windows.HWND(^uintptr(0))
	projectManagerHWNDNoTopMost = windows.HWND(^uintptr(1))
)

type projectManagerProcessEntry struct {
	PID       uint32
	ParentPID uint32
	ExeFile   string
}

type projectManagerWindowCandidate struct {
	HWND  windows.HWND
	Title string
	Score int
}

func focusProjectManagerTerminalWindow(runtime projectManagerSessionRuntime, session SessionSummary) error {
	processes, err := projectManagerSnapshotProcesses()
	if err != nil {
		return err
	}

	return focusProjectManagerTerminalWindowWithProcesses(runtime, session, processes)
}

func focusProjectManagerTerminalWindowWithProcesses(
	runtime projectManagerSessionRuntime,
	session SessionSummary,
	processes map[uint32]projectManagerProcessEntry,
) error {
	if err := validateProjectManagerSessionRuntime(runtime, processes); err != nil {
		return err
	}

	terminalPID, err := findProjectManagerTerminalPID(runtime.ShellPID, processes)
	if err != nil {
		return err
	}

	windowHandle, windowTitle, err := findProjectManagerMainWindow(terminalPID, buildProjectManagerWindowTitleHints(runtime, session))
	if err != nil {
		return err
	}

	log.Printf(
		"[ProjectManager] 激活 WT 窗口 session=%s shell_pid=%d terminal_pid=%d hwnd=%#x title=%q",
		session.ID,
		runtime.ShellPID,
		terminalPID,
		uintptr(windowHandle),
		windowTitle,
	)

	return projectManagerActivateWindow(windowHandle)
}

func buildProjectManagerWindowTitleHints(runtime projectManagerSessionRuntime, session SessionSummary) []string {
	candidates := []string{
		runtime.WindowID,
		runtime.TabTitle,
		session.ID,
		session.DisplayName,
		session.ProjectName,
		session.SourceName,
	}

	hints := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}

		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		hints = append(hints, trimmed)
	}

	return hints
}

func snapshotProjectManagerProcesses() (map[uint32]projectManagerProcessEntry, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	processes := make(map[uint32]projectManagerProcessEntry, 128)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}

	for {
		processes[entry.ProcessID] = projectManagerProcessEntry{
			PID:       entry.ProcessID,
			ParentPID: entry.ParentProcessID,
			ExeFile:   strings.TrimSpace(windows.UTF16ToString(entry.ExeFile[:])),
		}

		entry.Size = uint32(unsafe.Sizeof(entry))
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, err
		}
	}

	return processes, nil
}

func validateProjectManagerSessionRuntime(
	runtime projectManagerSessionRuntime,
	processes map[uint32]projectManagerProcessEntry,
) error {
	if strings.TrimSpace(runtime.SessionID) == "" {
		return errProjectManagerRuntimeInactive
	}

	if source := strings.TrimSpace(runtime.LaunchSource); source != "" && !strings.EqualFold(source, projectManagerRuntimeLaunchSource) {
		return errProjectManagerRuntimeInactive
	}

	if runtime.ShellPID == 0 {
		return errProjectManagerRuntimeInactive
	}

	process, ok := processes[runtime.ShellPID]
	if !ok {
		return errProjectManagerRuntimeInactive
	}

	name := strings.ToLower(strings.TrimSpace(process.ExeFile))
	if name != "pwsh.exe" && name != "powershell.exe" {
		return errProjectManagerRuntimeInactive
	}

	expectedStartTime := strings.TrimSpace(runtime.ShellStartedAt)
	if expectedStartTime == "" {
		return nil
	}

	expected, err := time.Parse(time.RFC3339Nano, expectedStartTime)
	if err != nil {
		return errProjectManagerRuntimeInactive
	}

	actual, err := projectManagerProcessStartTime(runtime.ShellPID)
	if err != nil {
		return errProjectManagerRuntimeInactive
	}

	diff := actual.Sub(expected.UTC())
	if diff < 0 {
		diff = -diff
	}
	if diff > 2*time.Second {
		return errProjectManagerRuntimeInactive
	}

	return nil
}

func projectManagerProcessStartTime(pid uint32) (time.Time, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return time.Time{}, err
	}
	defer windows.CloseHandle(handle)

	var creationTime windows.Filetime
	var exitTime windows.Filetime
	var kernelTime windows.Filetime
	var userTime windows.Filetime
	if err := windows.GetProcessTimes(handle, &creationTime, &exitTime, &kernelTime, &userTime); err != nil {
		return time.Time{}, err
	}

	return time.Unix(0, creationTime.Nanoseconds()).UTC(), nil
}

func findProjectManagerTerminalPID(
	shellPID uint32,
	processes map[uint32]projectManagerProcessEntry,
) (uint32, error) {
	currentPID := shellPID
	for depth := 0; depth < 16 && currentPID != 0; depth++ {
		process, ok := processes[currentPID]
		if !ok {
			break
		}

		switch strings.ToLower(strings.TrimSpace(process.ExeFile)) {
		case "windowsterminal.exe":
			return currentPID, nil
		case "openconsole.exe":
			parent, ok := processes[process.ParentPID]
			if ok && strings.EqualFold(strings.TrimSpace(parent.ExeFile), "WindowsTerminal.exe") {
				return parent.PID, nil
			}
		}

		if process.ParentPID == 0 || process.ParentPID == currentPID {
			break
		}
		currentPID = process.ParentPID
	}

	return 0, fmt.Errorf("会话已打开，但未找到对应的 Windows Terminal 进程")
}

func findProjectManagerMainWindow(targetPID uint32, titleHints []string) (windows.HWND, string, error) {
	candidates := make([]projectManagerWindowCandidate, 0, 8)
	enumErr := windows.EnumWindows(syscall.NewCallback(func(hwnd windows.HWND, lparam uintptr) uintptr {
		if !windows.IsWindowVisible(hwnd) {
			return 1
		}

		var processID uint32
		_, _ = windows.GetWindowThreadProcessId(hwnd, &processID)
		if processID != targetPID {
			return 1
		}

		title := projectManagerWindowTitle(hwnd)
		candidate := projectManagerWindowCandidate{
			HWND:  hwnd,
			Title: title,
			Score: projectManagerWindowTitleScore(title, titleHints),
		}
		candidates = append(candidates, candidate)
		return 1
	}), nil)
	if enumErr != nil && !errors.Is(enumErr, syscall.Errno(0)) {
		return 0, "", enumErr
	}

	if len(candidates) == 0 {
		return 0, "", fmt.Errorf("会话已打开，但未找到 Terminal 主窗口")
	}

	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Score > best.Score {
			best = candidate
		}
	}

	log.Printf(
		"[ProjectManager] 已枚举 WT 顶层窗口 target_pid=%d candidates=%d best_hwnd=%#x best_title=%q best_score=%d hints=%q",
		targetPID,
		len(candidates),
		uintptr(best.HWND),
		best.Title,
		best.Score,
		strings.Join(titleHints, " | "),
	)

	return best.HWND, best.Title, nil
}

func projectManagerWindowTitle(hwnd windows.HWND) string {
	if hwnd == 0 {
		return ""
	}

	buffer := make([]uint16, 512)
	result, _, _ := projectManagerGetWindowTextProc.Call(
		uintptr(hwnd),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if result == 0 {
		return ""
	}

	return strings.TrimSpace(windows.UTF16ToString(buffer))
}

func projectManagerWindowTitleScore(title string, hints []string) int {
	normalizedTitle := strings.ToLower(strings.TrimSpace(title))
	if normalizedTitle == "" {
		return 0
	}

	best := 0
	for index, hint := range hints {
		normalizedHint := strings.ToLower(strings.TrimSpace(hint))
		if normalizedHint == "" {
			continue
		}

		if normalizedTitle == normalizedHint {
			return 100 - index
		}
		if strings.Contains(normalizedTitle, normalizedHint) && best < 80-index {
			best = 80 - index
		}
	}

	return best
}

func projectManagerActivateWindow(hwnd windows.HWND) error {
	if hwnd == 0 {
		return errors.New("无效的窗口句柄")
	}

	// 先恢复窗口，避免目标 Terminal 最小化后只在任务栏闪烁。
	_ = projectManagerShowWindowAsync(hwnd, projectManagerWindowRestoreCommand)

	// Windows 对前台窗口切换限制挺多，单次 SetForegroundWindow 经常抽风。
	// 这里按“直接前台 -> 抬到顶层 -> topmost 脉冲”逐级兜底，尽量把真正的 WT 窗口拉回用户眼前。
	if err := projectManagerSetForegroundWindow(hwnd); err == nil || projectManagerForegroundWindowMatches(hwnd) {
		return nil
	}

	_ = projectManagerBringWindowToTop(hwnd)
	if err := projectManagerSetForegroundWindow(hwnd); err == nil || projectManagerForegroundWindowMatches(hwnd) {
		return nil
	}

	_ = projectManagerPulseWindowToFront(hwnd)
	if err := projectManagerSetForegroundWindow(hwnd); err == nil || projectManagerForegroundWindowMatches(hwnd) {
		return nil
	}

	return errors.New("切换 Terminal 窗口到前台失败")
}

func projectManagerShowWindowAsync(hwnd windows.HWND, command int) error {
	if hwnd == 0 {
		return errors.New("无效的窗口句柄")
	}

	result, _, callErr := projectManagerShowWindowAsyncProc.Call(uintptr(hwnd), uintptr(command))
	if result == 0 && callErr != syscall.Errno(0) {
		return fmt.Errorf("ShowWindowAsync 调用失败: %w", callErr)
	}
	return nil
}

func projectManagerSetForegroundWindow(hwnd windows.HWND) error {
	if hwnd == 0 {
		return errors.New("无效的窗口句柄")
	}

	result, _, callErr := projectManagerSetForegroundWndProc.Call(uintptr(hwnd))
	if result == 0 {
		if projectManagerForegroundWindowMatches(hwnd) {
			return nil
		}
		if callErr != syscall.Errno(0) {
			return fmt.Errorf("SetForegroundWindow 调用失败: %w", callErr)
		}
		return errors.New("切换 Terminal 窗口到前台失败")
	}
	return nil
}

func projectManagerBringWindowToTop(hwnd windows.HWND) error {
	if hwnd == 0 {
		return errors.New("无效的窗口句柄")
	}

	result, _, callErr := projectManagerBringWindowToTopProc.Call(uintptr(hwnd))
	if result == 0 && callErr != syscall.Errno(0) {
		return fmt.Errorf("BringWindowToTop 调用失败: %w", callErr)
	}
	return nil
}

func projectManagerPulseWindowToFront(hwnd windows.HWND) error {
	if err := projectManagerSetWindowPos(hwnd, projectManagerHWNDTopMost); err != nil {
		return err
	}
	return projectManagerSetWindowPos(hwnd, projectManagerHWNDNoTopMost)
}

func projectManagerSetWindowPos(hwnd windows.HWND, insertAfter windows.HWND) error {
	if hwnd == 0 {
		return errors.New("无效的窗口句柄")
	}

	flags := uintptr(projectManagerSWPNoMove | projectManagerSWPNoSize | projectManagerSWPShowWindow)
	result, _, callErr := projectManagerSetWindowPosProc.Call(
		uintptr(hwnd),
		uintptr(insertAfter),
		0,
		0,
		0,
		0,
		flags,
	)
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return fmt.Errorf("SetWindowPos 调用失败: %w", callErr)
		}
		return errors.New("调整窗口层级失败")
	}
	return nil
}

func projectManagerForegroundWindowMatches(hwnd windows.HWND) bool {
	if hwnd == 0 {
		return false
	}

	current, _, _ := projectManagerGetForegroundWndProc.Call()
	return windows.HWND(current) == hwnd
}
