//go:build windows

package services

import (
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const projectManagerWTFocusTimeout = 350 * time.Millisecond
const projectManagerCodexDangerousBypassFlag = "--dangerously-bypass-approvals-and-sandbox"

type projectManagerCommandRunner interface {
	Start() error
	Wait() error
}

var (
	projectManagerWTExecutableOnce  sync.Once
	projectManagerWTExecutablePath  string
	projectManagerWTExecutableReady bool
	projectManagerLookPath          = exec.LookPath
	projectManagerExecCommand       = func(name string, args ...string) projectManagerCommandRunner {
		return exec.Command(name, args...)
	}
	projectManagerWTCommandFactory       = exec.Command
	projectManagerAICommitCommandFactory = hideWindowCmd
)

func projectManagerProjectWindowID(projectPath string) string {
	normalized := normalizeProjectManagerProjectPath(projectPath)
	trimmed := strings.TrimSpace(normalized)
	if trimmed == "" {
		return ""
	}

	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(strings.ToLower(trimmed)))
	return fmt.Sprintf("codeswitch-project-%x", hasher.Sum64())
}

func projectManagerSessionTabTitle(session SessionSummary) string {
	name := strings.TrimSpace(session.DisplayName)
	if name == "" {
		name = strings.TrimSpace(session.SourceName)
	}
	if name == "" {
		name = strings.TrimSpace(session.ID)
	}

	// 这里不能再用 `|` 作为标题分隔符。
	// 实测在打包后的 windowsgui 进程里，`wt new-tab --title "[PM]id|name" -- pwsh.exe ...`
	// 会把后续 shell 命令解析炸掉，最终报 0x80070002。
	// 会话识别本来就靠 runtime/session id，不靠标题分隔符吃业务语义，所以直接换成安全 ASCII 分隔符。
	return fmt.Sprintf("[PM]%s - %s", strings.TrimSpace(session.ID), name)
}

func (s *ProjectManagerService) loadProjectManagerActiveRuntimes() (map[string]projectManagerSessionRuntime, error) {
	paths, err := listProjectManagerSessionRuntimePaths()
	if err != nil {
		return nil, err
	}

	runtimes := make(map[string]projectManagerSessionRuntime, len(paths))
	for _, path := range paths {
		var runtime projectManagerSessionRuntime
		if err := ReadJSONFile(path, &runtime); err != nil {
			continue
		}

		sessionID := strings.TrimSpace(runtime.SessionID)
		if sessionID == "" {
			continue
		}
		runtimes[sessionID] = runtime
	}

	return runtimes, nil
}

func (s *ProjectManagerService) findProjectManagerProjectRuntime(
	session SessionSummary,
	runtimes map[string]projectManagerSessionRuntime,
) (projectManagerSessionRuntime, bool) {
	projectWindowID := projectManagerProjectWindowID(session.ProjectPath)
	if projectWindowID == "" {
		projectWindowID = projectManagerProjectWindowID(session.Cwd)
	}
	if projectWindowID == "" {
		return projectManagerSessionRuntime{}, false
	}

	for _, runtime := range runtimes {
		if !strings.EqualFold(strings.TrimSpace(runtime.LaunchSource), projectManagerRuntimeLaunchSource) {
			continue
		}
		if strings.TrimSpace(runtime.WindowID) != projectWindowID {
			continue
		}
		return runtime, true
	}

	return projectManagerSessionRuntime{}, false
}

func (s *ProjectManagerService) countProjectManagerProjectRuntimes(
	session SessionSummary,
	runtimes map[string]projectManagerSessionRuntime,
) int {
	projectWindowID := projectManagerProjectWindowID(session.ProjectPath)
	if projectWindowID == "" {
		projectWindowID = projectManagerProjectWindowID(session.Cwd)
	}
	if projectWindowID == "" {
		return 0
	}

	count := 0
	for _, runtime := range runtimes {
		if !strings.EqualFold(strings.TrimSpace(runtime.LaunchSource), projectManagerRuntimeLaunchSource) {
			continue
		}
		if strings.TrimSpace(runtime.WindowID) != projectWindowID {
			continue
		}
		count++
	}
	return count
}

