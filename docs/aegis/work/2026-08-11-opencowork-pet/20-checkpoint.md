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
- 2026-08-13 Windows production build：直连执行 `task windows:build PRODUCTION=true` 在 `go mod tidy` 校验 `github.com/esiqveland/notify@v0.13.3` 时因 `sum.golang.org` 网络超时失败；设置 `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY` 为 `http://127.0.0.1:7890` 后同一任务退出码 0，产物为 `bin/CodeSwitch.exe`（约 44.3 MB）。
- 该构建过程按 `go mod tidy` 规范更新了 `go.mod`/`go.sum`；未关闭 `GOSUMDB`，未把本机代理写入项目配置。
- PetChat 录音静态检查：`MediaRecorder` 仅提交当前 generation 且使用 `audio/webm;codecs=opus` 优先编码；关闭/卸载/宠物切换调用 `discardVoiceCapture` 停止轨道并丢弃片段；录音转写复用 `sendMessage`，不复制聊天状态机。
- 本轮宠物专项首跑曾暴露 action-sheet 最后一行右对齐边界错误；修复 `SplitActionSheet` canonical owner 后，原测试完整通过。

## Drift Check

- Scope: aligned with approved plan。
- Compatibility: source data remains read-only；no key migration。
- Retirement: no old target logic removed；前端 `projectFolder` 仍作为兼容字段，但不再作为工具根目录事实源。
- Security: 基础路径穿越和普通 symlink 越界已拒绝；`SEC-TOCTOU-001`：`resolveExisting` 校验后再 `Stat/Open` 仍存在 symlink/reparse point TOCTOU，需 Windows handle 验证和 POSIX `openat/openat2` 才能完全消除；攻击者需具备 workspace 并发修改能力，本轮不阻塞功能验收。
- Decision: needs-verification。

## Next

主控已完成媒体、AI continuation、workspace resolver、Studio、PetChat 录音、模型类别配置和设置页 parity 接线，并通过 2026-08-13 宠物专项 Go、前端类型与 Vite 模块加载回归；下一步仍是 Windows Wails 启动、桌宠透明窗口、麦克风/转写链路和 Studio 流程手工验收。HTTP 200 仅证明 Vite 能提供模块，不能替代视觉或原生窗口验收。

## Resume State Hint

如果进程中断：保留当前未提交工作区；先读取本文件和 `git status --short`，不要重新迁移源文件。确认 `PetDAO.Resolve`、`PetAIService` resolver 注入、`services/pet_ai_workspace_test.go` 和前端音频文件仍在，再运行宠物专项回归。Windows 手工验收前不要宣称完整 parity；symlink TOCTOU 仍是已知残余风险。Studio 默认来源继续读取只读 `capybara`，不要把它改成 active skin。
