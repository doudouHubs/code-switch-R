package services

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const projectManagerAppServerResponseTimeout = 30 * time.Second

type projectManagerAppServerRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type projectManagerAppServerRPCResponse struct {
	ID     json.RawMessage                  `json:"id"`
	Result json.RawMessage                  `json:"result"`
	Error  *projectManagerAppServerRPCError `json:"error"`
	Method string                           `json:"method"`
}

var (
	projectManagerAppServerCommandFactory   = hideWindowCmd
	projectManagerForkSessionWithAppServer  = forkProjectManagerSessionWithAppServer
	projectManagerOpenForkedSessionTerminal = func(service *ProjectManagerService, session SessionSummary) error {
		return service.openProjectManagerSessionTerminal(session)
	}
)

func forkProjectManagerSessionWithAppServer(sessionID string, lastTurnID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	lastTurnID = strings.TrimSpace(lastTurnID)
	if sessionID == "" {
		return "", errors.New("会话 ID 不能为空")
	}
	if lastTurnID == "" {
		return "", errors.New("fork turn_id 不能为空")
	}

	cmd := buildProjectManagerAppServerCommand()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 Codex app-server 失败: %w", err)
	}
	defer cleanupProjectManagerAppServerProcess(cmd, stdin)

	responses, scanErrs := scanProjectManagerAppServerStdout(stdout)
	if err := writeProjectManagerAppServerMessage(stdin, map[string]any{
		"id":     1,
		"method": "initialize",
		"params": map[string]any{
			"clientInfo": map[string]any{
				"name":    "code-switch-r",
				"title":   "Code Switch CLI",
				"version": "project-manager",
			},
			"capabilities": map[string]any{
				"experimentalApi":                true,
				"requestAttestation":             false,
				"mcpServerOpenaiFormElicitation": false,
				"optOutNotificationMethods": []string{
					"thread/started",
					"thread/tokenUsage/updated",
				},
			},
		},
	}); err != nil {
		return "", err
	}
	if _, err := readProjectManagerAppServerResponse(responses, scanErrs, 1, &stderr); err != nil {
		return "", err
	}

	if err := writeProjectManagerAppServerMessage(stdin, map[string]any{
		"method": "initialized",
		"params": map[string]any{},
	}); err != nil {
		return "", err
	}

	if err := writeProjectManagerAppServerMessage(stdin, map[string]any{
		"id":     2,
		"method": "thread/fork",
		"params": map[string]any{
			"threadId":     sessionID,
			"lastTurnId":   lastTurnID,
			"excludeTurns": true,
			"threadSource": "user",
		},
	}); err != nil {
		return "", err
	}
	response, err := readProjectManagerAppServerResponse(responses, scanErrs, 2, &stderr)
	if err != nil {
		return "", err
	}

	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return "", fmt.Errorf("解析 Codex fork 响应失败: %w", err)
	}
	forkedSessionID := strings.TrimSpace(result.Thread.ID)
	if forkedSessionID == "" {
		return "", errors.New("Codex fork 响应缺少新会话 ID")
	}
	return forkedSessionID, nil
}

func buildProjectManagerAppServerCommand() *exec.Cmd {
	codexExecutable := resolveProjectManagerCodexExecutable()
	args := []string{"app-server", "--stdio"}
	if runtime.GOOS == "windows" && projectManagerNeedsCmdShell(codexExecutable) {
		// Volta/npm shim 通常是 .cmd。后台 JSON-RPC 不能走可见终端脚本，
		// 但 Windows CreateProcess 对 .cmd 兼容性不如 cmd.exe 稳，所以这里只包一层隐藏 cmd。
		return projectManagerAppServerCommandFactory("cmd.exe", append([]string{"/D", "/C", codexExecutable}, args...)...)
	}
	return projectManagerAppServerCommandFactory(codexExecutable, args...)
}

func resolveProjectManagerCodexExecutable() string {
	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			voltaCodex := filepath.Join(localAppData, "Volta", "bin", "codex.cmd")
			if _, err := os.Stat(voltaCodex); err == nil {
				return voltaCodex
			}
		}
	}
	if resolved, err := exec.LookPath("codex"); err == nil && strings.TrimSpace(resolved) != "" {
		return resolved
	}
	return "codex"
}

func projectManagerNeedsCmdShell(path string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	return ext == ".cmd" || ext == ".bat"
}

func scanProjectManagerAppServerStdout(stdout io.Reader) (<-chan string, <-chan error) {
	lines := make(chan string, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(lines)
		defer close(errs)

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		errs <- scanner.Err()
	}()
	return lines, errs
}

func writeProjectManagerAppServerMessage(writer io.Writer, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("写入 Codex app-server 请求失败: %w", err)
	}
	return nil
}

func readProjectManagerAppServerResponse(
	lines <-chan string,
	scanErrs <-chan error,
	requestID int,
	stderr *bytes.Buffer,
) (projectManagerAppServerRPCResponse, error) {
	timer := time.NewTimer(projectManagerAppServerResponseTimeout)
	defer timer.Stop()

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				if err := <-scanErrs; err != nil {
					return projectManagerAppServerRPCResponse{}, fmt.Errorf("读取 Codex app-server 响应失败: %w%s", err, formatProjectManagerAppServerStderr(stderr))
				}
				return projectManagerAppServerRPCResponse{}, fmt.Errorf("Codex app-server 已退出但没有返回请求 %d 的响应%s", requestID, formatProjectManagerAppServerStderr(stderr))
			}

			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			var response projectManagerAppServerRPCResponse
			if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
				return projectManagerAppServerRPCResponse{}, fmt.Errorf("Codex app-server 返回了无效 JSON: %w; line=%s", err, trimmed)
			}
			if !projectManagerAppServerResponseIDMatches(response.ID, requestID) {
				continue
			}
			if response.Error != nil {
				return projectManagerAppServerRPCResponse{}, fmt.Errorf("Codex app-server 请求失败: code=%d message=%s%s", response.Error.Code, response.Error.Message, formatProjectManagerAppServerStderr(stderr))
			}
			return response, nil
		case err, ok := <-scanErrs:
			if ok && err != nil {
				return projectManagerAppServerRPCResponse{}, fmt.Errorf("读取 Codex app-server 响应失败: %w%s", err, formatProjectManagerAppServerStderr(stderr))
			}
		case <-timer.C:
			return projectManagerAppServerRPCResponse{}, fmt.Errorf("等待 Codex app-server 请求 %d 响应超时%s", requestID, formatProjectManagerAppServerStderr(stderr))
		}
	}
}

func projectManagerAppServerResponseIDMatches(raw json.RawMessage, requestID int) bool {
	if len(raw) == 0 {
		return false
	}
	var numericID int
	if err := json.Unmarshal(raw, &numericID); err == nil {
		return numericID == requestID
	}
	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil {
		return stringID == fmt.Sprintf("%d", requestID)
	}
	return false
}

func formatProjectManagerAppServerStderr(stderr *bytes.Buffer) string {
	if stderr == nil {
		return ""
	}
	text := strings.TrimSpace(stderr.String())
	if text == "" {
		return ""
	}
	return ": " + text
}

func cleanupProjectManagerAppServerProcess(cmd *exec.Cmd, stdin io.Closer) {
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
