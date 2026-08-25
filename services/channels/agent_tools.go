package channels

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"codeswitch/services"
)

const (
	channelToolRead                   services.PetAgentToolName = "Read"
	channelToolWrite                  services.PetAgentToolName = "Write"
	channelToolEdit                   services.PetAgentToolName = "Edit"
	channelToolLS                     services.PetAgentToolName = "LS"
	channelToolGlob                   services.PetAgentToolName = "Glob"
	channelToolGrep                   services.PetAgentToolName = "Grep"
	channelToolBash                   services.PetAgentToolName = "Bash"
	channelToolSendMessage            services.PetAgentToolName = "PluginSendMessage"
	channelToolReplyMessage           services.PetAgentToolName = "PluginReplyMessage"
	channelToolGetGroupMessages       services.PetAgentToolName = "PluginGetGroupMessages"
	channelToolListGroups             services.PetAgentToolName = "PluginListGroups"
	channelToolSummarizeGroup         services.PetAgentToolName = "PluginSummarizeGroup"
	channelToolGetCurrentChatMessages services.PetAgentToolName = "PluginGetCurrentChatMessages"
	channelToolScopeSeparator                                   = "\x00"
	channelToolMaxOutputBytes                                   = 12 << 10
	channelToolDefaultShellTimeout                              = 10 * time.Minute
	channelToolMaxShellTimeout                                  = time.Hour
)

// channelToolScope 把 runtime 的实例、会话和当前 chat 绑定成一个不可见的内部标识。
// 模型拿不到 ToolScope；工具执行器也不会从模型参数里推断当前频道，避免模型借
// plugin_id 或 chat_id 把消息发到另一实例。
func channelToolScope(instanceID, sessionID, chatID string) string {
	return strings.Join([]string{strings.TrimSpace(instanceID), strings.TrimSpace(sessionID), strings.TrimSpace(chatID)}, channelToolScopeSeparator)
}

func parseChannelToolScope(scope string) (string, string, string, error) {
	parts := strings.Split(scope, channelToolScopeSeparator)
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
		return "", "", "", errors.New("channel tool scope is invalid")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), nil
}

// channelAgentToolExecutor 是频道专用工具 owner。它复用桌宠的只读工具实现，
// 但把写文件、Shell、消息路由和当前会话读取放在频道边界内，避免给桌宠 executor
// 添加任何写能力，也避免不同频道共享一个可变的权限上下文。
type channelAgentToolExecutor struct {
	store     *Store
	manager   *Manager
	eventSink EventSink

	instanceID string
	sessionID  string
	chatID     string
	workspace  string

	workspaceRoot string
	homeRoot      string
	readRoots     []string
	limits        services.PetAgentToolLimits

	mu            sync.Mutex
	readSnapshots map[string]string
}

func newChannelAgentToolExecutor(
	store *Store,
	manager *Manager,
	eventSink EventSink,
	instance ChannelInstance,
	sessionID string,
	chatID string,
	workspace string,
) (*channelAgentToolExecutor, error) {
	workspaceExecutor, err := services.NewPetAgentToolExecutor(workspace)
	if err != nil {
		return nil, err
	}
	workspaceRoot := workspaceExecutor.WorkspaceRoot()

	executor := &channelAgentToolExecutor{
		store:         store,
		manager:       manager,
		eventSink:     eventSink,
		instanceID:    strings.TrimSpace(instance.ID),
		sessionID:     strings.TrimSpace(sessionID),
		chatID:        strings.TrimSpace(chatID),
		workspace:     workspaceRoot,
		workspaceRoot: workspaceRoot,
		limits:        services.DefaultPetAgentToolLimits(),
		readSnapshots: make(map[string]string),
	}

	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		executor.homeRoot = canonicalDirectory(home)
	}
	// 只有存在且确实是目录的前缀才进入读取白名单；失效配置直接忽略，
	// 后续读取会返回稳定的路径越界错误，不把系统路径错误泄露给模型。
	for _, prefix := range instance.Permissions.ReadablePathPrefixes {
		root := canonicalDirectory(prefix)
		if root != "" {
			executor.readRoots = append(executor.readRoots, root)
		}
	}
	sort.Slice(executor.readRoots, func(i, j int) bool { return len(executor.readRoots[i]) > len(executor.readRoots[j]) })
	return executor, nil
}

