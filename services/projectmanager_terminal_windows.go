//go:build windows

package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func (s *ProjectManagerService) openProjectManagerSessionTerminal(session SessionSummary) error {
	launchDir := strings.TrimSpace(session.ProjectPath)
	if launchDir == "" {
		launchDir = strings.TrimSpace(session.Cwd)
	}
	if launchDir == "" || !filepath.IsAbs(launchDir) {
		launchDir = "."
	}

	// 这里继续沿用已经验证可用的 wt 打开路径，只额外补一层最小 resume 命令。
	// 不再恢复之前那套 profile 解析/窗口复用复杂逻辑，避免再次把“打开终端”本身搞坏。
	if wtPath := findProjectManagerWTExecutable(); wtPath != "" {
		args := buildProjectManagerWTArgs(launchDir, session.ID)
		cmd := exec.Command(wtPath, args...)
		cmd.Dir = launchDir
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Start(); err == nil {
			return nil
		}
	}

	return startProjectManagerFallbackTerminal(launchDir, session.ID)
}

func buildProjectManagerWTArgs(launchDir string, sessionID string) []string {
	return []string{
		"new-tab",
		"-d", launchDir,
		"pwsh",
		"-NoExit",
		"-Command",
		buildProjectManagerPowerShellResumeCommand(sessionID),
	}
}

func startProjectManagerFallbackTerminal(launchDir string, sessionID string) error {
	fallbackShell := "powershell.exe"
	if pwshPath, err := exec.LookPath("pwsh.exe"); err == nil && strings.TrimSpace(pwshPath) != "" {
		fallbackShell = pwshPath
	}

	// wt 不可用时退回直接启动 shell，并在该 shell 中 resume 指定会话。
	cmd := hideWindowCmd(
		"powershell.exe",
		"-NoProfile",
		"-WindowStyle", "Hidden",
		"-Command",
		fmt.Sprintf(
			"Start-Process -FilePath '%s' -ArgumentList '-NoExit','-Command','%s' -WorkingDirectory '%s'",
			escapeProjectManagerPowerShellSingleQuoted(fallbackShell),
			escapeProjectManagerPowerShellSingleQuoted(buildProjectManagerPowerShellResumeCommand(sessionID)),
			escapeProjectManagerPowerShellSingleQuoted(launchDir),
		),
	)
	cmd.Dir = launchDir
	return cmd.Start()
}

func buildProjectManagerPowerShellResumeCommand(sessionID string) string {
	escaped := escapeProjectManagerPowerShellSingleQuoted(sessionID)
	return fmt.Sprintf("codex resume '%s'", escaped)
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
