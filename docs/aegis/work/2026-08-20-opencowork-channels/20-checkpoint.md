# Execution Checkpoint

## Current todo

- [x] 基线快照与 OpenCowork 频道接口读取
- [x] 独立 channels.db、迁移、一次性导入和内置实例
- [x] ChannelManager、八个平台适配器和 Agent runtime
- [x] Wails service、事件和生命周期
- [x] `/channels` 页面、项目绑定、侧栏和文案
- [x] 定向测试与手工验收
- [x] 修复按项目生成的重复内置实例，完成 canonical 收敛并清理归档历史

## Active slice

本轮收尾：按 OpenCowork `ProjectArchivePage.tsx` 与 `PluginPanel.tsx` 对齐频道页面外层限宽、项目上下文和双栏工作台，移除非原版的会话展示；保留 WeChat Official QR 绑定/重新绑定流程；不操作已有进程。

## Completed implementation

- `services/channels/` 提供独立 SQLite store、一次性 OpenCowork 导入、八个平台 provider、canonical 收敛和归档历史清理、生命周期 manager、消息去重、会话/媒体持久化和 Agent runtime。
- `main.go` 注册 `ChannelService`，从当前项目事实源补齐实例，启动/关闭 provider，并把频道事件接到 Wails 与浏览器事件流。
- `frontend/src/components/Channels/Index.vue` 提供频道配置、项目绑定、启停、权限和 WeChat 重新绑定；Sidebar 与路由已接入，session/message 持久化仍由后端 runtime 保留但不在配置页展示。
- 频道页面只展示八个 active canonical 实例；归档实例、归档历史和只读入口已移除，列表和详情操作区有滚动/宽度约束。
- 频道页面已按原版设置工作台重做：左侧 Platforms 搜索和 China/International/Custom 分组，右侧配置检查器、字段 key badge、secret 显隐、模型 popover、Advanced accordion 和底部生命周期操作；项目绑定保留在页面内。
- 页面自动保存使用 500ms 防抖，不再每次输入后 reload 快照；旧快照中的重复内置平台由数据层收敛，不再生成归档展示分组。
- `ChannelService.RemoveInstance` 仅允许非内置实例，删除前停止 provider；bridge 和前端服务已同步暴露该窄接口。
- `services/pet_browser_bridge.go` 仅增加频道页面所需的窄 RPC 白名单，不开放通用 Wails 反射调用。
- `Index.vue` 的平台集合读取边界已标注为 `ReadonlySet<string>`，兼容后端可扩展的 `ChannelInstance.type`，不改变平台筛选行为。
- `services/channels/agent_provider_tools.go` 已补齐 Feishu 图片/文件、群成员、@、加急、Bitable CRUD 和 Weixin 图片/文件工具；媒体读取支持受限本地路径与 HTTP/HTTPS URL，Windows 盘符不会再被误判为 URL。
- 频道 Agent 不再解析独立 Provider/Model；`AgentConversationHub` 与 `PetCodexRuntime` 共用同一个 Codex app-server，进程直接继承当前 Codex CLI 的默认配置，不读取宠物 Agent、项目 `codex_provider_id` 或频道历史 Provider 覆盖项。
- `services/channels/runtime.go` 已忽略历史 `providerPlatform/providerId/model` 运行时字段；Provider 解析失败或 Agent 启动失败时会向原频道发送可见失败提示，同时继续发布安全错误事件。
- `frontend/src/components/Channels/Index.vue` 已移除 Provider/Model 列表请求和选择控件，频道页面只展示“继承客户端默认”，保存旧频道时清理覆盖项。
- `frontend/src/components/Channels/Index.vue` 已接入 WeChat Official 首次绑定、二维码轮询、过期换码、重新绑定、成功后保存凭据并按项目绑定状态启停；session key 仅存在于内存，不显示在页面。
- 频道页面已恢复原版项目上下文壳：外层 `24px` 留白、`max-width: 1480px`、居中工作台和 `border + rounded-md` 边界；平台按 descriptor 固定顺序展示，WeChat 操作按钮不可压缩。
- 频道页面容器显式占满主内容盒子，工作台仍由 `max-width: 1480px` 居中；所有生命周期和微信操作按钮保持不可压缩，避免宽屏/窄栏下文字被 flex 挤压。
- 频道页面已修复滚动边界：页面壳使用固定高度和 `overflow-hidden`，顶部项目上下文不随内容滚动；左侧 `.platforms-scroll` 与右侧 `.config-scroll` 分别承担独立滚动。
- WeChat Official 二维码显示已修复：官方 `qrcode_img_content` 是由微信页面生成二维码的登录 URL，不是图片；前端新增 QR data URL 转换，保留原登录 URL 作为扫码目标，并在二维码内容变化时重绘。
- `services/pet_codex_runtime.go` 已完成频道动态工具协议桥接：`thread/start.dynamicTools` 注册、`item/tool/call` thread/turn/工具权限校验、executor 结果映射和错误终态均由独立 Codex runtime 收口。
- 动态工具指纹已纳入 `GetChatHistory` 的内存/持久化 thread 兼容判断；权限或工具集合变化时不会复用旧 thread，也不会因读取历史偷偷创建新 thread。

