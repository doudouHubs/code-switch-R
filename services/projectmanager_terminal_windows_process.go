//go:build windows

package services

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	errProjectManagerRuntimeInactive = errors.New("project manager session runtime inactive")

	projectManagerUser32DLL            = windows.NewLazySystemDLL("user32.dll")
	projectManagerShowWindowAsyncProc  = projectManagerUser32DLL.NewProc("ShowWindowAsync")
	projectManagerSetForegroundWndProc = projectManagerUser32DLL.NewProc("SetForegroundWindow")
)

const projectManagerWindowRestoreCommand = 9

type projectManagerProcessEntry struct {
	PID       uint32
	ParentPID uint32
	ExeFile   string
}

func focusProjectManagerTerminalWindow(runtime projectManagerSessionRuntime) error {
	processes, err := snapshotProjectManagerProcesses()
	if err != nil {
		return err
	}

	if err := validateProjectManagerSessionRuntime(runtime, processes); err != nil {
		return err
	}

	terminalPID, err := findProjectManagerTerminalPID(runtime.ShellPID, processes)
	if err != nil {
		return err
	}

	windowHandle, err := findProjectManagerMainWindow(terminalPID)
	if err != nil {
		return err
	}

	// 先恢复窗口，再切前台，避免目标 Terminal 处于最小化时只变成任务栏闪烁。
	_ = projectManagerShowWindowAsync(windowHandle, projectManagerWindowRestoreCommand)
	if err := projectManagerSetForegroundWindow(windowHandle); err != nil {
		return err
	}
	return nil
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

func findProjectManagerMainWindow(targetPID uint32) (windows.HWND, error) {
	var result windows.HWND
	enumErr := windows.EnumWindows(syscall.NewCallback(func(hwnd windows.HWND, lparam uintptr) uintptr {
		if !windows.IsWindowVisible(hwnd) {
			return 1
		}

		var processID uint32
		_, _ = windows.GetWindowThreadProcessId(hwnd, &processID)
		if processID != targetPID {
			return 1
		}

		result = hwnd
		return 0
	}), nil)
	if enumErr != nil && !errors.Is(enumErr, syscall.Errno(0)) {
		return 0, enumErr
	}

	if result == 0 {
		return 0, fmt.Errorf("会话已打开，但未找到 Terminal 主窗口")
	}
	return result, nil
}

func projectManagerShowWindowAsync(hwnd windows.HWND, command int) error {
	if hwnd == 0 {
		return errors.New("无效的窗口句柄")
	}

	result, _, callErr := projectManagerShowWindowAsyncProc.Call(uintptr(hwnd), uintptr(command))
	if result == 0 && callErr != syscall.Errno(0) {
		return callErr
	}
	return nil
}

func projectManagerSetForegroundWindow(hwnd windows.HWND) error {
	if hwnd == 0 {
		return errors.New("无效的窗口句柄")
	}

	result, _, callErr := projectManagerSetForegroundWndProc.Call(uintptr(hwnd))
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return errors.New("切换 Terminal 窗口到前台失败")
	}
	return nil
}