func (e *channelAgentToolExecutor) Execute(ctx context.Context, call services.PetAgentToolCall) (services.PetAgentToolResult, error) {
	result := services.PetAgentToolResult{ToolCallID: call.ID, ToolName: string(call.Name)}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return result, err
		}
	}
	if e == nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel tool executor is unavailable"), nil
	}
	if call.ID == "" {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "tool call id is required"), nil
	}
	if int64(len(call.Arguments)) > e.limits.MaxArguments {
		return channelToolError(result, services.PetAgentToolErrorLimitExceeded, "tool arguments exceed the limit"), nil
	}
	args, err := decodeChannelToolArguments(call.Arguments)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	instance, err := e.currentInstance()
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, err.Error()), nil
	}
	if instance.Archived {
		return channelToolError(result, services.PetAgentToolErrorExecution, "archived channel is read-only"), nil
	}
	if !instance.Enabled {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel is disabled"), nil
	}
	if !channelToolEnabled(instance, call.Name) {
		return channelToolError(result, services.PetAgentToolErrorExecution, fmt.Sprintf("tool %q is disabled for this channel", call.Name)), nil
	}

	switch call.Name {
	case channelToolRead, channelToolLS, channelToolGlob, channelToolGrep:
		return e.executeReadOnly(ctx, call, args, result)
	case channelToolWrite:
		return e.executeWrite(ctx, args, result, instance)
	case channelToolEdit:
		return e.executeEdit(ctx, args, result, instance)
	case channelToolBash:
		return e.executeBash(ctx, args, result, instance)
	case channelToolSendMessage:
		return e.executePluginSend(ctx, args, result, instance)
	case channelToolReplyMessage:
		return e.executePluginReply(ctx, args, result, instance)
	case channelToolGetGroupMessages:
		return e.executePluginMessages(ctx, args, result, instance, 20)
	case channelToolSummarizeGroup:
		return e.executePluginMessages(ctx, args, result, instance, 50)
	case channelToolListGroups:
		return e.executePluginGroups(ctx, args, result, instance)
	case channelToolGetCurrentChatMessages:
		return e.executeCurrentChatMessages(ctx, args, result, instance)
	case channelToolFeishuSendImage,
		channelToolFeishuSendFile,
		channelToolFeishuListChatMembers,
		channelToolFeishuAtMember,
		channelToolFeishuSendUrgent,
		channelToolFeishuBitableListApps,
		channelToolFeishuBitableListTables,
		channelToolFeishuBitableListFields,
		channelToolFeishuBitableGetRecords,
		channelToolFeishuBitableCreateRecords,
		channelToolFeishuBitableUpdateRecords,
		channelToolFeishuBitableDeleteRecords,
		channelToolWeixinSendImage,
		channelToolWeixinSendFile:
		return e.executeProviderTool(ctx, call, args, result, instance)
	default:
		return channelToolError(result, services.PetAgentToolErrorUnknownTool, "unsupported channel tool"), nil
	}
}

func (e *channelAgentToolExecutor) currentInstance() (ChannelInstance, error) {
	if e.store == nil {
		return ChannelInstance{}, errors.New("channel store is unavailable")
	}
	instance, found, err := e.store.GetInstance(e.instanceID)
	if err != nil {
		return ChannelInstance{}, err
	}
	if !found {
		return ChannelInstance{}, errors.New("channel instance not found")
	}
	if instance.ID != e.instanceID {
		return ChannelInstance{}, errors.New("channel instance scope mismatch")
	}
	return instance, nil
}

func channelToolEnabled(instance ChannelInstance, name services.PetAgentToolName) bool {
	if instance.Tools == nil {
		return true
	}
	enabled, configured := instance.Tools[string(name)]
	return !configured || enabled
}

func (e *channelAgentToolExecutor) executeReadOnly(
	ctx context.Context,
	call services.PetAgentToolCall,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
) (services.PetAgentToolResult, error) {
	pathInput, err := channelReadPathArgument(call.Name, args)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	readExecutor, canonical, err := e.readExecutor(pathInput)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorPathOutsideRoot, err.Error()), nil
	}
	if readExecutor == nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "read executor is unavailable"), nil
	}
	output, err := readExecutor.Execute(ctx, call)
	if err != nil {
		return output, err
	}
	if !output.IsError && call.Name == channelToolRead && canonical != "" {
		if snapshotErr := e.rememberRead(canonical); snapshotErr != nil {
			return channelToolError(result, services.PetAgentToolErrorExecution, "file read snapshot unavailable"), nil
		}
	}
	return output, nil
}

