//go:build windows

package services

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
)

type projectManagerWindowTarget struct {
	Raw         string
	WindowToken string
	TabIndex    int
	HasTabIndex bool
}

func (s *ProjectManagerService) openProjectManagerSessionTerminal(session SessionSummary) error {
	launchDir := strings.TrimSpace(session.ProjectPath)
	if launchDir == "" {
		launchDir = strings.TrimSpace(session.Cwd)
	}
	if launchDir == "" || !filepath.IsAbs(launchDir) {
		launchDir = "."
	}

	wtPath := findProjectManagerWTExecutable()
	reused, err := s.tryReuseProjectManagerSessionTerminal(session, wtPath)
	if err != nil {
		return err
	}
	if reused {
		return nil
	}

	runtimePath, err := projectManagerSessionRuntimePath(session.ID)
	if err != nil {
		return err
	}

	// 这里继续沿用已经验证可用的 wt 打开路径，只在 shell 启动命令最前面挂一层运行态标记。
	// 这么做是为了准确判断“这个会话现在是否真的开着”，避免点卡片时反复新开重复终端。
	if wtPath != "" {
		args := buildProjectManagerWTArgs(launchDir, session.ID, runtimePath)
		cmd := exec.Command(wtPath, args...)
		cmd.Dir = launchDir
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Start(); err == nil {
			return nil
		}
	}

	return startProjectManagerFallbackTerminal(launchDir, session.ID, runtimePath)
}

func (s *ProjectManagerService) tryReuseProjectManagerSessionTerminal(session SessionSummary, wtPath string) (bool, error) {
	runtime, err := loadProjectManagerSessionRuntime(session.ID)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	if err := focusProjectManagerTerminalWindow(runtime); err != nil {
		// 运行态文件只代表“上次是这个 shell 拉起的会话”，并不保证窗口还活着；
		// 一旦 shell pid 已失效或 pid 被系统复用，必须先清理脏标记，避免之后每次点击都误判。
		if errors.Is(err, errProjectManagerRuntimeInactive) {
			_ = removeProjectManagerSessionRuntime(session.ID)
			return false, nil
		}
		return false, err
	}

	if wtPath != "" {
		target := parseProjectManagerWindowTarget(session.WindowID)
		if target.HasTabIndex {
			// 先把真实 Terminal 窗口切到前台，再用 `wt -w 0 focus-tab` 命中当前最活跃窗口。
			// 这样无需再赌历史 window token 是否仍然有效，tab 聚焦失败也不会影响窗口复用本身。
			_ = focusProjectManagerTerminalTab(wtPath, target.TabIndex)
		}
	}

	return true, nil
}

func buildProjectManagerWTArgs(launchDir string, sessionID string, runtimePath string) []string {
	return append([]string{
		"new-tab",
		"-d", launchDir,
	}, buildProjectManagerPowerShellCommandArgs("pwsh", sessionID, runtimePath)...)
}

func focusProjectManagerTerminalTab(wtPath string, tabIndex int) error {
	if strings.TrimSpace(wtPath) == "" || tabIndex < 0 {
		return nil
	}

	cmd := exec.Command(wtPath, "-w", "0", "focus-tab", "-t", strconv.Itoa(tabIndex))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func startProjectManagerFallbackTerminal(launchDir string, sessionID string, runtimePath string) error {
	fallbackShell := "powershell.exe"
	if pwshPath, err := exec.LookPath("pwsh.exe"); err == nil && strings.TrimSpace(pwshPath) != "" {
		fallbackShell = pwshPath
	}

	innerArgs := buildProjectManagerPowerShellCommandArgs(fallbackShell, sessionID, runtimePath)
	quotedInnerArgs := make([]string, 0, len(innerArgs))
	for _, arg := range innerArgs[1:] {
		quotedInnerArgs = append(quotedInnerArgs, fmt.Sprintf("'%s'", escapeProjectManagerPowerShellSingleQuoted(arg)))
	}

	// wt 不可用时退回直接启动 shell，并继续使用同一套运行态标记，保证复用判断口径一致。
	cmd := hideWindowCmd(
		"powershell.exe",
		"-NoProfile",
		"-WindowStyle", "Hidden",
		"-Command",
		fmt.Sprintf(
			"Start-Process -FilePath '%s' -ArgumentList %s -WorkingDirectory '%s'",
			escapeProjectManagerPowerShellSingleQuoted(innerArgs[0]),
			strings.Join(quotedInnerArgs, ","),
			escapeProjectManagerPowerShellSingleQuoted(launchDir),
		),
	)
	cmd.Dir = launchDir
	return cmd.Start()
}

func buildProjectManagerPowerShellCommandArgs(shell string, sessionID string, runtimePath string) []string {
	return []string{
		shell,
		"-NoExit",
		"-EncodedCommand",
		encodeProjectManagerPowerShellCommand(buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath)),
	}
}

