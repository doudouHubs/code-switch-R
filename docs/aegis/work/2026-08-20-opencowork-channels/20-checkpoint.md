# Execution Checkpoint

## Current todo

- [x] 基线快照与 OpenCowork 频道接口读取
- [x] 独立 channels.db、迁移、一次性导入和内置实例
- [x] ChannelManager、八个平台适配器和 Agent runtime
- [x] Wails service、事件和生命周期
- [x] `/channels` 页面、项目绑定、侧栏和文案
- [x] 定向测试与手工验收
- [x] 修复按项目生成的重复内置实例，完成 canonical/archived 迁移和只读边界

## Active slice

本轮收尾：按 OpenCowork `ProjectArchivePage.tsx` 与 `PluginPanel.tsx` 对齐频道页面外层限宽、项目上下文和双栏工作台，移除非原版的会话展示；保留 WeChat Official QR 绑定/重新绑定流程；不操作已有进程。

## Completed implementation

- `services/channels/` 提供独立 SQLite store、一次性 OpenCowork 导入、八个平台 provider、canonical/archived 迁移、生命周期 manager、消息去重、会话/媒体持久化和 Agent runtime。
- `main.go` 注册 `ChannelService`，从当前项目事实源补齐实例，启动/关闭 provider，并把频道事件接到 Wails 与浏览器事件流。
- `frontend/src/components/Channels/Index.vue` 提供频道配置、项目绑定、启停、权限和 WeChat 重新绑定；Sidebar 与路由已接入，session/message 持久化仍由后端 runtime 保留但不在配置页展示。
- 频道页面默认展示八个 active canonical 实例；归档实例折叠展示并保持历史只读，列表和详情操作区有滚动/宽度约束。
- 频道页面已按原版设置工作台重做：左侧 Platforms 搜索和 China/International/Custom 分组，右侧配置检查器、字段 key badge、secret 显隐、模型 popover、Advanced accordion 和底部生命周期操作；项目绑定保留在页面内。
- 页面自动保存使用 500ms 防抖，不再每次输入后 reload 快照；active/archived 互斥展示，并对旧快照的重复内置平台做显示层去重。
- `ChannelService.RemoveInstance` 仅允许非内置、非归档实例，删除前停止 provider；bridge 和前端服务已同步暴露该窄接口。
- `services/pet_browser_bridge.go` 仅增加频道页面所需的窄 RPC 白名单，不开放通用 Wails 反射调用。
- `Index.vue` 的平台集合读取边界已标注为 `ReadonlySet<string>`，兼容后端可扩展的 `ChannelInstance.type`，不改变平台筛选行为。
- `services/channels/agent_provider_tools.go` 已补齐 Feishu 图片/文件、群成员、@、加急、Bitable CRUD 和 Weixin 图片/文件工具；媒体读取支持受限本地路径与 HTTP/HTTPS URL，Windows 盘符不会再被误判为 URL。
- `services/client_default_codex_provider.go` 已将频道 Agent 的模型来源收敛到客户端 `~/.codex/config.toml` 的默认 `model`，并固定通过本地 Provider Relay 的 `/responses` 执行；不再读取宠物 Agent、项目 `codex_provider_id` 或频道历史 Provider 覆盖项。
- `services/channels/runtime.go` 已忽略历史 `providerPlatform/providerId/model` 运行时字段；Provider 解析失败或 Agent 启动失败时会向原频道发送可见失败提示，同时继续发布安全错误事件。
- `frontend/src/components/Channels/Index.vue` 已移除 Provider/Model 列表请求和选择控件，频道页面只展示“继承客户端默认”，保存旧频道时清理覆盖项。
- `frontend/src/components/Channels/Index.vue` 已接入 WeChat Official 首次绑定、二维码轮询、过期换码、重新绑定、成功后保存凭据并按项目绑定状态启停；session key 仅存在于内存，不显示在页面。
- 频道页面已恢复原版项目上下文壳：外层 `24px` 留白、`max-width: 1480px`、居中工作台和 `border + rounded-md` 边界；平台按 descriptor 固定顺序展示，WeChat 操作按钮不可压缩。
- 频道页面容器显式占满主内容盒子，工作台仍由 `max-width: 1480px` 居中；所有生命周期和微信操作按钮保持不可压缩，避免宽屏/窄栏下文字被 flex 挤压。
- 频道页面已修复滚动边界：页面壳使用固定高度和 `overflow-hidden`，顶部项目上下文不随内容滚动；左侧 `.platforms-scroll` 与右侧 `.config-scroll` 分别承担独立滚动。
- WeChat Official 二维码显示已修复：官方 `qrcode_img_content` 是由微信页面生成二维码的登录 URL，不是图片；前端新增 QR data URL 转换，保留原登录 URL 作为扫码目标，并在二维码内容变化时重绘。

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
- 2026-08-21 默认模型收敛后：`go test ./services/channels -count=1`、`go test ./services -run '^TestClientDefaultCodexProviderReader' -count=1`、`go test . -run '^$' -count=1`、`npm exec -- vue-tsc --noEmit` 和 locale JSON 解析均通过；新增测试确认 `gpt-5.6-luna` 经本地 Relay `/responses` 发送，历史 Provider 字段被忽略，默认模型解析失败会回传频道提示。
- `go test ./... -count=1`：未全通过；既有 `TestProvider_ValidateConfiguration` 的两个旧规则断言失败。单独执行 `go test . -run '^TestSeedMockRequestLogs$' -count=1` 还复现 `database is locked (5) (SQLITE_BUSY)`。

## Next step

本轮代码和定向验证已收口，不操作或停止已有开发进程；页面截图级验收需用户本地已有前端进程可访问后再确认。微信真实扫码、重新绑定和线上平台消息收发仍需在用户已有运行环境中手工验证。