func channelReadPathArgument(name services.PetAgentToolName, args map[string]json.RawMessage) (string, error) {
	key := "path"
	if name == channelToolRead {
		key = "file_path"
	}
	value, ok := args[key]
	if !ok {
		return "", nil
	}
	var path string
	if err := json.Unmarshal(value, &path); err != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return strings.TrimSpace(path), nil
}

func (e *channelAgentToolExecutor) readExecutor(pathInput string) (*services.PetAgentToolExecutor, string, error) {
	pathInput = strings.TrimSpace(pathInput)
	if pathInput == "" || !filepath.IsAbs(pathInput) {
		return e.newReadExecutor(e.workspaceRoot), filepath.Join(e.workspaceRoot, pathInput), nil
	}

	path := filepath.Clean(pathInput)
	canonical := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		canonical = filepath.Clean(resolved)
	}
	root := e.allowedReadRoot(canonical)
	if root == "" {
		return nil, "", errors.New("path is outside the channel readable roots")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		canonical = filepath.Clean(resolved)
	}
	return e.newReadExecutor(root), canonical, nil
}

func (e *channelAgentToolExecutor) newReadExecutor(root string) *services.PetAgentToolExecutor {
	executor, err := services.NewPetAgentToolExecutor(root)
	if err != nil {
		return nil
	}
	return executor
}

func (e *channelAgentToolExecutor) allowedReadRoot(path string) string {
	path = filepath.Clean(path)
	if withinChannelRoot(e.workspaceRoot, path) {
		return e.workspaceRoot
	}
	if e.permissionsAllowReadHome() && e.homeRoot != "" && withinChannelRoot(e.homeRoot, path) {
		return e.homeRoot
	}
	// 频道配置可以在 Agent 运行期间保存；白名单必须每次从 store 读取，
	// 否则旧 executor 会继续持有已撤销的权限，或拒绝刚刚授予的合法目录。
	readRoots := e.readRoots
	if instance, err := e.currentInstance(); err == nil {
		readRoots = make([]string, 0, len(instance.Permissions.ReadablePathPrefixes))
		for _, prefix := range instance.Permissions.ReadablePathPrefixes {
			if root := canonicalDirectory(prefix); root != "" {
				readRoots = append(readRoots, root)
			}
		}
		sort.Slice(readRoots, func(i, j int) bool { return len(readRoots[i]) > len(readRoots[j]) })
	} else {
		// currentInstance 失败时不能使用可能过期的自定义白名单；workspace 根目录
		// 仍由 NewPetAgentToolExecutor 在构造时校验过，其他路径保持 fail-closed。
		readRoots = nil
	}
	for _, root := range readRoots {
		if withinChannelRoot(root, path) {
			return root
		}
	}
	return ""
}

func (e *channelAgentToolExecutor) permissionsAllowReadHome() bool {
	instance, err := e.currentInstance()
	return err == nil && instance.Permissions.AllowReadHome
}

func (e *channelAgentToolExecutor) rememberRead(path string) error {
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) > e.limits.MaxFileBytes {
		return errors.New("read snapshot failed")
	}
	hash := sha256.Sum256(data)
	e.mu.Lock()
	e.readSnapshots[path] = hex.EncodeToString(hash[:])
	e.mu.Unlock()
	return nil
}

func (e *channelAgentToolExecutor) assertReadSnapshot(path string, requireSnapshot bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("file cannot be read")
	}
	if int64(len(data)) > e.limits.MaxFileBytes {
		return errors.New("file exceeds the tool limit")
	}
	hash := sha256.Sum256(data)
	current := hex.EncodeToString(hash[:])
	e.mu.Lock()
	previous := e.readSnapshots[path]
	e.mu.Unlock()
	if requireSnapshot && previous == "" {
		return errors.New("file must be read before it can be edited")
	}
	if previous != "" && previous != current {
		return errors.New("file changed since it was last read; read it again before editing")
	}
	return nil
}

func (e *channelAgentToolExecutor) updateReadSnapshot(path string, data []byte) {
	hash := sha256.Sum256(data)
	e.mu.Lock()
	e.readSnapshots[path] = hex.EncodeToString(hash[:])
	e.mu.Unlock()
}

