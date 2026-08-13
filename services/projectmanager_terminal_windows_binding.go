//go:build windows

package services

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	projectManagerTerminalTabBindTimeout = 3 * time.Second
	projectManagerTerminalTabBindPoll    = 50 * time.Millisecond
)

type projectManagerTerminalTabBaseline struct {
	Known bool
	Tabs  []projectManagerTerminalTabRef
}

func (s *ProjectManagerService) snapshotProjectManagerProjectTerminalTabs(
	session SessionSummary,
	runtimes map[string]projectManagerSessionRuntime,
) projectManagerTerminalTabBaseline {
	projectWindowID := projectManagerProjectWindowID(projectManagerSessionLaunchDir(session))
	if projectWindowID == "" {
		return projectManagerTerminalTabBaseline{}
	}

	processes, err := projectManagerSnapshotProcesses()
	if err != nil {
		log.Printf("[ProjectManager] 启动前读取 WT tab 快照失败 project_window=%s err=%v", projectWindowID, err)
		return projectManagerTerminalTabBaseline{}
	}

	for _, candidate := range runtimes {
		if !strings.EqualFold(strings.TrimSpace(candidate.LaunchSource), projectManagerRuntimeLaunchSource) ||
			!strings.EqualFold(strings.TrimSpace(candidate.WindowID), projectWindowID) {
			continue
		}

		windowHandle, _, err := findProjectManagerTerminalWindowWithProcesses(candidate, session, processes)
		if err != nil {
			continue
		}
		tabs, err := projectManagerReadTerminalTabs(windowHandle)
		if err != nil {
			log.Printf("[ProjectManager] 启动前读取 WT tab 失败 project_window=%s err=%v", projectWindowID, err)
			return projectManagerTerminalTabBaseline{}
		}
		return projectManagerTerminalTabBaseline{Known: true, Tabs: tabs}
	}

	return projectManagerTerminalTabBaseline{}
}

func bindProjectManagerSessionTerminalTab(
	sessionID string,
	runtimePath string,
	windowID string,
	initialTabTitle string,
	baseline projectManagerTerminalTabBaseline,
) {
	deadline := time.Now().Add(projectManagerTerminalTabBindTimeout)
	for time.Now().Before(deadline) {
		runtime, exists, err := loadProjectManagerSessionRuntimeIfExists(sessionID)
		if err != nil {
			log.Printf("[ProjectManager] 读取会话 tab 绑定运行态失败 session=%s err=%v", sessionID, err)
			return
		}
		if !exists {
			time.Sleep(projectManagerTerminalTabBindPoll)
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(runtime.WindowID), strings.TrimSpace(windowID)) {
			log.Printf("[ProjectManager] 会话 tab 绑定窗口不匹配 session=%s runtime_window=%s expected_window=%s", sessionID, runtime.WindowID, windowID)
			return
		}
		if projectManagerTerminalTabRuntimeIDAvailable(runtime.TabRuntimeID) {
			return
		}

		processes, err := projectManagerSnapshotProcesses()
		if err != nil {
			time.Sleep(projectManagerTerminalTabBindPoll)
			continue
		}
		if err := validateProjectManagerSessionRuntime(runtime, processes); err != nil {
			if errors.Is(err, errProjectManagerRuntimeInactive) {
				return
			}
			time.Sleep(projectManagerTerminalTabBindPoll)
			continue
		}

		windowHandle, _, err := findProjectManagerTerminalWindowWithProcesses(
			runtime,
			SessionSummary{ID: sessionID},
			processes,
		)
		if err != nil {
			time.Sleep(projectManagerTerminalTabBindPoll)
			continue
		}
		tabs, err := projectManagerReadTerminalTabs(windowHandle)
		if err != nil {
			time.Sleep(projectManagerTerminalTabBindPoll)
			continue
		}

		tab, found := projectManagerFindNewTerminalTab(baseline, tabs, initialTabTitle)
		if !found {
			time.Sleep(projectManagerTerminalTabBindPoll)
			continue
		}
		if err := updateProjectManagerSessionRuntimeTabRuntimeID(sessionID, runtime.ShellPID, tab.RuntimeID); err != nil {
			log.Printf("[ProjectManager] 写入会话稳定 tab 身份失败 session=%s runtime=%s err=%v", sessionID, runtimePath, err)
			return
		}
		log.Printf("[ProjectManager] 已绑定会话稳定 WT tab 身份 session=%s window=%s tab=%s title=%q", sessionID, windowID, projectManagerTerminalTabRuntimeIDKey(tab.RuntimeID), tab.Title)
		return
	}

	log.Printf("[ProjectManager] 未能在时限内绑定会话稳定 WT tab 身份 session=%s window=%s runtime=%s", sessionID, windowID, runtimePath)
}

