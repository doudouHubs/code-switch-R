# Verification Evidence

## Evidence action / check performed

- 对频道 bridge 的全部页面方法执行 dispatch 白名单测试。
- 执行频道 runtime/store 测试、bridge 定向测试和前端类型检查。
- 执行 `go test ./... -count=1`，并单独复核根包的 SQLite seed 测试及 provider 配置测试。
- 使用 Chrome 隔离 mock bridge 验收 `/channels` 页面交互和 SSE 刷新。
- 使用临时 SQLite 验证旧版无 `archived` 列数据库可以先补列再创建索引；验证多项目副本收敛为八个 canonical active 实例，旧项目、会话和消息历史保持可读。
- 对 provider 专用工具执行平台过滤、错误平台拒绝、媒体本地路径白名单、Windows 盘符、文件大小限制、HTTP URL 读取和 Bitable records JSON 转换测试。
- 对 WeChat Official 执行 bridge 方法契约和 session context 取消/二维码刷新相关定向测试；前端接入成功只在确认后替换 token。
- 对客户端默认 Codex reader 执行模型读取、历史引用忽略、Relay `/responses` endpoint 和缺失默认模型错误测试；对 runtime 执行默认 Provider 解析失败时的频道失败提示测试。
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
- 2026-08-21 定向复核：`go test ./services/channels -count=1`、`go test ./services -run '^TestPetBrowserBridge' -count=1`、`frontend/.node_modules/.bin/vue-tsc --noEmit`：均 exit `0`；`http://127.0.0.1:5175/` 和 Vite 频道组件转换请求：均返回 `200`。
- 2026-08-21 滚动边界修复后：`frontend/.node_modules/.bin/vue-tsc --noEmit` exit `0`；`5174`/`5175` 监听 PID 保持 `24660`/`23364`。
- 2026-08-21 微信二维码修复后：`vue-tsc --noEmit`、`go test ./services/channels -count=1`、Node QR data URL 生成检查均 exit `0`；Vite 根页和频道组件请求均返回 `200`。
- 2026-08-21 默认模型收敛后：`go test ./services/channels -count=1`、`go test ./services -run '^TestClientDefaultCodexProviderReader' -count=1`、`go test . -run '^$' -count=1`、`npm exec -- vue-tsc --noEmit` 和 locale JSON 解析均 exit `0`；未操作现有桌面进程或 Relay 端口。
- 全量测试：exit `1`，失败集中在既有 provider 规则断言；根包 seed 测试可复现 `SQLITE_BUSY`。

## Covered scope

- 频道数据 owner、导入幂等、项目绑定和未绑定保护。
- canonical/archived 迁移、active provider 类型唯一索引和 archived 全生命周期只读保护。
- 八个平台 provider 的启动、停止、收发消息、媒体和流式能力边界。
- Agent provider 解析、工具 continuation、实例/会话/项目隔离和消息持久化。
- Wails 注册、浏览器 bridge 白名单、前端路由/侧栏/配置/历史/发送链路。

## Uncovered scope

- 真实平台凭据下的在线 WebSocket、长轮询、relay 和卡片流式端到端验证。
- 当前已运行的旧版桌面进程不会被本轮接管或重启；其数据库迁移将在下一次用户主动正常启动时执行。
- 全量仓库测试在当前基线下的稳定通过。
- 真实微信扫码、重新绑定和平台线上 API/WS 端到端链路。

## Residual risk

- provider 专用工具的本地边界已覆盖，但真实平台凭据、网络错误和服务端字段变化仍需线上验证。
- SQLite seed 测试和 provider 旧规则测试会影响全量绿灯，但没有修改本次频道 owner。

## Confidence grade

`B`：目标功能和集成边界有直接定向证据，仍保留真实平台和既有全量测试风险。
