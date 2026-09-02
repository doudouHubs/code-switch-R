# Verification Evidence

## Evidence action / check performed

- 对频道 bridge 的全部页面方法执行 dispatch 白名单测试。
- 执行频道 runtime/store 测试、bridge 定向测试和前端类型检查。
- 执行 `go test ./... -count=1`，并单独复核根包的 SQLite seed 测试及 provider 配置测试。
- 使用 Chrome 隔离 mock bridge 验收 `/channels` 页面交互和 SSE 刷新。
- 使用临时 SQLite 验证旧版无 `archived` 列数据库可以先补列再创建索引；验证多项目副本收敛为八个 canonical active 实例，重复实例及其 session/message/media 历史被级联清理，导入模板保留。
- 对 provider 专用工具执行平台过滤、错误平台拒绝、媒体本地路径白名单、Windows 盘符、文件大小限制、HTTP URL 读取和 Bitable records JSON 转换测试。
- 对 WeChat Official 执行 bridge 方法契约和 session context 取消/二维码刷新相关定向测试；前端接入成功只在确认后替换 token。
- 对共享 Codex app-server/runtime 执行项目级 state/thread 复用、客户端默认配置继承、历史 session 迁移和入口权限边界测试。
- 对 Codex dynamic tool 执行 `thread/start.dynamicTools` schema、真实 JSONL fixture 的 `item/tool/call`、executor 成功结果、executor 错误 `success:false`、未注册工具拒绝和 `GetChatHistory` 工具指纹变化隔离测试。
- 对频道页面外层盒模型和操作按钮约束执行源码复核：主内容宽度显式占满，工作台保持 `max-width: 1480px` 居中，按钮使用不可压缩且不换行规则；未改动运行中的 `5174`/`5175` 进程。
- 对照原版 `ProjectArchivePage` 的滚动边界复核：频道页面根节点改为固定高度/`overflow-hidden`，左侧平台列表和右侧配置详情分别保留 `overflow-y: auto`，顶部项目上下文不再参与整体页面滚动。
- 对照 OpenCowork `normalizeQrDisplayUrl` 和微信官方页面源码复核：`qrcode_img_content` 返回 `liteapp.weixin.qq.com/q/...` HTML 页面，页面最终对 `window.location.href` 绘制 QR；当前前端使用同一 URL 生成 PNG data URL，避免把 HTML 当作 `<img>` 源。

## Result / exit status

- Bridge 定向测试：exit `0`。
- `services/channels`：exit `0`。
- `services/channels/agent_provider_tools_test.go`：包含在频道包测试中，exit `0`。
- `npx vue-tsc --noEmit`：exit `0`。
- locale JSON parse：exit `0`。
- `git diff --check`：exit `0`。
- 2026-08-27 dynamic tool 与历史指纹定向测试：`go test ./services -run 'TestPetCodex(ThreadStartParamsIncludesDynamicTools|ServerRequestExecutesRegisteredDynamicTool|ServerRequestReturnsFailureResultWhenDynamicToolExecutionFails|ServerRequestRejectsUnregisteredDynamicTool|RuntimeExecutesDynamicToolThroughAppServerFixture|HistoryDoesNotReuseThreadWhenDynamicToolFingerprintChanges|RuntimeMapsFailedTurnToFailedEvent)' -count=1 -v` exit `0`；`go test ./services/channels -run '^TestAgentRuntime' -count=1 -v` exit `0`。
- 2026-08-21 定向复核：`go test ./services/channels -count=1`、`go test ./services -run '^TestPetBrowserBridge' -count=1`、`frontend/.node_modules/.bin/vue-tsc --noEmit`：均 exit `0`；`http://127.0.0.1:5175/` 和 Vite 频道组件转换请求：均返回 `200`。
- 2026-08-21 滚动边界修复后：`frontend/.node_modules/.bin/vue-tsc --noEmit` exit `0`；`5174`/`5175` 监听 PID 保持 `24660`/`23364`。
- 2026-08-21 微信二维码修复后：`vue-tsc --noEmit`、`go test ./services/channels -count=1`、Node QR data URL 生成检查均 exit `0`；Vite 根页和频道组件请求均返回 `200`。
- 2026-08-21 默认配置收敛后：`go test ./services/channels -count=1`、`go test . -run '^$' -count=1`、`npm exec -- vue-tsc --noEmit` 和 locale JSON 解析均 exit `0`；未操作现有桌面进程或 Relay 端口。原 reader/Relay 定向测试属于已退休适配层的历史证据，不再作为当前代码入口。
- 全量测试：exit `1`，失败集中在既有 provider 规则断言；根包 seed 测试可复现 `SQLITE_BUSY`。
- 2026-08-27 重跑 `go test ./services ./services/channels -count=1`：`services/channels` exit `0`；`services` exit `1`，仍只有未改动的 `TestProvider_ValidateConfiguration` 两个旧断言失败。

## 2026-08-31 Agent 管家与频道同源会话验证

### Verified