func (e *channelAgentToolExecutor) executeWrite(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "file_path", "content"); err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	path, content, err := writeArguments(args)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	target, existed, err := e.resolveWritePath(path, instance.Permissions.AllowWriteOutside)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorPathOutsideRoot, err.Error()), nil
	}
	if existed {
		if err := e.assertReadSnapshot(target, true); err != nil {
			return channelToolError(result, services.PetAgentToolErrorExecution, err.Error()), nil
		}
	}
	if int64(len([]byte(content))) > e.limits.MaxFileBytes {
		return channelToolError(result, services.PetAgentToolErrorLimitExceeded, "content exceeds the file limit"), nil
	}
	if err := contextErrorChannel(ctx); err != nil {
		return result, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "cannot create parent directory"), nil
	}
	data := []byte(content)
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "cannot write file"), nil
	}
	e.updateReadSnapshot(target, data)
	return channelToolContent(result, map[string]any{
		"success": true,
		"path":    target,
		"op":      map[bool]string{true: "modify", false: "create"}[existed],
	}), nil
}

func (e *channelAgentToolExecutor) executeEdit(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "file_path", "old_string", "new_string", "replace_all"); err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	path, oldString, newString, replaceAll, err := editArguments(args)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	if oldString == "" {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "old_string must be non-empty"), nil
	}
	if oldString == newString {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "new_string must be different from old_string"), nil
	}
	target, existed, err := e.resolveWritePath(path, instance.Permissions.AllowWriteOutside)
	if err != nil || !existed {
		return channelToolError(result, services.PetAgentToolErrorPathOutsideRoot, "file does not exist or is outside the workspace"), nil
	}
	if err := e.assertReadSnapshot(target, true); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, err.Error()), nil
	}
	data, err := os.ReadFile(target)
	if err != nil || int64(len(data)) > e.limits.MaxFileBytes {
		return channelToolError(result, services.PetAgentToolErrorExecution, "cannot read file for edit"), nil
	}
	content := string(data)
	matched, replacement := findChannelEditVariant(oldString, newString, content)
	if matched == "" {
		return channelToolError(result, services.PetAgentToolErrorExecution, "string to replace was not found in the file"), nil
	}
	occurrences := strings.Count(content, matched)
	if occurrences > 1 && !replaceAll {
		return channelToolError(result, services.PetAgentToolErrorExecution, fmt.Sprintf("found %d matches; set replace_all to true or provide more context", occurrences)), nil
	}
	if err := contextErrorChannel(ctx); err != nil {
		return result, err
	}
	updated := replacement
	if replaceAll {
		updated = strings.ReplaceAll(content, matched, replacement)
	} else {
		updated = strings.Replace(content, matched, replacement, 1)
	}
	updatedData := []byte(updated)
	if int64(len(updatedData)) > e.limits.MaxFileBytes {
		return channelToolError(result, services.PetAgentToolErrorLimitExceeded, "edited file exceeds the file limit"), nil
	}
	if err := os.WriteFile(target, updatedData, 0o644); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "cannot write edited file"), nil
	}
	e.updateReadSnapshot(target, updatedData)
	return channelToolContent(result, map[string]any{"success": true, "path": target, "replaceAll": replaceAll}), nil
}

func (e *channelAgentToolExecutor) resolveWritePath(input string, allowOutside bool) (string, bool, error) {
	input = strings.TrimSpace(input)
	if input == "" || strings.IndexByte(input, 0) >= 0 {
		return "", false, errors.New("file_path is invalid")
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workspaceRoot, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", false, errors.New("file_path is invalid")
	}
	path = filepath.Clean(path)
	if info, statErr := os.Stat(path); statErr == nil {
		if info.IsDir() {
			return "", false, errors.New("file_path points to a directory")
		}
		canonical, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return "", false, errors.New("file_path cannot be resolved")
		}
		canonical = filepath.Clean(canonical)
		if !allowOutside && !withinChannelRoot(e.workspaceRoot, canonical) {
			return "", false, errors.New("path is outside the workspace")
		}
		return canonical, true, nil
	}

	parent := filepath.Dir(path)
	for {
		if _, statErr := os.Stat(parent); statErr == nil {
			canonicalParent, resolveErr := filepath.EvalSymlinks(parent)
			if resolveErr != nil {
				return "", false, errors.New("parent directory cannot be resolved")
			}
			relative, relativeErr := filepath.Rel(parent, path)
			if relativeErr != nil || filepath.IsAbs(relative) {
				return "", false, errors.New("file_path is invalid")
			}
			candidate := filepath.Join(canonicalParent, relative)
			if !allowOutside && !withinChannelRoot(e.workspaceRoot, candidate) {
				return "", false, errors.New("path is outside the workspace")
			}
			return filepath.Clean(candidate), false, nil
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", false, errors.New("parent directory cannot be resolved")
		}
		parent = next
	}
}

