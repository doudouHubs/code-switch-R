# Execution Checkpoint

## Current Todo

- [completed] Go/TypeScript 宠物契约、规则、经验、DAO、SQLite schema、迁移读取器。
- [completed] 梦境历史、媒体处理、provider 引用校验、透明窗口状态机、Vue 宠物页和设置页。
- [completed] `main.go` 数据库迁移、`PetService` Wails 注册、桌宠窗口生命周期接线。
- [completed] `Petted(petId string)` 显式契约、embed atlas 注入、记忆/梦境 API 和自动照料运行时。
- [completed] `PetSnapshot` 暴露 plans/dreams/memories，已由主控完成收口。
- [completed] 计划调度器测试、SQLite JobStore 和运行时/Wails 接线，已由主控完成收口。
- [completed] 聊天图片 Go 契约和三协议请求体：`Descartes / VisionWire` 调查后未落盘，主控已完成 `services/pet_ai.go`、`services/pet_ai_test.go`。
- [completed] 聊天图片选择和展示：`Plato / VisionComposer` 未落盘，主控已完成 `frontend/src/components/Pet/PetChat.vue`。
- [completed] 每日奖励/陪伴里程碑前端反馈：`Lorentz / MilestonePulse` 未落盘，主控已完成 `frontend/src/components/Pet/PetWindow.vue`。
- [completed] 主动搭话 persona 已补齐状态、等级、项目名和最近记忆上下文。
- [completed] Chroma-key、sprite normalization、atlas pack 已通过 `PetMediaAPIService` 暴露裸 base64 Wails 接口，并完成输入边界/PNG/manifest 单测；`main.go` 已注册服务。
- [completed] Studio 后端已完成受控 root、atlas/manifest 原子安装、`pet_skins` upsert、绑定/删除/列出及回滚测试；`main.go` 已注册 `PetStudioAPIService`。
- [completed] 侦察 Agent 已确认只读项目工具依赖后端 native agent loop；目标当前不具备 tool-call/result 多轮执行边界，未伪造实现。
- [completed] 侦察 Agent 已确认 TTS 源语义为文本按句切片后独立 PCM 流；主控先拆分非流式 chat-audio 兼容切片，真正流式另行设计。
- [completed] 侦察 Agent 已确认 Studio 还需要受控皮肤目录、原子写入、`pet_skins` 持久化和 UI；仅有无状态媒体 API 不代表 Studio 可用。
- [completed] 主控已将主动搭话和 persona 状态上下文接入现有 `vue-i18n` 的中英文 locale，不引入第二套语言存储。
- [completed] 源项目剩余能力只读盘点：上一轮 `Socrates / ParityScout` 已返回证据，不改代码。
- [completed] 只读项目工具已接入 OpenAI/Anthropic/Gemini native continuation；仅允许 Read/LS/Glob/Grep，含轮次、调用数、参数、结果和 workspace 限制。
- [completed] TTS `voiceMode` 实际链路、PCM16 24kHz 音频事件、前端句子队列、取消和卸载清理已接入；Studio UI 已接入媒体处理、atlas、皮肤保存/删除/列出。
- [completed] workspace 安全事实源已收口到 `PetDAO.Resolve(ctx, petId)`；普通 `StartChat` 才解析后端绑定目录，梦境/语音不自动启用工具；前端 `projectFolder` 仅保留兼容字段。
- [completed] PetChat 录音输入：MediaRecorder webm/opus、停止转写、自动发送、取消/卸载/切宠物释放麦克风和中英文文案。
- [completed] Provider/Gemini 模型用途类别已接入后端、表单、Gemini 专属字段和 Wails binding；未命中类别继续兼容旧模型名规则，重叠通配符按确定性规则选择。
- [pending] Windows 手工验收和最终 parity 证据。
- [completed] 设置页 parity 收口：总览默认 atlas、改名契约、真实 provider/model 与项目选择、皮肤预览/绑定/删除/刷新及中英文文案。
- [completed] 设置页收尾校验：`selectTab`、Agent/Sleep/Skins/Stats 条件渲染、语音预设类型和源版页签顺序已复核；Vite 模块加载重新验证。
- [completed] 设置页 Agent/Sleep parity 追补：模型按 platform/provider 分组，图片模型按 `enabled + image` 过滤，reasoning 只展示模型声明能力，主动互动显示每日配额，免打扰时段改为 24 小时选择，语音空态/试听错误/输入长度边界已补齐。
- [completed] 设置页 Stats/Skins parity 追补：Stats 区块改为 `16px / 8px`，汇总卡片使用左图标和右侧上下两行，皮肤缩略图固定 `48px`，路径和元数据允许换行，移动端去除固定左缩进。
- [completed] 后端 reasoning 能力字段已重新生成 Wails TypeScript bindings；旧 `CodeSwitch.exe` 进程 PID `19732`、`20640` 全程保留。
- [completed] 浏览器 bridge 隔离验证收口：`browserBridge.ts` 支持 hash 路由的 `petBridge` loopback 覆盖，最新副本 `18202/18203` 可读取真实 SQLite 并渲染设置页；未触碰旧进程。
- [completed] Agent 管家 Codex 能力闭环：交互 pending/resolve、Skill/Model 查询、review/compact/steer/interrupt 控制、API 转发和同项目并发共享 thread 测试均已收口。

