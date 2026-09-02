# Evidence Bundle

## Scope

- 本轮收口：宠物 AI native tool continuation、按 `petId` 的 workspace resolver、PCM16 音频事件、PetChat MediaRecorder 录音转写、Provider/Gemini 模型类别配置闭环和专项回归。
- 未改变 OpenCowork 源目录；未迁移 API Key；未把前端路径作为 workspace source of truth。

## Verified

- `go test ./services -run '^TestPet' -p 1 -count=1`：通过；包含 provider、native tool loop、梦境图片、Studio、窗口和调度器测试。
- 2026-08-13 fresh `go test ./services -run '^TestPet' -p 1 -count=1`：通过，退出码 0。
- 2026-08-13 fresh `npm exec -- vue-tsc --noEmit`（`frontend`）：通过，退出码 0。
- `go test . -count=1`：失败于既有 `TestSeedMockRequestLogs`，错误为 `database is locked (5) (SQLITE_BUSY)`；不是宠物模块测试失败。
- `go test ./... -p 1 -count=1`：根包因同一共享 request-log SQLite 锁失败，`resources/model-pricing` 和 `services` 通过。
- `go test ./services -run 'TestPetAIWorkspaceResolver|TestPetAIChatExecutesOpenAITool|TestPetAIToolResponses|TestPetAIProjectFolder|TestPetAIToolContinuationLimit' -count=1`：通过。
- `npm exec -- vue-tsc --noEmit`（`frontend`）：退出码 0；已对齐 Wails `GeminiProvider.envConfig` 的可选 map 值契约。
- `wails3 task common:generate:bindings`：成功；生成 binding 含 `Provider.modelCategories`、`GeminiProvider.modelCategory` 和 `GeminiPreset.modelCategory`。
- Vite HTTP 检查：`/`、`/pet`、`/pet/settings`、`PetWindow.vue`、`PetStudio.vue`、中英文 locale 均返回 HTTP 200。
- 2026-08-13 fresh Vite HTTP 检查：`/`、`/pet`、`/pet/settings` 均返回 `200`；服务仍监听 `127.0.0.1:5173`。
- 2026-08-13 static entry check：`main.go` 注册 `PetStudioAPIService`/`PetWindowAPI`，注入 `embed.FS`，桌宠窗口默认透明、点击穿透且不置顶。
- 2026-08-13 skin source check：Studio 空 `skinId` 固定读取只读默认 `capybara`；运行时 atlas 按 active skin 优先，职责边界明确。
- `PetWindow.vue` 的窗口层文案已接入 `pet.window` 中英文 locale；`PetStudio.vue` 的可选 i18n 参数和皮肤删除 busy 判断已修复。
- 前端只读审查确认 `pet.audio` 使用 PCM16 little-endian、24 kHz、跨 chunk 半字节拼接，并在取消和卸载时丢弃旧 session。
- workspace 测试覆盖：伪造 `projectFolder`、未绑定宠物、绑定目录不存在、多宠物隔离。
- `go test ./services -run '^TestPet' -p 1 -count=1` 在修复 action-sheet 最后一行右对齐边界后通过；该回归覆盖 5 帧序列图的布局、像素顺序和非法输入。
- PetChat 录音链路静态核对：优先 `audio/webm;codecs=opus`，停止时释放 `MediaStreamTrack`，小于 1 KB 丢弃，转写成功后复用 `sendMessage` 自动发送；组件销毁和宠物切换通过 generation 丢弃旧回调。
- `git diff --check`：通过；所有 `services/pet_*.go` 经 `gofmt -l` 检查无输出。
- 2026-08-13 `git diff --check`：通过；本次修改涉及的 Go 宠物文件单文件 `gofmt -d` 无输出。`main.go` 保持仓库现有 CRLF，不做无关换行重写。
- 2026-08-13 Windows production build：无代理时 `go mod tidy` 因访问 `sum.golang.org` 超时失败；通过 `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY=http://127.0.0.1:7890` 重跑 `task windows:build PRODUCTION=true` 成功退出 `0`，完成 Vite production bundle、bindings、`.syso` 和 `go build`，生成 `bin/CodeSwitch.exe`。
- 2026-08-13 settings parity slice：`git diff --check`、`npm exec -- vue-tsc --noEmit`、`go test ./services -run '^TestPet' -p 1 -count=1` 均通过；`en.json`/`zh.json` 通过 `ConvertFrom-Json`；临时 Vite 服务下 `/`、`/pet/settings`、`src/components/Pet/PetSettings.vue` 均返回 `200`。
- 2026-08-13 settings slice final rerun：默认 Capybara atlas 预览修正后，`git diff --check`、`npm exec -- vue-tsc --noEmit`、`go test ./services -run '^TestPet' -p 1 -count=1` 仍全部通过；locale JSON 解析通过；临时 Vite 进程已停止。
- 2026-08-13 settings follow-up：`selectTab` 仍存在且可被模板页签调用；源版 `PET_TABS` 顺序与目标一致；Agent、语音、Sleep、Skins、Stats 的 `v-show` 区域逐段复核；语音预设匹配已改为 `some()`。
- 2026-08-13 settings Vite fresh check：`/`、`/pet/settings`、`/src/components/Pet/PetSettings.vue` 均返回 HTTP `200`；临时服务随后已停止。
- 2026-08-13 final focused rerun：`git diff --check`、`en.json`/`zh.json` 解析、`npm exec -- vue-tsc --noEmit` 和 `go test ./services -run '^TestPet' -p 1 -count=1` 均退出码 `0`；Go 专项输出 `ok codeswitch/services`。
- 2026-08-13 settings browser evidence：独立 Vite `5175` + Edge CDP `9227` 下实际读取 `PetSettings.vue`，页面 `loading=false`、无错误提示，显示真实 `Kapi`、SQLite snapshot 和默认 atlas；普通内容宽度 `672px`，总览预览 `672x198px`，8 个页签高度均为 `32px` 且未折行。截图证据保留在 `temp-pet-settings-5175.png`、`temp-pet-settings-1440-after-nowrap.png`。
- 2026-08-13 settings model parity follow-up：Agent、Voice、Sleep 模型分别按 platform/provider 分组；图片模型只显示 `enabled + image`，reasoning 只显示选中模型声明的等级；主动互动额度显示为低/中/高频 `1/2/4`，免打扰开始/结束改为 `00:00` 至 `23:00` 下拉。
- 2026-08-13 settings visual parity follow-up：Stats 区块为 `16px` padding、`8px` radius，汇总卡片是左图标和右侧上下两行；Skins atlas 缩略图为 `48px`，皮肤目录、路径和 metadata 支持断行，移动端操作项不再使用固定 `margin-left`。
- 2026-08-13 bindings refresh：`wails3 task common:generate:bindings` 退出码 `0`，处理 `572 Packages, 33 Services, 248 Methods, 19 Enums, 147 Models`；`frontend/bindings/codeswitch/services/models.ts` 包含 `modelReasoningEffortLevels` 和 Gemini `reasoningEffortLevels`。
- 2026-08-13 focused verification：`git diff --check`、`npm exec -- vue-tsc --noEmit`（`frontend`）、`go test ./services -run '^TestPet' -p 1 -count=1 -timeout 60s`、`ConvertFrom-Json` 解析 `en.json`/`zh.json` 均退出码 `0`；Go 输出 `ok codeswitch/services`。
- 2026-08-13 process preservation：PID `19732` 与 `20640` 的旧 `CodeSwitch.exe` 均保持运行；本轮没有按进程名批量杀桌面进程。
- 2026-08-13 loopback bridge evidence：`18101` 的 `PetService.GetSnapshot`、`ProjectManagerService.GetSnapshot`、`PetStudioAPIService.ReadSkin(default, '')` 均返回 HTTP `200`；`ReadSkin` 的默认皮肤 ID 契约是空字符串，使用字面量 `default` 返回 `400` 为预期行为。
- 2026-08-13 process hygiene evidence：仅结束了带明确 `--remote-debugging-port=9226/9227` 和独立临时 profile 的 Chrome/Edge 验证进程树；PID `19732` 和 `20640` 的旧 `CodeSwitch.exe` 实例在清理后仍存在，监听端口 `5175/9226/9227` 均已释放。
- 2026-08-13 browser bridge root-cause evidence：隔离验证首次页面请求仍落到默认 `18101`，页面显示“无法连接宠物本地服务”；在 `frontend/src/wails-runtime-compat/browserBridge.ts` 增加 `petBridge` hash/query 覆盖并限制为 loopback `/api/pet/call` 后，`18203` bridge 的真实 SQLite 快照可正常渲染。
- 2026-08-13 isolated browser evidence：PID `15964` 使用 `18202/18203`，Chrome `5177` 读取到 `Kapi`、Lv.5、金币、默认 Capybara atlas；总览、属性、Agent、睡眠、记忆、历史梦境、皮肤和 Studio 页签均无加载错误，页面截图显示 atlas 正常。
- 2026-08-13 isolated native window evidence：`PetWindowAPI.State` 返回 `open=true`、`mode=passive`、`clickThrough=true`、`alwaysOnTop=false`；验证后只停止 PID `15964` 和本轮 Vite，旧桌面进程未按进程名清理。
- 2026-08-13 音频/转写取消回归：`go test ./services -run 'TestPetAudioCancelBeforeProviderResolutionStopsRegistrationWindow|TestPetAISynthesizeSpeechCancelBeforeProviderResolution|TestPetAITranscribeAudioPreservesCallerCancellation|TestPetAISynthesizeChatAudioCanBeCancelled' -p 1 -count=5 -timeout 60s` 退出码 `0`；覆盖 provider 解析取消、HTTP context 取消、流式 TTS 取消和同步 chat-audio 取消。
- 2026-08-13 浏览器 bridge 取消实现：`browserBridge.ts` 的 health `GET` 与业务 `POST` 共用调用级 `AbortSignal`；调用方取消透传 `AbortError`，只有内部超时才转换为服务提示；SSE 事件流仍使用无 signal 的共享 token 加载。
- 2026-08-13 PetChat 录音限制：源码固定最长 `60s`、累计 `240 KiB` 上限，超限停止捕获并显示中英文限制文案；这是 multipart 上传前的静态边界，Windows 麦克风/真实转写 provider 仍未手工验收。
- 2026-08-13 进程保护：PID `16932`（`F:\下载\CodeSwitch.exe`）与 PID `23448`（`F:\GitlabProjects\code-switch-cli\bin\pet-verify\CodeSwitch.exe`）复核仍存，本轮未按进程名批量清理 `CodeSwitch.exe`。