## Explicit non-edits

- 不修改 `F:\GitlabProjects\OpenCowork` 原仓库文件。
- 不迁移 OpenCowork renderer 状态或历史会话文件。
- 不把频道工具权限并入桌宠默认工具链。
- 真实平台在线凭据下的 native API/WS/长轮询端到端验证不在本地测试边界内；descriptor 已暴露的专用工具均通过 Manager capability 路由，未把通用 Wails 反射开放给前端。

## Evidence refs

- `services/channels/store_test.go`：独立 SQLite、迁移 marker、导入幂等、实例补齐、未绑定约束和消息去重。
- `services/channels/runtime_test.go`：provider 解析、上下文取消和运行时边界。
- `services/channels/agent_tools_test.go`：频道实例隔离、路径白名单、Shell/写入权限和消息工具持久化。
- `services/pet_browser_bridge_test.go`：token、Origin/CORS、Vite 端口和频道方法白名单。
- `main.go`：Wails service 注册、项目绑定解析、事件桥接和 shutdown 生命周期。
- `frontend/src/components/Channels/Index.vue`：频道页面手工验收入口。

- `frontend/src/components/Channels/Index.vue`：频道页面、原版工作台布局、项目绑定和平台类型边界。
- `frontend/src/services/channels.ts`：频道 service bridge 契约，包括自定义实例移除。
- `services/channels/agent_provider_tools_test.go`：专用工具平台过滤、跨平台拒绝、媒体路径/大小/HTTP 读取和 Bitable records 参数转换。
- `services/channels/weixin_login.go`：二维码 session、取消、过期刷新和确认成功凭据结果。

## Verification

