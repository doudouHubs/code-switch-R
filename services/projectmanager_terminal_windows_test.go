//go:build windows

package services

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
)

type fakeProjectManagerCommandRunner struct {
	startErr error
	waitErr  error
	waitCh   <-chan struct{}
}

func (f fakeProjectManagerCommandRunner) Start() error {
	return f.startErr
}

func (f fakeProjectManagerCommandRunner) Wait() error {
	if f.waitCh != nil {
		<-f.waitCh
	}
	return f.waitErr
}

func TestBuildProjectManagerWTArgs(t *testing.T) {
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerLookPath = originalLookPath
	})
	projectManagerLookPath = func(file string) (string, error) {
		if file == "pwsh.exe" {
			return `E:\software\PowerShell7\7\pwsh.exe`, nil
		}
		return "", errors.New("not found")
	}

	launchDir := `F:\GitlabProjects\code-switch-R`
	sessionID := "session-001"
	runtimePath := `C:\Users\X1\.code-switch\project-manager-runtime\session-001.json`
	windowID := projectManagerProjectWindowID(launchDir)
	tabTitle := "[PM]session-001 - Session 001"
	tabIndex := 2

	got := buildProjectManagerWTArgs(launchDir, sessionID, runtimePath, windowID, tabTitle, tabIndex)
	want := []string{
		"-w", windowID,
		"new-tab",
		"-d", launchDir,
		"--title", tabTitle,
		"--",
		`E:\software\PowerShell7\7\pwsh.exe`,
		"-NoExit",
		"-EncodedCommand",
		encodeProjectManagerPowerShellCommand(buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath, windowID, tabTitle, tabIndex)),
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
			return `E:\software\PowerShell7\7\pwsh.exe`, nil
		}
		return "", errors.New("not found")
	}

	projectPath := `F:\GitlabProjects\code-switch-R`
	windowID := projectManagerProjectWindowID(projectPath)

	got := buildProjectManagerProjectTerminalWTArgs(projectPath, windowID)
	want := []string{
		"-w", windowID,
		"new-tab",
		"-d", projectPath,
		"--",
		`E:\software\PowerShell7\7\pwsh.exe`,
		"-NoExit",
		"-EncodedCommand",
		encodeProjectManagerPowerShellCommand(buildProjectManagerProjectTerminalPowerShellCommand(projectPath)),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("项目终端 WT 参数不对，want=%v got=%v", want, got)
	}
}

func TestBuildProjectManagerPowerShellLaunchCommand(t *testing.T) {
	sessionID := "session'o1"
	runtimePath := `C:\Users\X1\.code-switch\project-manager-runtime\session-o1.json`
	windowID := "codeswitch-project-deadbeef"
	tabTitle := "[PM]session'o1 - Alpha"
	tabIndex := 3

	got := buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath, windowID, tabTitle, tabIndex)
	expectedParts := []string{
		"$__codeSwitchRuntimePath = 'C:\\Users\\X1\\.code-switch\\project-manager-runtime\\session-o1.json'",
		"shell_pid = $PID",
		"shell_started_at = (Get-Process -Id $PID).StartTime.ToUniversalTime().ToString('o')",
		"launch_source = 'project-manager'",
		"window_id = 'codeswitch-project-deadbeef'",
		"tab_title = '[PM]session''o1 - Alpha'",
		"tab_index = 3",
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
	projectPath := `F:\GitlabProjects\code-switch-R`

	got := buildProjectManagerProjectTerminalPowerShellCommand(projectPath)
	expectedParts := []string{
		"$__codeSwitchCodexCommand = 'codex'",
		"Set-Location -LiteralPath 'F:\\GitlabProjects\\code-switch-R'",
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
	runtimePath := `C:\Users\X1\.code-switch\project-manager-runtime\session-encoded.json`
	windowID := "codeswitch-project-encoded"
	tabTitle := "[PM]session-encoded - Encoded"
	tabIndex := 1

	got := buildProjectManagerPowerShellCommandArgs("pwsh", sessionID, runtimePath, windowID, tabTitle, tabIndex)
	wantPrefix := []string{"pwsh", "-NoExit", "-EncodedCommand"}
	if !reflect.DeepEqual(got[:3], wantPrefix) {
		t.Fatalf("PowerShell 参数前缀不对，want=%v got=%v", wantPrefix, got[:3])
	}

	decoded := decodeProjectManagerPowerShellEncodedCommand(t, got[3])
	wantCommand := buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath, windowID, tabTitle, tabIndex)
	if decoded != wantCommand {
		t.Fatalf("EncodedCommand 解码后不对，want=%q got=%q", wantCommand, decoded)
	}
}