## Residual Risk

- 2026-08-17 Windows 启动前台回归：`main` 基线与当前分支的 ProjectManager Terminal 激活主体一致，新增内容仅是运行时诊断；代码差异集中在桌宠分支新增的 Wails 主窗/宠物窗生命周期。Wails v3 alpha.38 `windowsWebviewWindow.show()` 使用 `SW_SHOW`，首次 `navigationCompleted()` 在非 Hidden 窗口上会调用 `parent.Show()`；当前主窗改为 Hidden 后以 `SWP_NOACTIVATE` 首次显示。
- 2026-08-17 首屏双显示收口：`main_window_windows.go` 的第二次 `ShowWindowAsync(SW_SHOWNOACTIVATE)` 只在 `SetWindowPos(...SWP_SHOWWINDOW | SWP_NOACTIVATE)` 后窗口仍不可见时执行；避免正常路径多出第二次显示请求。未构建或运行验证，原因是用户明确自行构建和验收。
- 2026-08-17 时间戳证据：当前 `bin/CodeSwitch.exe` 为 `09:07:49`，`main.go` 为 `09:50:16`、`main_window_windows.go` 为 `09:58:01`；运行日志中的最新 GUI PID `15128` 仍是旧实现，包含 `main-window-show focus=false`，不包含最新无激活路径日志。
- 2026-08-17 WT 持续闪动证据：GUI PID `15128` 生命周期内记录到 `108` 次 Windows Terminal 前台事件；同时 `terminal-activate-*`、`terminal-tab-focus-*`、`pet-native-focus` 和 `pet-native-release-focus` 均为 `0`。启动初始的 `CodeSwitch -> WT` 切换可由旧 `mainWindow.Show()` 的 Wails `SW_SHOW` 路径解释；其余持续事件尚无足够证据归因，状态为 `deepest-confirmed-root-unknown`。
- 2026-08-17 观测升级：`services/runtime_foreground_windows.go` 新增 Win32 `EVENT_SYSTEM_FOREGROUND` 回调记录，注册发生在 Wails 消息循环主线程；它记录回调原始 HWND，避免 100ms 轮询漏掉瞬态 `CodeSwitch -> WT` 切换。未构建、未启动、未结束任何进程，等待用户使用新二进制复现。

