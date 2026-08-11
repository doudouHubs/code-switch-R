//go:build windows

package services

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestBuildProjectManagerWTArgs(t *testing.T) {
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerLookPath = originalLookPath
	})
	projectManagerLookPath = func(file string) (string, error) {
		if file == "pwsh.exe" {
			return `C:\Tools\PowerShell\7\pwsh.exe`, nil
		}
		return "", errors.New("not found")
	}

	launchDir := `C:\workspace\code-switch-test`
	windowID := projectManagerProjectWindowID(launchDir)
	tabTitle := "[PM]session-001 - Session 001"
	wrapperPath := `C:\Users\TestUser\.code-switch\project-manager-terminal-wrappers\session-001.cmd`

	got := buildProjectManagerWTArgs(launchDir, wrapperPath, windowID, tabTitle)
	want := []string{
		"-w", windowID,
		"new-tab",
		"-d", launchDir,
		"--title", tabTitle,
		"--",
		wrapperPath,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WT 参数不对，want=%v got=%v", want, got)
	}
}

func TestBuildProjectManagerProjectTerminalWTArgs(t *testing.T) {
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerLookPath = originalLookPath
	})
	projectManagerLookPath = func(file string) (string, error) {
		if file == "pwsh.exe" {
			return `C:\Tools\PowerShell\7\pwsh.exe`, nil
		}
		return "", errors.New("not found")
	}

	projectPath := `C:\workspace\code-switch-test`
	windowID := projectManagerProjectWindowID(projectPath)
	wrapperPath := `C:\Users\TestUser\.code-switch\project-manager-terminal-wrappers\project.cmd`

	got := buildProjectManagerProjectTerminalWTArgs(projectPath, windowID, wrapperPath)
	want := []string{
		"-w", windowID,
		"new-tab",
		"-d", projectPath,
		"--",
		wrapperPath,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("项目终端 WT 参数不对，want=%v got=%v", want, got)
	}
}

func TestBuildProjectManagerProjectTaskWTArgsUsesWrapperBoundary(t *testing.T) {
	projectPath := `C:\workspace\code-switch-test`
	windowID := projectManagerProjectWindowID(projectPath)
	tabTitle := "[PM]AI-Commit - code-switch-cli"
	wrapperPath := `C:\Users\TestUser\.code-switch\project-manager-terminal-wrappers\ai-commit.cmd`

	got := buildProjectManagerProjectTaskWTArgs(projectPath, windowID, tabTitle, wrapperPath)
	want := []string{
		"-w", windowID,
		"new-tab",
		"-d", projectPath,
		"--title", tabTitle,
		"--",
		wrapperPath,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("项目任务 WT 参数不对，want=%v got=%v", want, got)
	}
}

func TestProjectManagerProjectTaskTabTitles(t *testing.T) {
	projectPath := `C:\workspace\code-switch-cli`
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "project run", got: projectManagerProjectRunTabTitle(projectPath), want: "[PM]Run - code-switch-cli"},
		{name: "AI-Commit", got: projectManagerAICommitTabTitle(projectPath), want: "[PM]AI-Commit - code-switch-cli"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("项目任务 tab 标题不对，want=%q got=%q", test.want, test.got)
			}
		})
	}
}

func TestBuildProjectManagerPowerShellLaunchCommand(t *testing.T) {
	sessionID := "session'o1"
	runtimePath := `C:\Users\TestUser\.code-switch\project-manager-runtime\session-o1.json`
	windowID := "codeswitch-project-deadbeef"
	tabTitle := "[PM]session'o1 - Alpha"

	got := buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath, windowID, tabTitle)
	expectedParts := []string{
		"$__codeSwitchRuntimePath = 'C:\\Users\\TestUser\\.code-switch\\project-manager-runtime\\session-o1.json'",
		"shell_pid = $PID",
		"shell_started_at = (Get-Process -Id $PID).StartTime.ToUniversalTime().ToString('o')",
		"launch_source = 'project-manager'",
		"window_id = 'codeswitch-project-deadbeef'",
		"tab_title = '[PM]session''o1 - Alpha'",
		"$__codeSwitchCodexCommand = 'codex'",
		"Volta\\bin\\codex.cmd",
		"Set-Content -LiteralPath $__codeSwitchRuntimePath -Encoding utf8 -ErrorAction Stop",
		"& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox resume 'session''o1'",
		"Remove-Item -LiteralPath $__codeSwitchRuntimePath -Force -ErrorAction SilentlyContinue",
	}

	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("PowerShell 启动命令缺少片段 %q，got=%q", part, got)
		}
	}
}