func TestBuildProjectManagerProjectTerminalCommandArgs(t *testing.T) {
	projectPath := `F:\GitlabProjects\code-switch-R`

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

func TestBuildProjectManagerWTLaunchCommand(t *testing.T) {
	wtPath := `C:\Users\X1\AppData\Local\Microsoft\WindowsApps\wt.exe`
	workingDir := `F:\GitlabProjects\code-switch-R`
	wtArgs := []string{
		"-w",
		"codeswitch-project-deadbeef",
		"new-tab",
		"-d",
		workingDir,
		"--",
		`E:\software\PowerShell7\7\pwsh.exe`,
		"-NoExit",
		"-EncodedCommand",
		"encoded-command",
	}

	got := buildProjectManagerWTLaunchCommand(wtPath, wtArgs, workingDir)
	expectedParts := []string{
		"Start-Process -FilePath 'C:\\Users\\X1\\AppData\\Local\\Microsoft\\WindowsApps\\wt.exe'",
		"-ArgumentList @(",
		"'-w'",
		"'codeswitch-project-deadbeef'",
		"'--'",
		"'E:\\software\\PowerShell7\\7\\pwsh.exe'",
		"'-EncodedCommand'",
		"'encoded-command'",
		"-WorkingDirectory 'F:\\GitlabProjects\\code-switch-R'",
	}
	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("WT launcher 命令缺少片段 %q，got=%q", part, got)
		}
	}
}

func TestBuildProjectManagerWTLauncherArgs(t *testing.T) {
	wtPath := `C:\Users\X1\AppData\Local\Microsoft\WindowsApps\wt.exe`
	workingDir := `F:\GitlabProjects\code-switch-R`
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
				return `E:\software\PowerShell7\7\pwsh.exe`, nil
			case "powershell.exe":
				return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
			default:
				return "", errors.New("not found")
			}
		}

		got := projectManagerPreferredShellExecutable()
		want := `E:\software\PowerShell7\7\pwsh.exe`
		if got != want {
			t.Fatalf("优先 shell 不对，want=%q got=%q", want, got)
		}
	})

	t.Run("fallback to powershell", func(t *testing.T) {
		projectManagerLookPath = func(file string) (string, error) {
			switch file {
			case "powershell.exe":
				return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
			default:
				return "", errors.New("not found")
			}
		}

		got := projectManagerPreferredShellExecutable()
		want := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
		if got != want {
			t.Fatalf("fallback shell 不对，want=%q got=%q", want, got)
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
	projectPath := `F:\GitlabProjects\code-switch-R`

	got := buildProjectManagerAICommitPowerShellCommand(projectPath)
	expectedParts := []string{
		"$__codeSwitchCodexCommand = 'codex'",
		"Volta\\bin\\codex.cmd",
		"Set-Location -LiteralPath 'F:\\GitlabProjects\\code-switch-R'",
		"& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox -p commit-fast exec '$commit commit本地文件'",
		"if ($__exitCode -eq 0) { exit 0 }",
		"Read-Host | Out-Null",
	}

	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("AI-Commit PowerShell 命令缺少片段 %q，got=%q", part, got)
		}
	}
}

func TestStartProjectManagerWTCommandUsesHiddenPowerShellLauncher(t *testing.T) {
	originalFactory := projectManagerWTCommandFactory
	originalLookPath := projectManagerLookPath
	t.Cleanup(func() {
		projectManagerWTCommandFactory = originalFactory
		projectManagerLookPath = originalLookPath
	})
	projectManagerLookPath = func(file string) (string, error) {
		if file == "powershell.exe" {
			return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
		}
		return "", errors.New("not found")
	}

	var captured *exec.Cmd
	projectManagerWTCommandFactory = func(name string, args ...string) *exec.Cmd {
		captured = exec.Command("cmd", "/c", "exit", "0")
		captured.Path = name
		captured.Args = append([]string{name}, args...)
		captured.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return captured
	}

	workingDir := `F:\GitlabProjects\code-switch-R`
	wtPath := `C:\Users\X1\AppData\Local\Microsoft\WindowsApps\wt.exe`
	wtArgs := []string{"-w", "codeswitch-project-deadbeef", "new-tab"}

	if err := startProjectManagerWTCommand(workingDir, wtPath, wtArgs); err != nil {
		t.Fatalf("期望 WT 直接启动成功，got err=%v", err)
	}
	if captured == nil {
		t.Fatalf("期望捕获到 WT 启动命令")
	}
	launcher := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	if captured.Path != launcher {
		t.Fatalf("WT 应通过隐藏 PowerShell launcher 启动，want=%q got=%q", launcher, captured.Path)
	}
	if captured.Dir != workingDir {
		t.Fatalf("launcher 工作目录不对，want=%q got=%q", workingDir, captured.Dir)
	}
	if len(captured.Args) != 4 || captured.Args[1] != "-NoProfile" || captured.Args[2] != "-EncodedCommand" {
		t.Fatalf("launcher 参数前缀不对，got=%v", captured.Args)
	}
	decoded := decodeProjectManagerPowerShellEncodedCommand(t, captured.Args[3])
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
			return `E:\software\PowerShell7\7\pwsh.exe`, nil
		}

		got, err := projectManagerRequiredPwshExecutable()
		if err != nil {
			t.Fatalf("期望成功解析 pwsh.exe，got err=%v", err)
		}
		want := `E:\software\PowerShell7\7\pwsh.exe`
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

