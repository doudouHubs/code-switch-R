//go:build windows

package services

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestBuildProjectManagerWTArgs(t *testing.T) {
	launchDir := `F:\GitlabProjects\code-switch-R`
	sessionID := "session-001"
	runtimePath := `C:\Users\X1\.code-switch\project-manager-runtime\session-001.json`

	got := buildProjectManagerWTArgs(launchDir, sessionID, runtimePath)
	want := []string{
		"new-tab",
		"-d", launchDir,
		"pwsh",
		"-NoExit",
		"-EncodedCommand",
		encodeProjectManagerPowerShellCommand(buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath)),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WT 参数不对，want=%v got=%v", want, got)
	}
}

func TestBuildProjectManagerPowerShellLaunchCommand(t *testing.T) {
	sessionID := "session'o1"
	runtimePath := `C:\Users\X1\.code-switch\project-manager-runtime\session-o1.json`

	got := buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath)
	expectedParts := []string{
		"$__codeSwitchRuntimePath = 'C:\\Users\\X1\\.code-switch\\project-manager-runtime\\session-o1.json'",
		"shell_pid = $PID",
		"shell_started_at = (Get-Process -Id $PID).StartTime.ToUniversalTime().ToString('o')",
		"Set-Content -LiteralPath $__codeSwitchRuntimePath -Encoding utf8 -ErrorAction Stop",
		"codex resume 'session''o1'",
		"Remove-Item -LiteralPath $__codeSwitchRuntimePath -Force -ErrorAction SilentlyContinue",
	}

	for _, part := range expectedParts {
		if !strings.Contains(got, part) {
			t.Fatalf("PowerShell 启动命令缺少片段 %q，got=%q", part, got)
		}
	}
}

func TestBuildProjectManagerPowerShellCommandArgs(t *testing.T) {
	sessionID := "session-encoded"
	runtimePath := `C:\Users\X1\.code-switch\project-manager-runtime\session-encoded.json`

	got := buildProjectManagerPowerShellCommandArgs("pwsh", sessionID, runtimePath)
	wantPrefix := []string{"pwsh", "-NoExit", "-EncodedCommand"}
	if !reflect.DeepEqual(got[:3], wantPrefix) {
		t.Fatalf("PowerShell 参数前缀不对，want=%v got=%v", wantPrefix, got[:3])
	}

	decoded := decodeProjectManagerPowerShellEncodedCommand(t, got[3])
	wantCommand := buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath)
	if decoded != wantCommand {
		t.Fatalf("EncodedCommand 解码后不对，want=%q got=%q", wantCommand, decoded)
	}
}

func TestBuildProjectManagerPowerShellResumeCommand(t *testing.T) {
	got := buildProjectManagerPowerShellResumeCommand("session'o1")
	want := "codex resume 'session''o1'"
	if got != want {
		t.Fatalf("resume 命令不对，want=%q got=%q", want, got)
	}
}

func TestParseProjectManagerWindowTarget(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want projectManagerWindowTarget
	}{
		{
			name: "window with tab index",
			raw:  "019ebad2-5eb6-7372-ba5b-dd74fa51ed53:0",
			want: projectManagerWindowTarget{
				Raw:         "019ebad2-5eb6-7372-ba5b-dd74fa51ed53:0",
				WindowToken: "019ebad2-5eb6-7372-ba5b-dd74fa51ed53",
				TabIndex:    0,
				HasTabIndex: true,
			},
		},
		{
			name: "custom token without tab",
			raw:  "codeswitch-session-019eca28-24df-7ad2-b2da-424f97f02511",
			want: projectManagerWindowTarget{
				Raw:         "codeswitch-session-019eca28-24df-7ad2-b2da-424f97f02511",
				WindowToken: "codeswitch-session-019eca28-24df-7ad2-b2da-424f97f02511",
			},
		},
		{
			name: "blank",
			raw:  "   ",
			want: projectManagerWindowTarget{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseProjectManagerWindowTarget(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("window target 解析不对，want=%+v got=%+v", tt.want, got)
			}
		})
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
