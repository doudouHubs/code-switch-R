# 2026-08-25 Pet Chat History Reflection

## Outcome

设置页聊天历史已成为唯一完整 transcript 入口，浮窗 `PetChat` 继续只承担输入和实时状态；历史页复用同一 Codex thread 和 composer，避免维护第二套发送协议。

## Evidence Boundary

`vue-tsc`、历史解析测试、完整 `^TestPet` 专项、Vite HMR 和 Chrome 桌面/窄屏 DOM/截图检查均通过。浏览器 bridge 不允许 `GetChatHistory` 与 `StartChat`，所以真实 Windows Wails/Codex 发送、流式回填和取消仍未获得原生证据。

## Residual Risk

最新 desktop bundle 尚未重启；原生验收时应重点确认已有 thread 的历史读取、发送后的乐观用户消息、流式宠物消息、取消后的状态和完成后的历史刷新不会互相覆盖。

## 2026-08-25 Agent Manager Reflection

### Outcome

聊天 transcript 已从宠物设置页提取到独立 `/agent` 页面，左侧入口改名为“Agent管家”。页面继续复用既有历史面板和 `PetChat` composer，因此仍是一条长会话链路，不会产生第二个 Codex session owner。

### Evidence Boundary

`vue-tsc`、两份 locale JSON、`git diff --check` 和 Chrome 桌面页面检查均通过；已确认菜单 active、设置页旧页签消失、聊天输入焦点保持。浏览器 bridge 不允许真实 Wails 历史/发送调用，故不能把本轮结果表述为原生 Codex 端到端通过。

### Residual Risk

最新 Windows desktop bundle 尚未重启；原生验收仍需确认真实 thread 历史、发送后的乐观消息、流式回复、取消状态和完成后的历史刷新。

## Scope Discipline

本轮未恢复浮窗 transcript，未修改 Go runtime、历史解析后端或 Codex session 协议，也未回退工作树中前序未提交的 provider/runtime 文件。

## 2026-08-25 Pet Window Menu Performance Follow-up

### Outcome

右下角菜单的延迟根因已收敛到宠物窗口交互模式桥：前端旧 Promise 队列会堆积过期目标，Windows 驱动每次模式切换又经过 Wails setter、额外主线程样式修复和同步状态回读。当前实现改为单飞最新目标同步，并直接维护已有 HWND 的点击穿透位。

### Evidence Boundary

`TestPetWindow*`、Windows 样式位测试、`vue-tsc` 和格式检查均通过；代码静态复核确认 `WS_EX_LAYERED`/`WS_EX_NOACTIVATE` 保留，模式切换新增 start/fail/completed 耗时日志。最新 desktop bundle 尚未重启，因此真实 Windows 菜单连续开关仍是下一步原生验收，不把代码级证据冒充成 UI 实测。

### Residual Risk

如果新 bundle 仍然出现秒级延迟，应根据 `pet-native-mouse-mode-start` 到完成日志的间隔继续区分 HWND 样式调用、Wails binding 调度和 renderer 事件链；本轮没有改动其它窗口生命周期路径。