func TestBuildProjectManagerProjectTerminalPowerShellCommand(t *testing.T) {
	projectPath := `C:\workspace\code-switch-test`

	got := buildProjectManagerProjectTerminalPowerShellCommand(projectPath)
	expectedParts := []string{
		"$__codeSwitchCodexCommand = 'codex'",
		"Set-Location -LiteralPath 'C:\\workspace\\code-switch-test'",
		"& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox",
	}
	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("项目终端 PowerShell 命令缺少片段 %q，got=%q", part, got)
		}
	}
	if strings.Contains(got, "resume") {
		t.Fatalf("项目终端命令不该包含 resume，got=%q", got)
	}
}

func TestBuildProjectManagerPowerShellCommandArgs(t *testing.T) {
	sessionID := "session-encoded"
	runtimePath := `C:\Users\TestUser\.code-switch\project-manager-runtime\session-encoded.json`
	windowID := "codeswitch-project-encoded"
	tabTitle := "[PM]session-encoded - Encoded"

	got := buildProjectManagerPowerShellCommandArgs("pwsh", sessionID, runtimePath, windowID, tabTitle)
	wantPrefix := []string{"pwsh", "-NoExit", "-EncodedCommand"}
	if !reflect.DeepEqual(got[:3], wantPrefix) {
		t.Fatalf("PowerShell 参数前缀不对，want=%v got=%v", wantPrefix, got[:3])
	}

	decoded := decodeProjectManagerPowerShellEncodedCommand(t, got[3])
	wantCommand := buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath, windowID, tabTitle)
	if decoded != wantCommand {
		t.Fatalf("EncodedCommand 解码后不对，want=%q got=%q", wantCommand, decoded)
	}
}

func TestBuildProjectManagerProjectTerminalCommandArgs(t *testing.T) {
	projectPath := `C:\workspace\code-switch-test`

	got := buildProjectManagerProjectTerminalCommandArgs("pwsh", projectPath)
	wantPrefix := []string{"pwsh", "-NoExit", "-EncodedCommand"}
	if !reflect.DeepEqual(got[:3], wantPrefix) {
		t.Fatalf("项目终端参数前缀不对，want=%v got=%v", wantPrefix, got[:3])
	}

	decoded := decodeProjectManagerPowerShellEncodedCommand(t, got[3])
	wantCommand := buildProjectManagerProjectTerminalPowerShellCommand(projectPath)
	if decoded != wantCommand {
		t.Fatalf("项目终端 EncodedCommand 解码后不对，want=%q got=%q", wantCommand, decoded)
	}
}

func TestBuildProjectManagerPowerShellFileArgs(t *testing.T) {
	scriptPath := `C:\Users\TestUser\.code-switch\project-manager-terminal-scripts\project.ps1`

	got := buildProjectManagerPowerShellFileArgs(`C:\Tools\PowerShell\7\pwsh.exe`, scriptPath)
	want := []string{
		`C:\Tools\PowerShell\7\pwsh.exe`,
		"-NoExit",
		"-ExecutionPolicy",
		"Bypass",
		"-File",
		scriptPath,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PowerShell File 参数不对，want=%v got=%v", want, got)
	}
}