func (s *ProjectManagerService) openProjectManagerSessionTerminal(session SessionSummary) error {
	launchDir := projectManagerSessionLaunchDir(session)
	projectWindowID := projectManagerProjectWindowID(launchDir)
	tabTitle := projectManagerSessionTabTitle(session)

	wtPath := findProjectManagerWTExecutable()
	reused, err := s.tryReuseProjectManagerSessionTerminal(session)
	if err != nil {
		return fmt.Errorf("恢复项目管理器终端失败: %w", err)
	}
	if reused {
		return nil
	}

	runtimePath, err := projectManagerSessionRuntimePath(session.ID)
	if err != nil {
		return err
	}

	activeRuntimes, err := s.loadProjectManagerActiveRuntimes()
	if err != nil {
		return fmt.Errorf("读取项目管理器运行态失败: %w", err)
	}
	tabIndex := s.countProjectManagerProjectRuntimes(session, activeRuntimes)

	// 这里继续沿用已经验证可用的 wt 打开路径，只在 shell 启动命令最前面挂一层运行态标记。
	// 这么做是为了准确判断“这个会话现在是否真的开着”，避免点卡片时反复新开重复终端。
	if wtPath != "" {
		if _, ok := s.findProjectManagerProjectRuntime(session, activeRuntimes); ok {
			log.Printf("[ProjectManager] 同项目窗口已存在，准备追加新 tab session=%s window=%s title=%q", session.ID, projectWindowID, tabTitle)
		}

		args := buildProjectManagerWTArgs(launchDir, session.ID, runtimePath, projectWindowID, tabTitle, tabIndex)
		if err := startProjectManagerWTCommand(launchDir, wtPath, args); err == nil {
			log.Printf("[ProjectManager] 已启动 WT 会话 tab session=%s window=%s dir=%s title=%q", session.ID, projectWindowID, launchDir, tabTitle)
			return nil
		} else {
			log.Printf("[ProjectManager] 启动 WT 失败，准备回退到 shell session=%s window=%s title=%q err=%v", session.ID, projectWindowID, tabTitle, err)
		}
	}

	if err := startProjectManagerFallbackTerminal(launchDir, session.ID, runtimePath, projectWindowID, tabTitle, tabIndex); err != nil {
		return fmt.Errorf("启动项目管理器终端失败: %w", err)
	}
	log.Printf("[ProjectManager] WT 不可用，已回退到 shell 启动 session=%s dir=%s", session.ID, launchDir)
	return nil
}

func (s *ProjectManagerService) runProjectManagerAICommit(projectPath string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}

	projectInfo, err := os.Stat(projectPath)
	if err != nil || !projectInfo.IsDir() {
		return errors.New("项目路径不存在或不是目录")
	}

	changed, err := projectManagerHasCommittableChanges(projectPath)
	if err != nil {
		return fmt.Errorf("读取 git 变更失败: %w", err)
	}
	if !changed {
		return errors.New("当前项目没有可提交变更")
	}

	if err := startProjectManagerAICommitTerminal(projectPath); err != nil {
		return fmt.Errorf("启动 AI-Commit 失败: %w", err)
	}

	log.Printf("[ProjectManager] 已启动 AI-Commit project=%s", projectPath)
	return nil
}

func (s *ProjectManagerService) openProjectManagerProjectTerminal(projectPath string) error {
	projectPath = normalizeProjectManagerProjectPath(projectPath)
	if projectPath == "" {
		return errors.New("项目路径不能为空")
	}

	projectInfo, err := os.Stat(projectPath)
	if err != nil || !projectInfo.IsDir() {
		return errors.New("项目路径不存在或不是目录")
	}

	projectWindowID := projectManagerProjectWindowID(projectPath)
	wtPath := findProjectManagerWTExecutable()
	if wtPath != "" {
		args := buildProjectManagerProjectTerminalWTArgs(projectPath, projectWindowID)
		if err := startProjectManagerWTCommand(projectPath, wtPath, args); err == nil {
			log.Printf("[ProjectManager] 已启动项目新终端 project=%s window=%s", projectPath, projectWindowID)
			return nil
		} else {
			log.Printf("[ProjectManager] 启动项目 WT 终端失败，准备回退 shell project=%s window=%s err=%v", projectPath, projectWindowID, err)
		}
	}

	if err := startProjectManagerProjectFallbackTerminal(projectPath); err != nil {
		return fmt.Errorf("启动项目终端失败: %w", err)
	}

	log.Printf("[ProjectManager] WT 不可用，已回退到 shell 项目终端 project=%s", projectPath)
	return nil
}

