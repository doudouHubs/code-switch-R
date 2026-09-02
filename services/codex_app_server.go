package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultCodexAppServerResponseTimeout = 30 * time.Second
	// WaitDelay 只覆盖 cmd.Wait 已经开始后的管道收口；Close 还会主动关闭
	// stdout，所以这里保留一个较短兜底，避免异常子进程持有句柄时永久等待。
	codexAppServerWaitDelay   = 2 * time.Second
	codexAppServerCloseBudget = 5 * time.Second
)

// errCodexAppServerExited 是进程生命周期的结构化边界。调用方不能只看
// cmd.Wait 的具体文本，因为 turn/start response 与 reader 收口存在竞态，
// 同一次进程退出可能从不同路径返回不同的底层错误。
var errCodexAppServerExited = errors.New("Codex app-server process exited")

// CodexAppServerCommandFactory 只负责创建后台进程，协议客户端不依赖具体平台的
// 窗口隐藏实现；测试可以用同一个接口注入自托管的 JSONL fixture。
type CodexAppServerCommandFactory func(name string, args ...string) *exec.Cmd

// CodexAppServerClientOptions 描述一个长期存活的 codex app-server 进程。
// Executable 为空时沿用当前用户 PATH/Volta 中的 codex，不复制配置或认证文件。
type CodexAppServerClientOptions struct {
	Executable            string
	WorkingDirectory      string
	CommandFactory        CodexAppServerCommandFactory
	ResponseTimeout       time.Duration
	ServerRequestHandler  CodexAppServerServerRequestHandler
	ServerRequestObserver CodexAppServerServerRequestObserver
}

// CodexAppServerMessage 是 app-server 的 JSONL 信封。响应和通知共用这个形状，
// 调用方按 ID/Method 分流，避免在不同业务 owner 内重复实现协议解析。
type CodexAppServerMessage struct {
	ID     json.RawMessage         `json:"id,omitempty"`
	Method string                  `json:"method,omitempty"`
	Params json.RawMessage         `json:"params,omitempty"`
	Result json.RawMessage         `json:"result,omitempty"`
	Error  *CodexAppServerRPCError `json:"error,omitempty"`
}

type CodexAppServerRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexAppServerPendingResponse struct {
	message CodexAppServerMessage
	err     error
}

// CodexAppServerServerRequestResponse 是 server request 的单次响应。
// Result 和 Error 互斥：正常结果走 JSON-RPC result，协议拒绝走 JSON-RPC error，
// 不能把错误对象伪装成业务 result，否则 Codex 会继续等待下一条合法响应。
type CodexAppServerServerRequestResponse struct {
	Result any
	Error  *CodexAppServerRPCError
}

// CodexAppServerServerRequestHandler 由业务 runtime 注入 server request 策略。
// handler 在独立 goroutine 中调用，stdout reader 不会被慢的 MCP/工具处理堵住。
type CodexAppServerServerRequestHandler func(context.Context, CodexAppServerMessage) CodexAppServerServerRequestResponse

// CodexAppServerServerRequestObserver 只负责把 server-request 交给上层 owner，
// 返回 true 表示 owner 已经把请求登记为 pending，并会稍后调用
// ResolveServerRequest；返回 false 则继续走旧的同步 handler。这样动态工具等
// 必须立即执行的请求不被 UI pending 阻塞，而审批/表单可以真正等待用户决定。
type CodexAppServerServerRequestObserver func(context.Context, CodexAppServerMessage) bool

// CodexAppServerClient 管理一个 app-server 进程及其所有 JSON-RPC 请求。
// reader goroutine 是唯一的 stdout owner；调用方永远不会并发读取同一管道。
type CodexAppServerClient struct {
	cmd                   *exec.Cmd
	stdin                 io.WriteCloser
	stdout                io.ReadCloser
	stderr                *bytes.Buffer
	responseTimeout       time.Duration
	serverRequestHandler  CodexAppServerServerRequestHandler
	serverRequestObserver CodexAppServerServerRequestObserver
	serverRequestContext  context.Context
	serverRequestCancel   context.CancelFunc

	mu             sync.Mutex
	writeMu        sync.Mutex
	nextID         int64
	pending        map[string]chan codexAppServerPendingResponse
	serverRequests map[string]chan CodexAppServerServerRequestResponse
	done           chan struct{}
	stop           chan struct{}
	notifies       chan CodexAppServerMessage
	err            error
	closing        bool
	finish         sync.Once
	stopOnce       sync.Once
}