func TestBuildProjectManagerTerminalScriptContent(t *testing.T) {
	command := "Set-Location -LiteralPath 'C:\\workspace\\code-switch-test'; codex"

	got := buildProjectManagerTerminalScriptContent(command)
	expectedParts := []string{
		"$ErrorActionPreference = 'Stop'",
		command,
	}
	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("终端脚本缺少片段 %q，got=%q", part, got)
		}
	}
	if !strings.Contains(got, "\r\n") {
		t.Fatalf("Windows 终端脚本应使用 CRLF，got=%q", got)
	}
}

func TestBuildProjectManagerProjectCommandPowerShellCommand(t *testing.T) {
	projectPath := `C:\workspace\code-switch-test`
	userCommand := "npm run dev\npnpm test -- --watch"

	got := buildProjectManagerProjectCommandPowerShellCommand(projectPath, userCommand)
	expectedParts := []string{
		"Set-Location -LiteralPath 'C:\\workspace\\code-switch-test'",
		"npm run dev",
		"pnpm test -- --watch",
	}
	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("项目运行脚本缺少片段 %q，got=%q", part, got)
		}
	}
	if !strings.Contains(got, "\r\n") {
		t.Fatalf("项目运行脚本应使用 CRLF 分隔主步骤，got=%q", got)
	}
}

func TestBuildProjectManagerTerminalWrapperContent(t *testing.T) {
	got := buildProjectManagerTerminalWrapperContent(
		`C:\Tools\PowerShell\7\pwsh.exe`,
		`C:\Users\TestUser\.code-switch\project-manager-terminal-scripts\project.ps1`,
	)
	expectedParts := []string{
		"@echo off",
		`call "C:\Tools\PowerShell\7\pwsh.exe" -NoExit -ExecutionPolicy Bypass -File "C:\Users\TestUser\.code-switch\project-manager-terminal-scripts\project.ps1"`,
		"exit /b %ERRORLEVEL%",
	}
	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("终端 wrapper 缺少片段 %q，got=%q", part, got)
		}
	}
}

func TestBuildProjectManagerWTArgsOnlyPassesWrapperToWT(t *testing.T) {
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerLookPath = originalLookPath
	})
	projectManagerLookPath = func(file string) (string, error) {
		if file == "pwsh.exe" {
			return `C:\Tools\PowerShell\7\pwsh.exe`, nil
		}
		return "", errors.New("not found")
	}

	got := buildProjectManagerWTArgs(
		`C:\workspace\code-switch-test`,
		`C:\Users\TestUser\.code-switch\project-manager-terminal-wrappers\project.cmd`,
		"codeswitch-project-deadbeef",
		"[PM]session-001 - Alpha",
	)
	joined := strings.Join(got, " ")
	for _, forbidden := range []string{"pwsh.exe", "-NoExit", "-EncodedCommand", "-ExecutionPolicy", "-File"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("WT 参数不应再直接携带 %s，got=%v", forbidden, got)
		}
	}
	if got[len(got)-1] != `C:\Users\TestUser\.code-switch\project-manager-terminal-wrappers\project.cmd` {
		t.Fatalf("WT 尾部只应接收 wrapper.cmd，got=%v", got)
	}
}

func TestBuildProjectManagerWTLaunchCommand(t *testing.T) {
	wtPath := `C:\Users\TestUser\AppData\Local\Microsoft\WindowsApps\wt.exe`
	workingDir := `C:\workspace\code-switch-test`
	wtArgs := []string{
		"-w",
		"codeswitch-project-deadbeef",
		"new-tab",
		"-d",
		workingDir,
		"--",
		`C:\Users\TestUser\.code-switch\project-manager-terminal-wrappers\project.cmd`,
	}

	got := buildProjectManagerWTLaunchCommand(wtPath, wtArgs, workingDir)
	expectedParts := []string{
		"Set-Location -LiteralPath 'C:\\workspace\\code-switch-test'",
		"$__codeSwitchWT = 'C:\\Users\\TestUser\\AppData\\Local\\Microsoft\\WindowsApps\\wt.exe'",
		"$__codeSwitchWTArgs = @(",
		"& $__codeSwitchWT @__codeSwitchWTArgs",
		"'-w'",
		"'codeswitch-project-deadbeef'",
		"'--'",
		"'C:\\Users\\TestUser\\.code-switch\\project-manager-terminal-wrappers\\project.cmd'",
	}
	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("WT launcher 命令缺少片段 %q，got=%q", part, got)
		}
	}
}