func (s *ProjectManagerService) tryReuseProjectManagerSessionTerminal(session SessionSummary) (bool, error) {
	runtime, exists, err := loadProjectManagerSessionRuntimeIfExists(session.ID)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}

	if strings.TrimSpace(runtime.LaunchSource) != "" && !strings.EqualFold(strings.TrimSpace(runtime.LaunchSource), projectManagerRuntimeLaunchSource) {
		log.Printf("[ProjectManager] runtime 来源非法，已丢弃 session=%s source=%s", session.ID, runtime.LaunchSource)
		_ = removeProjectManagerSessionRuntime(session.ID)
		return false, nil
	}

	// 复用前必须先确认 runtime 绑定的 shell 还真活着。
	// 否则只靠残留 json 就直接 focus-tab，会把某个 WT 窗口拉出来冒充“已恢复会话”，
	// 结果后续不会重新 new-tab，更不会执行 codex resume，用户看到的就是开了终端但没进目标会话。
	processes, err := projectManagerSnapshotProcesses()
	if err != nil {
		return false, fmt.Errorf("读取终端进程快照失败: %w", err)
	}
	if err := validateProjectManagerSessionRuntime(runtime, processes); err != nil {
		if errors.Is(err, errProjectManagerRuntimeInactive) {
			log.Printf("[ProjectManager] runtime 预检已失效，清理后重新打开 session=%s shell_pid=%d window=%s", session.ID, runtime.ShellPID, runtime.WindowID)
			_ = removeProjectManagerSessionRuntime(session.ID)
			return false, nil
		}
		return false, fmt.Errorf("预检项目管理器运行态失败: %w", err)
	}

	if wtPath := findProjectManagerWTExecutable(); wtPath != "" && strings.TrimSpace(runtime.WindowID) != "" {
		if err := focusProjectManagerNamedWTTab(wtPath, runtime, session); err == nil {
			log.Printf("[ProjectManager] 已通过 WT 项目窗口恢复会话 tab session=%s shell_pid=%d window=%s title=%q", session.ID, runtime.ShellPID, runtime.WindowID, runtime.TabTitle)
			return true, nil
		} else {
			log.Printf("[ProjectManager] WT 命名窗口恢复失败，准备回退 Win32 激活 session=%s shell_pid=%d window=%s title=%q err=%v", session.ID, runtime.ShellPID, runtime.WindowID, runtime.TabTitle, err)
		}
	}

	if err := focusProjectManagerTerminalWindowWithProcesses(runtime, session, processes); err != nil {
		// 运行态文件只代表“上次是这个 shell 拉起的会话”，并不保证窗口还活着；
		// 一旦 shell pid 已失效或 pid 被系统复用，必须先清理脏标记，避免之后每次点击都误判。
		if errors.Is(err, errProjectManagerRuntimeInactive) {
			log.Printf("[ProjectManager] runtime 已失效，清理后重新打开 session=%s shell_pid=%d window=%s", session.ID, runtime.ShellPID, runtime.WindowID)
			_ = removeProjectManagerSessionRuntime(session.ID)
			return false, nil
		}
		return false, fmt.Errorf("已识别到项目管理器终端实例，但恢复窗口失败: %w", err)
	}
	log.Printf("[ProjectManager] 已恢复现有终端窗口 session=%s shell_pid=%d window=%s", session.ID, runtime.ShellPID, runtime.WindowID)
	return true, nil
}

func buildProjectManagerWTArgs(
	launchDir string,
	sessionID string,
	runtimePath string,
	windowID string,
	tabTitle string,
	tabIndex int,
) []string {
	// WT 主路径故意使用裸 `pwsh.exe`，不再提前解析成完整路径。
	// Windows Terminal 会对 new-tab 的 commandline 做二次解析；完整路径加参数在某些版本里
	// 会被合成一个带空格的 executable，最终触发 0x80070002。
	// `--` 是 WT 自身参数和目标 commandline 的硬边界；没有它，WT 仍可能把后续 token 错当自身参数或整体重组。
	// 用户环境已保证 pwsh.exe 在全局 PATH，由 WT 自己解析裸命令才能稳定保持 wt -> pwsh -> codex。
	shellExecutable := "pwsh.exe"
	return append([]string{
		"-w", resolveProjectManagerWTWindowName(windowID),
		"new-tab",
		"-d", launchDir,
		"--title", tabTitle,
		"--",
	}, buildProjectManagerPowerShellCommandArgs(shellExecutable, sessionID, runtimePath, windowID, tabTitle, tabIndex)...)
}