func buildProjectManagerPowerShellLaunchCommand(sessionID string, runtimePath string) string {
	resumeCommand := buildProjectManagerPowerShellResumeCommand(sessionID)
	trimmedRuntimePath := strings.TrimSpace(runtimePath)
	if trimmedRuntimePath == "" {
		return resumeCommand
	}

	escapedRuntimePath := escapeProjectManagerPowerShellSingleQuoted(trimmedRuntimePath)
	escapedSessionID := escapeProjectManagerPowerShellSingleQuoted(strings.TrimSpace(sessionID))

	parts := []string{
		fmt.Sprintf("$__codeSwitchRuntimePath = '%s'", escapedRuntimePath),
		"$__codeSwitchRuntimeDir = [System.IO.Path]::GetDirectoryName($__codeSwitchRuntimePath)",
		"if (-not [string]::IsNullOrWhiteSpace($__codeSwitchRuntimeDir)) { [System.IO.Directory]::CreateDirectory($__codeSwitchRuntimeDir) | Out-Null }",
		fmt.Sprintf("$__codeSwitchRuntime = @{ session_id = '%s'; shell_pid = $PID; shell_started_at = (Get-Process -Id $PID).StartTime.ToUniversalTime().ToString('o') }", escapedSessionID),
		"try { $__codeSwitchRuntime | ConvertTo-Json -Compress | Set-Content -LiteralPath $__codeSwitchRuntimePath -Encoding utf8 -ErrorAction Stop } catch {}",
		fmt.Sprintf("try { %s } finally { Remove-Item -LiteralPath $__codeSwitchRuntimePath -Force -ErrorAction SilentlyContinue }", resumeCommand),
	}
	return strings.Join(parts, "; ")
}

func buildProjectManagerPowerShellResumeCommand(sessionID string) string {
	escaped := escapeProjectManagerPowerShellSingleQuoted(sessionID)
	return fmt.Sprintf("codex resume '%s'", escaped)
}

func encodeProjectManagerPowerShellCommand(command string) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}

	encodedRunes := utf16.Encode([]rune(command))
	buf := make([]byte, 0, len(encodedRunes)*2)
	for _, r := range encodedRunes {
		buf = append(buf, byte(r), byte(r>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func parseProjectManagerWindowTarget(raw string) projectManagerWindowTarget {
	target := projectManagerWindowTarget{
		Raw: strings.TrimSpace(raw),
	}
	if target.Raw == "" {
		return target
	}

	if splitAt := strings.LastIndex(target.Raw, ":"); splitAt > 0 && splitAt < len(target.Raw)-1 {
		if tabIndex, err := strconv.Atoi(strings.TrimSpace(target.Raw[splitAt+1:])); err == nil && tabIndex >= 0 {
			target.WindowToken = strings.TrimSpace(target.Raw[:splitAt])
			target.TabIndex = tabIndex
			target.HasTabIndex = true
			return target
		}
	}

	target.WindowToken = target.Raw
	return target
}

func findProjectManagerWTExecutable() string {
	candidates := []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WindowsApps", "wt.exe"),
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func escapeProjectManagerPowerShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, `'`, `''`)
}
