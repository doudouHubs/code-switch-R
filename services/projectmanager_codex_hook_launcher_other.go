//go:build !windows

package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 非 Windows 平台不存在 CMD 的整体引号解析缺陷，保留直接启动可执行文件的最短链路。
func prepareProjectManagerCodexHookCommand(executable string) (string, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return "", errors.New("CodeSwitch 可执行文件路径不能为空")
	}
	absPath, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("解析 CodeSwitch 可执行文件绝对路径失败: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("检查 CodeSwitch 可执行文件失败: %w", err)
	}
	if info.IsDir() {
		return "", errors.New("CodeSwitch 可执行文件路径不能是目录")
	}
	return `"` + strings.ReplaceAll(absPath, `"`, `\"`) + `" ` + projectManagerCodexHookCommandMarker, nil
}