- 2026-08-17 根因收口：新版 GUI PID `29056` 的完整生命周期没有 `terminal-wt-start`、`terminal-activate-*`、`terminal-tab-focus-*` 或 `new-tab` 记录，同时间段的 `project-manager-terminal-debug.log` 也没有启动记录，因此“ProjectManager 循环开 tab”被排除。
- `foreground-window-event` 直接记录到 `WT(0x60832) -> CodeSwitch 主窗(0x110674) -> WT(0x60832)`，其中 CodeSwitch 主窗只在 203ms 内短暂成为前台，随后 94ms 后 WT 再次成为前台；这与用户看到的 WT 闪烁一致。
- Wails v3 alpha.38 的 Windows 实现会在 `WM_SETFOCUS` 中调用内部 `focus()`，而 `focus()` 无条件调用 `SetForegroundWindow`。`Hidden: true` 只移除初始 `WS_VISIBLE`，不会设置 `WS_EX_NOACTIVATE`，所以 WebView 初始化仍可能激活主窗。
- 源码修复已收敛到主窗初始化样式：Windows 创建阶段使用 `WS_EX_APPWINDOW | WS_EX_NOACTIVATE`，首次通过 `SWP_NOACTIVATE` 显示后，使用 `SetWindowLongPtr` 无激活移除临时 `WS_EX_NOACTIVATE`；没有修改 ProjectManager 的终端创建或激活逻辑。
- 当前状态：上述 `WS_EX_NOACTIVATE` 实验方案未通过用户实测，已撤回，不再作为当前实现。

- 2026-08-17 启动链回归：`main.go` 已恢复稳定主线方案，主窗口不再使用 `Hidden`/`Windows.ExStyle`，创建后立即执行 `showMainWindow(false)`；宠物窗口恢复为 `ApplicationStarted -> petWindow.Open()`。
- 2026-08-17 实验文件清理：已删除仅服务于隐藏主窗、导航完成接管和非激活显示的 `main_window_windows.go`、`main_window_other.go`；前台监控仍只写日志，不改变窗口行为。
- 本轮未构建、未启动、未结束进程、未运行测试，等待用户使用回归主线启动链的新二进制验收。