## Baseline

- Root: `F:\GitlabProjects\code-switch-cli`
- Branch: `dev-pets`
- TaskStart HEAD: `55cc2d5217b2e4fb8941f300a644a808a5657b60`
- Initial worktree: clean

## Active Slice

主控已修改 `main.go`：在数据库迁移后注入 `embed.FS` 资源源，创建并注册 `PetMemoryService`、`PetDreamAPIService`、`PetStudioAPIService`，创建 `PetRuntime` 并以 30 秒心跳驱动，创建 `PetWindow` 后注册 `PetWindowAPI`；关闭时停止运行时心跳并释放原生窗口。当前追加了 `PetDAO.Resolve` 到 `PetAIService` 的 workspace resolver 接线，并完成 Provider/Gemini 模型类别配置闭环。

当前 Agent 写入边界：

- `Descartes / VisionWire`：调查后 `BLOCKED/NO_PATCH`，主控接管 `services/pet_ai.go`、`services/pet_ai_test.go`；共享字段固定为 `images[{data,mediaType}]`。
- `Plato / VisionComposer`：调查后 `BLOCKED/NO_PATCH`，主控接管 `frontend/src/components/Pet/PetChat.vue`。
- `Lorentz / MilestonePulse`：调查后 `BLOCKED/NO_PATCH`，主控接管 `frontend/src/components/Pet/PetWindow.vue`。
- `Socrates / ParityScout`：只读调查 `F:\GitlabProjects\OpenCowork` 与目标宠物模块，不写任何文件，证据已回报。
- `Anscombe / 工具边界侦察员`：只读确认 native tool loop、四个只读工具及路径安全边界，`DONE / NO_PATCH`。
- `Hilbert / 语音链路侦察员`：只读确认 voiceMode、整段 chat-audio 和 PCM 流式语义，`DONE / NO_PATCH`。
- `Planck / Studio 媒体侦察员`：只读确认 Studio UI/持久化缺口，`DONE / NO_PATCH`。
- `Popper / 整段语音兼容实现员`：实现非流式 chat-audio，写入边界为 `services/pet_contract.go`、`services/pet_ai.go`、相关测试，已由主控完成定向回归。
- `Faraday / Studio 持久化边界实现员`：实现受控皮肤目录的原子保存/删除/列出/绑定，写入边界为 `services/pet_studio_api.go` 及测试，已由主控审查并接入 `main.go`。
- `Helmholtz / 宠物语音流实现员`：当前写入边界为 `services/pet_ai.go`、`services/pet_ai_api.go`、`services/pet_contract.go`、`services/pet_audio.go` 及定向测试；禁止修改前端和主入口。
- `Bohr / 原生只读工具循环实现员`：当前写入边界为新建 `services/pet_agent_tools.go` 及定向测试；禁止伪造 HTTP tools 字段和修改共享 AI 入口。
- `Godel / Pet Studio 前端实现员`：当前写入边界为 `frontend/src/components/Pet/PetStudio.vue`、`petStudioApi.ts`，必要时只做 `PetSettings.vue` 窄入口；禁止修改后端和窗口页。
- Peirce / ResolverTest：新增 `services/pet_ai_workspace_test.go`，覆盖伪造路径、未绑定项目、目录不存在、多宠物隔离；`DONE / NO_PATCH`。
- Erdos / FrontendQA：只读审查 `PetWindow` 音频事件、取消/卸载、`projectFolder` 和类型差异；`DONE / NO_PATCH`。
- Ptolemy / WorkspaceAudit：只读审查通过；发现 symlink TOCTOU 风险，未修改文件，已记录为剩余安全风险。
- 主控：审查 Agent 改动、处理共享契约和集成冲突，负责最终测试、checkpoint 和完成声明。

## Evidence