- `go test ./services -run '^TestPetBrowserBridge' -count=1`：通过。
- `go test ./services/channels -count=1`：通过。
- provider 专用工具聚焦测试已包含在 `go test ./services/channels -count=1`：通过。
- `npx vue-tsc --noEmit`：通过。
- locale JSON 解析检查：通过。
- 修复平台集合的 TypeScript 字面量联合边界后，重新执行 `npx vue-tsc --noEmit`：通过。
- `go test . -run '^$' -count=1`：通过（仅编译根包测试，不执行测试体）。
- `git diff --check`：通过；仅有 Windows LF/CRLF 转换提示，无 whitespace error。
- Chrome 隔离 mock bridge 手工链路：页面渲染、侧栏入口、项目绑定、未绑定启停保护、启用/启动/停止、历史加载、消息发送和 SSE 刷新均通过。
- 本轮尝试连接 in-app browser，`agent.browsers.list()` 返回空数组；没有伪造新的截图或像素级视觉验收结论。
- 2026-08-21 定向复核：`go test ./services/channels -count=1`、`go test ./services -run '^TestPetBrowserBridge' -count=1`、`frontend/.node_modules/.bin/vue-tsc --noEmit` 均通过；`5174` 原版与 `5175` 当前项目均保持监听并返回 `200`，未停止或重启进程。
- 当前环境未提供 in-app browser；浏览器连接诊断发现的是 Chrome 扩展实例而非可用的 in-app browser，因此仍不宣称截图级视觉验收已完成。
- 2026-08-21 滚动边界修复后：`frontend/.node_modules/.bin/vue-tsc --noEmit` 通过，`5175` 根页和频道组件转换请求均返回 `200`，未操作 `5174`/`5175` 进程。
- 2026-08-21 微信二维码修复后：`vue-tsc --noEmit`、`go test ./services/channels -count=1`、QR `data:image/png` 生成实测均通过；`qrcode_img_content` 官方响应已确认是 HTML 页面 URL。
- 2026-08-21 默认配置收敛后：`go test ./services/channels -count=1`、`go test . -run '^$' -count=1`、`npm exec -- vue-tsc --noEmit` 和 locale JSON 解析均通过；当时的 reader/Relay 边界测试已在 2026-08-31 `$check-fix` 中随废弃适配层一并删除，当前验证以共享 Codex app-server/runtime 为准。
- 2026-08-27 Agent 管家收口后：dynamic tool 静态 schema、成功 executor、失败 `success:false`、未注册拒绝、历史指纹隔离和真实 JSONL app-server fixture 均 exit `0`；`go test ./services/channels -run '^TestAgentRuntime' -count=1 -v` exit `0`。
- `go test ./... -count=1`：未全通过；既有 `TestProvider_ValidateConfiguration` 的两个旧规则断言失败。单独执行 `go test . -run '^TestSeedMockRequestLogs$' -count=1` 还复现 `database is locked (5) (SQLITE_BUSY)`。

## Next step

本轮 Agent 管家接入代码和定向验证已收口，不操作或停止已有开发进程；页面截图级验收需用户本地已有前端进程可访问后再确认。微信真实扫码、重新绑定和线上平台消息收发仍需在用户已有运行环境中手工验证；仓库整包绿灯仍受既有 provider 规则断言影响。

## 2026-09-01 归档历史清理收尾

- [completed] 前端删除归档历史列表、归档展开入口、只读状态和相关文案；频道页只保留八个 canonical active 实例。
- [completed] 后端删除 `ChannelInstance.Archived` 业务字段、API 生命周期只读分支和归档 provider 操作限制；启动收敛事务删除旧归档实例及其级联历史，并删除重复内置实例。
- [completed] 保留 SQLite `archived` 兼容列，避免旧数据库迁移失败；业务不会再创建归档实例，唯一索引改为每个平台只允许一个内置实例。
- [verified] `go test ./services/channels -count=1` 通过；当前运行中的旧进程未被停止或重启。
- [verified] 默认库只读检查结果为 `240` 条实例，其中 `8` 条 active canonical、`232` 条旧归档；归档关联 session/message/media 均为 `0`。这些旧行将在下一次正常启动时由 `EnsureBuiltinInstances()` 清理，本轮不直接改动运行中的数据库。

## 2026-08-31 Agent 管家与频道同源会话收尾