- `SEC-TOCTOU-001`：`services/pet_agent_tools.go` 先通过 `EvalSymlinks`/`filepath.Rel` 校验，再以路径执行 `Stat`/`Open`；workspace 内部若被并发替换 symlink，理论上仍存在 TOCTOU。彻底修复需要 `pet_agent_secure_open_windows.go`、`pet_agent_secure_unix.go` 等 Windows handle/reparse-point 与 POSIX `openat/openat2` 实现，本轮未引入该 syscall 层；该风险不阻塞当前功能验收。
- Windows Wails 实机窗口、`AudioContext`、Wails 事件时序和真实 provider 流式响应尚未手工验收；现有旧 `CodeSwitch.exe` 实例使用旧内嵌资源，不能作为最新前端修改的原生窗口证据。
- Windows 实际麦克风权限、`MediaRecorder` 编码器可用性、真实 `audio/transcriptions` provider 和 Wails `TranscribeAudio` 调用尚未手工验收。
- 当前内置浏览器连接器不可用，因此未取得页面截图或点击级视觉证据；HTTP 200 只证明 Vite 能提供模块。
- `F:\GitlabProjects\OpenCowork` 当前可读；本轮对 `PetPanel.tsx`、`PetOverviewTab.tsx`、`PetStatsTab.tsx`、`PetSkinsTab.tsx`、`PetAgentTab.tsx`、`PetSleepTab.tsx` 做了定点文件级复核，但未取得截图或点击级视觉证据。
- 本轮设置页对照仍以此前保存的 OpenCowork `PetPanel`、`PetOverviewTab`、`PetSkinsTab` 和 `PetAgentTab` 证据为基线；总览固定读取默认 Capybara atlas，运行态 active atlas 只用于桌宠和皮肤列表。
- 构建依赖网络仍依赖可用的 Go proxy/checksum 服务；项目未硬编码个人代理，也未采用 `GOSUMDB=off` 这类削弱校验的规避方案。
- 本轮 bridge/设置页浏览器证据不覆盖真实 provider 流式聊天、麦克风权限、转写、TTS、梦境生成和 Studio 保存/删除；这些仍需最新 desktop bundle 的原生手工验收。
- 本轮 bridge 取消验证覆盖 health token 获取和业务 POST 的 signal 传播，但没有替代最新 desktop bundle 的 Wails 原生音频事件、麦克风权限或真实 provider 验收。

## Next Gate

- 使用最新 desktop bundle 继续验证 `/pet` 原生交互：聊天/只读工具、录音转写与取消、PCM 音频取消、梦境生成、Studio 保存/删除和应用退出清理。

## 2026-08-18 Runtime/Model Contract Evidence

- `go test ./services -run 'TestPet(Service|Snapshot|Window)' -p 1 -count=1 -timeout 120s`：退出码 `0`。
- `go test ./services -run '^TestPet' -p 1 -count=1 -timeout 180s`：退出码 `0`，输出 `ok codeswitch/services`。
- `npm exec -- vue-tsc --noEmit`（`frontend`）：退出码 `0`。
- `go test ./services -run 'Test(Fetch|Decode)ProviderModels' -p 1 -count=1 -timeout 120s`：退出码 `0`，覆盖 provider `/models` 请求认证、数组/对象响应和去重排序。
- `git diff --check`：退出码 `0`；Git 仅报告 LF/CRLF 转换提示。

## 2026-08-18 Uncovered / Baseline Failure

- `go test ./services -run '^TestProvider' -p 1 -count=1 -timeout 120s`：退出码 `1`，仅失败于既有 `TestProvider_ValidateConfiguration/警告-无白名单` 与 `警告-自映射`；当前实现已允许这两类 provider 配置，测试仍保留旧的阻塞式校验预期。本轮未把该无关契约迁移混入宠物改动。
- 未运行生产构建、bindings 生成和最新 desktop bundle 原生手工验收；原因是当前任务未授权构建，且旧 `F:\下载\CodeSwitch.exe` PID `15224` 仍在运行，bindings 目录存在文件占用风险。

## 2026-08-21 Codex Chat Runtime Verification