func findChannelEditVariant(oldString, newString, content string) (string, string) {
	variants := []struct{ old, replacement string }{{oldString, newString}}
	if strings.Contains(oldString, "\n") {
		lf := strings.ReplaceAll(oldString, "\r\n", "\n")
		variants = append(variants, struct{ old, replacement string }{lf, strings.ReplaceAll(newString, "\r\n", "\n")})
		if strings.Contains(content, "\r\n") {
			variants = append(variants, struct{ old, replacement string }{strings.ReplaceAll(lf, "\n", "\r\n"), strings.ReplaceAll(newString, "\n", "\r\n")})
		}
	}
	for _, variant := range variants {
		if variant.old != "" && strings.Contains(content, variant.old) {
			return variant.old, variant.replacement
		}
	}
	return "", ""
}

func (e *channelAgentToolExecutor) executeBash(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if !instance.Permissions.AllowShell {
		return channelToolError(result, services.PetAgentToolErrorExecution, "Shell execution is disabled for this channel"), nil
	}
	if err := rejectChannelArgs(args, "command", "timeout", "run_in_background", "force_foreground", "description", "cwd"); err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	command, err := requiredChannelString(args, "command")
	if err != nil || command == "" {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "command is required"), nil
	}
	timeout, err := optionalChannelInt(args, "timeout", int(channelToolDefaultShellTimeout/time.Millisecond))
	if err != nil || timeout < 1 {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "timeout must be a positive integer"), nil
	}
	if timeout > int(channelToolMaxShellTimeout/time.Millisecond) {
		timeout = int(channelToolMaxShellTimeout / time.Millisecond)
	}
	cwd, err := optionalChannelString(args, "cwd", e.workspaceRoot)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	if cwd == "" {
		cwd = e.workspaceRoot
	}
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(e.workspaceRoot, cwd)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "cwd is invalid"), nil
	}
	cwd, err = filepath.EvalSymlinks(filepath.Clean(cwd))
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorPathOutsideRoot, "cwd cannot be resolved"), nil
	}
	if !withinChannelRoot(e.workspaceRoot, cwd) {
		return channelToolError(result, services.PetAgentToolErrorPathOutsideRoot, "cwd is outside the channel workspace"), nil
	}

	commandCtx := ctx
	if commandCtx == nil {
		commandCtx = context.Background()
	}
	commandCtx, cancel := context.WithTimeout(commandCtx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	cmd := shellCommand(commandCtx, command)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	runErr := cmd.Run()
	timedOut := errors.Is(commandCtx.Err(), context.DeadlineExceeded)
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if timedOut {
			exitCode = 124
		}
	}
	stdoutText := truncateChannelOutput(stdout.String())
	stderrText := truncateChannelOutput(stderr.String())
	combined := truncateChannelOutput(stdoutText + stderrText)
	return channelToolContent(result, map[string]any{
		"exitCode": exitCode,
		"stdout":   stdoutText,
		"stderr":   stderrText,
		"output":   combined,
		"timedOut": timedOut,
		"cwd":      cwd,
		"command":  command,
		"totalMs":  time.Since(started).Milliseconds(),
	}), nil
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", command)
	}
	return exec.CommandContext(ctx, "/bin/zsh", "-lc", command)
}

func truncateChannelOutput(value string) string {
	if len(value) <= channelToolMaxOutputBytes {
		return value
	}
	return value[:channelToolMaxOutputBytes] + "\n[channel output truncated]"
}

func (e *channelAgentToolExecutor) executePluginSend(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "content"); err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	pluginID, chatID, content, err := pluginMessageArguments(args, true)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, err.Error()), nil
	}
	if e.manager == nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel manager is unavailable"), nil
	}
	messageID, err := e.manager.SendMessage(ctx, instance.ID, chatID, content)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel send failed"), nil
	}
	if persistErr := e.persistOutbound(instance, chatID, content, messageID); persistErr != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel message persistence failed"), nil
	}
	return channelToolContent(result, map[string]any{"success": true, "messageId": messageID}), nil
}