func TestBuildProjectManagerWTLauncherArgs(t *testing.T) {
	wtPath := `C:\Users\TestUser\AppData\Local\Microsoft\WindowsApps\wt.exe`
	workingDir := `C:\workspace\code-switch-test`
	wtArgs := []string{"-w", "codeswitch-project-deadbeef", "new-tab"}

	got := buildProjectManagerWTLauncherArgs(wtPath, wtArgs, workingDir)
	wantPrefix := []string{"-NoProfile", "-EncodedCommand"}
	if !reflect.DeepEqual(got[:2], wantPrefix) {
		t.Fatalf("WT launcher 参数前缀不对，want=%v got=%v", wantPrefix, got[:2])
	}

	decoded := decodeProjectManagerPowerShellEncodedCommand(t, got[2])
	want := buildProjectManagerWTLaunchCommand(wtPath, wtArgs, workingDir)
	if decoded != want {
		t.Fatalf("WT launcher EncodedCommand 解码后不对，want=%q got=%q", want, decoded)
	}
}

func TestProjectManagerPreferredShellExecutable(t *testing.T) {
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerLookPath = originalLookPath
	})

	t.Run("prefer pwsh when available", func(t *testing.T) {
		projectManagerLookPath = func(file string) (string, error) {
			switch file {
			case "pwsh.exe":
				return `C:\Tools\PowerShell\7\pwsh.exe`, nil
			case "powershell.exe":
				return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
			default:
				return "", errors.New("not found")
			}
		}

		got := projectManagerPreferredShellExecutable()
		want := `C:\Tools\PowerShell\7\pwsh.exe`
		if got != want {
			t.Fatalf("优先 shell 不对，want=%q got=%q", want, got)
		}
	})

	t.Run("does not fallback to powershell", func(t *testing.T) {
		projectManagerLookPath = func(file string) (string, error) {
			if file == "powershell.exe" {
				return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
			}
			return "", errors.New("not found")
		}

		got := projectManagerPreferredShellExecutable()
		want := `pwsh.exe`
		if got != want {
			t.Fatalf("缺少 pwsh 时不应回退 powershell.exe，want=%q got=%q", want, got)
		}
	})
}

func TestBuildProjectManagerPowerShellResumeCommand(t *testing.T) {
	got := buildProjectManagerPowerShellResumeCommand("session'o1")
	want := "& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox resume 'session''o1'"
	if got != want {
		t.Fatalf("resume 命令不对，want=%q got=%q", want, got)
	}
}

func TestBuildProjectManagerAICommitPowerShellCommand(t *testing.T) {
	projectPath := `C:\workspace\code-switch-test`

	got := buildProjectManagerAICommitPowerShellCommand(projectPath)
	expectedParts := []string{
		"$__codeSwitchCodexCommand = 'codex'",
		"Volta\\bin\\codex.cmd",
		"Set-Location -LiteralPath 'C:\\workspace\\code-switch-test'",
		"& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox -p commit-fast exec --ephemeral '$commit 无人值守提交本地文件。",
		"禁止询问用户或等待确认",
		"用户已通过 AI-Commit 按钮明确授权",
		"使用 -ForceIgnored 精确提交",
		"跳过该 ignored 文件并继续提交其余可提交变更",
		"if ($__exitCode -eq 0) { exit 0 }",
		"Read-Host | Out-Null",
	}

	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("AI-Commit PowerShell 命令缺少片段 %q，got=%q", part, got)
		}
	}
}

