package services

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	projectManagerRuntimeDir          = "project-manager-runtime"
	projectManagerRuntimeLaunchSource = "project-manager"
)

type projectManagerSessionRuntime struct {
	SessionID      string `json:"session_id"`
	ShellPID       uint32 `json:"shell_pid"`
	ShellStartedAt string `json:"shell_started_at"`
	LaunchSource   string `json:"launch_source"`
	WindowID       string `json:"window_id"`
	TabTitle       string `json:"tab_title"`
	TabRuntimeID   []int  `json:"tab_runtime_id,omitempty"`
}

func projectManagerSessionRuntimeRootPath() (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, appSettingsDir, projectManagerRuntimeDir), nil
}

func projectManagerSessionRuntimePath(sessionID string) (string, error) {
	root, err := projectManagerSessionRuntimeRootPath()
	if err != nil {
		return "", err
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("会话 ID 不能为空")
	}

	return filepath.Join(root, sessionID+".json"), nil
}

func listProjectManagerSessionRuntimePaths() ([]string, error) {
	root, err := projectManagerSessionRuntimeRootPath()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		paths = append(paths, filepath.Join(root, entry.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func loadProjectManagerSessionRuntime(sessionID string) (projectManagerSessionRuntime, error) {
	path, err := projectManagerSessionRuntimePath(sessionID)
	if err != nil {
		return projectManagerSessionRuntime{}, err
	}

	var runtime projectManagerSessionRuntime
	if err := ReadJSONFile(path, &runtime); err != nil {
		return projectManagerSessionRuntime{}, err
	}
	return runtime, nil
}

func loadProjectManagerSessionRuntimeIfExists(sessionID string) (projectManagerSessionRuntime, bool, error) {
	runtime, err := loadProjectManagerSessionRuntime(sessionID)
	if err != nil {
		if os.IsNotExist(err) {
			return projectManagerSessionRuntime{}, false, nil
		}
		return projectManagerSessionRuntime{}, false, err
	}
	return runtime, true, nil
}

func removeProjectManagerSessionRuntime(sessionID string) error {
	path, err := projectManagerSessionRuntimePath(sessionID)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func updateProjectManagerSessionRuntimeTabRuntimeID(
	sessionID string,
	expectedShellPID uint32,
	tabRuntimeID []int,
) error {
	if len(tabRuntimeID) == 0 {
		return errors.New("Windows Terminal tab 身份不能为空")
	}

	runtimePath, err := projectManagerSessionRuntimePath(sessionID)
	if err != nil {
		return err
	}

	var runtime projectManagerSessionRuntime
	if err := ReadJSONFile(runtimePath, &runtime); err != nil {
		return err
	}
	if strings.TrimSpace(runtime.SessionID) != strings.TrimSpace(sessionID) {
		return errors.New("运行态会话 ID 不匹配")
	}
	if expectedShellPID != 0 && runtime.ShellPID != expectedShellPID {
		// 会话可能在绑定阶段被用户重新打开；不能把旧 tab 身份写进新 shell 的 runtime。
		return errors.New("运行态 shell 已变化，放弃写入旧 tab 身份")
	}

	runtime.TabRuntimeID = append([]int(nil), tabRuntimeID...)
	return AtomicWriteJSON(runtimePath, runtime)
}
