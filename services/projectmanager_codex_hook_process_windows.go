//go:build windows

package services

import (
	"errors"
	"os"
	"strings"
	"time"
)

func resolveProjectManagerCodexAncestorProcess() (uint32, string, error) {
	processes, err := snapshotProjectManagerProcesses()
	if err != nil {
		return 0, "", err
	}

	current := uint32(os.Getpid())
	for depth := 0; depth < 24 && current != 0; depth++ {
		process, ok := processes[current]
		if !ok {
			break
		}
		name := strings.ToLower(strings.TrimSpace(process.ExeFile))
		if name == "codex.exe" || name == "codex" || strings.HasPrefix(name, "codex-") {
			startedAt, startErr := projectManagerProcessStartTime(process.PID)
			if startErr != nil {
				return process.PID, "", nil
			}
			return process.PID, startedAt.Format(time.RFC3339Nano), nil
		}
		if process.ParentPID == 0 || process.ParentPID == current {
			break
		}
		current = process.ParentPID
	}
	return 0, "", errors.New("未找到 Codex 祖先进程")
}

func isProjectManagerCodexProcessAlive(pid uint32, startedAt string) bool {
	if pid == 0 {
		return false
	}
	processes, err := snapshotProjectManagerProcesses()
	if err != nil {
		return false
	}
	process, ok := processes[pid]
	if !ok {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(process.ExeFile))
	if name != "codex.exe" && name != "codex" && !strings.HasPrefix(name, "codex-") {
		return false
	}
	expected, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(startedAt))
	if err != nil {
		return true
	}
	actual, err := projectManagerProcessStartTime(pid)
	if err != nil {
		return false
	}
	delta := actual.Sub(expected.UTC())
	if delta < 0 {
		delta = -delta
	}
	return delta <= 2*time.Second
}