func buildProjectManagerProjectTerminalWTArgs(projectPath string, windowID string) []string {
	// 项目终端与会话终端共享同一条主链路约束：WT 只接收裸 `pwsh.exe` 和参数 token。
	// `--` 明确切断 WT 参数解析，fallback 才负责解析完整 shell 路径，主路径不能把本机绝对路径塞给 WT。
	shellExecutable := "pwsh.exe"
	return append([]string{
		"-w", resolveProjectManagerWTWindowName(windowID),
		"new-tab",
		"-d", projectPath,
		"--",
	}, buildProjectManagerProjectTerminalCommandArgs(shellExecutable, projectPath)...)
}

func startProjectManagerWTCommand(workingDir string, wtPath string, wtArgs []string) error {
	// 这里保持最短链路：CodeSwitch -> WT -> pwsh。
	// 之前额外套 cmd/PowerShell 虽能绕过部分参数解析问题，但会污染 PATH 命中并拖慢启动；
	// 现在把 Codex 版本选择放回 pwsh 脚本内部处理，WT 只负责开 tab。
	log.Printf("[ProjectManager] 准备启动 WT working_dir=%q wt=%q args=%q", workingDir, wtPath, wtArgs)
	cmd := projectManagerWTCommandFactory(wtPath, wtArgs...)
	cmd.Dir = workingDir
	return cmd.Start()
}

func resolveProjectManagerWTWindowName(windowID string) string {
	trimmed := strings.TrimSpace(windowID)
	if trimmed == "" {
		return "new"
	}
	return trimmed
}

func focusProjectManagerNamedWTTab(
	wtPath string,
	runtime projectManagerSessionRuntime,
	session SessionSummary,
) error {
	windowID := strings.TrimSpace(runtime.WindowID)
	tabTitle := strings.TrimSpace(runtime.TabTitle)
	if strings.TrimSpace(wtPath) == "" || windowID == "" || tabTitle == "" {
		return errors.New("缺少 WT 命名窗口恢复参数")
	}

	// 这里先让 WT 自己激活项目窗口，再交给 Win32 基于 tab 标题二次兜底定位。
	// 不直接赌 tab index，是因为同项目下 tab 顺序会持续变化，靠索引早晚翻车。
	tabIndex := runtime.TabIndex
	if tabIndex < 0 {
		tabIndex = 0
	}

	// 这里故意不再同步傻等 WT CLI 完整退出。
	// 用户点击卡片最需要的是“窗口立刻有反应”，而 WT 在某些机器上 focus-tab 退出会慢几百毫秒到几秒。
	// 只要命令已成功启动并进入 WT 处理阶段，就视为复用请求已被接管，后续由 WT/Win32 自己完成切前台。
	cmd := projectManagerExecCommand(wtPath, "-w", windowID, "focus-tab", "-t", fmt.Sprintf("%d", tabIndex))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wt focus-tab 启动失败: %w", err)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	select {
	case err := <-waitDone:
		if err != nil {
			return fmt.Errorf("wt focus-tab 执行失败: %w", err)
		}
	case <-time.After(projectManagerWTFocusTimeout):
	}

	log.Printf("[ProjectManager] WT 项目窗口激活成功 session=%s window=%s title=%q", session.ID, windowID, tabTitle)
	return nil
}

func startProjectManagerFallbackTerminal(
	launchDir string,
	sessionID string,
	runtimePath string,
	windowID string,
	tabTitle string,
	tabIndex int,
) error {
	fallbackShell := projectManagerPreferredShellExecutable()
	innerArgs := buildProjectManagerPowerShellCommandArgs(fallbackShell, sessionID, runtimePath, windowID, tabTitle, tabIndex)
	quotedInnerArgs := make([]string, 0, len(innerArgs))
	for _, arg := range innerArgs[1:] {
		quotedInnerArgs = append(quotedInnerArgs, fmt.Sprintf("'%s'", escapeProjectManagerPowerShellSingleQuoted(arg)))
	}

	// 这里是用户要直接看到并操作的交互式终端，不能再偷偷用 Hidden 把窗口藏起来。
	// 当 WT 不可用时，退回直接启动 shell，也必须保持可见，否则前端点击就会表现成“没反应”。
	cmd := exec.Command(
		"powershell.exe",
		"-NoProfile",
		"-Command",
		fmt.Sprintf(
			"Start-Process -FilePath '%s' -ArgumentList %s -WorkingDirectory '%s'",
			escapeProjectManagerPowerShellSingleQuoted(innerArgs[0]),
			strings.Join(quotedInnerArgs, ","),
			escapeProjectManagerPowerShellSingleQuoted(launchDir),
		),
	)
	cmd.Dir = launchDir
	return cmd.Start()
}

