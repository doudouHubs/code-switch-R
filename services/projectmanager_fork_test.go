package services

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestProjectManagerForkAppServerRPCShape(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "requests.jsonl")

	originalFactory := projectManagerAppServerCommandFactory
	defer func() {
		projectManagerAppServerCommandFactory = originalFactory
	}()

	projectManagerAppServerCommandFactory = func(_ string, _ ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestProjectManagerForkAppServerFakeProcess$", "--", capturePath)
		cmd.Env = append(os.Environ(), "GO_WANT_PROJECT_MANAGER_FORK_HELPER=1")
		return cmd
	}

	forkedID, err := forkProjectManagerSessionWithAppServer(" source-thread ", " turn-2 ")
	if err != nil {
		t.Fatalf("forkProjectManagerSessionWithAppServer 失败: %v", err)
	}
	if forkedID != "forked-session-from-helper" {
		t.Fatalf("fork 返回的新会话 ID 不对，got=%q", forkedID)
	}

	messages := readProjectManagerForkCapturedRequests(t, capturePath)
	if len(messages) != 3 {
		t.Fatalf("app-server 请求数不对，want=3 got=%d requests=%v", len(messages), messages)
	}
	if messages[0]["method"] != "initialize" {
		t.Fatalf("第一条请求必须初始化 app-server，got=%v", messages[0]["method"])
	}
	if messages[1]["method"] != "initialized" {
		t.Fatalf("第二条请求必须发送 initialized 通知，got=%v", messages[1]["method"])
	}
	if messages[2]["method"] != "thread/fork" {
		t.Fatalf("第三条请求必须调用 thread/fork，got=%v", messages[2]["method"])
	}

	params, ok := messages[2]["params"].(map[string]any)
	if !ok {
		t.Fatalf("thread/fork params 类型不对: %#v", messages[2]["params"])
	}
	expectedParams := map[string]any{
		"threadId":     "source-thread",
		"lastTurnId":   "turn-2",
		"excludeTurns": true,
		"threadSource": "user",
	}
	for key, want := range expectedParams {
		if !reflect.DeepEqual(params[key], want) {
			t.Fatalf("thread/fork 参数 %s 不对，want=%#v got=%#v", key, want, params[key])
		}
	}
}

func TestProjectManagerForkAppServerFakeProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PROJECT_MANAGER_FORK_HELPER") != "1" {
		return
	}
	if len(os.Args) == 0 {
		os.Exit(2)
	}
	capturePath := os.Args[len(os.Args)-1]
	if err := runProjectManagerForkFakeAppServer(capturePath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestProjectManagerBuildAppServerCommandUsesCmdShellForWindowsShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上验证 .cmd shim 启动路径")
	}

	localAppData := filepath.Join(t.TempDir(), "Local App Data")
	shimPath := filepath.Join(localAppData, "Volta", "bin", "codex.cmd")
	if err := os.MkdirAll(filepath.Dir(shimPath), 0o755); err != nil {
		t.Fatalf("创建 Volta shim 目录失败: %v", err)
	}
	if err := os.WriteFile(shimPath, []byte("@echo off\r\n"), 0o600); err != nil {
		t.Fatalf("写入 Volta codex.cmd 失败: %v", err)
	}
	t.Setenv("LOCALAPPDATA", localAppData)

	originalFactory := projectManagerAppServerCommandFactory
	defer func() {
		projectManagerAppServerCommandFactory = originalFactory
	}()

	var gotName string
	var gotArgs []string
	projectManagerAppServerCommandFactory = func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return exec.Command(name, args...)
	}

	_ = buildProjectManagerAppServerCommand()

	if gotName != "cmd.exe" {
		t.Fatalf("Windows .cmd shim 必须通过 cmd.exe 启动，got=%q", gotName)
	}
	expectedArgs := []string{"/D", "/C", shimPath, "app-server", "--stdio"}
	if !reflect.DeepEqual(gotArgs, expectedArgs) {
		t.Fatalf("cmd.exe 参数不对，want=%v got=%v", expectedArgs, gotArgs)
	}
}

func runProjectManagerForkFakeAppServer(capturePath string) error {
	capture, err := os.OpenFile(capturePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer capture.Close()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if _, err := fmt.Fprintln(capture, line); err != nil {
			return err
		}

		var request map[string]any
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			return err
		}
		switch request["method"] {
		case "initialize":
			if _, err := fmt.Fprintln(os.Stdout, `{"id":1,"result":{"protocolVersion":"2026-01-01","capabilities":{}}}`); err != nil {
				return err
			}
		case "thread/fork":
			if _, err := fmt.Fprintln(os.Stdout, `{"id":2,"result":{"thread":{"id":"forked-session-from-helper"}}}`); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func readProjectManagerForkCapturedRequests(t *testing.T, capturePath string) []map[string]any {
	t.Helper()

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("读取捕获请求失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	messages := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(trimmed), &message); err != nil {
			t.Fatalf("解析捕获请求失败: %v line=%s", err, trimmed)
		}
		messages = append(messages, message)
	}
	return messages
}