- Target uses Go 1.24, Wails v3 alpha.38, Vue 3.5 and modernc SQLite。
- Wails alpha.38 exposes transparent windows, always-on-top and `SetIgnoreMouseEvents`；没有 Electron `setFocusable`。
- OpenCowork pet state is split across six persisted stores plus dream DB/assets。
- `go test ./services -run '^TestPet' -p 1 -count=1`：通过；包含 provider、native tool loop、梦境图片、Studio、窗口、调度器、转写和 action-sheet 拆帧测试。
- `go test . -count=1`：失败于既有 `TestSeedMockRequestLogs`，错误为 `database is locked (5) (SQLITE_BUSY)`；不是宠物测试失败。
- `go test ./... -p 1 -count=1`：根包仍因同一 `TestSeedMockRequestLogs` 的共享 request-log SQLite 锁失败；`resources/model-pricing` 和 `services` 通过。
- `go test ./services -run 'TestPetAIWorkspaceResolver|TestPetAIChatExecutesOpenAITool|TestPetAIToolResponses|TestPetAIProjectFolder|TestPetAIToolContinuationLimit' -count=1`：通过。
- `npm exec -- vue-tsc --noEmit`（`frontend`）：退出码 0；已对齐 Wails `GeminiProvider.envConfig` 的可选 map 值契约。
- `wails3 task common:generate:bindings`：成功，处理 573 packages、32 services、258 methods、19 enums、143 models；`Provider.modelCategories`、`GeminiProvider/GeminiPreset.modelCategory` 已进入 `frontend/bindings`。
- `go test ./services -run '^TestPet' -p 1 -count=1`：通过；包含模型类别、provider 解析、AI、转写、Studio、窗口和调度器测试。
- 2026-08-13 fresh `go test ./services -run '^TestPet' -p 1 -count=1`：通过，退出码 0，耗时约 4.3s。
- 2026-08-13 fresh `npm exec -- vue-tsc --noEmit`（`frontend`）：通过，退出码 0。
- `go test ./services -run 'TestGeminiProviderDuplicatePreservesModelCategory|TestGeminiPreset_Fields|TestPet' -p 1 -count=1`：通过；覆盖 Gemini 复制类别持久化和宠物专项回归。
- Vite 开发服务器 `http://127.0.0.1:5173/`、`/pet`、`/pet/settings`、`PetWindow.vue`、`PetStudio.vue` 和中英文 locale 请求均返回 HTTP 200；两份 locale 可被 `ConvertFrom-Json` 解析。
- 2026-08-13 fresh HTTP check：`/`、`/pet`、`/pet/settings` 均返回 `200`；Vite 仍由 PID `27348` 监听 `127.0.0.1:5173`。
- 2026-08-13 static runtime check：`main.go` 注入 `embed.FS` 并注册 `PetStudioAPIService`/`PetWindowAPI`；桌宠窗口使用透明背景、点击穿透初始模式，并显式 `SetAlwaysOnTop(false)`。
- 2026-08-13 source-of-truth check：Studio 的空 `skinId` 固定读取只读默认 `capybara`；运行时 atlas 则按 active skin 优先，二者职责保持分离。
- 2026-08-13 collaboration closeout：本轮六个协作 Agent 均已关闭，未遗留运行中的 Agent。
- 2026-08-13 settings slice：`git diff --check`、`npm exec -- vue-tsc --noEmit`、`go test ./services -run '^TestPet' -p 1 -count=1` 均通过；两份 locale 可被 `ConvertFrom-Json` 解析；临时 Vite 服务下 `/`、`/pet/settings` 和 `PetSettings.vue` 均返回 `200`。
- 2026-08-13 settings follow-up：确认 `selectTab(tab: PetTab)` 仍在 `PetSettings.vue`；Agent、语音、Sleep、Skins、Stats 各区域的 `v-show` 边界与源版 `PET_TABS` 顺序一致；语音预设改用类型安全的 `some()` 匹配，不再使用 `as never`。
- 2026-08-13 settings Vite fresh check：临时 Vite 服务请求 `/`、`/pet/settings`、`/src/components/Pet/PetSettings.vue` 均返回 `200`，检查完成后已停止临时服务。
- 2026-08-13 final focused rerun：`git diff --check`、locale JSON 解析、`npm exec -- vue-tsc --noEmit`、`go test ./services -run '^TestPet' -p 1 -count=1` 均通过；未遗留临时 Vite 进程。
- 2026-08-13 settings browser evidence：独立 Vite `5175` + Edge CDP `9227` 下，`PetSettings.vue` 实际加载 `loading=false`、无错误提示，真实 `Kapi`、SQLite snapshot 和默认 atlas 均显示；普通内容宽度为 `672px`，总览预览为 `672x198px`，8 个页签均为 `32px` 且未折行。
- 2026-08-13 settings model parity follow-up：`PetSettings.vue` 使用 `agentModelGroups`、`voiceModelGroups`、`imageModelGroups` 分组渲染；图片候选仅接受 `modelCategory === image`，reasoning 候选来自选中模型的 `reasoningEffortLevels`；主动互动配额为低/中/高频 `1/2/4`，免打扰时间使用 `PET_HOURS`。
- 2026-08-13 settings visual follow-up：Stats 汇总卡片已改为图标加右侧双行信息，Stats 区块使用 `16px` padding 和 `8px` radius，Skins 缩略图容器与 atlas display height 均为 `48px`，长路径/皮肤 metadata 使用可换行规则。
- 2026-08-13 bindings refresh：`wails3 task common:generate:bindings` 成功，处理 `572 Packages, 33 Services, 248 Methods, 19 Enums, 147 Models`；生成类型包含 `Provider.modelReasoningEffortLevels` 与 Gemini `reasoningEffortLevels`。
- 2026-08-13 settings focused rerun：`git diff --check`、`npm exec -- vue-tsc --noEmit`（`frontend`）、`go test ./services -run '^TestPet' -p 1 -count=1 -timeout 60s` 和两份 locale JSON 解析均通过；Go 输出 `ok codeswitch/services`。
- 2026-08-13 process preservation check：PID `19732`（`F:\GitlabProjects\code-switch-cli\bin\CodeSwitch.exe`）与 PID `20640`（`F:\下载\CodeSwitch.exe`）仍存在，本轮未按进程名批量结束桌面应用。
- 2026-08-13 browser bridge root-cause check：浏览器最初仍固定请求 `18101`，导致隔离副本页面显示“无法连接宠物本地服务”；根因是前端没有消费隔离 bridge 地址，不是 SQLite 或页面渲染失败。
- 2026-08-13 isolated browser parity check：新副本 PID `15964` 使用 `127.0.0.1:18202/18203` 启动，Chrome `5177` 通过 `#/pet/settings?petBridge=...` 读取真实 `Kapi`、Lv.5、金币和默认 Capybara atlas；总览、属性、Agent、睡眠、记忆、历史梦境、皮肤和 Studio 均无加载错误。
- 2026-08-13 isolated native state check：PID `15964` 的 `PetWindowAPI.State` 返回 `open=true`、`mode=passive`、`clickThrough=true`、`alwaysOnTop=false`；验证结束后仅停止该隔离副本和临时 Vite，未按进程名清理桌面应用。
- 2026-08-13 loopback bridge evidence：`18101` 上的 `PetService.GetSnapshot`、`ProjectManagerService.GetSnapshot` 和 `PetStudioAPIService.ReadSkin(default, '')` 均返回 HTTP `200`；`ReadSkin` 传空皮肤 ID 是默认皮肤契约，传入字面量 `default` 返回 `400` 属于预期校验。
- 2026-08-13 process hygiene：仅结束带 `--remote-debugging-port=9226/9227` 且使用独立临时 profile 的浏览器验证进程树；旧桌面进程 PID `19732`（`bin/CodeSwitch.exe`）和 PID `20640`（`F:\下载\CodeSwitch.exe`）均保留，未按进程名批量结束。
- 2026-08-13 Windows production build：直连执行 `task windows:build PRODUCTION=true` 在 `go mod tidy` 校验 `github.com/esiqveland/notify@v0.13.3` 时因 `sum.golang.org` 网络超时失败；设置 `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY` 为 `http://127.0.0.1:7890` 后同一任务退出码 0，产物为 `bin/CodeSwitch.exe`（约 44.3 MB）。
- 该构建过程按 `go mod tidy` 规范更新了 `go.mod`/`go.sum`；未关闭 `GOSUMDB`，未把本机代理写入项目配置。
- PetChat 录音静态检查：`MediaRecorder` 仅提交当前 generation 且使用 `audio/webm;codecs=opus` 优先编码；关闭/卸载/宠物切换调用 `discardVoiceCapture` 停止轨道并丢弃片段；录音转写复用 `sendMessage`，不复制聊天状态机。
- 本轮宠物专项首跑曾暴露 action-sheet 最后一行右对齐边界错误；修复 `SplitActionSheet` canonical owner 后，原测试完整通过。
- 2026-08-13 音频/转写取消回归：`TestPetAudioCancelBeforeProviderResolutionStopsRegistrationWindow`、`TestPetAISynthesizeSpeechCancelBeforeProviderResolution`、`TestPetAITranscribeAudioPreservesCallerCancellation` 和 `TestPetAISynthesizeChatAudioCanBeCancelled` 使用 `-count=5` 均通过；provider 解析阶段和 HTTP transport 阶段都能观察到 context 取消。
- 2026-08-13 浏览器 bridge 取消收口：`loadBridgeToken` 的 health `GET` 复用调用级 `AbortSignal`；主动取消保留原始 `AbortError`，超时才转换为本地服务超时提示；SSE 重连继续使用共享 token 请求。
- 2026-08-13 PetChat 录音边界：录音最长 `60s`，累计 Blob 超过 `240 KiB` 时停止并提示，不发起必然超过 Go multipart 上限的转写请求；该项为源码级边界检查，尚未完成 Windows 实机麦克风验收。
- 2026-08-13 进程保护复核：PID `16932`（`F:\下载\CodeSwitch.exe`）与 PID `23448`（`F:\GitlabProjects\code-switch-cli\bin\pet-verify\CodeSwitch.exe`）仍存在，本轮未按进程名批量结束旧桌面进程。