- `go test ./services/channels -count=1`：exit `0`；覆盖频道 Agent runtime、项目绑定、失败回传、共享 runtime 生命周期和广播投递。
- `go test ./services -run '^(TestAgentConversationHub.*|TestPetCodexRuntimeSharesStateAndThreadAcrossAgentEntriesByProject|TestPetCodexRuntimeMigratesLegacyPetSessionToAgentSession|TestPetCodexRuntimeAllowsConsecutiveTurns|TestPetCodexRuntimeDeduplicatesTurnStartFailureAfterClientExit|TestPetCodexServerRequestRejectsDynamicToolWithoutExecutionScope)$' -count=5 -v`：exit `0`；连续 5 轮覆盖 canonical persona、项目 state/thread 复用、旧 `pet_codex_session` 到 `agent_codex_session` 迁移、连续 turn、进程退出竞态、Codex fixture 和动态工具权限边界。
- `npx vue-tsc --noEmit`（工作目录 `frontend`）：exit `0`；确认 queued 状态回执分支没有类型错误。
- `npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）与中英文 locale JSON 解析：exit `0`；确认删除旧频道模型选择器、更新默认配置文案后前端契约和资源格式仍然有效。
- `git diff --check`：exit `0`；只有既有 Windows LF/CRLF 转换提示，没有 whitespace error。
- `TestPetCodexServerRequestRejectsDynamicToolWithoutExecutionScope`：确认无 `ToolExecutionScope` 时直接返回协议错误，executor 不会创建、工具不会执行。
- `TestPetCodexRuntimeSharesStateAndThreadAcrossAgentEntriesByProject`：确认管家和频道不同入口只启动一个 Codex app-server，并持有同一个项目 thread。
- `TestPetCodexRuntimeMigratesLegacyPetSessionToAgentSession`：使用真实 PetDAO/SQLite 表确认首次项目请求写入 `agent_codex_session` 并保留旧 thread 元数据。
- `TestAgentRuntimeBroadcastProjectDeliversOriginalAndConfiguredTargetOnce`：确认原频道只走一次 reply，显式 `broadcastChatId` 目标只发送一次，不重复发送原频道。

### Check-fix residue cleanup

- `rg` 确认 `channel_provider_resolver.go`、`ClientDefaultCodexProviderReader`、频道旧 `codex_session` adapter 和 `ChannelModelPicker.vue` 没有生产调用；对应内部代码与专属测试已删除。
- `channel_sessions` 的 `codex_thread_id`、`codex_persona_fingerprint`、`codex_tool_fingerprint`、`codex_protocol_version` 仍由 schema/迁移读取，未执行删列、清数据或其它破坏性持久化操作。
- `go test ./services/channels -count=1` 与共享 runtime 定向测试用于复核删除后的调用链；未停止、重启或杀掉任何现有进程。

### Boundary

- 本轮没有调用协作消息、共享任务或其它 agent；`AgentConversationHub` 仅是应用内 `services` 包的会话队列/事件 owner。
- 未停止、重启或按进程名清理已有 `CodeSwitch.exe`、Codex 或前端进程；没有使用构建作为正确性验证。

### Residual Risk

- 真实平台凭据、线上频道 API/WS、最新 desktop bundle 的 Wails 原生长会话和浏览器工具仍需用户主动启动最新版本后手工验收。
- 仓库全量测试的既有 provider 规则断言和根包 SQLite 锁问题仍未改变，本轮只声明上述定向范围已通过。

## Covered scope

- 频道数据 owner、导入幂等、项目绑定和未绑定保护。
- canonical 收敛、归档实例及其级联历史清理、active provider 类型唯一索引和兼容列迁移。
- 八个平台 provider 的启动、停止、收发消息、媒体和流式能力边界。
- Agent provider 解析、工具 continuation、实例/会话/项目隔离和消息持久化。
- Wails 注册、浏览器 bridge 白名单、前端路由/侧栏/配置/历史/发送链路。

## Uncovered scope

- 真实平台凭据下的在线 WebSocket、长轮询、relay 和卡片流式端到端验证。
- 当前已运行的旧版桌面进程不会被本轮接管或重启；其数据库迁移将在下一次用户主动正常启动时执行。
- 全量仓库测试在当前基线下的稳定通过。
- 本轮新增的 dynamic tool fixture 只覆盖 app-server 协议与 executor 边界，不替代真实 Codex CLI 版本下的线上工具协商。
- 真实微信扫码、重新绑定和平台线上 API/WS 端到端链路。

## Residual risk

- provider 专用工具的本地边界已覆盖，但真实平台凭据、网络错误和服务端字段变化仍需线上验证。
- SQLite seed 测试和 provider 旧规则测试会影响全量绿灯，但没有修改本次频道 owner。

## 2026-09-01 归档历史清理验证

### Evidence action / check performed

- `go test ./services/channels -count=1`。
- 以只读方式查询当前默认 `C:\Users\X1\.code-switch\channels.db`，核对实例、归档实例关联 session/message/media 和频道媒体记录。
- 检查 `frontend/src` 与 `services/channels` 中的归档业务引用，确认只剩兼容列、启动清理 SQL、迁移注释和测试覆盖。

### Result / exit status

- 频道包测试：exit `0`。
- 默认库：总计 `240` 条实例，其中 `8` 条 active canonical、`232` 条旧归档；归档关联 session/message/media 均为 `0`；现有 `7` 条媒体记录全部属于 active WeChat Official 实例。
- 当前进程未被停止、重启或杀掉；默认库不做外部写入，旧归档行由下一次正常启动的 `EnsureBuiltinInstances()` 事务清理。

### Boundary

- SQLite `archived` 列作为旧库兼容列保留，但新业务不再暴露、创建或展示归档实例。
- `channel-media` 中的文件没有对应归档媒体记录，本轮不执行无依据的物理文件删除。

## Archive Cleanup Confidence

`B`：目标功能和集成边界有直接定向证据，仍保留真实平台和既有全量测试风险。

## 2026-09-01 Agent 模型展示来源修正验证

### Evidence action / check performed

- 只读查询 `C:\Users\X1\.code-switch\app.db` 的 `pet_agent.config_json`，核对 default 宠物的模型字段。
- 静态检查 `frontend/src/components/Agent/Index.vue`，确认展示使用 `snapshot.agent.modelId`，且首屏不再调用 `agentApi.listModels`。
- `npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）。
- `go test ./services -run '^TestPet' -p 1 -count=1 -timeout 300s`。
- `go test ./services -run '^TestAgentConversationHub|^TestPetCodexRuntimeSharesStateAndThreadAcrossAgentEntriesByProject$' -p 1 -count=1 -timeout 180s`。

