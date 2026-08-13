//go:build !windows

package services

import (
	"os"
	"syscall"
)

func resolveProjectManagerCodexAncestorProcess() (uint32, string, error) {
	return uint32(os.Getppid()), "", nil
}

func isProjectManagerCodexProcessAlive(pid uint32, _ string) bool {
	if pid == 0 {
		return false
	}
	process, err := os.FindProcess(int(pid))
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}