## Drift Check

- Scope: aligned with approved plan。
- Compatibility: source data remains read-only；no key migration。
- Retirement: no old target logic removed；前端 `projectFolder` 仍作为兼容字段，但不再作为工具根目录事实源。
- Security: 基础路径穿越和普通 symlink 越界已拒绝；`SEC-TOCTOU-001`：`resolveExisting` 校验后再 `Stat/Open` 仍存在 symlink/reparse point TOCTOU，需 Windows handle 验证和 POSIX `openat/openat2` 才能完全消除；攻击者需具备 workspace 并发修改能力，本轮不阻塞功能验收。
- Decision: needs-verification。

## Next

主控已完成媒体、AI continuation、workspace resolver、Studio、PetChat 录音、模型类别/reasoning 能力配置、设置页 parity、浏览器 bridge 隔离验证和取消链路专项回归；当前剩余的是完整原生交互闭环（聊天工具循环、录音转写、TTS、梦境生成、Studio 保存/删除）的手工验收，旧 `CodeSwitch.exe` 实例不能代表最新前端修改。

## 2026-08-17 Windows Startup Focus Slice

- 已以 `main` 为回归基线比对主窗、项目管理器 Terminal 和 Codex Hook 路径。`SetForegroundWindow` / WT 激活逻辑与 `main` 的功能实现一致，当前分支仅新增诊断写入，不能解释启动期差异。
- 差异收敛到宠物分支新增的 Wails 窗口生命周期。Windows 主窗改为 `Hidden: true` 完成首个 WebView 导航，再通过 `SetWindowPos(SWP_SHOWWINDOW | SWP_NOACTIVATE)` 显示；Wails alpha.38 默认 `Show()` 使用 `SW_SHOW`，会激活窗口。
- `main_window_windows.go` 已删除首次显示时冗余的 `window.Run()`，并将 `ShowWindowAsync(SW_SHOWNOACTIVATE)` 改为仅在 `SetWindowPos` 后 HWND 仍隐藏时的兼容兜底，避免正常首屏发出两次显示请求。
- 证据边界：`bin/CodeSwitch.exe` 最后写入时间为 `2026-08-17 09:07:49`，`main.go` 为 `09:50:16`、`main_window_windows.go` 为 `09:58:01`；最新 GUI 日志仍有旧事件 `main-window-show focus=false`，未出现新路径的 `main-window-show-deferred` / `main-window-initial-show-*`。当前用户反馈尚不能用于判断最新源码。
- 启动首屏的已证实近因：旧 GUI PID `15128` 在 `09:42:36` 写入 `main-window-show focus=false`，随后 `09:42:39` 主窗成为前台、`09:42:42` WT 回到前台；Wails alpha.38 的 `Show()` 固定调用 `SW_SHOW`，故“focus=false”并不等于无激活。这个链路解释启动初始闪动，但不覆盖后续所有 WT 跳转。
- 持续 WT 闪动的因果状态仍为 `deepest-confirmed-root-unknown`：同一 GUI 生命周期记录到 `108` 次 WT 前台事件，但 `terminal-activate-*`、`terminal-tab-focus-*`、`pet-native-focus`、`pet-native-release-focus` 均为 `0`。因此不能把 ProjectManager 终端复用或桌宠焦点恢复冒充为根因。
- 已新增 `StartRuntimeForegroundEventMonitor`：Wails 主线程订阅 Win32 `EVENT_SYSTEM_FOREGROUND`，以回调原始 HWND 写入 `foreground-window-event`；原有 100ms 轮询仍保留为兜底。该改动只提高观测精度，不改变终端、Hook、Show 或 Focus 行为。
- 验证由用户执行：不在本轮构建、启动、杀进程或运行测试。下一次只读取新 GUI PID 的 `foreground-monitor-event-hook-ready`、`foreground-window-event` 和主窗启动事件，按每次 WT 事件的直接前序窗口判定持续闪动调用链。