### Result / exit status

- 数据库返回 `providerPlatform=codex`、`modelId=gpt-5.6-luna`、`reasoningEffort=null`。
- 页面来源断言通过：没有 `configuredModel.displayName`、`modelsLoading`、`modelsError` 或 `agentApi.listModels` 的首屏引用。
- `vue-tsc`、`^TestPet` 和共享 Hub/runtime 定向测试均 exit `0`。

### Boundary

- 保留后端 `model/list` 原生命令，未删除 Codex 能力查询契约。
- 未修改数据库、未停止或重启已有桌面进程，未宣称旧 bundle 已加载本次前端修改。

## Confidence grade

`B`：配置来源、页面静态行为和相关 runtime 回归均有新证据；真实桌面 bundle 仍需用户主动启动后验收。

## 2026-09-02 Hook 旁路与即时占位验证

### Evidence action / check performed

- 执行 `go test ./services/channels -count=1 -timeout 300s`，覆盖频道 runtime、Hook 频道直发、外部项目广播、来源过滤、幂等和消息落库。
- 执行 `go test ./services -run '^(TestProjectManagerCodex|TestDecodeProjectManagerCodex|TestCodexHook|TestBuildCodexHook|TestAgentConversationHub|TestPetCodexRuntime|TestPetAIAPI|TestPetBrowserBridge)' -count=1 -timeout 300s`，覆盖 Hook payload 解码/状态通知、共享 Hub、Codex runtime、API 和 bridge 回归。
- 执行 `go test . -run '^$' -count=1`、`npm exec -- vue-tsc --noEmit`（工作目录 `frontend`）和 `git diff --check`。
- 静态复核 `main.go` 的共享 Hub 注入、Hook notification sink、shutdown 顺序，以及 `PetChatHistoryPanel.vue` 的 `...` 占位与历史刷新竞态保护。

### Result / exit status

- `services/channels`：exit `0`。
- 共享 Hub/Codex/Hook/API/bridge 定向 services 测试：exit `0`。
- 根包空测试编译：exit `0`；前端 `vue-tsc`：exit `0`；`git diff --check`：exit `0`，无 whitespace error。
- 自动化结果确认 Hook 状态事件不会重新进入 Agent，管理器来源不会转发到频道；频道消息能够按精确来源或显式广播目标投递并按 `EventID` 去重。

### Covered scope

- Hook 四类通知识别、字段模板、来源匹配、频道旁路投递、消息幂等和历史落库。
- Agent 管家与频道共用项目级 Codex runtime、事件元数据和前端即时 `...` 占位。
- Wails 主进程依赖注入、共享 runtime 关闭边界、根包编译和前端类型契约。

### Uncovered scope

- 最新 Windows desktop bundle 的真实 Wails 原生交互、Codex 默认配置下的线上 Hook 回调、真实平台发送和退出耗时。
- `go test ./...` 的既有 provider 规则断言失败与根包 SQLite 锁问题仍未改变；本轮未把无关基线问题混入修复。
- `go test -race` 仍受当前环境 `CGO_ENABLED=0` 且缺少 C 编译器限制。

### Boundary

- 未停止、重启或杀掉任何已有 CodeSwitch、Codex 或前端进程；未使用构建作为正确性验证。
- 未把 Agent 管家 Hook 写入聊天历史或转发到频道；没有新增第二个 Codex session owner。

## Hook Notification Confidence

`B`：Hook 旁路、来源隔离、共享 runtime 和前端占位均有 fresh 定向证据；真实桌面 bundle 与线上平台仍需用户主动验收。
