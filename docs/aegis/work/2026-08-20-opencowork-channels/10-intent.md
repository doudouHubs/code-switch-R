# OpenCowork Channels Integration

## Requested outcome

在 Code Switch CLI 中以单进程 Go runtime 集成 OpenCowork 聊天频道能力，支持频道配置、项目绑定、平台生命周期、会话消息和 Agent 自动回复。

## Scope fence

- 新增独立 `~/.code-switch/channels.db`，不把频道数据写入现有 `app.db`。
- 首次启动只读取一次 `~/.open-cowork/plugins.json`，不做双向同步。
- 全局只保留 Feishu、DingTalk、WeCom、QQ、WeChat Official、Telegram、Discord、WhatsApp 八类 active 内置实例；旧版按项目生成的副本自动归档并保留项目、会话和消息历史。
- 未绑定项目的实例不可启用、不可启动。
- 频道页面负责项目绑定，左侧菜单提供 `/channels` 入口。
- 复用现有 PetAI 的模型协议、SSE、工具 continuation 和 provider 配置读取，不建立 Node sidecar。

## Non-goals

- 不迁移 OpenCowork 的 renderer 状态或历史会话文件。
- 不修改 OpenCowork 原仓库文件。
- 不把频道权限放宽到桌宠默认工具链。

## Baseline lock

- `main.go`
- `services/database.go`
- `services/pet_ai.go`
- `services/pet_agent_tools.go`
- `services/projectmanagerservice.go`
- `frontend/src/App.vue`
- `frontend/src/router/index.ts`
- `frontend/src/components/Sidebar.vue`
- `F:\GitlabProjects\OpenCowork\src\main\channels\`

## Verification boundary

- 共享层：独立临时 SQLite、迁移 marker、导入幂等、项目实例补齐和未绑定约束测试。
- 运行层：mock HTTP/WS fixture 验证 provider 归一化、消息路由、Agent continuation 和生命周期。
- 前端层：Wails service 调用和 `/channels` 页面手工链路验证。

## Risk hints

- 平台协议的入站方式并不统一，WebSocket、长轮询和用户配置 relay 必须由 provider owner 分别处理。
- 频道允许 Shell、写文件和媒体工具时，权限必须以频道实例为边界，不能复用桌宠默认只读假设。