func TestBuildProjectManagerAICommitLaunchCommand(t *testing.T) {
	projectPath := `F:\GitlabProjects\code-switch-R`
	shellExecutable := `E:\software\PowerShell7\7\pwsh.exe`

	got := buildProjectManagerAICommitLaunchCommand(projectPath, shellExecutable)
	expectedParts := []string{
		"Start-Process -FilePath 'E:\\software\\PowerShell7\\7\\pwsh.exe'",
		"-ArgumentList @(",
		"'-NoProfile'",
		"'-ExecutionPolicy'",
		"'Bypass'",
		"'-Command'",
		"& $__codeSwitchCodexCommand --dangerously-bypass-approvals-and-sandbox -p commit-fast exec ''$commit commit本地文件''",
		"-WorkingDirectory 'F:\\GitlabProjects\\code-switch-R'",
	}

	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("AI-Commit 启动命令缺少片段 %q，got=%q", part, got)
		}
	}
}

func TestBuildProjectManagerAICommitLauncherArgs(t *testing.T) {
	projectPath := `F:\GitlabProjects\code-switch-R`
	shellExecutable := `E:\software\PowerShell7\7\pwsh.exe`

	got := buildProjectManagerAICommitLauncherArgs(projectPath, shellExecutable)
	wantPrefix := []string{"-NoProfile", "-EncodedCommand"}
	if !reflect.DeepEqual(got[:2], wantPrefix) {
		t.Fatalf("AI-Commit launcher 参数前缀不对，want=%v got=%v", wantPrefix, got[:2])
	}

	decoded := decodeProjectManagerPowerShellEncodedCommand(t, got[2])
	wantCommand := buildProjectManagerAICommitLaunchCommand(projectPath, shellExecutable)
	if decoded != wantCommand {
		t.Fatalf("AI-Commit launcher EncodedCommand 解码后不对，want=%q got=%q", wantCommand, decoded)
	}
}

