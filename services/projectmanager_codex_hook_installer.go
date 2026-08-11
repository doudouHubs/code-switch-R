package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const projectManagerCodexHookCommandMarker = "--codex-hook-event"

var projectManagerCodexVersionPattern = regexp.MustCompile(`(?i)codex(?:-cli)?\s+(\d+)\.(\d+)\.(\d+)`)

type projectManagerCodexVersion struct {
	Raw   string
	Major int
	Minor int
	Patch int
}

func installProjectManagerCodexHooks() (CodexStatusMonitorInfo, error) {
	version := detectProjectManagerCodexVersion()
	info := CodexStatusMonitorInfo{
		CodexVersion:        version.Raw,
		AgentHooksSupported: version.atLeast(0, 133, 0),
	}

	executable, err := projectManagerCodexCurrentExecutable()
	if err != nil {
		return info, fmt.Errorf("定位 CodeSwitch 可执行文件失败: %w", err)
	}
	command, err := prepareProjectManagerCodexHookCommand(executable)
	if err != nil {
		return info, fmt.Errorf("准备 Codex Hook 启动命令失败: %w", err)
	}
	codexHome, err := projectManagerCodexHomePath()
	if err != nil {
		return info, err
	}
	if err := mergeProjectManagerCodexHooks(filepath.Join(codexHome, "hooks.json"), command, info.AgentHooksSupported); err != nil {
		return info, err
	}
	if err := enableProjectManagerCodexHooksFeature(filepath.Join(codexHome, "config.toml"), version); err != nil {
		return info, err
	}

	info.Installed = true
	// 0.122 的 hooks 尚无信任层；0.131 起通过官方 app-server 接口只信任 CodeSwitch 自己的处理器。
	if version.atLeast(0, 131, 0) {
		if err := trustProjectManagerCodexHooks(codexHome); err != nil {
			info.Error = fmt.Sprintf("Hook 已安装，但自动信任失败: %v", err)
		}
	}
	return info, nil
}

func projectManagerCodexHomePath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
		return filepath.Abs(configured)
	}
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func detectProjectManagerCodexVersion() projectManagerCodexVersion {
	output, err := runProjectManagerCodexCommandOutput("--version")
	if err != nil {
		return projectManagerCodexVersion{}
	}
	text := strings.TrimSpace(string(output))
	match := projectManagerCodexVersionPattern.FindStringSubmatch(text)
	if len(match) != 4 {
		return projectManagerCodexVersion{Raw: text}
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	return projectManagerCodexVersion{Raw: text, Major: major, Minor: minor, Patch: patch}
}

func (v projectManagerCodexVersion) atLeast(major, minor, patch int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	return v.Patch >= patch
}

func runProjectManagerCodexCommandOutput(args ...string) ([]byte, error) {
	executable := resolveProjectManagerCodexExecutable()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" && projectManagerNeedsCmdShell(executable) {
		cmd = hideWindowCmd("cmd.exe", append([]string{"/D", "/C", executable}, args...)...)
	} else {
		cmd = hideWindowCmd(executable, args...)
	}
	return cmd.CombinedOutput()
}

func mergeProjectManagerCodexHooks(path, command string, agentHooksSupported bool) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("Codex Hook 启动命令不能为空")
	}

	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			// 配置损坏时必须 fail-fast，绝不能为了装状态灯覆盖用户的 hooks。
			return fmt.Errorf("解析 Codex hooks.json 失败: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 Codex hooks.json 失败: %w", err)
	}

	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		if root["hooks"] != nil {
			return errors.New("Codex hooks.json 的 hooks 字段不是对象")
		}
		hooks = map[string]any{}
		root["hooks"] = hooks
	}

	// PreToolUse 从 0.122 起已经可用，只有多 Agent 生命周期事件需要 0.133+。
	// 把它错误地和 Agent 能力绑在一起，会导致旧版永远无法显示“等待用户输入”。
	events := []string{"SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest", "PreToolUse", "PostToolUse"}
	if agentHooksSupported {
		events = append(events, "SubagentStart", "SubagentStop")
	}
	desired := make(map[string]struct{}, len(events))
	for _, eventName := range events {
		desired[eventName] = struct{}{}
	}

	for eventName, rawGroups := range hooks {
		groups, groupsOK := rawGroups.([]any)
		if !groupsOK {
			continue
		}
		cleaned := removeProjectManagerCodexHookHandlers(groups)
		if _, keep := desired[eventName]; keep {
			cleaned = append(cleaned, buildProjectManagerCodexHookGroup(eventName, command))
			delete(desired, eventName)
		}
		hooks[eventName] = cleaned
	}
	for eventName := range desired {
		hooks[eventName] = []any{buildProjectManagerCodexHookGroup(eventName, command)}
	}

	if description, ok := root["description"].(string); !ok || strings.TrimSpace(description) == "" {
		root["description"] = "Lifecycle hooks managed by CodeSwitch. Other user hooks are preserved."
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, encoded) {
		return nil
	}
	return AtomicWriteBytes(path, encoded)
}

func buildProjectManagerCodexHookGroup(eventName, command string) map[string]any {
	group := map[string]any{
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       command,
			"timeout":       2,
			"statusMessage": "Updating CodeSwitch status",
		}},
	}
	if eventName == "PreToolUse" {
		// 只监听 request_user_input，避免每次普通工具调用都额外启动一个 Hook 进程。
		group["matcher"] = "^request_user_input$"
	}
	return group
}