func projectManagerFindNewTerminalTab(
	baseline projectManagerTerminalTabBaseline,
	current []projectManagerTerminalTabRef,
	initialTabTitle string,
) (projectManagerTerminalTabRef, bool) {
	// Codex 可能在启动后很快覆盖 WT 标题；先利用这段短暂窗口精确命中，
	// 再退到创建前后 UI Automation RuntimeId 的差集，二者都不会依赖可漂移索引。
	for _, tab := range current {
		if strings.TrimSpace(tab.Title) == strings.TrimSpace(initialTabTitle) &&
			projectManagerTerminalTabRuntimeIDAvailable(tab.RuntimeID) {
			return tab, true
		}
	}

	knownRuntimeIDs := make(map[string]struct{}, len(baseline.Tabs))
	for _, tab := range baseline.Tabs {
		if key := projectManagerTerminalTabRuntimeIDKey(tab.RuntimeID); key != "" {
			knownRuntimeIDs[key] = struct{}{}
		}
	}

	newTabs := make([]projectManagerTerminalTabRef, 0, len(current))
	for _, tab := range current {
		key := projectManagerTerminalTabRuntimeIDKey(tab.RuntimeID)
		if key == "" {
			continue
		}
		if _, existed := knownRuntimeIDs[key]; !existed {
			newTabs = append(newTabs, tab)
		}
	}
	if len(newTabs) == 1 {
		return newTabs[0], true
	}

	// 新项目窗口没有历史 tab，且当前只有一个 tab 时，它只能是刚创建的会话。
	if !baseline.Known && len(current) == 1 && projectManagerTerminalTabRuntimeIDAvailable(current[0].RuntimeID) {
		return current[0], true
	}

	return projectManagerTerminalTabRef{}, false
}

func focusProjectManagerBoundTerminalTab(
	runtime projectManagerSessionRuntime,
	session SessionSummary,
	processes map[uint32]projectManagerProcessEntry,
) error {
	if !projectManagerTerminalTabRuntimeIDAvailable(runtime.TabRuntimeID) {
		return errors.New("会话缺少稳定的 Windows Terminal tab 身份；请重新打开该会话")
	}

	windowHandle, windowTitle, err := findProjectManagerTerminalWindowWithProcesses(runtime, session, processes)
	if err != nil {
		return err
	}
	if err := projectManagerActivateWindow(windowHandle); err != nil {
		return err
	}
	if err := projectManagerSelectTerminalTab(windowHandle, runtime.TabRuntimeID); err != nil {
		return fmt.Errorf("按稳定 tab 身份定位失败: %w", err)
	}

	log.Printf(
		"[ProjectManager] 已精确恢复 WT 会话 tab session=%s shell_pid=%d hwnd=%#x title=%q tab=%s",
		session.ID,
		runtime.ShellPID,
		uintptr(windowHandle),
		windowTitle,
		projectManagerTerminalTabRuntimeIDKey(runtime.TabRuntimeID),
	)
	return nil
}