- [completed] `AgentConversationHub` 以持久化宠物设置解析 canonical persona，忽略管家或频道入口自带的 persona，避免同一项目因人格指纹不同拆成多条 thread。
- [completed] `PetCodexRuntime` 以 `projectId` 作为 state/thread owner；Agent 管家和频道入口复用同一个 runtime state、Codex app-server 和 `agent_codex_session` 记录。
- [completed] 旧 `pet_codex_session` 首次项目请求会迁移写入 `agent_codex_session`；频道完成广播只对原频道完成一次回复，并按显式 `broadcastChatId` 投递其它目标。
- [completed] 动态工具执行必须具备当前入口的 `ToolExecutionScope`；Agent 管家无频道执行 scope 时 fail-fast，不猜测或借用其它频道权限。
- [completed] `PetChat` 保留异步到达的 queued 状态，StartChat 回执不会覆盖已排队请求；前端类型检查已通过。
- [completed] `$check-fix` 已删除无生产调用的 `channel_provider_resolver.go`、客户端默认 Provider reader、频道旧 Codex session adapter、旧 `ChannelModelPicker.vue` 及其专属测试；`channel_sessions.codex_*` 历史列和兼容数据保持不变。

## Resume State Hint

本计划只在当前会话和当前工作树完成，不发送到共享协作 Hub；后续原生验收沿用最新 desktop bundle，重点确认真实 Codex 默认配置下的长会话历史、频道入口共用 thread、广播目标和动态工具权限边界。

## 2026-09-01 Agent 模型展示来源修正

- [completed] Agent 管家模型标题直接读取 `GetSettingsSnapshot().agent.modelId`，不再使用 `model/list` 的 `displayName` 或 `isDefault` 推断当前模型；因此目录别名 `sol` 不会覆盖宠物 Agent 的实际配置。
- [completed] Agent 管家首屏不再请求仅用于补充展示的 `model/list`；Codex 原生 `models` 控制命令仍由共享 runtime 保留。
- [completed] 共享 `PetCodexRuntime` 的聊天模型/可选 reasoning 以持久化宠物 Agent 配置为准；频道没有独立模型覆盖。Codex CLI 默认配置继续负责认证、provider、权限、sandbox 和网络等其余运行时设置。
- [verified] 当前默认库只读结果为 `modelId=gpt-5.6-luna`；前端 `vue-tsc`、模型/runtime、Agent Hub 和 `^TestPet` 定向回归均通过。
- [needs-verification] 当前运行中的旧桌面进程未被接管；最新 bundle 仍需用户主动正常启动后确认界面显示与真实长会话。

## 2026-09-02 Hook 旁路与即时占位收尾

- [completed] Hook receiver 兼容 snake/camel payload，补充 `managed`、错误和工具字段；状态服务识别 `waiting_approval`、`waiting_user_input`、`system_error`、`session_ended` 四类通知。
- [completed] Agent 管家来源的 Hook 在状态服务和频道 runtime 双重过滤；频道来源按 `ChannelInstanceID + ChannelChatID` 精确投递，外部 Hook 才按项目和 `broadcastChatId` 广播，`EventID` 作为本地幂等键。
- [completed] 通知模板包含项目、路径、会话、事件、时间、Session/Thread/Turn、工具、参数、原因、错误和工具响应等上下文；Hook 旁路不经过 Agent，不消耗共享 Codex turn。
- [completed] `AgentConversationHub` 与频道入口共用项目级 Codex runtime；Agent 管家发送后立即在历史对话区域显示 `...`，生命周期事件覆盖占位，完成后再刷新持久化历史。
- [verified] `go test ./services/channels -count=1 -timeout 300s`：通过。
- [verified] `go test ./services -run '^(TestProjectManagerCodex|TestDecodeProjectManagerCodex|TestCodexHook|TestBuildCodexHook|TestAgentConversationHub|TestPetCodexRuntime|TestPetAIAPI|TestPetBrowserBridge)' -count=1 -timeout 300s`：通过。
- [verified] `go test . -run '^$' -count=1`、`npm exec -- vue-tsc --noEmit`（`frontend`）和 `git diff --check`：均通过；后者只有既有 LF/CRLF 转换提示。
- [needs-verification] 最新 Windows desktop bundle 的真实 Hook 文件消费、频道线上投递、长会话交互和退出体验仍需用户主动启动后验证；本轮未停止、重启或杀掉已有进程。