func removeProjectManagerCodexHookHandlers(groups []any) []any {
	cleaned := make([]any, 0, len(groups))
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			cleaned = append(cleaned, rawGroup)
			continue
		}
		handlers, ok := group["hooks"].([]any)
		if !ok {
			cleaned = append(cleaned, rawGroup)
			continue
		}
		keptHandlers := make([]any, 0, len(handlers))
		for _, rawHandler := range handlers {
			handler, isObject := rawHandler.(map[string]any)
			command, _ := handler["command"].(string)
			if isObject && strings.Contains(command, projectManagerCodexHookCommandMarker) {
				continue
			}
			keptHandlers = append(keptHandlers, rawHandler)
		}
		if len(keptHandlers) == 0 {
			continue
		}
		copyGroup := make(map[string]any, len(group))
		for key, value := range group {
			copyGroup[key] = value
		}
		copyGroup["hooks"] = keptHandlers
		cleaned = append(cleaned, copyGroup)
	}
	return cleaned
}

func enableProjectManagerCodexHooksFeature(path string, version projectManagerCodexVersion) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = nil
	} else if err != nil {
		return fmt.Errorf("读取 Codex config.toml 失败: %w", err)
	}
	var parsed map[string]any
	if len(bytes.TrimSpace(data)) > 0 {
		if err := toml.Unmarshal(data, &parsed); err != nil {
			return fmt.Errorf("解析 Codex config.toml 失败: %w", err)
		}
	}

	preferredKey := "codex_hooks"
	if version.atLeast(0, 131, 0) {
		preferredKey = "hooks"
	}
	updated := updateProjectManagerCodexFeatureText(string(data), preferredKey)
	if updated == string(data) {
		return nil
	}
	return AtomicWriteText(path, updated)
}

func updateProjectManagerCodexFeatureText(content, preferredKey string) string {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	featuresStart := -1
	featuresEnd := len(lines)
	foundFeature := false
	inFeatures := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inFeatures {
				featuresEnd = index
				inFeatures = false
			}
			if trimmed == "[features]" {
				featuresStart = index
				inFeatures = true
			}
			continue
		}
		if !inFeatures {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		if ok && (key == "codex_hooks" || key == "hooks") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[index] = indent + key + " = true"
			foundFeature = true
		}
	}
	if inFeatures {
		featuresEnd = len(lines)
	}
	if !foundFeature {
		entry := preferredKey + " = true"
		if featuresStart >= 0 {
			lines = append(lines[:featuresEnd], append([]string{entry}, lines[featuresEnd:]...)...)
		} else {
			if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
				lines = append(lines, "")
			}
			lines = append(lines, "[features]", entry)
		}
	}
	return strings.Join(lines, newline)
}

func trustProjectManagerCodexHooks(codexHome string) error {
	cmd := buildProjectManagerAppServerCommand()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer cleanupProjectManagerAppServerProcess(cmd, stdin)
	responses, scanErrs := scanProjectManagerAppServerStdout(stdout)

	if err := writeProjectManagerAppServerMessage(stdin, projectManagerCodexInitializeRequest(1)); err != nil {
		return err
	}
	if _, err := readProjectManagerAppServerResponse(responses, scanErrs, 1, &stderr); err != nil {
		return err
	}
	if err := writeProjectManagerAppServerMessage(stdin, map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	if err := writeProjectManagerAppServerMessage(stdin, map[string]any{
		"id": 2, "method": "hooks/list", "params": map[string]any{"cwds": []string{codexHome}},
	}); err != nil {
		return err
	}
	response, err := readProjectManagerAppServerResponse(responses, scanErrs, 2, &stderr)
	if err != nil {
		return err
	}

	var listed struct {
		Data []struct {
			Hooks []struct {
				Key         string `json:"key"`
				Command     string `json:"command"`
				CurrentHash string `json:"currentHash"`
				TrustStatus string `json:"trustStatus"`
			} `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		return err
	}
	trusts := map[string]any{}
	for _, entry := range listed.Data {
		for _, hook := range entry.Hooks {
			if !strings.Contains(hook.Command, projectManagerCodexHookCommandMarker) || hook.Key == "" || hook.CurrentHash == "" {
				continue
			}
			if hook.TrustStatus == "trusted" || hook.TrustStatus == "managed" {
				continue
			}
			trusts[hook.Key] = map[string]any{"trusted_hash": hook.CurrentHash}
		}
	}
	if len(trusts) == 0 {
		return nil
	}
	if err := writeProjectManagerAppServerMessage(stdin, map[string]any{
		"id":     3,
		"method": "config/batchWrite",
		"params": map[string]any{
			"edits":            []any{map[string]any{"keyPath": "hooks.state", "value": trusts, "mergeStrategy": "upsert"}},
			"reloadUserConfig": true,
		},
	}); err != nil {
		return err
	}
	_, err = readProjectManagerAppServerResponse(responses, scanErrs, 3, &stderr)
	return err
}

func projectManagerCodexInitializeRequest(id int) map[string]any {
	return map[string]any{
		"id":     id,
		"method": "initialize",
		"params": map[string]any{
			"clientInfo":   map[string]any{"name": "code-switch-r", "title": "Code Switch CLI", "version": "codex-status"},
			"capabilities": map[string]any{"experimentalApi": true},
		},
	}
}
