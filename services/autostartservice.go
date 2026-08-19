package services

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type AutoStartService struct{}

func NewAutoStartService() *AutoStartService {
	return &AutoStartService{}
}

// IsEnabled 检查是否已启用开机自启动
func (as *AutoStartService) IsEnabled() (bool, error) {
	switch runtime.GOOS {
	case "windows":
		return as.isEnabledWindows()
	case "darwin":
		return as.isEnabledDarwin()
	case "linux":
		return as.isEnabledLinux()
	default:
		return false, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// Enable 启用开机自启动
func (as *AutoStartService) Enable() error {
	switch runtime.GOOS {
	case "windows":
		return as.enableWindows()
	case "darwin":
		return as.enableDarwin()
	case "linux":
		return as.enableLinux()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// Disable 禁用开机自启动
func (as *AutoStartService) Disable() error {
	switch runtime.GOOS {
	case "windows":
		return as.disableWindows()
	case "darwin":
		return as.disableDarwin()
	case "linux":
		return as.disableLinux()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// Windows 实现
const (
	windowsRunKey             = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	windowsStartupApprovedKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
	windowsAutoStartValue     = "CodeSwitch"
)

func windowsRegExe() string {
	if windir := os.Getenv("WINDIR"); windir != "" {
		candidate := filepath.Join(windir, "System32", "reg.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "reg.exe"
}

func (as *AutoStartService) isEnabledWindows() (bool, error) {
	regExe := windowsRegExe()

	// GUI 进程不能让 reg.exe 继承 WT 控制台；否则子进程退出时 Windows
	// 可能把前台短暂落到 Wails 的隐藏消息窗口，表现为 WT 窗口闪烁置顶。
	cmd := hideWindowCmd(regExe, "query", windowsRunKey, "/v", windowsAutoStartValue)
	out, err := cmd.CombinedOutput()
	if err != nil {
		lowerOut := strings.ToLower(string(out))
		if strings.Contains(lowerOut, "unable to find") ||
			strings.Contains(lowerOut, "无法找到") ||
			strings.Contains(lowerOut, "找不到") {
			return false, nil
		}
		return false, fmt.Errorf("查询 Windows 自启动注册表失败: %w, 输出: %s",
			err, strings.TrimSpace(string(out)))
	}

	// 注册表里的启动项可能已经指向旧版 EXE；读取状态时必须确认它仍然属于当前程序。
	if exePath, exeErr := os.Executable(); exeErr == nil {
		if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = resolved
		}
		exePath = strings.TrimPrefix(exePath, `\\?\`)
		if !strings.Contains(strings.ToLower(string(out)), strings.ToLower(exePath)) {
			return false, nil
		}
	}

	// Windows 10/11 在任务管理器禁用启动项时会写入 StartupApproved 标记。
	approvedCmd := hideWindowCmd(regExe, "query", windowsStartupApprovedKey, "/v", windowsAutoStartValue)
	approvedOut, err := approvedCmd.CombinedOutput()
	if err == nil {
		outStr := string(approvedOut)
		if idx := strings.Index(strings.ToUpper(outStr), "REG_BINARY"); idx != -1 {
			hexPart := strings.TrimSpace(outStr[idx+len("REG_BINARY"):])
			if spaceIdx := strings.IndexAny(hexPart, " \t\r\n"); spaceIdx != -1 {
				hexPart = hexPart[:spaceIdx]
			}
			if len(hexPart) >= 2 && strings.EqualFold(hexPart[:2], "03") {
				return false, nil
			}
		}
	}

	// StartupApproved 不存在或解析失败时，沿用主线兼容策略，视为启用。
	return true, nil
}

func (as *AutoStartService) enableWindows() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	exePath = strings.TrimPrefix(exePath, `\\?\`)

	quotedPath := fmt.Sprintf(`"%s"`, exePath)
	regExe := windowsRegExe()
	cmd := hideWindowCmd(regExe, "add", windowsRunKey, "/v", windowsAutoStartValue,
		"/t", "REG_SZ", "/d", quotedPath, "/f")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add registry key: %w, output: %s",
			err, strings.TrimSpace(string(out)))
	}

	// 清理系统曾经写入的禁用标记，避免“已添加但任务管理器仍禁用”的假状态。
	_ = hideWindowCmd(regExe, "delete", windowsStartupApprovedKey, "/v", windowsAutoStartValue, "/f").Run()
	return nil
}

func (as *AutoStartService) disableWindows() error {
	regExe := windowsRegExe()
	cmd := hideWindowCmd(regExe, "delete", windowsRunKey, "/v", windowsAutoStartValue, "/f")
	// 忽略不存在的错误
	_ = cmd.Run()
	return nil
}

// macOS 实现
func (as *AutoStartService) isEnabledDarwin() (bool, error) {
	plistPath := as.getDarwinPlistPath()
	_, err := os.Stat(plistPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (as *AutoStartService) enableDarwin() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	plistPath := as.getDarwinPlistPath()
	plistDir := filepath.Dir(plistPath)
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		return fmt.Errorf("failed to create launch agents directory: %w", err)
	}

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.codeswitch.app</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
</dict>
</plist>`, exePath)

	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		return fmt.Errorf("failed to write plist file: %w", err)
	}

	return nil
}

func (as *AutoStartService) disableDarwin() error {
	plistPath := as.getDarwinPlistPath()
	// 忽略不存在的错误
	_ = os.Remove(plistPath)
	return nil
}

func (as *AutoStartService) getDarwinPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "com.codeswitch.app.plist")
}

// Linux 实现 (使用 .desktop 文件)
func (as *AutoStartService) isEnabledLinux() (bool, error) {
	desktopPath := as.getLinuxDesktopPath()
	_, err := os.Stat(desktopPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (as *AutoStartService) enableLinux() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	desktopPath := as.getLinuxDesktopPath()
	desktopDir := filepath.Dir(desktopPath)
	if err := os.MkdirAll(desktopDir, 0o755); err != nil {
		return fmt.Errorf("failed to create autostart directory: %w", err)
	}

	desktopContent := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=CodeSwitch
Exec=%s
Hidden=false
NoDisplay=false
X-GNOME-Autostart-enabled=true`, exePath)

	if err := os.WriteFile(desktopPath, []byte(desktopContent), 0o644); err != nil {
		return fmt.Errorf("failed to write desktop file: %w", err)
	}

	return nil
}

func (as *AutoStartService) disableLinux() error {
	desktopPath := as.getLinuxDesktopPath()
	// 忽略不存在的错误
	_ = os.Remove(desktopPath)
	return nil
}

func (as *AutoStartService) getLinuxDesktopPath() string {
	home, _ := os.UserHomeDir()
	// 优先使用 XDG_CONFIG_HOME，如果未设置则使用 ~/.config
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "autostart", "codeswitch.desktop")
}