func TestProjectManagerWTLauncherExecutableUsesPwshOnly(t *testing.T) {
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerLookPath = originalLookPath
	})

	projectManagerLookPath = func(file string) (string, error) {
		if file != "pwsh.exe" {
			t.Fatalf("WT launcher 只允许查找 pwsh.exe，got=%q", file)
		}
		return `C:\Tools\PowerShell\7\pwsh.exe`, nil
	}

	got, err := projectManagerWTLauncherExecutable()
	if err != nil {
		t.Fatalf("期望解析 pwsh launcher 成功，got err=%v", err)
	}
	if got != `C:\Tools\PowerShell\7\pwsh.exe` {
		t.Fatalf("WT launcher 不对，want=%q got=%q", `C:\Tools\PowerShell\7\pwsh.exe`, got)
	}
}

func TestStartProjectManagerWTCommandUsesPwshLauncherStarter(t *testing.T) {
	originalStarter := projectManagerWTCommandStarter
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerWTCommandStarter = originalStarter
		projectManagerLookPath = originalLookPath
	})
	projectManagerLookPath = func(file string) (string, error) {
		if file == "pwsh.exe" {
			return `C:\Tools\PowerShell\7\pwsh.exe`, nil
		}
		return "", errors.New("not found")
	}

	var capturedWorkingDir string
	var capturedName string
	var capturedArgs []string
	projectManagerWTCommandStarter = func(workingDir string, name string, args ...string) error {
		capturedWorkingDir = workingDir
		capturedName = name
		capturedArgs = append([]string(nil), args...)
		return nil
	}

	workingDir := `C:\workspace\code-switch-test`
	wtPath := `C:\Users\TestUser\AppData\Local\Microsoft\WindowsApps\wt.exe`
	wtArgs := []string{"-w", "codeswitch-project-deadbeef", "new-tab"}

	if err := startProjectManagerWTCommand(workingDir, wtPath, wtArgs); err != nil {
		t.Fatalf("期望 WT 直接启动成功，got err=%v", err)
	}
	if capturedName == "" {
		t.Fatalf("期望捕获到 WT 启动命令")
	}
	launcher := `C:\Tools\PowerShell\7\pwsh.exe`
	if capturedName != launcher {
		t.Fatalf("WT 应通过隐藏 pwsh launcher 启动，want=%q got=%q", launcher, capturedName)
	}
	if capturedWorkingDir != workingDir {
		t.Fatalf("launcher 工作目录不对，want=%q got=%q", workingDir, capturedWorkingDir)
	}
	if len(capturedArgs) != 3 || capturedArgs[0] != "-NoProfile" || capturedArgs[1] != "-EncodedCommand" {
		t.Fatalf("launcher 参数前缀不对，got=%v", capturedArgs)
	}
	decoded := decodeProjectManagerPowerShellEncodedCommand(t, capturedArgs[2])
	wantCommand := buildProjectManagerWTLaunchCommand(wtPath, wtArgs, workingDir)
	if decoded != wantCommand {
		t.Fatalf("launcher EncodedCommand 解码后不对，want=%q got=%q", wantCommand, decoded)
	}
}

func TestProjectManagerRequiredPwshExecutable(t *testing.T) {
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerLookPath = originalLookPath
	})

	t.Run("returns resolved pwsh path", func(t *testing.T) {
		projectManagerLookPath = func(file string) (string, error) {
			if file != "pwsh.exe" {
				t.Fatalf("期望只查找 pwsh.exe，got=%q", file)
			}
			return `C:\Tools\PowerShell\7\pwsh.exe`, nil
		}

		got, err := projectManagerRequiredPwshExecutable()
		if err != nil {
			t.Fatalf("期望成功解析 pwsh.exe，got err=%v", err)
		}
		want := `C:\Tools\PowerShell\7\pwsh.exe`
		if got != want {
			t.Fatalf("pwsh 路径不对，want=%q got=%q", want, got)
		}
	})

	t.Run("returns error when pwsh missing", func(t *testing.T) {
		projectManagerLookPath = func(file string) (string, error) {
			if file != "pwsh.exe" {
				t.Fatalf("期望只查找 pwsh.exe，got=%q", file)
			}
			return "", errors.New("not found")
		}

		_, err := projectManagerRequiredPwshExecutable()
		if err == nil || !strings.Contains(err.Error(), "pwsh.exe") {
			t.Fatalf("期望提示缺少 pwsh.exe，got=%v", err)
		}
	})
}