func (e *channelAgentToolExecutor) executePluginReply(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "message_id", "content"); err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	messageID, err := requiredChannelString(args, "message_id")
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	content, err := requiredChannelString(args, "content")
	if err != nil || content == "" {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "content is required"), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, err.Error()), nil
	}
	if e.manager == nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel manager is unavailable"), nil
	}
	newMessageID, err := e.manager.ReplyMessage(ctx, instance.ID, messageID, content)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel reply failed"), nil
	}
	if persistErr := e.persistOutbound(instance, e.chatID, content, newMessageID); persistErr != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel message persistence failed"), nil
	}
	return channelToolContent(result, map[string]any{"success": true, "messageId": newMessageID}), nil
}

func (e *channelAgentToolExecutor) executePluginMessages(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
	defaultCount int,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "count"); err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	pluginID, chatID, err := pluginChatArguments(args)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, err.Error()), nil
	}
	count, err := optionalChannelInt(args, "count", defaultCount)
	if err != nil || count < 1 {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "count must be a positive integer"), nil
	}
	if count > 100 {
		count = 100
	}
	if e.manager == nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel manager is unavailable"), nil
	}
	messages, err := e.manager.GetGroupMessages(ctx, instance.ID, chatID, count)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel history request failed"), nil
	}
	return channelToolContent(result, messages), nil
}

func (e *channelAgentToolExecutor) executePluginGroups(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id"); err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, err.Error()), nil
	}
	if e.manager == nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel manager is unavailable"), nil
	}
	groups, err := e.manager.ListGroups(ctx, instance.ID)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel group request failed"), nil
	}
	return channelToolContent(result, groups), nil
}

func (e *channelAgentToolExecutor) executeCurrentChatMessages(
	ctx context.Context,
	args map[string]json.RawMessage,
	result services.PetAgentToolResult,
	instance ChannelInstance,
) (services.PetAgentToolResult, error) {
	if err := rejectChannelArgs(args, "plugin_id", "chat_id", "count"); err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	pluginID, err := optionalChannelString(args, "plugin_id", instance.ID)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, err.Error()), nil
	}
	if err := e.validatePluginTarget(instance, pluginID, true); err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, err.Error()), nil
	}
	chatID, err := optionalChannelString(args, "chat_id", e.chatID)
	if err != nil || chatID == "" || chatID != e.chatID {
		return channelToolError(result, services.PetAgentToolErrorExecution, "current chat scope does not match the requested chat"), nil
	}
	count, err := optionalChannelInt(args, "count", 20)
	if err != nil || count < 1 {
		return channelToolError(result, services.PetAgentToolErrorInvalidArguments, "count must be a positive integer"), nil
	}
	if count > 100 {
		count = 100
	}
	if e.store == nil || e.sessionID == "" {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel session is unavailable"), nil
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return result, err
		}
	}
	messages, err := e.store.ListMessages(e.sessionID, count)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "channel session history request failed"), nil
	}
	return channelToolContent(result, map[string]any{"sessionId": e.sessionID, "messages": messages}), nil
}

func (e *channelAgentToolExecutor) validatePluginTarget(instance ChannelInstance, pluginID string, required bool) error {
	pluginID = strings.TrimSpace(pluginID)
	if required && pluginID == "" {
		return errors.New("plugin_id is required")
	}
	if pluginID != "" && pluginID != instance.ID {
		return errors.New("plugin tool cannot access another channel instance")
	}
	return nil
}

func (e *channelAgentToolExecutor) persistOutbound(instance ChannelInstance, chatID, content, externalID string) error {
	if e == nil {
		return errors.New("channel store is unavailable")
	}
	return appendChannelOutboundMessage(e.store, e.eventSink, instance, chatID, content, externalID)
}

func appendChannelOutboundMessage(store *Store, eventSink EventSink, instance ChannelInstance, chatID, content, externalID string) error {
	if store == nil {
		return errors.New("channel store is unavailable")
	}
	chatID = strings.TrimSpace(chatID)
	content = strings.TrimSpace(content)
	if chatID == "" || content == "" {
		return errors.New("outbound channel message is empty")
	}
	if strings.TrimSpace(externalID) == "" {
		externalID = "channel-outbound-" + fmt.Sprint(time.Now().UnixNano())
	}
	session, found, err := store.GetSession(instance.ID, chatID)
	if err != nil {
		return err
	}
	sessionID := ""
	if found {
		sessionID = session.ID
		session.UpdatedAt = nowMillis()
		if err := store.UpsertSession(session); err != nil {
			return err
		}
	}
	message := ChannelMessage{
		ID:         sessionKey(instance.ID, externalID, fmt.Sprint(time.Now().UnixNano())),
		InstanceID: instance.ID,
		SessionID:  sessionID,
		ExternalID: externalID,
		Role:       "assistant",
		ChatID:     chatID,
		SenderName: instance.Name,
		Content:    content,
		Timestamp:  nowMillis(),
	}
	inserted, err := store.AppendMessageIfNew(message)
	if err != nil {
		return err
	}
	if inserted && eventSink != nil {
		eventSink(ChannelEvent{Type: "message", InstanceID: instance.ID, PluginType: instance.Type, Data: message, At: nowMillis()})
	}
	return nil
}

