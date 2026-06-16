package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const projectManagerRuntimeDir = "project-manager-runtime"

type projectManagerSessionRuntime struct {
	SessionID      string `json:"session_id"`
	ShellPID       uint32 `json:"shell_pid"`
	ShellStartedAt string `json:"shell_started_at"`
}

func projectManagerSessionRuntimePath(sessionID string) (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("会话 ID 不能为空")
	}

	return filepath.Join(home, appSettingsDir, projectManagerRuntimeDir, sessionID+".json"), nil
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
