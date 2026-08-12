# Evidence Bundle

## Scope

- 本轮收口：宠物 AI native tool continuation、按 `petId` 的 workspace resolver、PCM16 音频事件、PetChat MediaRecorder 录音转写、Provider/Gemini 模型类别配置闭环和专项回归。
- 未改变 OpenCowork 源目录；未迁移 API Key；未把前端路径作为 workspace source of truth。

## Verified

- `go test ./services -run '^TestPet' -p 1 -count=1`：通过；包含 provider、native tool loop、梦境图片、Studio、窗口和调度器测试。
- `go test . -count=1`：失败于既有 `TestSeedMockRequestLogs`，错误为 `database is locked (5) (SQLITE_BUSY)`；不是宠物模块测试失败。
- `go test ./... -p 1 -count=1`：根包因同一共享 request-log SQLite 锁失败，`resources/model-pricing` 和 `services` 通过。
- `go test ./services -run 'TestPetAIWorkspaceResolver|TestPetAIChatExecutesOpenAITool|TestPetAIToolResponses|TestPetAIProjectFolder|TestPetAIToolContinuationLimit' -count=1`：通过。
- `npm exec -- vue-tsc --noEmit`（`frontend`）：退出码 0；已对齐 Wails `GeminiProvider.envConfig` 的可选 map 值契约。
- `wails3 task common:generate:bindings`：成功；生成 binding 含 `Provider.modelCategories`、`GeminiProvider.modelCategory` 和 `GeminiPreset.modelCategory`。
- Vite HTTP 检查：`/`、`/pet`、`/pet/settings`、`PetWindow.vue`、`PetStudio.vue`、中英文 locale 均返回 HTTP 200。
- `PetWindow.vue` 的窗口层文案已接入 `pet.window` 中英文 locale；`PetStudio.vue` 的可选 i18n 参数和皮肤删除 busy 判断已修复。
- 前端只读审查确认 `pet.audio` 使用 PCM16 little-endian、24 kHz、跨 chunk 半字节拼接，并在取消和卸载时丢弃旧 session。
- workspace 测试覆盖：伪造 `projectFolder`、未绑定宠物、绑定目录不存在、多宠物隔离。
- `go test ./services -run '^TestPet' -p 1 -count=1` 在修复 action-sheet 最后一行右对齐边界后通过；该回归覆盖 5 帧序列图的布局、像素顺序和非法输入。
- PetChat 录音链路静态核对：优先 `audio/webm;codecs=opus`，停止时释放 `MediaStreamTrack`，小于 1 KB 丢弃，转写成功后复用 `sendMessage` 自动发送；组件销毁和宠物切换通过 generation 丢弃旧回调。
- `git diff --check`：通过；所有 `services/pet_*.go` 经 `gofmt -l` 检查无输出。

## Residual Risk

- `SEC-TOCTOU-001`：`services/pet_agent_tools.go` 先通过 `EvalSymlinks`/`filepath.Rel` 校验，再以路径执行 `Stat`/`Open`；workspace 内部若被并发替换 symlink，理论上仍存在 TOCTOU。彻底修复需要 `pet_agent_secure_open_windows.go`、`pet_agent_secure_unix.go` 等 Windows handle/reparse-point 与 POSIX `openat/openat2` 实现，本轮未引入该 syscall 层；该风险不阻塞当前功能验收。
- Windows Wails 实机窗口、`AudioContext`、Wails 事件时序和真实 provider 流式响应尚未手工验收。
- Windows 实际麦克风权限、`MediaRecorder` 编码器可用性、真实 `audio/transcriptions` provider 和 Wails `TranscribeAudio` 调用尚未手工验收。
- 当前内置浏览器连接器不可用，因此未取得页面截图或点击级视觉证据；HTTP 200 只证明 Vite 能提供模块。

## Next Gate

- 启动 Windows Wails 开发环境，验证 `/pet`、透明置顶窗口、点击穿透切换、聊天、只读工具、录音转写/取消、PCM 音频取消、梦境、Studio 保存/删除和应用退出清理。