## 2026-08-17 Windows Startup Focus Root Cause

- 新版 GUI PID `29056` 的完整生命周期没有 `terminal-wt-start`、`terminal-activate-*`、`terminal-tab-focus-*` 或 `new-tab` 记录；同一时间段的 `project-manager-terminal-debug.log` 也没有启动记录，因此“ProjectManager 循环开 tab”被排除。
- `foreground-window-event` 直接记录到 `WT(0x60832) -> CodeSwitch 主窗(0x110674) -> WT(0x60832)`，其中 CodeSwitch 主窗只在 203ms 内短暂成为前台，随后 94ms 后 WT 再次成为前台；这与用户看到的 WT 闪烁一致。
- Wails v3 alpha.38 的 Windows 实现会在 `WM_SETFOCUS` 中调用内部 `focus()`，而 `focus()` 无条件调用 `SetForegroundWindow`。`Hidden: true` 只移除初始 `WS_VISIBLE`，不会设置 `WS_EX_NOACTIVATE`，所以 WebView 初始化仍可能激活主窗。
- 源码修复已收敛到主窗初始化样式：Windows 创建阶段使用 `WS_EX_APPWINDOW | WS_EX_NOACTIVATE`，首次通过 `SWP_NOACTIVATE` 显示后，使用 `SetWindowLongPtr` 无激活移除临时 `WS_EX_NOACTIVATE`；没有修改 ProjectManager 的终端创建或激活逻辑。
- 当前状态：上述 `WS_EX_NOACTIVATE` 实验方案未通过用户实测，已撤回，不再作为当前实现。

## 2026-08-17 Windows Startup Main Baseline Rollback

- `main.go` 已恢复稳定主线启动时序：主窗口不再使用 `Hidden`/`Windows.ExStyle`，创建后立即执行 `showMainWindow(false)`。
- 宠物窗口已恢复为 `ApplicationStarted -> petWindow.Open()`，不再绑定主窗口 `WebViewNavigationCompleted`，也不再通过主线程消息队列延迟创建。
- `main_window_windows.go`、`main_window_other.go` 已删除；它们只服务于已撤回的隐藏主窗、非激活显示和临时激活样式方案。
- `StartRuntimeForegroundMonitor` 与 WinEvent 监控仅记录前台变化，不调用 `Show`、`Focus`、`SetForegroundWindow` 或终端 API，暂时保留用于区分新版进程行为。
- 本轮未构建、未启动、未结束进程、未运行测试；等待用户使用回归主线启动链的新二进制验收。

## Resume State Hint

如果进程中断：保留当前未提交工作区；先读取本文件和 `git status --short`，不要重新迁移源文件。确认 `PetDAO.Resolve`、`PetAIService` resolver 注入、`services/pet_ai_workspace_test.go` 和前端音频文件仍在，再运行宠物专项回归。Windows 手工验收前不要宣称完整 parity；symlink TOCTOU 仍是已知残余风险。Studio 默认来源继续读取只读 `capybara`，不要把它改成 active skin。

## 2026-08-18 Runtime/Model Contract Closeout