func startProjectManagerProjectFallbackTerminal(projectPath string) error {
	fallbackShell := projectManagerPreferredShellExecutable()
	innerArgs := buildProjectManagerProjectTerminalCommandArgs(fallbackShell, projectPath)
	quotedInnerArgs := make([]string, 0, len(innerArgs))
	for _, arg := range innerArgs[1:] {
		quotedInnerArgs = append(quotedInnerArgs, fmt.Sprintf("'%s'", escapeProjectManagerPowerShellSingleQuoted(arg)))
	}

	// 这里是用户主动点“打开终端”要拿到一个全新的可交互 codex 终端，
	// 所以 fallback 也必须像系统“在终端中打开”那样直接起可见 shell，不能偷藏后台进程。
	cmd := exec.Command(
		"powershell.exe",
		"-NoProfile",
		"-Command",
		fmt.Sprintf(
			"Start-Process -FilePath '%s' -ArgumentList %s -WorkingDirectory '%s'",
			escapeProjectManagerPowerShellSingleQuoted(innerArgs[0]),
			strings.Join(quotedInnerArgs, ","),
			escapeProjectManagerPowerShellSingleQuoted(projectPath),
		),
	)
	cmd.Dir = projectPath
	return cmd.Start()
}

func buildProjectManagerPowerShellCommandArgs(
	shell string,
	sessionID string,
	runtimePath string,
	windowID string,
	tabTitle string,
	tabIndex int,
) []string {
	return []string{
		shell,
		"-NoExit",
		"-EncodedCommand",
		encodeProjectManagerPowerShellCommand(buildProjectManagerPowerShellLaunchCommand(sessionID, runtimePath, windowID, tabTitle, tabIndex)),
	}
}

func buildProjectManagerProjectTerminalCommandArgs(shell string, projectPath string) []string {
	return []string{
		shell,
		"-NoExit",
		"-EncodedCommand",
		encodeProjectManagerPowerShellCommand(buildProjectManagerProjectTerminalPowerShellCommand(projectPath)),
	}
}

func projectManagerPreferredShellExecutable() string {
	candidates := []string{"pwsh.exe", "powershell.exe"}
	for _, candidate := range candidates {
		resolved, err := projectManagerLookPath(candidate)
		if err != nil {
			continue
		}
		resolved = strings.TrimSpace(resolved)
		if resolved != "" {
			return resolved
		}
	}
	return "powershell.exe"
}

func projectManagerRequiredPwshExecutable() (string, error) {
	resolved, err := projectManagerLookPath("pwsh.exe")
	if err != nil {
		return "", errors.New("未找到 pwsh.exe，请先安装 PowerShell 7 或检查 PATH")
	}

	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return "", errors.New("未找到 pwsh.exe，请先安装 PowerShell 7 或检查 PATH")
	}

	return resolved, nil
}

func projectManagerHasCommittableChanges(projectPath string) (bool, error) {
	cmd := hideWindowCmd("git", "status", "--porcelain")
	cmd.Dir = projectPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return false, errors.New(message)
		}
		return false, err
	}

	return strings.TrimSpace(string(output)) != "", nil
}

func startProjectManagerAICommitTerminal(projectPath string) error {
	shellExecutable, err := projectManagerRequiredPwshExecutable()
	if err != nil {
		return err
	}

	cmd := projectManagerAICommitCommandFactory(shellExecutable, buildProjectManagerAICommitLauncherArgs(projectPath, shellExecutable)...)
	cmd.Dir = projectPath
	return cmd.Start()
}

func buildProjectManagerAICommitLauncherArgs(projectPath string, shellExecutable string) []string {
	return []string{
		"-NoProfile",
		"-EncodedCommand",
		encodeProjectManagerPowerShellCommand(buildProjectManagerAICommitLaunchCommand(projectPath, shellExecutable)),
	}
}