var defaultCodexAppServerCommandFactory = CodexAppServerCommandFactory(hideWindowCmd)

// app-server 是独立子进程，GUI 模式下 stdout/stderr 不一定有可见宿主；统一写入
// 已有 runtime diagnostic 文件，只记录协议阶段和字节数，不记录 prompt、回复正文或凭据。
func writeCodexAppServerDiagnostic(event string, details ...string) {
	fields := make([]string, 0, len(details)+1)
	fields = append(fields, "component=codex-app-server")
	fields = append(fields, details...)
	// 诊断不能反过来占住 stdout reader 或 RPC 调用；统一交给有界单 worker，
	// 高频通知堆积时最多保留有限条记录，不为每条 JSONL 创建 goroutine。
	WriteRuntimeDiagnosticAsync(event, fields...)
}

// NewCodexAppServerClient 启动 app-server 并立即开始消费 stdout。
// initialize 由上层显式调用，这样 ProjectManager fork 和宠物 runtime 可以各自
// 设置正确的 clientInfo，但共用进程、响应匹配和退出收口规则。
func NewCodexAppServerClient(options CodexAppServerClientOptions) (*CodexAppServerClient, error) {
	factory := options.CommandFactory
	if factory == nil {
		factory = defaultCodexAppServerCommandFactory
	}
	executable := strings.TrimSpace(options.Executable)
	if executable == "" {
		executable = resolveCodexExecutable()
	}
	cmd := buildCodexAppServerCommand(factory, executable)
	markCodexAppServerManaged(cmd)
	cmd.WaitDelay = codexAppServerWaitDelay
	if workingDirectory := strings.TrimSpace(options.WorkingDirectory); workingDirectory != "" {
		cmd.Dir = filepath.Clean(workingDirectory)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("获取 Codex app-server stdin 失败: %w", err)
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("创建 Codex app-server stdout 管道失败: %w", err)
	}
	// 不使用 StdoutPipe：os/exec 会在 Wait 中管理它的复制 goroutine，
	// 而 Close 又必须主动关闭读端并终止进程树。显式管道把 reader 和 Wait
	// 的所有权拆开，避免 Windows 下“读端已关但 Wait 仍等继承句柄”的竞态。
	cmd.Stdout = stdoutWriter
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("启动 Codex app-server 失败: %w", err)
	}
	// 子进程已经继承 stdoutWriter；父进程必须立刻释放自己的副本，否则
	// 子进程退出后 reader 也永远收不到 EOF，进而拖住 reader/Wait 收口。
	_ = stdoutWriter.Close()
	writeCodexAppServerDiagnostic(
		"codex-app-server-start",
		fmt.Sprintf("pid=%d", cmd.Process.Pid),
		fmt.Sprintf("command=%q", strings.Join(cmd.Args, " ")),
		fmt.Sprintf("cwd=%q", cmd.Dir),
	)

	responseTimeout := options.ResponseTimeout
	if responseTimeout <= 0 {
		responseTimeout = defaultCodexAppServerResponseTimeout
	}
	serverRequestContext, serverRequestCancel := context.WithCancel(context.Background())
	client := &CodexAppServerClient{
		cmd:                   cmd,
		stdin:                 stdin,
		stdout:                stdoutReader,
		stderr:                stderr,
		responseTimeout:       responseTimeout,
		serverRequestHandler:  options.ServerRequestHandler,
		serverRequestObserver: options.ServerRequestObserver,
		serverRequestContext:  serverRequestContext,
		serverRequestCancel:   serverRequestCancel,
		pending:               make(map[string]chan codexAppServerPendingResponse),
		serverRequests:        make(map[string]chan CodexAppServerServerRequestResponse),
		done:                  make(chan struct{}),
		stop:                  make(chan struct{}),
		notifies:              make(chan CodexAppServerMessage, 512),
	}
	go client.readAndWait(stdoutReader)
	return client, nil
}