- `go test ./services -run 'Test(PetAIAPIWithChatRuntime|PetCodexSession|ParseAndVerify|CodexAppServer|PetCodexRuntime)' -count=1 -timeout 180s`：退出码 `0`，覆盖独立 thread/session、JSONL RPC、流式 delta、usage 合并、resume stale turn、cancel 和 API 接线。
- `go test ./services -run 'TestPetCodexRuntimeDeduplicatesTurnStartFailureAfterClientExit' -count=10 -timeout 180s`：退出码 `0`；进程在 `turn/start` 返回前退出时，失败事件每次恰好一条且 active/client 均清空。
- `go test ./services -run '^TestPet' -count=1 -timeout 240s`：退出码 `0`，输出 `ok codeswitch/services`。
- `frontend\\node_modules\\.bin\\vue-tsc.cmd --noEmit`（工作目录 `frontend`）：退出码 `0`。
- `gofmt -l services/codex_app_server.go services/codex_app_server_test.go services/pet_codex_runtime.go services/pet_codex_session.go services/pet_ai_api_test.go`：无输出。
- `git diff --check`：退出码 `0`；仅有既有 LF/CRLF 转换提示。
- `go test ./services -race -run 'Test(CodexAppServer|PetCodexRuntime)' -count=1`：未执行成功，环境报错 `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`；进一步确认本机未找到 `gcc`/`clang`，因此该项保留为环境限制。
- 新增回归 fixture 和测试位于 `services/codex_app_server_test.go`；生产竞态收口位于 `services/pet_codex_runtime.go` 的 `releaseActiveForStartFailure`。

## 2026-08-22 Workspace Binding Repair Verification

- `go test ./services -run 'TestPetProjectWorkspaceResolver|TestPetCodexRuntimeWorkspace|TestPetCodexRuntimeStartsTurnWithProjectIDWorkspace|TestPetAIWorkspaceResolver|TestPetCodexRuntimeUsesCodexDefaultsWithoutModelOverrides' -p 1 -count=1 -timeout 60s`：退出码 `0`。
- `go test ./services -run '^TestPet' -p 1 -count=1 -timeout 120s`：退出码 `0`，宠物专项回归通过。
- `npm exec -- vue-tsc --noEmit`（`frontend`）：退出码 `0`。
- `frontend/src/locales/zh.json` 与 `frontend/src/locales/en.json`：`ConvertFrom-Json` 解析通过。
- `gofmt -d services/pet_workspace_resolver.go services/pet_workspace_resolver_test.go`：无输出；相关 tracked 文件 `git diff --check` 退出码 `0`。
- Codex fixture 回归确认 `projectId` 解析出的 `ProjectSummary.Path` 进入 `thread/start`，请求中的伪造 `projectFolder` 未改变保存的 session workspace。
- 默认配置回归继续确认 `thread/start`、`thread/resume`、`turn/start` 不携带 `model`、`modelProvider`、`model_provider` 或 `effort` 覆盖字段。

## 2026-08-22 Unrelated Baseline Failure

- `go test ./services -p 1 -count=1 -timeout 120s`：退出码 `1`，仅失败于既有 `TestProvider_ValidateConfiguration/警告-无白名单` 与 `警告-自映射`；本次未修改 provider service 或该测试，宠物专项仍独立通过。

## 2026-08-22 Workspace Binding Residual Risk

- 尚未使用最新 desktop bundle 做真实 Wails/Codex 原生聊天验收；当前验证覆盖 resolver、Codex JSONL fixture、前端类型和 locale，不覆盖真实进程读取本机 Codex 配置的端到端运行。
- 已有 `pet_agent_tools.go` 的 symlink/reparse-point TOCTOU 风险仍保持原记录，本次没有扩大或修复该安全边界。

## 2026-08-24 Codex Chat Timeout Verification

- `go test ./services -run '^TestPetCodexRuntimeCleansUpTurnStartTimeoutAndReinitializes$' -count=3 -v -timeout 180s`：退出码 `0`；3 次均先得到 `PET_AI_TIMEOUT`，随后确认 active/client 清空并用新 client 完成重试。
- `go test ./services -run 'Test(CodexAppServer|PetCodexRuntime)' -count=3 -timeout 300s`：退出码 `0`，输出 `ok codeswitch/services 37.325s`；覆盖通知先于响应、连续 turn、超时重建、取消先于响应和 client 退出收口。
- `go test ./services -run '^TestPet' -p 1 -count=1 -timeout 300s`：退出码 `0`，输出 `ok codeswitch/services 29.915s`。
- `gofmt -d services/pet_codex_runtime.go services/codex_app_server_test.go`：无输出；`git diff --check`：退出码 `0`，仅保留既有 LF/CRLF 转换提示。
- 诊断确认：`500ms` 测试预算会在 Windows fixture 初始化阶段误报 `PET_AI_DEPENDENCY_UNAVAILABLE`；调整为 `2s` 后，超时分支连续 3 次稳定通过。该调整只作用于测试 fixture，不改变生产 runtime 默认 `30s` RPC 预算。

## 2026-08-24 Uncovered Scope

- 未运行 `go test -race`：当前环境 `CGO_ENABLED=0` 且未找到 `gcc`/`clang`。
- 未构建或重启最新桌面 bundle；PID `14508` 仍是旧 `CodeSwitch.exe`，不能作为本轮新 runtime 的 Wails/Codex 原生聊天证据。

## 2026-08-24 Codex Default And Focus Follow-up

