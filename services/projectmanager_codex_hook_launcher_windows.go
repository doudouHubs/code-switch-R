//go:build windows

package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	projectManagerCodexHookLauncherDir  = "project-manager-codex-hook"
	projectManagerCodexHookLauncherName = "CodeSwitch.codex-hook.cmd"
)

// prepareProjectManagerCodexHookCommand 把复杂的 EXE 调用藏进批处理文件。
// Codex 在 Windows 会经由 `cmd.exe /C` 执行 Hook；外层只能传无引号的安全 .cmd 路径，
// 否则 CMD 会把“程序路径 + 参数”误识别为同一个可执行文件。
func prepareProjectManagerCodexHookCommand(executable string) (string, error) {
	executable, err := projectManagerCodexHookExecutablePath(executable)
	if err != nil {
		return "", err
	}

	launcherPath, err := projectManagerCodexHookLauncherPath()
	if err != nil {
		return "", err
	}
	content := buildProjectManagerCodexHookLauncherContent(executable)
	if existing, readErr := os.ReadFile(launcherPath); readErr != nil || string(existing) != content {
		if err := AtomicWriteText(launcherPath, content); err != nil {
			return "", fmt.Errorf("写入 Codex Hook 启动器失败: %w", err)
		}
	}

	safeLauncherPath, err := projectManagerCodexHookCmdSafePath(launcherPath)
	if err != nil {
		return "", err
	}
	return safeLauncherPath + " " + projectManagerCodexHookCommandMarker, nil
}

func projectManagerCodexHookExecutablePath(executable string) (string, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return "", errors.New("CodeSwitch 可执行文件路径不能为空")
	}
	absPath, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("解析 CodeSwitch 可执行文件绝对路径失败: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("检查 CodeSwitch 可执行文件失败: %w", err)
	}
	if info.IsDir() {
		return "", errors.New("CodeSwitch 可执行文件路径不能是目录")
	}
	return absPath, nil
}

func projectManagerCodexHookLauncherPath() (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, appSettingsDir, projectManagerCodexHookLauncherDir, projectManagerCodexHookLauncherName), nil
}

func buildProjectManagerCodexHookLauncherContent(executable string) string {
	return strings.Join([]string{
		"@echo off",
		// EXE 路径只在批处理内部引用，允许安装目录含空格；外层 Codex 命令从不携带这段带引号文本。
		fmt.Sprintf("%s %%*", quoteProjectManagerCmdFileArgument(executable)),
		"exit /b %ERRORLEVEL%",
		"",
	}, "\r\n")
}

func projectManagerCodexHookCmdSafePath(path string) (string, error) {
	path, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("解析 Codex Hook 启动器绝对路径失败: %w", err)
	}
	if projectManagerCodexHookCmdPathIsSafe(path) {
		return path, nil
	}

	shortPath, err := projectManagerCodexHookShortPath(path)
	if err != nil {
		return "", fmt.Errorf("获取 Codex Hook 启动器短路径失败: %w", err)
	}
	if !projectManagerCodexHookCmdPathIsSafe(shortPath) {
		return "", fmt.Errorf("Codex Hook 启动器路径无法安全传给 cmd.exe: %s", path)
	}
	return shortPath, nil
}

func projectManagerCodexHookCmdPathIsSafe(path string) bool {
	// 外层命令不能出现 CMD 会重解析的空白、引号或元字符；短路径不可用时直接报错，
	// 避免再次落回必现 0x80070002 的错误命令形态。
	return path != "" && !strings.ContainsAny(path, " \t\r\n\"&|<>()^%!")
}

func projectManagerCodexHookShortPath(path string) (string, error) {
	input, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}

	bufferSize := uint32(512)
	for attempt := 0; attempt < 2; attempt++ {
		buffer := make([]uint16, bufferSize)
		length, callErr := windows.GetShortPathName(input, &buffer[0], bufferSize)
		if length == 0 {
			if callErr != nil {
				return "", callErr
			}
			return "", errors.New("GetShortPathNameW 返回空路径")
		}
		if length < bufferSize {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		bufferSize = length + 1
	}
	return "", errors.New("Codex Hook 启动器短路径超过系统限制")
}