func resolveCodexExecutable() string {
	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			voltaCodex := filepath.Join(localAppData, "Volta", "bin", "codex.cmd")
			// Volta 的 shim 可能被用户改成指向本地源码构建；文件存在不代表
			// 目标仍存在。失效 shim 继续排在 PATH 前面会让 app-server 启动后
			// 立刻退出，前端只能等 watchdog 把它误报成“回复超时”。
			if codexExecutableUsable(voltaCodex) {
				return voltaCodex
			}
			if installed := findInstalledWindowsCodex(localAppData); installed != "" {
				return installed
			}
		}
	}
	if resolved, err := exec.LookPath("codex"); err == nil && strings.TrimSpace(resolved) != "" {
		if codexExecutableUsable(resolved) {
			return resolved
		}
	}
	return "codex"
}

func codexExecutableUsable(path string) bool {
	path = strings.Trim(strings.TrimSpace(path), `"`)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" {
		return true
	}
	return codexBatchShimTargetUsable(path)
}

func codexBatchShimTargetUsable(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	// 只校验批处理文件中明确写死的绝对可执行文件。普通的 `volta run`
	// 或 PATH 转发脚本没有可静态解析的目标，仍交给 cmd.exe 自己处理。
	const marker = `@"`
	start := strings.Index(content, marker)
	if start < 0 {
		return true
	}
	targetStart := start + len(marker)
	targetEnd := strings.IndexByte(content[targetStart:], '"')
	if targetEnd < 0 {
		return true
	}
	target := strings.TrimSpace(content[targetStart : targetStart+targetEnd])
	if !filepath.IsAbs(target) {
		return true
	}
	info, err := os.Stat(target)
	return err == nil && !info.IsDir()
}

func findInstalledWindowsCodex(localAppData string) string {
	patterns := []string{
		// Volta 安装的官方包优先于桌面应用缓存，符合用户在终端执行
		// `codex` 时的默认版本，同时不依赖易失的 hash 目录名。
		filepath.Join(localAppData, "Volta", "tools", "image", "packages", "@openai", "codex", "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "*", "bin", "codex.exe"),
		filepath.Join(localAppData, "Volta", "tools", "image", "node", "*", "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "*", "codex", "codex.exe"),
		filepath.Join(localAppData, "OpenAI", "Codex", "bin", "*", "codex.exe"),
	}
	var bestPath string
	var bestTime time.Time
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if !codexExecutableUsable(match) {
				continue
			}
			info, err := os.Stat(match)
			if err != nil {
				continue
			}
			if bestPath == "" || info.ModTime().After(bestTime) {
				bestPath = match
				bestTime = info.ModTime()
			}
		}
	}
	return bestPath
}

func codexAppServerNeedsCmdShell(path string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	return ext == ".cmd" || ext == ".bat"
}

func buildCodexAppServerCommand(factory CodexAppServerCommandFactory, executable string) *exec.Cmd {
	if factory == nil {
		factory = defaultCodexAppServerCommandFactory
	}
	args := []string{"app-server", "--stdio"}
	if runtime.GOOS == "windows" && codexAppServerNeedsCmdShell(executable) {
		// Windows 的 .cmd shim 不能稳定地作为 CreateProcess 目标，后台 JSON-RPC
		// 进程统一通过隐藏 cmd.exe 启动，避免弹出控制台也避免 stdin 断连。
		return factory("cmd.exe", append([]string{"/D", "/C", executable}, args...)...)
	}
	return factory(executable, args...)
}

