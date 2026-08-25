package services

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
)

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

	client, err := NewCodexAppServerClient(CodexAppServerClientOptions{
		CommandFactory: CodexAppServerCommandFactory(projectManagerAppServerCommandFactory),
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Call(context.Background(), "initialize", codexAppServerInitializeParams(
		"code-switch-r",
		"Code Switch CLI",
		"project-manager",
	)); err != nil {
		return "", err
	}
	if err := client.Notify("initialized", map[string]any{}); err != nil {
		return "", err
	}

	response, err := client.Call(context.Background(), "thread/fork", map[string]any{
		"threadId":     sessionID,
		"lastTurnId":   lastTurnID,
		"excludeTurns": true,
		"threadSource": "user",
	})
	if err != nil {
		return "", err
	}

	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return "", errors.New("解析 Codex fork 响应失败")
	}
	forkedSessionID := strings.TrimSpace(result.Thread.ID)
	if forkedSessionID == "" {
		return "", errors.New("Codex fork 响应缺少新会话 ID")
	}
	return forkedSessionID, nil
}

func buildProjectManagerAppServerCommand() *exec.Cmd {
	return buildCodexAppServerCommand(
		CodexAppServerCommandFactory(projectManagerAppServerCommandFactory),
		resolveProjectManagerCodexExecutable(),
	)
}

func resolveProjectManagerCodexExecutable() string {
	return resolveCodexExecutable()
}

func projectManagerNeedsCmdShell(path string) bool {
	return codexAppServerNeedsCmdShell(path)
}

func projectManagerAppServerResponseIDMatches(raw json.RawMessage, requestID int) bool {
	return codexAppServerResponseIDMatches(raw, int64(requestID))
}