func TestOpenProjectManagerProjectTerminalRejectsMissingDirectory(t *testing.T) {
	service := NewProjectManagerService()

	err := service.openProjectManagerProjectTerminal(`F:\not-exists\project`)
	if err == nil || !strings.Contains(err.Error(), "项目路径不存在或不是目录") {
		t.Fatalf("期望目录校验失败，got=%v", err)
	}
}

func TestProjectManagerProjectWindowID(t *testing.T) {
	tests := []struct {
		name        string
		projectPath string
		wantEmpty   bool
	}{
		{
			name:        "normal project path",
			projectPath: `C:\workspace\code-switch-test`,
		},
		{
			name:        "blank project path",
			projectPath: "   ",
			wantEmpty:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectManagerProjectWindowID(tt.projectPath)
			if tt.wantEmpty {
				if got != "" {
					t.Fatalf("期望空 window id，got=%q", got)
				}
				return
			}
			if !strings.HasPrefix(got, "codeswitch-project-") {
				t.Fatalf("project window id 前缀不对，got=%q", got)
			}
		})
	}
}

func TestProjectManagerProjectWindowIDStableWithinProject(t *testing.T) {
	projectPath := `C:\workspace\code-switch-test`
	first := projectManagerProjectWindowID(projectPath)
	second := projectManagerProjectWindowID(filepath.Clean(projectPath))
	if first != second {
		t.Fatalf("同项目 window id 应稳定一致，first=%q second=%q", first, second)
	}
}

func TestProjectManagerSessionTabTitle(t *testing.T) {
	session := SessionSummary{
		ID:          "session-001",
		DisplayName: "Alpha",
	}

	got := projectManagerSessionTabTitle(session)
	want := "[PM]session-001 - Alpha"
	if got != want {
		t.Fatalf("tab 标题不对，want=%q got=%q", want, got)
	}
}

func TestValidateProjectManagerSessionRuntimeRejectsNonProjectManagerSource(t *testing.T) {
	runtime := projectManagerSessionRuntime{
		SessionID:      "session-001",
		ShellPID:       100,
		ShellStartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		LaunchSource:   "external",
	}
	processes := map[uint32]projectManagerProcessEntry{
		100: {
			PID:       100,
			ParentPID: 90,
			ExeFile:   "pwsh.exe",
		},
	}

	err := validateProjectManagerSessionRuntime(runtime, processes)
	if !errors.Is(err, errProjectManagerRuntimeInactive) {
		t.Fatalf("期望 runtime 被判失效，got=%v", err)
	}
}

func TestBuildProjectManagerWindowTitleHints(t *testing.T) {
	runtime := projectManagerSessionRuntime{
		WindowID: "codeswitch-project-001",
	}
	session := SessionSummary{
		ID:          "session-001",
		DisplayName: "My Session",
		ProjectName: "My Project",
		SourceName:  "My Session",
	}

	got := buildProjectManagerWindowTitleHints(runtime, session)
	want := []string{
		"codeswitch-project-001",
		"session-001",
		"My Session",
		"My Project",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("窗口标题提示词不对，want=%v got=%v", want, got)
	}
}

func TestProjectManagerWindowTitleScore(t *testing.T) {
	hints := []string{
		"codeswitch-project-001",
		"session-001",
		"My Session",
	}

	tests := []struct {
		name  string
		title string
		want  int
	}{
		{
			name:  "exact window id match wins",
			title: "codeswitch-project-001",
			want:  100,
		},
		{
			name:  "contains display name uses fallback score",
			title: "X1 | My Session",
			want:  78,
		},
		{
			name:  "irrelevant title scores zero",
			title: "another window",
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectManagerWindowTitleScore(tt.title, hints)
			if got != tt.want {
				t.Fatalf("窗口标题得分不对，want=%d got=%d", tt.want, got)
			}
		})
	}
}