func markCodexAppServerManaged(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	} else {
		env = append([]string(nil), env...)
	}
	filtered := env[:0]
	for _, entry := range env {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(entry, "=", 2)[0]), "CODESWITCH_CODEX_APP_SERVER_MANAGED") {
			continue
		}
		filtered = append(filtered, entry)
	}
	// 只有 CodeSwitch 自己创建的 app-server 才打 managed 标记；Hook 路由据此
	// 区分“可按已登记 thread 精确归属”的进程和用户在终端启动的外部 Codex。
	cmd.Env = append(filtered, "CODESWITCH_CODEX_APP_SERVER_MANAGED=1")
}

func (c *CodexAppServerClient) readAndWait(stdout io.ReadCloser) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var scanErr error
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message CodexAppServerMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			scanErr = fmt.Errorf("Codex app-server 返回了无效 JSON: %w", err)
			if c.cmd != nil && c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
			break
		}
		if c.dispatch(message) {
			continue
		}
	}
	if scanErr == nil {
		scanErr = scanner.Err()
	}

	waitErr := c.cmd.Wait()
	if scanErr != nil {
		writeCodexAppServerDiagnostic(
			"codex-app-server-read-error",
			fmt.Sprintf("error=%q", scanErr.Error()),
			c.stderrDiagnosticField(),
		)
		c.finishWithError(c.decorateError("读取 Codex app-server 响应失败", scanErr))
		return
	}
	if waitErr != nil {
		writeCodexAppServerDiagnostic(
			"codex-app-server-exit-error",
			fmt.Sprintf("error=%q", waitErr.Error()),
			c.stderrDiagnosticField(),
		)
		c.finishWithError(errors.Join(
			errCodexAppServerExited,
			c.decorateError("Codex app-server 已退出", waitErr),
		))
		return
	}
	writeCodexAppServerDiagnostic("codex-app-server-exit", "reason=process-exited")
	c.finishWithError(errors.Join(
		errCodexAppServerExited,
		c.decorateError("Codex app-server 已退出", errors.New("process exited")),
	))
}

// dispatch 返回 true 表示消息已经被消费。带 method 和 id 的消息是 server request，
// 必须交给注入的 owner；没有 owner 时快速返回 -32601，不能让 Codex 等待超时。
func (c *CodexAppServerClient) dispatch(message CodexAppServerMessage) bool {
	if len(message.ID) > 0 && strings.TrimSpace(message.Method) != "" {
		go c.handleServerRequest(message)
		return true
	}
	if len(message.ID) > 0 {
		key := codexAppServerMessageIDKey(message.ID)
		c.mu.Lock()
		pending := c.pending[key]
		if pending != nil {
			delete(c.pending, key)
		}
		c.mu.Unlock()
		if pending == nil {
			return true
		}
		if message.Error != nil {
			pending <- codexAppServerPendingResponse{err: fmt.Errorf(
				"Codex app-server 请求失败: code=%d message=%s",
				message.Error.Code,
				strings.TrimSpace(message.Error.Message),
			)}
		} else {
			pending <- codexAppServerPendingResponse{message: message}
		}
		return true
	}
	if strings.TrimSpace(message.Method) == "" {
		return true
	}
	writeCodexAppServerDiagnostic(
		"codex-notification",
		fmt.Sprintf("method=%q", message.Method),
		fmt.Sprintf("params_bytes=%d", len(message.Params)),
	)
	select {
	case c.notifies <- message:
	case <-c.stop:
	case <-c.done:
	}
	return true
}

func (c *CodexAppServerClient) handleServerRequest(message CodexAppServerMessage) {
	method := strings.TrimSpace(message.Method)
	var response CodexAppServerServerRequestResponse
	writeCodexAppServerDiagnostic(
		"codex-server-request",
		fmt.Sprintf("method=%q", method),
		fmt.Sprintf("id=%s", codexAppServerMessageIDKey(message.ID)),
		fmt.Sprintf("params_bytes=%d", len(message.Params)),
	)
	if c != nil && c.serverRequestObserver != nil {
		pending, err := c.registerServerRequest(message.ID)
		if err == nil && c.serverRequestObserver(c.serverRequestContext, message) {
			response = c.waitServerRequest(message.ID, pending)
			c.writeServerRequestResponse(message, method, response)
			return
		}
		// observer 返回 false 表示该方法仍由同步兼容 handler 负责；如果
		// observer 已登记但之后拒绝接管，必须先删除 pending，避免下一次
		// 同 ID 请求误取到上一条响应。
		if err == nil {
			c.removeServerRequest(message.ID)
		}
	}
	response = c.handleLegacyServerRequest(message)
	c.writeServerRequestResponse(message, method, response)
}