func TestStartProjectManagerAICommitTerminalUsesHiddenPwshLauncher(t *testing.T) {
	originalLookPath := projectManagerLookPath
	originalCommandContext := projectManagerAICommitCommandFactory
	t.Cleanup(func() {
		projectManagerLookPath = originalLookPath
		projectManagerAICommitCommandFactory = originalCommandContext
	})

	projectManagerLookPath = func(file string) (string, error) {
		if file != "pwsh.exe" {
			t.Fatalf("期望只查找 pwsh.exe，got=%q", file)
		}
		return `E:\software\PowerShell7\7\pwsh.exe`, nil
	}

	var captured *exec.Cmd
	projectManagerAICommitCommandFactory = func(name string, args ...string) *exec.Cmd {
		captured = exec.Command("cmd", "/c", "exit", "0")
		captured.Path = name
		captured.Args = append([]string{name}, args...)
		captured.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return captured
	}

	projectPath := `F:\GitlabProjects\code-switch-R`
	if err := startProjectManagerAICommitTerminal(projectPath); err != nil {
		t.Fatalf("期望启动入口构造成功，got err=%v", err)
	}
	if captured == nil {
		t.Fatalf("期望捕获到启动命令")
	}
	if captured.Path != `E:\software\PowerShell7\7\pwsh.exe` {
		t.Fatalf("AI-Commit 启动器不对，want=%q got=%q", `E:\software\PowerShell7\7\pwsh.exe`, captured.Path)
	}
	if captured.Dir != projectPath {
		t.Fatalf("AI-Commit 工作目录不对，want=%q got=%q", projectPath, captured.Dir)
	}
	if len(captured.Args) != 4 {
		t.Fatalf("AI-Commit launcher 参数数量不对，got=%v", captured.Args)
	}
	if captured.Args[1] != "-NoProfile" || captured.Args[2] != "-EncodedCommand" {
		t.Fatalf("AI-Commit launcher 参数前缀不对，got=%v", captured.Args)
	}
	if captured.SysProcAttr == nil || !captured.SysProcAttr.HideWindow {
		t.Fatalf("AI-Commit 外层启动器必须隐藏窗口，got=%+v", captured.SysProcAttr)
	}
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
			projectPath: `F:\GitlabProjects\code-switch-R`,
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
	projectPath := `F:\GitlabProjects\code-switch-R`
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

func TestFocusProjectManagerNamedWTTabReturnsQuicklyAfterStart(t *testing.T) {
	originalFactory := projectManagerExecCommand
	t.Cleanup(func() {
		projectManagerExecCommand = originalFactory
	})

	waitCh := make(chan struct{})
	var started atomic.Int32
	projectManagerExecCommand = func(name string, args ...string) projectManagerCommandRunner {
		started.Add(1)
		return fakeProjectManagerCommandRunner{
			waitCh: waitCh,
		}
	}

	runtime := projectManagerSessionRuntime{
		WindowID: "codeswitch-project-001",
		TabTitle: "[PM]session-001 - Alpha",
		TabIndex: 1,
	}
	session := SessionSummary{
		ID: "session-001",
	}

	startedAt := time.Now()
	if err := focusProjectManagerNamedWTTab("wt.exe", runtime, session); err != nil {
		t.Fatalf("期望快速返回成功，got err=%v", err)
	}
	if started.Load() != 1 {
		t.Fatalf("期望仅启动一次 WT 命令，got=%d", started.Load())
	}
	if elapsed := time.Since(startedAt); elapsed > projectManagerWTFocusTimeout*2 {
		t.Fatalf("focus-tab 返回太慢，elapsed=%s", elapsed)
	}

	close(waitCh)
}

func TestFocusProjectManagerNamedWTTabReturnsErrorWhenStartFails(t *testing.T) {
	originalFactory := projectManagerExecCommand
	t.Cleanup(func() {
		projectManagerExecCommand = originalFactory
	})

	projectManagerExecCommand = func(name string, args ...string) projectManagerCommandRunner {
		return fakeProjectManagerCommandRunner{
			startErr: errors.New("boom"),
		}
	}

	runtime := projectManagerSessionRuntime{
		WindowID: "codeswitch-project-001",
		TabTitle: "[PM]session-001 - Alpha",
	}
	session := SessionSummary{
		ID: "session-001",
	}

	err := focusProjectManagerNamedWTTab("wt.exe", runtime, session)
	if err == nil || !strings.Contains(err.Error(), "启动失败") {
		t.Fatalf("期望启动失败错误，got=%v", err)
	}
}

func TestFocusProjectManagerNamedWTTabReturnsErrorWhenWaitFailsImmediately(t *testing.T) {
	originalFactory := projectManagerExecCommand
	t.Cleanup(func() {
		projectManagerExecCommand = originalFactory
	})

	projectManagerExecCommand = func(name string, args ...string) projectManagerCommandRunner {
		return fakeProjectManagerCommandRunner{
			waitErr: errors.New("wait boom"),
		}
	}

	runtime := projectManagerSessionRuntime{
		WindowID: "codeswitch-project-001",
		TabTitle: "[PM]session-001 - Alpha",
	}
	session := SessionSummary{
		ID: "session-001",
	}

	err := focusProjectManagerNamedWTTab("wt.exe", runtime, session)
	if err == nil || !strings.Contains(err.Error(), "执行失败") {
		t.Fatalf("期望执行失败错误，got=%v", err)
	}
}

func TestTryReuseProjectManagerSessionTerminalSkipsInactiveRuntimeBeforeFocus(t *testing.T) {
	home := setupProjectManagerTestHome(t)
	service := NewProjectManagerService()
	sessionID := "session-inactive"
	runtimePath := filepath.Join(home, ".code-switch", "project-manager-runtime", sessionID+".json")
	runtimeContent := `{"session_id":"session-inactive","shell_pid":45678,"shell_started_at":"2026-06-16T10:02:47.6262548Z","launch_source":"project-manager","window_id":"codeswitch-project-deadbeef","tab_title":"[PM]session-inactive - Dead","tab_index":0}`
	if err := AtomicWriteText(runtimePath, runtimeContent); err != nil {
		t.Fatalf("写入 runtime fixture 失败: %v", err)
	}

	originalSnapshot := projectManagerSnapshotProcesses
	originalFactory := projectManagerExecCommand
	t.Cleanup(func() {
		projectManagerSnapshotProcesses = originalSnapshot
		projectManagerExecCommand = originalFactory
	})

	projectManagerSnapshotProcesses = func() (map[uint32]projectManagerProcessEntry, error) {
		return map[uint32]projectManagerProcessEntry{}, nil
	}

	focusAttempted := false
	projectManagerExecCommand = func(name string, args ...string) projectManagerCommandRunner {
		focusAttempted = true
		return fakeProjectManagerCommandRunner{}
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
	if focusAttempted {
		t.Fatalf("失效 runtime 不该继续尝试 focus-tab")
	}
	if _, statErr := os.Stat(runtimePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("失效 runtime 应被清理，statErr=%v", statErr)
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
