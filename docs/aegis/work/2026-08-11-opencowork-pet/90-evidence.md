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

## Residual Risk

- `SEC-TOCTOU-001`：`services/pet_agent_tools.go` 先通过 `EvalSymlinks`/`filepath.Rel` 校验，再以路径执行 `Stat`/`Open`；workspace 内部若被并发替换 symlink，理论上仍存在 TOCTOU。彻底修复需要 `pet_agent_secure_open_windows.go`、`pet_agent_secure_unix.go` 等 Windows handle/reparse-point 与 POSIX `openat/openat2` 实现，本轮未引入该 syscall 层；该风险不阻塞当前功能验收。
- Windows Wails 实机窗口、`AudioContext`、Wails 事件时序和真实 provider 流式响应尚未手工验收。
- Windows 实际麦克风权限、`MediaRecorder` 编码器可用性、真实 `audio/transcriptions` provider 和 Wails `TranscribeAudio` 调用尚未手工验收。
- 当前内置浏览器连接器不可用，因此未取得页面截图或点击级视觉证据；HTTP 200 只证明 Vite 能提供模块。
- `F:\GitlabProjects\OpenCowork` 当前可读；本轮对 `PetPanel.tsx`、`PetOverviewTab.tsx`、`PetStatsTab.tsx`、`PetSkinsTab.tsx`、`PetAgentTab.tsx`、`PetSleepTab.tsx` 做了定点文件级复核，但未取得截图或点击级视觉证据。
- 本轮设置页对照仍以此前保存的 OpenCowork `PetPanel`、`PetOverviewTab`、`PetSkinsTab` 和 `PetAgentTab` 证据为基线；总览固定读取默认 Capybara atlas，运行态 active atlas 只用于桌宠和皮肤列表。
- 构建依赖网络仍依赖可用的 Go proxy/checksum 服务；项目未硬编码个人代理，也未采用 `GOSUMDB=off` 这类削弱校验的规避方案。

## Next Gate

- 启动 Windows Wails 开发环境，验证 `/pet`、透明置顶窗口、点击穿透切换、聊天、只读工具、录音转写/取消、PCM 音频取消、梦境、Studio 保存/删除和应用退出清理。