func writeArguments(args map[string]json.RawMessage) (string, string, error) {
	path, err := requiredChannelString(args, "file_path")
	if err != nil {
		return "", "", err
	}
	content, err := requiredChannelRawString(args, "content")
	return path, content, err
}

func editArguments(args map[string]json.RawMessage) (string, string, string, bool, error) {
	path, err := requiredChannelString(args, "file_path")
	if err != nil {
		return "", "", "", false, err
	}
	oldString, err := requiredChannelRawString(args, "old_string")
	if err != nil {
		return "", "", "", false, err
	}
	newString, err := requiredChannelRawString(args, "new_string")
	if err != nil {
		return "", "", "", false, err
	}
	replaceAll, err := optionalChannelBool(args, "replace_all", false)
	return path, oldString, newString, replaceAll, err
}

func pluginMessageArguments(args map[string]json.RawMessage, requiredPlugin bool) (string, string, string, error) {
	pluginID, err := requiredOrOptionalChannelString(args, "plugin_id", requiredPlugin)
	if err != nil {
		return "", "", "", err
	}
	chatID, err := requiredChannelString(args, "chat_id")
	if err != nil {
		return "", "", "", err
	}
	content, err := requiredChannelRawString(args, "content")
	if err != nil || content == "" {
		return "", "", "", errors.New("content is required")
	}
	return pluginID, chatID, content, nil
}

func pluginChatArguments(args map[string]json.RawMessage) (string, string, error) {
	pluginID, err := requiredChannelString(args, "plugin_id")
	if err != nil {
		return "", "", err
	}
	chatID, err := requiredChannelString(args, "chat_id")
	return pluginID, chatID, err
}

func rejectChannelArgs(args map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range args {
		if _, ok := set[key]; !ok {
			return fmt.Errorf("unknown argument %q", key)
		}
	}
	return nil
}

func decodeChannelToolArguments(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var args map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&args); err != nil || args == nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("arguments must contain one JSON value")
	}
	return args, nil
}

func requiredChannelString(args map[string]json.RawMessage, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	return decodeChannelString(value, key)
}

func requiredOrOptionalChannelString(args map[string]json.RawMessage, key string, required bool) (string, error) {
	if _, ok := args[key]; !ok {
		if required {
			return "", fmt.Errorf("%s is required", key)
		}
		return "", nil
	}
	return decodeChannelString(args[key], key)
}

func requiredChannelRawString(args map[string]json.RawMessage, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return result, nil
}

func optionalChannelString(args map[string]json.RawMessage, key, fallback string) (string, error) {
	if _, ok := args[key]; !ok {
		return fallback, nil
	}
	return decodeChannelString(args[key], key)
}

func decodeChannelString(value json.RawMessage, key string) (string, error) {
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return strings.TrimSpace(result), nil
}

func optionalChannelInt(args map[string]json.RawMessage, key string, fallback int) (int, error) {
	if _, ok := args[key]; !ok {
		return fallback, nil
	}
	var result int
	if err := json.Unmarshal(args[key], &result); err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return result, nil
}

func optionalChannelBool(args map[string]json.RawMessage, key string, fallback bool) (bool, error) {
	if _, ok := args[key]; !ok {
		return fallback, nil
	}
	var result bool
	if err := json.Unmarshal(args[key], &result); err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return result, nil
}

func channelToolContent(result services.PetAgentToolResult, value any) services.PetAgentToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return channelToolError(result, services.PetAgentToolErrorExecution, "cannot encode tool result")
	}
	result.Content = string(data)
	return result
}

func channelToolError(result services.PetAgentToolResult, code, message string) services.PetAgentToolResult {
	data, _ := json.Marshal(services.PetAgentToolError{Code: code, Message: message})
	result.Content = string(data)
	result.IsError = true
	return result
}