- `go test ./services -run 'TestPet(Service|Snapshot|Window)' -p 1 -count=1 -timeout 120s`：通过。
- `go test ./services -run '^TestPet' -p 1 -count=1 -timeout 180s`：通过，运行时轻量快照、独立 atlas、动作 state、窗口状态机、AI continuation 和设置页相关服务回归均通过。
- `npm exec -- vue-tsc --noEmit`（`frontend`）：通过。
- `go test ./services -run 'Test(Fetch|Decode)ProviderModels' -p 1 -count=1 -timeout 120s`：通过，provider `/models` 目录读取、认证和响应解码回归通过。
- `git diff --check`：退出码 `0`；仅有 Git 关于工作区 LF/CRLF 转换的提示，没有 whitespace 错误。
- `go test ./services -run '^TestProvider'` 仍被既有 `TestProvider_ValidateConfiguration` 卡住：测试要求“无 supportedModels”和“自映射”返回错误，但当前 HEAD 的 `ValidateConfiguration` 已允许这两类配置；本轮未修改无关旧测试。
- 本轮没有运行生产构建或重新生成 bindings；当前仍有旧进程 `F:\下载\CodeSwitch.exe`（PID `15224`），未按进程名清理。

当前停止点仍是 Windows 最新 bundle 的原生手工验收；浏览器/静态证据不能替代真实窗口、provider 流式响应、麦克风和 Wails 事件验证。

## 2026-08-21 Codex Chat Runtime Slice

- [completed] 主聊天已由 `PetAIAPIService` 接入独立 `PetCodexRuntime`；每只宠物按 `petId` 持有独立 app-server、thread 和持久化 session。
- [completed] `turn/start` 失败与 app-server 进程退出的 active 所有权竞态已补回归；失败终态事件不会重复，宠物状态不会残留 `in-flight`。
- [completed] 定向 runtime/app-server/session/API 测试、`^TestPet` 专项和前端 `vue-tsc` 检查均通过。
- [needs-verification] `-race` 未能执行：当前环境 `CGO_ENABLED=0` 且未安装 `gcc`/`clang`；不改变功能测试结论，但仍是并发证据缺口。

## Resume State Hint

如果继续本任务，先保留当前未提交工作树，读取本节和 `90-evidence.md`；不要回退频道/provider 改动。下一步优先使用启用 CGO 的环境重跑 `go test ./services -race -run 'Test(CodexAppServer|PetCodexRuntime)' -count=1`，然后再进行最新 desktop bundle 的原生聊天验收。

## 2026-08-22 Workspace Binding Repair Slice

- [completed] 新增 `PetProjectWorkspaceResolver`：以持久化 `projectId` 查询 `ProjectManagerService.ListProjects()`，返回当前 `ProjectSummary.Path`；旧 `projectFolder` 仅在没有 `projectId` 时兼容读取。
- [completed] `PetAIService` 与 `PetCodexRuntime` 共用同一个 resolver；请求中的 `projectFolder` 不再参与主聊天 workspace 解析。
- [completed] 新增 `PET_AI_WORKSPACE_UNAVAILABLE`，前端中英文错误提示改为明确要求重新绑定项目；普通 `PET_AI_INVALID_REQUEST` 仍保留通用输入校验语义。
- [completed] 补充 projectId 解析、旧字段兼容、项目缺失、路径安全、多宠物隔离和 Codex `thread/start` fixture 回归。
- [needs-verification] 最新 desktop bundle 的真实 Codex 配置和 Wails 原生聊天尚未手工验收；现有代码级 fixture 已确认不传 model/provider/reasoning override。

## Resume State Hint

继续时先读取本节与 `90-evidence.md`，保留工作区前序频道/provider 改动；不要把 `PetDAO.Resolve` 恢复成主聊天事实源。若做原生验收，应启动隔离的最新 bundle，不要批量结束已有 `CodeSwitch.exe` 进程。

## 2026-08-24 Codex Chat Timeout Closure

- [completed] 将 app-server RPC、session 持久化和 UI activity emitter 移出 `state.mu` 长临界区；通知消费不再被 `turn/start` 响应或慢 emitter 反向堵住。
- [completed] `turn/start` 超时、取消先于响应、通知先于 RPC 响应和 app-server 退出均有独立回归；超时会关闭旧 client，下一轮重新初始化，避免迟到通知污染新 turn。
- [completed] 修正 Windows fixture 回归的初始化时间预算，并通过目标 runtime/app-server 测试 3 次及完整 `^TestPet` 回归。
- [needs-verification] `-race` 仍受 `CGO_ENABLED=0` 且缺少 `gcc`/`clang` 限制；最新 desktop bundle 的真实 Wails/Codex 聊天仍需重启新二进制手工验收。

## Resume State Hint

继续时保留当前未提交工作树；优先使用新构建的 desktop bundle 验证真实 Codex 默认配置、流式回复和超时后的重试。不要把旧 PID `14508` 的运行结果当作本轮 runtime 证据，也不要批量结束已有 `CodeSwitch.exe` 进程。

## 2026-08-24 Codex Default And Focus Follow-up

