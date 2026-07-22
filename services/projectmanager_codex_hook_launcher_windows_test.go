//go:build windows

package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareProjectManagerCodexHookCommandCreatesCmdLauncher(t *testing.T) {
	home := setProjectManagerCodexTestHome(t)
	executable := filepath.Join(t.TempDir(), "CodeSwitch.exe")
	if err := os.WriteFile(executable, []byte("test executable"), 0o600); err != nil {
		t.Fatal(err)
	}

	command, err := prepareProjectManagerCodexHookCommand(executable)
	if err != nil {
		t.Fatalf("prepare hook command: %v", err)
	}
	launcherPath := filepath.Join(home, appSettingsDir, projectManagerCodexHookLauncherDir, projectManagerCodexHookLauncherName)
	safeLauncherPath, err := projectManagerCodexHookCmdSafePath(launcherPath)
	if err != nil {
		t.Fatalf("safe launcher path: %v", err)
	}
	wantCommand := safeLauncherPath + " " + projectManagerCodexHookCommandMarker
	if command != wantCommand {
		t.Fatalf("hook command = %q, want %q", command, wantCommand)
	}
	launcherToken, marker, found := strings.Cut(command, " ")
	if !found || marker != projectManagerCodexHookCommandMarker || !projectManagerCodexHookCmdPathIsSafe(launcherToken) {
		t.Fatalf("outer hook command must contain one unquoted safe CMD token: %q", command)
	}

	content, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	wantContent := buildProjectManagerCodexHookLauncherContent(executable)
	if string(content) != wantContent {
		t.Fatalf("launcher content = %q, want %q", content, wantContent)
	}
}

func TestProjectManagerCodexHookCmdPathIsSafe(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: `C:\Users\X1\.code-switch\CodeSwitch.codex-hook.cmd`, want: true},
		{path: `C:\Users\Jane Doe\.code-switch\CodeSwitch.codex-hook.cmd`, want: false},
		{path: `C:\work&unsafe\CodeSwitch.codex-hook.cmd`, want: false},
		{path: ``, want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := projectManagerCodexHookCmdPathIsSafe(test.path); got != test.want {
				t.Fatalf("safe(%q) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}