func (c *CodexAppServerClient) handleLegacyServerRequest(message CodexAppServerMessage) CodexAppServerServerRequestResponse {
	response := CodexAppServerServerRequestResponse{
		Error: &CodexAppServerRPCError{
			Code:    -32601,
			Message: "server request is not supported by this Codex client",
		},
	}
	if c != nil && c.serverRequestHandler != nil {
		response = c.serverRequestHandler(c.serverRequestContext, message)
	}
	return response
}

func (c *CodexAppServerClient) writeServerRequestResponse(message CodexAppServerMessage, method string, response CodexAppServerServerRequestResponse) {
	if response.Error != nil {
		if response.Error.Code == 0 {
			response.Error.Code = -32603
		}
		if strings.TrimSpace(response.Error.Message) == "" {
			response.Error.Message = "Codex server request failed"
		}
		if err := c.writeServerRequestError(message.ID, response.Error.Code, response.Error.Message); err != nil {
			writeCodexAppServerDiagnostic("codex-server-request-response-error", fmt.Sprintf("method=%q", method), fmt.Sprintf("error=%q", err.Error()))
		}
		return
	}
	if err := c.writeServerRequestResult(message.ID, response.Result); err != nil {
		writeCodexAppServerDiagnostic("codex-server-request-response-error", fmt.Sprintf("method=%q", method), fmt.Sprintf("error=%q", err.Error()))
	}
}

func (c *CodexAppServerClient) registerServerRequest(id json.RawMessage) (chan CodexAppServerServerRequestResponse, error) {
	key := codexAppServerMessageIDKey(id)
	if key == "" {
		return nil, errors.New("Codex server request 缺少 id")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	if c.closing {
		return nil, errors.New("Codex app-server client 正在关闭")
	}
	if c.serverRequests == nil {
		c.serverRequests = make(map[string]chan CodexAppServerServerRequestResponse)
	}
	if _, exists := c.serverRequests[key]; exists {
		return nil, fmt.Errorf("Codex server request id %q 已存在", key)
	}
	response := make(chan CodexAppServerServerRequestResponse, 1)
	c.serverRequests[key] = response
	return response, nil
}

// ResolveServerRequest 是异步 server-request 的唯一响应入口。id 使用 app-server
// 原始 JSON-RPC id 的规范化文本，既兼容数字也兼容字符串，避免前端把数字 id
// 转成浮点数后失去精度。
func (c *CodexAppServerClient) ResolveServerRequest(id json.RawMessage, response CodexAppServerServerRequestResponse) error {
	key := codexAppServerMessageIDKey(id)
	if key == "" {
		return errors.New("Codex server request id 不能为空")
	}
	c.mu.Lock()
	pending := c.serverRequests[key]
	if pending != nil {
		delete(c.serverRequests, key)
	}
	c.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("Codex server request %q 不存在或已结束", key)
	}
	pending <- response
	return nil
}

func (c *CodexAppServerClient) removeServerRequest(id json.RawMessage) {
	if c == nil {
		return
	}
	key := codexAppServerMessageIDKey(id)
	if key == "" {
		return
	}
	c.mu.Lock()
	delete(c.serverRequests, key)
	c.mu.Unlock()
}

func (c *CodexAppServerClient) waitServerRequest(id json.RawMessage, pending <-chan CodexAppServerServerRequestResponse) CodexAppServerServerRequestResponse {
	select {
	case response := <-pending:
		return response
	case <-c.done:
		c.removeServerRequest(id)
		return CodexAppServerServerRequestResponse{Error: &CodexAppServerRPCError{
			Code:    -32603,
			Message: "Codex app-server 已停止，无法完成 server request",
		}}
	case <-c.serverRequestContext.Done():
		c.removeServerRequest(id)
		return CodexAppServerServerRequestResponse{Error: &CodexAppServerRPCError{
			Code:    -32603,
			Message: "Codex server request 已取消",
		}}
	}
}