- [completed] 使用本机默认 Codex 配置执行真实单点：app-server 完成一次 turn，读取到 `modelProvider="code-switch-r"`、`model="gpt-5.6-luna"`，未向请求注入模型覆盖。
- [completed] 最新 `^TestPet`、`vue-tsc`、locale JSON 和 `git diff --check` 均通过；Codex runtime 继续保持独立 thread、超时重建和 stale turn 隔离。
- [completed] 修复 `PetWindow.vue` 焦点保护定时器在输入框持续聚焦时每 16ms 自我重排的问题；输入焦点或 IME 组合期间不再启动无意义轮询。
- [completed] 对齐旧宠物兼容契约：`PetChat` 的 workspace 门禁同时接受 `projectId` 和遗留 `projectFolder`，历史面板也监听遗留字段变化。
- [needs-verification] 未构建或重启最新桌面 bundle；真实 Windows Wails 输入法交互仍需用户在新 bundle 上验收。

## Resume State Hint

保留当前未提交工作树；真实单点已经证明本机默认 Codex 配置可用，后续若继续只需使用最新 bundle 做原生 UI 验收，不要回退 `PetChat` 的 thread 复用或 `PetWindow` 的焦点保护逻辑。

## 2026-08-25 Pet Chat Floating Transcript Removal

- [completed] 按确认方案移除聊天浮窗的用户/助手消息列表、空状态、自动滚动和消息专属 CSS；浮窗只保留输入、附件、录音、发送/取消、关闭和运行状态反馈。
- [completed] 保留 Codex 流式文本清洗、计划解析、宠物气泡回复、失败/重试、取消和 watchdog；未修改长会话、历史 API 或设置页历史模块。
- [completed] 静态扫描确认 `PetChat.vue` 不再包含 transcript DOM、消息列表状态或消息列表样式；`GetChatHistory` 仍只存在于设置页历史组件。
- [needs-verification] 未构建或重启最新桌面 bundle；真实 Wails 窗口截图和交互仍需新资源手工验收。

## Resume State Hint

继续时保留当前未提交工作树；不要把 `PetChat` 的消息列表恢复为长会话展示，完整历史唯一入口是宠物设置页，实时回复唯一展示出口是 `PetWindow` 宠物气泡。

## 2026-08-25 Agent Manager Entry Slice

- [completed] 新增 `/agent` 独立页面和左侧“Agent管家”菜单入口，固定复用 `DEFAULT_PET_ID`，不新增宠物选择器。
- [completed] 将 transcript、历史刷新、实时乐观消息和 `PetChat` composer 收口到独立页面；宠物设置页移除旧 `chat-history` 页签，历史 API、Codex session 和持久化数据未改动。
- [completed] 页面在快照读取失败时展示错误和重试；根节点使用普通容器，避免嵌套应用壳层的 `<main>` landmark 和重复区域标签，并在 keep-alive 重新激活时刷新快照。
- [completed] `vue-tsc`、locale JSON 解析、目标 diff 检查和 Chrome 页面/焦点检查通过；`/agent` 菜单 active，聊天输入填入中文等待后仍保持焦点。
- [needs-verification] 浏览器 bridge 拒绝 `GetChatHistory`、`StartChat` 等真实 Wails 调用，最新 Windows desktop bundle 的历史读取、Codex 回复、取消和完成后回填仍需原生验收。

## Resume State Hint

继续时保留 Agent 管家作为聊天历史唯一入口；不要恢复宠物设置页的 `chat-history` tab，也不要复制第二套聊天状态机。原生验收只使用最新 desktop bundle，确认真实 Codex 默认配置下的历史加载、发送、流式回复、取消和刷新回填。

## 2026-08-25 Pet Chat History Transcript And Composer

- [completed] 设置页聊天历史改为左右气泡 transcript，用户消息靠右、宠物消息靠左，并保留时间、状态和图片展示。
- [completed] 历史页复用 `PetChat` composer，支持文本、图片、录音、发送、取消、失败重试，并通过统一生命周期事件同步乐观消息和流式回复。
- [completed] 历史读取、刷新、自动滚动、“回到最新消息”和响应式窄屏布局已接入；刷新失败保留已有 transcript，不覆盖正在发送的乐观消息。
- [completed] `vue-tsc`、历史解析定向测试、完整 `^TestPet` 专项和页面级桌面/窄屏检查均通过。
- [needs-verification] 浏览器 bridge 禁止 `GetChatHistory` 和 `StartChat` 等 Wails 宿主调用，因此浏览器只能证明布局和输入焦点，真实 Windows Wails/Codex 发送链路仍需最新桌面 bundle 验收。

## Resume State Hint

继续时保持历史页唯一展示完整 transcript 的边界；不要把消息列表重新放回浮窗 `PetChat`。若做原生验收，应启动最新 desktop bundle，验证历史读取、发送后的乐观消息、流式回复、取消和刷新回填。

## 2026-08-25 Pet Window Menu Responsiveness Slice

- [completed] 前端窗口模式桥改为单飞同步；同一时间只保留一个原生调用，完成后只收敛到最新目标，避免 pointer 事件堆积旧 Promise。
- [completed] `PetWindow.SetMode` 仅在点击穿透位发生变化时调用 driver；`interactive` 与 `keyboard` 之间只处理焦点，不重复写相同的原生样式。
- [completed] Windows 驱动直接维护已有 HWND 的 `WS_EX_TRANSPARENT`/`WS_EX_LAYERED`，保留并校验 `WS_EX_NOACTIVATE`，新增模式切换起止和耗时诊断。
- [completed] 状态机回归、Windows 样式位回归、前端类型检查和格式检查均通过。
- [needs-verification] 尚未用最新 desktop bundle 做真实 Windows 菜单连续开关测量；旧 bundle 不能证明本次 native 热路径已生效。

