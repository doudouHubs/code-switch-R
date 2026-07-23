//go:build windows

package services

import (
	"os"
	"strings"
	"testing"
)

func TestProjectManagerUIAutomationSmokeSelectsCurrentTerminalTab(t *testing.T) {
	if os.Getenv("CODESWITCH_UIA_SMOKE") != "1" {
		t.Skip("set CODESWITCH_UIA_SMOKE=1 to exercise the live Windows Terminal UI Automation bridge")
	}

	processes, err := projectManagerSnapshotProcesses()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if !strings.EqualFold(strings.TrimSpace(process.ExeFile), "WindowsTerminal.exe") {
			continue
		}

		windowHandle, windowTitle, err := findProjectManagerMainWindow(process.PID, nil)
		if err != nil {
			continue
		}
		tabs, err := listProjectManagerTerminalTabs(windowHandle)
		if err != nil || len(tabs) == 0 {
			continue
		}
		for _, tab := range tabs {
			t.Logf("候选 tab title=%q runtime_id=%s", tab.Title, projectManagerTerminalTabRuntimeIDKey(tab.RuntimeID))
			if projectManagerUIASmokeTitleKey(tab.Title) != projectManagerUIASmokeTitleKey(windowTitle) {
				continue
			}
			if err := selectProjectManagerTerminalTab(windowHandle, tab.RuntimeID); err != nil {
				t.Fatalf("UI Automation 选中当前 tab 失败: %v", err)
			}
			t.Logf("UI Automation 已选中当前 tab title=%q runtime_id=%s", tab.Title, projectManagerTerminalTabRuntimeIDKey(tab.RuntimeID))
			return
		}
	}

	t.Skip("未找到可安全重选的当前 Windows Terminal tab")
}

func projectManagerUIASmokeTitleKey(title string) string {
	parts := strings.Fields(strings.TrimSpace(title))
	if len(parts) == 0 {
		return ""
	}
	first := []rune(parts[0])
	if len(parts) > 1 && len(first) == 1 && first[0] >= 0x2800 && first[0] <= 0x28ff {
		// Codex 工作时会把 Braille 动画帧写进标题；该帧不属于 tab 身份。
		return strings.Join(parts[1:], " ")
	}
	return strings.Join(parts, " ")
}