func (c *CodexAppServerClient) writeServerRequestError(id json.RawMessage, code int, message string) error {
	return c.writePayload(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func (c *CodexAppServerClient) writeServerRequestResult(id json.RawMessage, result any) error {
	return c.writePayload(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
}

// Call 发送一个请求并等待匹配的 JSON-RPC 响应。ctx 取消只放弃当前调用，
// 不会自动重放 turn，也不会把旧响应串进下一条请求。
func (c *CodexAppServerClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c == nil || strings.TrimSpace(method) == "" {
		return nil, errors.New("Codex app-server client 不可用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("编码 Codex app-server 请求失败: %w", err)
	}

	id, key, responseChannel, err := c.registerRequest()
	if err != nil {
		writeCodexAppServerDiagnostic("codex-rpc-register-error", fmt.Sprintf("method=%q", method))
		return nil, err
	}
	startedAt := time.Now()
	request := map[string]any{
		"id":     id,
		"method": method,
		"params": json.RawMessage(body),
	}
	if err := c.writePayload(request); err != nil {
		c.removePending(key)
		writeCodexAppServerDiagnostic(
			"codex-rpc-write-error",
			fmt.Sprintf("method=%q", method),
			fmt.Sprintf("id=%d", id),
			fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
		)
		return nil, err
	}

	waitCtx := ctx
	cancel := func() {}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && c.responseTimeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, c.responseTimeout)
	}
	defer cancel()
	select {
	case response := <-responseChannel:
		if response.err != nil {
			writeCodexAppServerDiagnostic(
				"codex-rpc-response-error",
				fmt.Sprintf("method=%q", method),
				fmt.Sprintf("id=%d", id),
				fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
			)
			return nil, response.err
		}
		writeCodexAppServerDiagnostic(
			"codex-rpc-response",
			fmt.Sprintf("method=%q", method),
			fmt.Sprintf("id=%d", id),
			fmt.Sprintf("result_bytes=%d", len(response.message.Result)),
			fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
		)
		if len(response.message.Result) == 0 {
			return json.RawMessage("null"), nil
		}
		return response.message.Result, nil
	case <-waitCtx.Done():
		c.removePending(key)
		event := "codex-rpc-cancelled"
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			event = "codex-rpc-timeout"
		}
		writeCodexAppServerDiagnostic(
			event,
			fmt.Sprintf("method=%q", method),
			fmt.Sprintf("id=%d", id),
			fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
		)
		return nil, waitCtx.Err()
	case <-c.done:
		c.removePending(key)
		err := c.Err()
		writeCodexAppServerDiagnostic(
			"codex-rpc-client-done",
			fmt.Sprintf("method=%q", method),
			fmt.Sprintf("id=%d", id),
			fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
		)
		return nil, err
	}
}

func (c *CodexAppServerClient) registerRequest() (int64, string, chan codexAppServerPendingResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.closing {
		if c.err != nil {
			return 0, "", nil, c.err
		}
		return 0, "", nil, errors.New("Codex app-server client 正在关闭")
	}
	c.nextID++
	id := c.nextID
	key := strconv.FormatInt(id, 10)
	response := make(chan codexAppServerPendingResponse, 1)
	c.pending[key] = response
	return id, key, response, nil
}

func (c *CodexAppServerClient) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *CodexAppServerClient) Notify(method string, params any) error {
	if c == nil || strings.TrimSpace(method) == "" {
		return errors.New("Codex app-server client 不可用")
	}
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("编码 Codex app-server 通知失败: %w", err)
	}
	return c.writePayload(map[string]any{
		"method": method,
		"params": json.RawMessage(body),
	})
}

func (c *CodexAppServerClient) writePayload(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码 Codex app-server 消息失败: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.currentWriteError(); err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("写入 Codex app-server 请求失败: %w", err)
	}
	return nil
}