- `CODE_SWITCH_REAL_CODEX=1 go test ./services -run '^TestPetCodexRuntimeRealLocalConfigSinglePoint$' -count=1 -v -timeout 100s`：退出码 `0`；真实本机 Codex 完成一次 turn，日志为 `modelProvider="code-switch-r"`、`model="gpt-5.6-luna"`、`textBytes=2`，耗时约 `20.43s`。
- `go test ./services -run '^TestPet' -p 1 -count=1 -timeout 300s`：退出码 `0`，耗时约 `16.13s`。
- `npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）：退出码 `0`。
- 两份 locale 使用 `ConvertFrom-Json` 解析通过；`git diff --check` 退出码 `0`，仅有既有 LF/CRLF 转换提示。
- 静态复核确认 `PetChat` watcher 仅按 `petId` 清空本地消息；`GetChatHistory` 只由设置页 `chat-history` 页签挂载；`thread/start`、`thread/resume`、`turn/start` 不携带模型/provider/effort 覆盖。
- 修复 `PetWindow.vue` 焦点保护定时器：输入框已聚焦或 IME 组合时直接停止释放定时器，消除每 `16ms` 重排的后台空转；前端类型检查覆盖该修改。
- 兼容性复核发现并修复 `PetChat` 只接受 `projectId` 的 UI 门禁回归：现在与后端 resolver 一样接受旧 `projectFolder`，`PetChatHistoryPanel` 同步监听该字段；`TestPetProjectWorkspaceResolver` 与 `vue-tsc` 均通过。

## 2026-08-24 Remaining Scope

- 未构建或重启最新桌面 bundle，因此真实 Windows Wails 输入法、退出清理和原生 UI 交互仍未在新资源上完成手工验收。
- `go test -race` 仍受 `CGO_ENABLED=0` 且缺少 `gcc`/`clang` 限制；不影响已通过的普通并发回归，但保留竞态证据缺口。

## 2026-08-25 Pet Chat Floating Transcript Removal

- `npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）：退出码 `0`。
- `go test ./services -run '^TestPet' -p 1 -count=1 -timeout 300s`：退出码 `0`，耗时约 `22.575s`。
- 静态扫描确认 `PetChat.vue` 不再包含 `messagesRef`、`activeAssistantId`、`findMessage`、`scrollMessagesToBottom`、`createMessage`、`.pet-chat__messages`、`.pet-chat__message` 或 `.pet-chat__empty`；设置页 `PetChatHistoryPanel.vue` 仍保留唯一 `GetChatHistory` 调用。
- 两份 locale 使用 `ConvertFrom-Json` 解析通过；`git diff --check` 退出码 `0`，仅有既有 LF/CRLF 转换提示。
- 当前未构建或重启最新桌面 bundle，未取得新 Wails 资源上的截图级手工验收证据。

## 2026-08-25 Agent Manager Entry Verification

### Verified

- `npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）：退出码 `0`。
- `frontend/src/locales/zh.json` 与 `frontend/src/locales/en.json`：PowerShell `ConvertFrom-Json` 解析通过。
- `git diff --check`：退出码 `0`；仅有 Git 关于工作区 LF/CRLF 转换的提示。
- Chrome `http://127.0.0.1:5174/#/agent` DOM 检查确认左侧“Agent管家”、`Agent管家` transcript、`聊天消息` log、`宠物聊天` composer 和 `聊天输入` textbox 均存在；菜单按钮 class 为 `nav-item active`。
- Chrome 页面截图确认 Agent 管家独立页面的左右气泡区域、刷新按钮、底部输入控件和响应式壳层已渲染。
- Chrome `http://127.0.0.1:5174/#/pet/settings` DOM 检查确认设置页只保留总览、属性、形象工坊、我的宠物、记忆、Agent、睡眠和历史梦境，没有 `chat-history` 页签。
- Chrome 在 `/agent` 输入框填入中文后等待 `1200ms`，值保持不变且 `document.activeElement` 仍为输入框；未发送测试消息。

### Uncovered / Residual Risk

- 浏览器 bridge 明确拒绝 `codeswitch/services.PetAIAPIService.GetChatHistory` 与 `StartChat`，所以本轮只能证明页面结构、错误重试态和输入焦点，不能证明真实历史数据和 Codex 回复。
- 最新 Windows desktop bundle 尚未重启；真实 Wails 历史读取、长会话发送、流式回填、取消和完成后的历史刷新仍需原生手工验收。

### Next Gate

- 使用最新 desktop bundle 打开左侧“Agent管家”，验证已有 Codex thread 的历史读取和发送闭环；不要把浏览器 bridge 的拒绝误判为 Codex runtime 故障。

## 2026-08-25 Pet Chat History Transcript And Composer

### Verified