func buildProjectManagerAICommitLaunchCommand(projectPath string, shellExecutable string) string {
	escapedProjectPath := escapeProjectManagerPowerShellSingleQuoted(projectPath)
	escapedShellExecutable := escapeProjectManagerPowerShellSingleQuoted(shellExecutable)
	innerArgs := []string{
		"-NoProfile",
		"-ExecutionPolicy",
		"Bypass",
		"-Command",
		buildProjectManagerAICommitPowerShellCommand(projectPath),
	}
	quotedInnerArgs := make([]string, 0, len(innerArgs))
	for _, arg := range innerArgs {
		quotedInnerArgs = append(quotedInnerArgs, fmt.Sprintf("'%s'", escapeProjectManagerPowerShellSingleQuoted(arg)))
	}

	// 这里外层不能再直接从 GUI 进程裸起一个 pwsh 去跑 commit。
	// 那样命令虽然可能在后台执行，但不会带出用户可见的终端窗口，体感就是“点了没反应”。
	// 所以外层隐藏 pwsh 只负责 Start-Process，真正给用户看的任务窗口必须由内层可见 pwsh 拉起。
	return fmt.Sprintf(
		"$ErrorActionPreference = 'Stop'; Start-Process -FilePath '%s' -ArgumentList @(%s) -WorkingDirectory '%s' | Out-Null",
		escapedShellExecutable,
		strings.Join(quotedInnerArgs, ", "),
		escapedProjectPath,
	)
}

func buildProjectManagerAICommitPowerShellCommand(projectPath string) string {
	escapedProjectPath := escapeProjectManagerPowerShellSingleQuoted(projectPath)
	commitPrompt := escapeProjectManagerPowerShellSingleQuoted(`$commit commit本地文件`)
	failureMessage := escapeProjectManagerPowerShellSingleQuoted("AI-Commit 执行失败，按 Enter 关闭窗口")

	// 这里必须把 -p/--profile 放在 codex 根命令层，而不是 exec 子命令层。
	// 当前用户机器上的 codex-cli 0.122.0 存在实测差异：
	// `codex exec -p commit-fast ...` 会报 profile not found，
	// 但 `codex -p commit-fast exec ...` 才能正确加载 `CODEX_HOME/commit-fast.config.toml`。
	// 所以这里明确使用根命令 profile 写法，别再赌 CLI 子命令参数继承。
	//
	// 这里直接让可见 shell 自己跑完 commit 命令。
	// 成功就 exit 0 自动关窗；失败再停留等待用户确认，这样不会为了“保留失败现场”额外再炸出第二个窗口。
	return fmt.Sprintf(
		"%s; Set-Location -LiteralPath '%s'; %s; $__exitCode = $LASTEXITCODE; if ($__exitCode -eq 0) { exit 0 }; Write-Host '%s'; Read-Host | Out-Null; exit $__exitCode",
		buildProjectManagerCodexResolverPowerShell(),
		escapedProjectPath,
		buildProjectManagerCodexCommand("-p", "commit-fast", "exec", fmt.Sprintf("'%s'", commitPrompt)),
		failureMessage,
	)
}

func buildProjectManagerPowerShellLaunchCommand(
	sessionID string,
	runtimePath string,
	windowID string,
	tabTitle string,
	tabIndex int,
) string {
	resumeCommand := buildProjectManagerPowerShellResumeCommand(sessionID)
	trimmedRuntimePath := strings.TrimSpace(runtimePath)
	if trimmedRuntimePath == "" {
		return resumeCommand
	}

	escapedRuntimePath := escapeProjectManagerPowerShellSingleQuoted(trimmedRuntimePath)
	escapedSessionID := escapeProjectManagerPowerShellSingleQuoted(strings.TrimSpace(sessionID))
	escapedWindowID := escapeProjectManagerPowerShellSingleQuoted(strings.TrimSpace(windowID))
	escapedTabTitle := escapeProjectManagerPowerShellSingleQuoted(strings.TrimSpace(tabTitle))

	parts := []string{
		buildProjectManagerCodexResolverPowerShell(),
		fmt.Sprintf("$__codeSwitchRuntimePath = '%s'", escapedRuntimePath),
		"$__codeSwitchRuntimeDir = [System.IO.Path]::GetDirectoryName($__codeSwitchRuntimePath)",
		"if (-not [string]::IsNullOrWhiteSpace($__codeSwitchRuntimeDir)) { [System.IO.Directory]::CreateDirectory($__codeSwitchRuntimeDir) | Out-Null }",
		// 这里把 window_id 固定成项目级窗口，把 tab_title 固定成会话级标题。
		// 这样同项目多个会话会并入一个 WT 窗口，但仍然能靠唯一标题判断“这个会话 tab 是否已经打开”。
		fmt.Sprintf("$__codeSwitchRuntime = @{ session_id = '%s'; shell_pid = $PID; shell_started_at = (Get-Process -Id $PID).StartTime.ToUniversalTime().ToString('o'); launch_source = '%s'; window_id = '%s'; tab_title = '%s'; tab_index = %d }", escapedSessionID, projectManagerRuntimeLaunchSource, escapedWindowID, escapedTabTitle, tabIndex),
		"try { $__codeSwitchRuntime | ConvertTo-Json -Compress | Set-Content -LiteralPath $__codeSwitchRuntimePath -Encoding utf8 -ErrorAction Stop } catch {}",
		fmt.Sprintf("try { %s } finally { Remove-Item -LiteralPath $__codeSwitchRuntimePath -Force -ErrorAction SilentlyContinue }", resumeCommand),
	}
	return strings.Join(parts, "; ")
}