func (c *CodexAppServerClient) currentWriteError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.closing {
		return errors.New("Codex app-server client 正在关闭")
	}
	return nil
}

func (c *CodexAppServerClient) finishWithError(err error) {
	c.finish.Do(func() {
		if err == nil {
			err = errors.New("Codex app-server 已停止")
		}
		c.stopOnce.Do(func() { close(c.stop) })
		if c.serverRequestCancel != nil {
			c.serverRequestCancel()
		}
		c.mu.Lock()
		c.err = err
		if c.closing {
			// 主动关闭不是业务失败；Close 的调用方只需要知道进程是否已经收口。
			c.err = nil
		}
		pending := make([]chan codexAppServerPendingResponse, 0, len(c.pending))
		serverRequests := make([]chan CodexAppServerServerRequestResponse, 0, len(c.serverRequests))
		for key, channel := range c.pending {
			delete(c.pending, key)
			pending = append(pending, channel)
		}
		for key, channel := range c.serverRequests {
			delete(c.serverRequests, key)
			serverRequests = append(serverRequests, channel)
		}
		close(c.done)
		c.mu.Unlock()
		for _, channel := range pending {
			channel <- codexAppServerPendingResponse{err: err}
		}
		for _, channel := range serverRequests {
			channel <- CodexAppServerServerRequestResponse{Error: &CodexAppServerRPCError{
				Code:    -32603,
				Message: "Codex app-server 已停止，无法完成 server request",
			}}
		}
		close(c.notifies)
	})
}

func (c *CodexAppServerClient) decorateError(prefix string, err error) error {
	if c == nil || err == nil {
		return err
	}
	if c.isClosing() {
		return err
	}
	stderr := ""
	if c.stderr != nil {
		stderr = strings.TrimSpace(c.stderr.String())
	}
	if stderr != "" {
		if len(stderr) > 8*1024 {
			stderr = stderr[len(stderr)-8*1024:]
		}
		return fmt.Errorf("%s: %w: %s", prefix, err, stderr)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

func (c *CodexAppServerClient) stderrDiagnosticField() string {
	if c == nil || c.stderr == nil {
		return "stderr_bytes=0"
	}
	stderr := strings.TrimSpace(c.stderr.String())
	if stderr == "" {
		return "stderr_bytes=0"
	}
	if len(stderr) > 4*1024 {
		stderr = stderr[len(stderr)-4*1024:]
	}
	return fmt.Sprintf("stderr_bytes=%d stderr_tail=%q", len(stderr), stderr)
}

func (c *CodexAppServerClient) isClosing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closing
}

func (c *CodexAppServerClient) Notifications() <-chan CodexAppServerMessage {
	if c == nil {
		return nil
	}
	return c.notifies
}

func (c *CodexAppServerClient) Done() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.done
}

func (c *CodexAppServerClient) Err() error {
	if c == nil {
		return errors.New("Codex app-server client 不可用")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// terminateCodexAppServerProcess 负责终止 app-server 进程树。Windows 下
// codex.cmd 实际会形成 cmd.exe -> codex.exe 两级进程，只杀父进程会让子进程
// 继续持有 stdout 管道；taskkill 的 /T 是这里不可省略的进程边界。
func terminateCodexAppServerProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	if runtime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(process.Pid), "/T", "/F").Run(); err == nil {
			return nil
		}
	}
	return process.Kill()
}

