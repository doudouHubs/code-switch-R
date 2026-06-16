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

	// 用户要的是跟 Windows 右键“在终端打开”一致的原生行为，
	// 所以这里只负责把 Terminal 打开到目标目录，不再自作聪明追加 codex resume、profile 或窗口复用参数。
	if wtPath := findProjectManagerWTExecutable(); wtPath != "" {
		args := buildProjectManagerWTArgs(launchDir)
		cmd := exec.Command(wtPath, args...)
		cmd.Dir = launchDir
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Start(); err == nil {
			return nil
		}
	}

	return startProjectManagerFallbackTerminal(launchDir)
}

func buildProjectManagerWTArgs(launchDir string) []string {
	return []string{
		"new-tab",
		"-d", launchDir,
	}
}

func startProjectManagerFallbackTerminal(launchDir string) error {
	fallbackShell := "powershell.exe"
	if pwshPath, err := exec.LookPath("pwsh.exe"); err == nil && strings.TrimSpace(pwshPath) != "" {
		fallbackShell = pwshPath
	}

	// wt 不可用时也只做“打开到目录”这一件事，别再往里塞任何命令，保持行为单纯可预期。
	cmd := hideWindowCmd(
		"powershell.exe",
		"-NoProfile",
		"-WindowStyle", "Hidden",
		"-Command",
		fmt.Sprintf(
			"Start-Process -FilePath '%s' -WorkingDirectory '%s'",
			escapeProjectManagerPowerShellSingleQuoted(fallbackShell),
			escapeProjectManagerPowerShellSingleQuoted(launchDir),
		),
	)
	cmd.Dir = launchDir
	return cmd.Start()
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