func contextErrorChannel(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func canonicalDirectory(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return ""
	}
	return filepath.Clean(resolved)
}

func withinChannelRoot(root, candidate string) bool {
	root, candidate = filepath.Clean(root), filepath.Clean(candidate)
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.VolumeName(root), filepath.VolumeName(candidate))
	}
	return true
}

// channelToolDefinitionsForInstance 只返回当前实例可见的工具，权限检查仍在 executor
// 再做一次。定义过滤负责减少模型误调用，执行时复核负责覆盖配置热更新和伪造参数。
func channelToolDefinitionsForInstance(instance ChannelInstance) []services.PetAgentToolDefinition {
	if instance.Archived {
		return nil
	}
	definitions := make([]services.PetAgentToolDefinition, 0, 16)
	for _, definition := range services.PetAgentToolDefinitions() {
		if channelToolEnabled(instance, definition.Name) {
			definitions = append(definitions, definition)
		}
	}
	custom := channelCustomToolDefinitions()
	for _, definition := range custom {
		if definition.Name == channelToolBash && !instance.Permissions.AllowShell {
			continue
		}
		if channelToolEnabled(instance, definition.Name) {
			definitions = append(definitions, definition)
		}
	}
	for _, definition := range channelProviderToolDefinitions(instance) {
		if channelToolEnabled(instance, definition.Name) {
			definitions = append(definitions, definition)
		}
	}
	return definitions
}

func channelCustomToolDefinitions() []services.PetAgentToolDefinition {
	return []services.PetAgentToolDefinition{
		{Name: channelToolWrite, Description: "Write text to a file in the channel project workspace.", InputSchema: channelObjectSchema(map[string]any{"file_path": channelStringSchema("Absolute path or path relative to the project workspace"), "content": channelStringSchema("Complete file content")}, "file_path", "content")},
		{Name: channelToolEdit, Description: "Replace an exact string in a file after it has been read.", InputSchema: channelObjectSchema(map[string]any{"file_path": channelStringSchema("Absolute path or path relative to the project workspace"), "old_string": channelStringSchema("Existing text to replace"), "new_string": channelStringSchema("Replacement text"), "replace_all": map[string]any{"type": "boolean", "description": "Replace every match instead of requiring one match"}}, "file_path", "old_string", "new_string")},
		{Name: channelToolBash, Description: "Execute a shell command in the channel project workspace.", InputSchema: channelObjectSchema(map[string]any{"command": channelStringSchema("Shell command to execute"), "timeout": map[string]any{"type": "integer", "description": "Timeout in milliseconds"}, "run_in_background": map[string]any{"type": "boolean"}, "force_foreground": map[string]any{"type": "boolean"}, "description": channelStringSchema("Short description of the command"), "cwd": channelStringSchema("Optional directory within the project workspace")}, "command")},
		{Name: channelToolSendMessage, Description: "Send a message to a chat or group through the current messaging channel.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("Current channel instance ID"), "chat_id": channelStringSchema("Target chat or group ID"), "content": channelStringSchema("Message content")}, "plugin_id", "chat_id", "content")},
		{Name: channelToolReplyMessage, Description: "Reply to a message through the current messaging channel.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("Current channel instance ID"), "message_id": channelStringSchema("Message ID to reply to"), "content": channelStringSchema("Reply content")}, "plugin_id", "message_id", "content")},
		{Name: channelToolGetGroupMessages, Description: "Get recent messages from a chat or group through the current channel.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("Current channel instance ID"), "chat_id": channelStringSchema("Chat or group ID"), "count": map[string]any{"type": "integer", "description": "Number of messages, default 20"}}, "plugin_id", "chat_id")},
		{Name: channelToolListGroups, Description: "List available chats and groups through the current channel.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("Current channel instance ID")}, "plugin_id")},
		{Name: channelToolSummarizeGroup, Description: "Get recent group messages for summarization.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("Current channel instance ID"), "chat_id": channelStringSchema("Chat or group ID"), "count": map[string]any{"type": "integer", "description": "Number of messages, default 50"}}, "plugin_id", "chat_id")},
		{Name: channelToolGetCurrentChatMessages, Description: "Get recent messages from the current channel chat session.", InputSchema: channelObjectSchema(map[string]any{"plugin_id": channelStringSchema("Optional current channel instance ID"), "chat_id": channelStringSchema("Optional current chat ID"), "count": map[string]any{"type": "integer", "description": "Number of messages, default 20"}}, "")},
	}
}

func channelObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func channelStringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