- Chrome `http://127.0.0.1:5174/#/pet/settings` 桌面视口截图确认聊天历史页渲染为独立 transcript 区域，底部挂载图片、语音、文本输入和发送控件。
- Chrome `390x844` 窄屏视口截图确认历史页使用移动端纵向状态布局；聊天输入填入中文后等待 `350ms`，textarea 仍保持焦点，随后已清理测试文本，未发送测试消息。
- 页面 DOM snapshot 确认 `聊天历史` 页签、`聊天消息` log、`宠物聊天` region、`聊天输入` textbox、`选择图片`、`添加图片` 和 `语音输入` 均存在。
- Vite 会话只产生 PetChatHistoryPanel 的 HMR 更新，没有编译错误输出。
- `npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）：退出码 `0`。
- `go test ./services -run 'Test(ParsePetCodexHistory|PetCodexHistory)' -p 1 -count=1 -timeout 120s`：退出码 `0`。
- `go test ./services -run '^TestPet' -p 1 -count=1 -timeout 300s`：退出码 `0`，输出 `ok codeswitch/services 23.316s`。
- `git diff --check`：退出码 `0`；仅有既有 LF/CRLF 转换提示。
- `frontend/src/locales/zh.json` 与 `frontend/src/locales/en.json`：PowerShell `ConvertFrom-Json` 解析通过。

### Uncovered / Residual Risk

- 浏览器 bridge 明确拒绝 `codeswitch/services.PetAIAPIService.GetChatHistory`，因此浏览器页面显示可重试空态，不能作为真实历史数据读取成功证据。
- 浏览器环境也不允许完整 Wails `StartChat`，本轮未发送测试消息，未把本地验证误报成真实 Codex 流式成功。
- 最新 Windows desktop bundle 尚未重启；真实 Wails 历史读取、乐观消息、流式回复、取消和 thread 刷新回填仍需原生手工验收。

### Next Gate

- 使用最新 desktop bundle 验证真实 Codex thread 的历史读取与发送闭环；浏览器截图只作为页面结构和响应式证据。

## 2026-08-25 Pet Window Menu Responsiveness Verification

### Verified

- `go test ./services -run '^TestPetWindow' -count=1 -timeout 120s`：退出码 `0`，覆盖窗口状态机、模式切换和焦点回滚。
- `go test ./services -run '^TestPetWindowApplyIgnoreMouseStyle' -count=1 -timeout 120s`：退出码 `0`，覆盖 `WS_EX_TRANSPARENT` 切换、`WS_EX_LAYERED`/`WS_EX_NOACTIVATE` 保留和幂等位运算。
- `go test ./services -run '^(TestPetWindow|TestPetWindowAPI)' -count=3 -timeout 180s`：退出码 `0`，连续 3 次通过。
- `npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）：退出码 `0`。
- `gofmt -d services/pet_window.go services/pet_window_windows.go services/pet_window_test.go services/pet_window_windows_test.go`：无输出；`git diff --check` 退出码 `0`，仅有既有 LF/CRLF 转换提示。
- 静态复核确认 Windows `SetIgnoreMouseEvents` 不再调用 Wails `SetIgnoreMouseEvents`、`IsIgnoreMouseEvents` 或模式切换专用 `InvokeSync`；显示、平台监视和焦点恢复路径仍保留原有线程边界。

### Root Cause Evidence

- Wails alpha.38 的 `SetIgnoreMouseEvents` 自身通过 `InvokeSync` 写样式，旧驱动随后又执行一次 `InvokeSyncWithError` 和一次 `IsIgnoreMouseEvents` 的 `InvokeSyncWithResult`；pointer 事件串行排队时会把三段同步主线程等待叠加到菜单交互上。
- 旧 `windowModeQueue` 会保留每次请求的 Promise 链；菜单和全屏 pointer 观测产生的过期目标会依次进入原生桥，即使最终只需要 passive，也要先等待中间状态完成。
- 新日志边界为 `pet-native-mouse-mode-start`、`pet-native-mouse-mode-failed` 和 `pet-native-mouse-mode`，完成日志包含 `duration_ms`、目标/实际穿透状态和前台 HWND。

### Uncovered / Residual Risk

- 尚未重启最新 desktop bundle 做真实 Windows 菜单连续开关和前台窗口手工测量；当前证据证明代码路径、状态机和样式规则，但不替代原生 UI 体验证据。
- `go test -race` 仍受当前环境 `CGO_ENABLED=0` 且缺少 `gcc`/`clang` 限制；本次未把该环境问题误判为功能失败。

### Next Gate

- 使用最新 desktop bundle 连续打开/关闭右下角菜单，确认菜单响应不再等待约秒级延迟，并回读新日志的 `duration_ms`；若仍慢，再按 start/complete 间隔区分 native 调用慢还是 Wails/renderer 调度慢。

## 2026-08-27 Foreground Diagnostic Retirement