func TestResolveProjectManagerWTWindowName(t *testing.T) {
	tests := []struct {
		name     string
		windowID string
		want     string
	}{
		{
			name:     "keep named window",
			windowID: "codeswitch-project-001",
			want:     "codeswitch-project-001",
		},
		{
			name:     "blank falls back to new",
			windowID: "   ",
			want:     "new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveProjectManagerWTWindowName(tt.windowID)
			if got != tt.want {
				t.Fatalf("WT window name 不对，want=%q got=%q", tt.want, got)
			}
		})
	}
}

func TestTryReuseProjectManagerSessionTerminalSkipsInactiveRuntimeBeforeFocus(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()
	sessionID := "session-inactive"
	runtimePath := filepath.Join(home, ".code-switch", "project-manager-runtime", sessionID+".json")
	runtimeContent := `{"session_id":"session-inactive","shell_pid":45678,"shell_started_at":"2026-06-16T10:02:47.6262548Z","launch_source":"project-manager","window_id":"codeswitch-project-deadbeef","tab_title":"[PM]session-inactive - Dead","tab_runtime_id":[42,7866700,4,3974]}`
	if err := AtomicWriteText(runtimePath, runtimeContent); err != nil {
		t.Fatalf("写入 runtime fixture 失败: %v", err)
	}

	originalSnapshot := projectManagerSnapshotProcesses
	t.Cleanup(func() {
		projectManagerSnapshotProcesses = originalSnapshot
	})

	projectManagerSnapshotProcesses = func() (map[uint32]projectManagerProcessEntry, error) {
		return map[uint32]projectManagerProcessEntry{}, nil
	}

	reused, err := service.tryReuseProjectManagerSessionTerminal(SessionSummary{
		ID: "session-inactive",
	})
	if err != nil {
		t.Fatalf("预期失效 runtime 直接回退重开，不该报错: %v", err)
	}
	if reused {
		t.Fatalf("失效 runtime 不该被当成已复用")
	}
	if _, statErr := os.Stat(runtimePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("失效 runtime 应被清理，statErr=%v", statErr)
	}
}

func TestTryReuseProjectManagerSessionTerminalRejectsLegacyRuntimeWithoutStableTabID(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()
	sessionID := "session-legacy"
	runtimePath := filepath.Join(home, ".code-switch", "project-manager-runtime", sessionID+".json")
	runtimeContent := `{"session_id":"session-legacy","shell_pid":45678,"launch_source":"project-manager","window_id":"codeswitch-project-deadbeef","tab_title":"[PM]session-legacy - Old"}`
	if err := AtomicWriteText(runtimePath, runtimeContent); err != nil {
		t.Fatalf("写入 runtime fixture 失败: %v", err)
	}

	originalSnapshot := projectManagerSnapshotProcesses
	t.Cleanup(func() {
		projectManagerSnapshotProcesses = originalSnapshot
	})
	projectManagerSnapshotProcesses = func() (map[uint32]projectManagerProcessEntry, error) {
		return map[uint32]projectManagerProcessEntry{
			45678: {PID: 45678, ExeFile: "pwsh.exe"},
		}, nil
	}

	reused, err := service.tryReuseProjectManagerSessionTerminal(SessionSummary{ID: sessionID})
	if reused {
		t.Fatal("缺少稳定 tab 身份的旧 runtime 不应被当成已精确复用")
	}
	if err == nil || !strings.Contains(err.Error(), "无法精确定位") {
		t.Fatalf("旧 runtime 应返回明确定位错误，got=%v", err)
	}
}

func decodeProjectManagerPowerShellEncodedCommand(t *testing.T, encoded string) string {
	t.Helper()

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("EncodedCommand base64 解码失败: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("EncodedCommand 字节长度非法: %d", len(raw))
	}

	words := make([]uint16, 0, len(raw)/2)
	for index := 0; index < len(raw); index += 2 {
		words = append(words, uint16(raw[index])|uint16(raw[index+1])<<8)
	}
	return string(utf16.Decode(words))
}