func buildProjectManagerPowerShellResumeCommand(sessionID string) string {
	escaped := escapeProjectManagerPowerShellSingleQuoted(sessionID)
	return buildProjectManagerCodexCommand("resume", fmt.Sprintf("'%s'", escaped))
}

func buildProjectManagerProjectTerminalPowerShellCommand(projectPath string) string {
	escapedProjectPath := escapeProjectManagerPowerShellSingleQuoted(projectPath)

	// 这里故意只做两件事：切到项目目录，然后进入新的 codex 交互终端。
	// 头部按钮的职责是“新开一个项目终端”，不是恢复历史会话，所以绝不能混入 resume。
	return fmt.Sprintf("%s; Set-Location -LiteralPath '%s'; %s", buildProjectManagerCodexResolverPowerShell(), escapedProjectPath, buildProjectManagerCodexCommand())
}

func buildProjectManagerCodexCommand(args ...string) string {
	parts := append([]string{"& $__codeSwitchCodexCommand", projectManagerCodexDangerousBypassFlag}, args...)
	return strings.Join(parts, " ")
}

func buildProjectManagerCodexResolverPowerShell() string {
	// 不再裸写 `codex` 交给 PATH 猜版本。
	// 用户机器上 Volta 可能同时存在 user shim 和 node image 快照；
	// PATH 若先命中 `tools/image/node/.../codex`，就会启动旧版 Codex。
	// 这里优先使用 Volta 的用户 shim，它会按 Volta 当前包记录解析到最新 @openai/codex。
	parts := []string{
		"$__codeSwitchCodexCommand = 'codex'",
		"$__codeSwitchVoltaCodex = Join-Path $env:LOCALAPPDATA 'Volta\\bin\\codex.cmd'",
		"if (Test-Path -LiteralPath $__codeSwitchVoltaCodex) { $__codeSwitchCodexCommand = $__codeSwitchVoltaCodex }",
	}
	return strings.Join(parts, "; ")
}

func encodeProjectManagerPowerShellCommand(command string) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}

	encodedRunes := utf16.Encode([]rune(command))
	buf := make([]byte, 0, len(encodedRunes)*2)
	for _, r := range encodedRunes {
		buf = append(buf, byte(r), byte(r>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func findProjectManagerWTExecutable() string {
	projectManagerWTExecutableOnce.Do(func() {
		projectManagerWTExecutableReady = true
		candidates := []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WindowsApps", "wt.exe"),
		}
		for _, candidate := range candidates {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			if _, err := os.Stat(candidate); err == nil {
				projectManagerWTExecutablePath = candidate
				return
			}
		}
	})
	if !projectManagerWTExecutableReady {
		return ""
	}
	return projectManagerWTExecutablePath
}

func escapeProjectManagerPowerShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, `'`, `''`)
}

func projectManagerSessionLaunchDir(session SessionSummary) string {
	launchDir := strings.TrimSpace(session.ProjectPath)
	if launchDir == "" {
		launchDir = strings.TrimSpace(session.Cwd)
	}
	if launchDir == "" || !filepath.IsAbs(launchDir) {
		return "."
	}
	return launchDir
}