- 根据性能排查结果，已移除 GUI 默认启动的前台窗口诊断轮询和 Win32 前台事件监听；这两条路径原本只用于定位历史焦点闪烁问题，不属于宠物或 Agent 业务功能。
- 代码退役范围为 `main.go` 的两个启动入口及 `services/runtime_foreground_windows.go`、`services/runtime_foreground_other.go`；其他依赖 `WriteRuntimeDiagnostic` 的 Codex、宠物和窗口诊断保持不变。
- 已有运行日志不删除、不重写；新版本验收标准是日志不再产生 `foreground-monitor-*`、`foreground-window-*`，并由用户手动重建后观察 CodeSwitch CPU。

## 2026-08-27 Pet Heartbeat Verification

### Verified

- `go test ./services -run 'PetHeartbeat' -count=1 -timeout 180s`：通过。
- `go test ./services -run 'PetHeartbeat' -count=10 -timeout 240s`：通过，连续 10 次覆盖 timer、终态推进、人工聊天冲突、单次执行、取消、恢复和 workspace 门禁。
- `TestPetHeartbeatDefaultsToDisabled`：通过，首次启动默认关闭，不创建 timer，也不提交 Agent 任务。
- `go test ./services -run 'PetHeartbeat|PetBrowserBridge' -count=1 -timeout 180s`：通过，覆盖心跳 API 的 bridge 白名单相邻回归。
- `npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）：通过；已移除未使用的心跳类型导入和状态函数。
- `frontend/src/locales/zh.json`、`frontend/src/locales/en.json`：`ConvertFrom-Json` 解析通过。
- `gofmt -d services/pet_heartbeat.go services/pet_heartbeat_api.go services/pet_ai_completion_broker.go services/pet_heartbeat_test.go services/pet_browser_bridge.go services/pet_dao.go`：无输出。
- `git diff --check`：退出码 `0`；仅有既有 LF/CRLF 转换提示，没有 whitespace error。
- 静态接线复核确认 `main.go` 注册并启动 `PetHeartbeatAPI`，终态事件先进入 `PetAICompletionBroker`，配置/运行态写入 `pet_heartbeat`，关闭阶段调用 `PetHeartbeatService.Close`。

### Uncovered / Residual Risk

- `go test -race ./services -run 'PetHeartbeat' -count=1` 未执行成功：环境报错 `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`，且当前未找到 `gcc`/`clang`。
- 尚未构建或重启最新 Windows desktop bundle；真实 Codex provider、原生 Wails 事件时序、长任务完成后下一轮心跳和应用退出取消仍需手工验收。
- 浏览器 bridge 只能证明页面 RPC/事件接线，不能替代桌面宿主中的真实 Agent 管家调用。

### Next Gate

- 使用最新 desktop bundle 打开“宠物心跳”，先绑定有效项目，保存提示词和频率后观察一次 `RunNow` 或完整周期；确认任务终态后才出现下一次倒计时，并验证人工聊天占用时显示等待空闲。

## 2026-09-01 Agent Manager Codex Capability Verification

### Verified

- `go test ./services -run '^TestAgentConversationHubSharesConcurrentProjectRequestsAndThread$' -count=1 -v`：通过；首轮建立同一 `client/thread/turn` 后取消，第二轮收到 `delta + completed`，Codex 子进程数保持 `1`。
- `go test ./services -run 'Test(CodexAppServer|PetCodex|AgentConversation|PetAI|PetBrowserBridge)' -count=1 -timeout 180s`：通过，退出码 `0`，覆盖 Codex app-server、宠物 runtime、Agent Hub、PetAI API 和浏览器 bridge。
- `go test ./services -run '^(TestPetAIAPI|TestAgentConversationHub|TestPetCodexRuntime|TestCodexAppServer)' -count=1 -timeout 180s`：通过，退出码 `0`；新增 API 命令转发回归和四类 pending interaction resolve 均通过。
- `services/codex_app_server_test.go` 的 `pet-shared-concurrent` fixture 已补齐第二轮 `item/agentMessage/delta` 与 `turn/completed`；测试等待首轮 active turn 建立后再验证队列取消，避免把启动期取消误当成共享 thread 失败。
- `AgentConversationHub` 的 review 任务进入项目 FIFO；`compact/steer/interrupt` 保持对当前 thread/turn 的直接控制，普通聊天和频道完成事件仍从同一 Hub 出口发出。

### Root Cause And Repair

- 原并发测试只等待公共 `started` 事件就取消第一轮，实际 runtime 仍在 `initialize`，取消后按生命周期契约关闭首个 app-server；同时 fixture 第二轮没有发完成通知，因此测试同时混入了错误前置条件和不完整模拟。
- 修复边界限定在测试 fixture 和测试同步点，没有为生产 runtime 增加重试、fallback 或第二个 session owner；生产复用路径由首轮已建立的 `client/thread` 直接证明。

### Uncovered / Residual Risk

- 最新 Windows desktop bundle 尚未重启，真实 Wails/Codex 默认配置下的 Agent 管家历史、Skill、审批交互和频道转发仍需原生手工验收。
- `go test -race` 仍无法执行：当前环境 `CGO_ENABLED=0`，且未找到 `gcc`/`clang`；普通并发 fixture 已通过，但未取得 race detector 证据。