// Close 终止进程并等待 reader/Wait goroutine 完成。它只用于 runtime shutdown
// 或替换 workspace/persona 时回收旧进程，不会重放未完成 turn；无论子进程
// 是否正确响应 stdin，Close 都必须在有限预算内返回，不能把 Wails OnShutdown
// 变成永久阻塞。
func (c *CodexAppServerClient) Close() error {
	if c == nil {
		return nil
	}
	var stdin io.WriteCloser
	var stdout io.ReadCloser
	var process *os.Process
	firstClose := false
	c.mu.Lock()
	if !c.closing {
		firstClose = true
		c.closing = true
		c.stopOnce.Do(func() { close(c.stop) })
		stdin = c.stdin
		stdout = c.stdout
		if c.cmd != nil && c.cmd.Process != nil {
			process = c.cmd.Process
		}
	}
	done := c.done
	c.mu.Unlock()
	if !firstClose {
		select {
		case <-done:
			return nil
		case <-time.After(codexAppServerCloseBudget):
			return errors.New("关闭 Codex app-server 超时")
		}
	}

	startedAt := time.Now()
	writeCodexAppServerDiagnostic(
		"codex-app-server-close-start",
		fmt.Sprintf("pid=%d", processID(process)),
	)
	// 先关闭读端，让 scanner.Scan 从“等待继承管道 EOF”中醒来；随后再杀
	// 进程树，reader 才能进入 cmd.Wait 并触发 finishWithError 收口 pending。
	if stdin != nil {
		if err := stdin.Close(); err != nil {
			writeCodexAppServerDiagnostic("codex-app-server-close-stdin-error", fmt.Sprintf("error=%q", err.Error()))
		}
	}
	if stdout != nil {
		if err := stdout.Close(); err != nil {
			writeCodexAppServerDiagnostic("codex-app-server-close-stdout-error", fmt.Sprintf("error=%q", err.Error()))
		}
	}
	terminateErr := terminateCodexAppServerProcess(process)

	timer := time.NewTimer(codexAppServerCloseBudget)
	defer timer.Stop()
	select {
	case <-done:
		// taskkill/Kill 与 app-server 自然退出可能同时发生；只要 reader 和
		// cmd.Wait 已经收口，进程终止调用返回的“already exited/access denied”
		// 就是竞态噪声，不应被记录成一次关闭失败。
		writeCodexAppServerDiagnostic(
			"codex-app-server-close-complete",
			fmt.Sprintf("pid=%d", processID(process)),
			fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
		)
		return nil
	case <-timer.C:
		if terminateErr != nil {
			writeCodexAppServerDiagnostic("codex-app-server-close-process-error", fmt.Sprintf("error=%q", terminateErr.Error()))
		}
		writeCodexAppServerDiagnostic(
			"codex-app-server-close-timeout",
			fmt.Sprintf("pid=%d", processID(process)),
			fmt.Sprintf("elapsed_ms=%d", time.Since(startedAt).Milliseconds()),
		)
		return errors.New("关闭 Codex app-server 超时")
	}
}

func processID(process *os.Process) int {
	if process == nil {
		return 0
	}
	return process.Pid
}

func codexAppServerMessageIDKey(raw json.RawMessage) string {
	return strings.TrimSpace(string(raw))
}

func codexAppServerResponseIDMatches(raw json.RawMessage, requestID int64) bool {
	if len(raw) == 0 {
		return false
	}
	var numericID int64
	if err := json.Unmarshal(raw, &numericID); err == nil {
		return numericID == requestID
	}
	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil {
		return stringID == strconv.FormatInt(requestID, 10)
	}
	return false
}

func codexAppServerInitializeParams(name, title, version string) map[string]any {
	return map[string]any{
		"clientInfo": map[string]any{
			"name":    strings.TrimSpace(name),
			"title":   strings.TrimSpace(title),
			"version": strings.TrimSpace(version),
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
	}
}

func codexPetAppServerInitializeParams(name, title, version string) map[string]any {
	params := codexAppServerInitializeParams(name, title, version)
	capabilities, ok := params["capabilities"].(map[string]any)
	if !ok {
		return params
	}
	// 宠物 runtime 没有第二套 MCP 表单配置；能力声明必须和实际的
	// elicitation handler 一致，否则 MCP server 会在握手后直接拒绝请求。
	capabilities["mcpServerOpenaiFormElicitation"] = true
	// 宠物需要完整消费 Codex 的 turn/started 和 token usage 通知，不能沿用
	// 项目管理器为降低噪声设置的 opt-out 列表。
	delete(capabilities, "optOutNotificationMethods")
	return params
}
