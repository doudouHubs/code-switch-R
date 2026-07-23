//go:build windows

package services

import "testing"

func TestProjectManagerFindNewTerminalTabPrefersInitialTitle(t *testing.T) {
	baseline := projectManagerTerminalTabBaseline{
		Known: true,
		Tabs: []projectManagerTerminalTabRef{
			{RuntimeID: []int{42, 7866700, 4, 3974}, Title: "Codex A"},
		},
	}
	current := []projectManagerTerminalTabRef{
		{RuntimeID: []int{42, 7866700, 4, 3974}, Title: "Codex A"},
		{RuntimeID: []int{42, 7866700, 4, 3975}, Title: "[PM]session-002 - Alpha"},
		{RuntimeID: []int{42, 7866700, 4, 3976}, Title: "Other new tab"},
	}

	got, found := projectManagerFindNewTerminalTab(baseline, current, "[PM]session-002 - Alpha")
	if !found {
		t.Fatal("启动标题仍可见时应精确绑定对应 tab")
	}
	if !projectManagerTerminalTabRuntimeIDsEqual(got.RuntimeID, []int{42, 7866700, 4, 3975}) {
		t.Fatalf("绑定了错误 tab: %+v", got)
	}
}

func TestProjectManagerFindNewTerminalTabUsesRuntimeIDDiffAfterTitleChanges(t *testing.T) {
	baseline := projectManagerTerminalTabBaseline{
		Known: true,
		Tabs: []projectManagerTerminalTabRef{
			{RuntimeID: []int{42, 7866700, 4, 3974}, Title: "Codex A"},
			{RuntimeID: []int{42, 7866700, 4, 3975}, Title: "Codex B"},
		},
	}
	// 模拟用户拖动了现有 tab，Codex 同时覆盖了新 tab 的初始 [PM] 标题。
	current := []projectManagerTerminalTabRef{
		{RuntimeID: []int{42, 7866700, 4, 3975}, Title: "Codex B"},
		{RuntimeID: []int{42, 7866700, 4, 3976}, Title: "code-switch-R | New task"},
		{RuntimeID: []int{42, 7866700, 4, 3974}, Title: "Codex A"},
	}

	got, found := projectManagerFindNewTerminalTab(baseline, current, "[PM]session-003 - New task")
	if !found {
		t.Fatal("标题变更后应按 RuntimeId 差集绑定新 tab")
	}
	if !projectManagerTerminalTabRuntimeIDsEqual(got.RuntimeID, []int{42, 7866700, 4, 3976}) {
		t.Fatalf("RuntimeId 差集选错 tab: %+v", got)
	}
}

func TestProjectManagerFindNewTerminalTabRejectsAmbiguousDiff(t *testing.T) {
	baseline := projectManagerTerminalTabBaseline{
		Known: true,
		Tabs: []projectManagerTerminalTabRef{
			{RuntimeID: []int{42, 7866700, 4, 3974}, Title: "Codex A"},
		},
	}
	current := []projectManagerTerminalTabRef{
		{RuntimeID: []int{42, 7866700, 4, 3974}, Title: "Codex A"},
		{RuntimeID: []int{42, 7866700, 4, 3975}, Title: "Unknown 1"},
		{RuntimeID: []int{42, 7866700, 4, 3976}, Title: "Unknown 2"},
	}

	if _, found := projectManagerFindNewTerminalTab(baseline, current, "[PM]session-004 - Missing"); found {
		t.Fatal("多个未知新 tab 时不应猜测绑定目标")
	}
}

func TestProjectManagerFindNewTerminalTabUsesOnlyTabInNewWindow(t *testing.T) {
	current := []projectManagerTerminalTabRef{
		{RuntimeID: []int{42, 7866700, 4, 3974}, Title: "code-switch-R | New task"},
	}

	got, found := projectManagerFindNewTerminalTab(projectManagerTerminalTabBaseline{}, current, "[PM]session-005 - New task")
	if !found || !projectManagerTerminalTabRuntimeIDsEqual(got.RuntimeID, current[0].RuntimeID) {
		t.Fatalf("新窗口唯一 tab 应可绑定，got=%+v found=%t", got, found)
	}
}