## Resume State Hint

继续时保留当前未提交工作树；原生验收只使用重新生成的 desktop bundle，查看 `codeswitch-runtime-debug.log` 中 `pet-native-mouse-mode-start` 与 `pet-native-mouse-mode` 的 `duration_ms`，不要把旧进程日志当成本次修复证据。

## 2026-08-27 Foreground Diagnostic Retirement

- 已从 GUI 启动链移除 `StartRuntimeForegroundMonitor` 和 `StartRuntimeForegroundEventMonitor`；不再创建 Win32 前台窗口轮询或 `EVENT_SYSTEM_FOREGROUND` 监听。
- 已删除 `services/runtime_foreground_windows.go` 与 `services/runtime_foreground_other.go`。`runtime_diagnostic.go` 保留给 Codex Hook、宠物运行时和窗口操作的其他诊断事件使用。
- 既有 `codeswitch-runtime-debug.log` 作为历史证据保留，不做用户数据清理；新版本不再追加 `foreground-monitor-*` 或 `foreground-window-*` 事件。
- 当前运行中的旧进程不受本次源码删除影响，需由用户手动重建并启动新版本后观察 CPU；本次未终止或重启任何进程。

## 2026-08-27 Pet Heartbeat Slice

- [completed] 新增默认关闭的独立 `/pet/heartbeat` 菜单和页面，可配置 `1-1440` 分钟频率、心跳提示词和启用开关，并通过现有 Wails/loopback bridge 暴露窄 API；首次启动不会创建 timer 或提交 Agent 任务。
- [completed] 心跳 worker 通过 `PetAIAPIService -> PetCodexRuntime` 复用当前宠物 Agent thread；提示词支持 `{{name}}`、`{{status}}`、`{{project}}`，不复制 provider、workspace 或凭据。
- [completed] timer 只在 AI `completed/failed/cancelled` 终态后重新创建；人工聊天冲突进入 `waiting_for_idle`，不会消耗周期；应用退出将活动任务标记为 `interrupted` 并取消请求。
- [completed] `pet_heartbeat` SQLite 分区、状态恢复、启停/立即执行/取消和 stale terminal 防护均有定向回归测试。
- [needs-verification] 当前环境 `CGO_ENABLED=0` 且无 `gcc`/`clang`，`go test -race` 无法执行；最新 Windows desktop bundle 尚未重启，真实 Wails/Codex 心跳仍需手工验收。

## 2026-09-01 Agent Manager Codex Capability Slice

- [completed] `CodexAppServerClient` 新增异步 server-request observer/pending map；外部可通过原始 JSON-RPC id 回传 result/error。
- [completed] pending server-request 在 app-server 进程退出、client `Close` 和 server-request context 取消时释放；旧同步 `ServerRequestHandler` 继续作为兼容路径，动态工具不会被 UI pending 阻塞。
- [completed] app-server JSONL fixture 已覆盖异步 observer 收到审批请求、`acceptForSession` 回传和原始 RPC 继续完成。
- [completed] `PetCodexRuntime` 交互请求、Skill/Model 查询和 Codex 控制命令已落地，并完成 API、Hub 和 JSONL fixture 回归。

## Resume State Hint

继续时保留当前未提交工作树；不要回退频道、Provider、PetChat 历史和心跳改动。第 2 步只扩展 `PetAIEvent`/`PetChatRequest` 与 `PetCodexRuntime`，先完成交互 pending 和命令协议，再接 API/前端；不要把模型/provider/权限覆盖重新注入 `thread/start` 或 `turn/start`。

## 2026-09-01 Agent Manager Codex Capability Completion

- [completed] `PetCodexRuntime` 已接入 approval、permission、user input、MCP form 的 pending interaction 展示与单次 resolve；重复 resolve、client 退出和关闭路径均有终态收口。
- [completed] `skills/list`、`model/list`、`thread/compact/start`、`turn/steer`、`turn/interrupt` 和 inline `review/start` 已通过同一项目 runtime 暴露；聊天 model/可选 effort 读取持久化宠物 Agent 配置，provider、权限和 sandbox 等仍不由入口覆盖。
- [completed] `AgentConversationHub` 继续作为 Agent 管家和频道的唯一会话 owner；普通 turn 按项目 FIFO，控制命令保持即时控制，完成文本通过公共事件出口广播。
- [completed] JSONL fixture、PetAI API stub 和同项目并发测试已覆盖能力查询、控制命令、四类交互、review 终态、队列和单进程/thread 复用。
- [needs-verification] 最新 Windows bundle 的真实 Wails/Codex 原生交互仍需用户手工验收；`go test -race` 继续受当前环境 `CGO_ENABLED=0` 且缺少 `gcc`/`clang` 限制。

## Resume State Hint

保留当前未提交工作树；原生验收使用最新 desktop bundle，确认 Agent 管家和聊天频道在真实 Codex 默认配置下共用历史、Skill、控制命令及审批交互。不要恢复浮窗 transcript，也不要为频道创建第二个 Codex runtime。
