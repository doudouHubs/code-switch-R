# OpenCowork 宠物模块复刻执行计划

## Goal

在 Code Switch CLI 中复刻 OpenCowork 宠物模块的用户可见行为、状态转移、持久化语义和资源语义。Windows 先完成同等验收，macOS/Linux 仅保留公共接口。

## Architecture

- Go 持有宠物状态、养成规则、持久化、迁移、provider 适配、AI、语音、图像和窗口。
- Vue 只负责桌宠渲染、动画、设置页和用户输入。
- provider/model 只迁移引用，不迁移 API Key；不可解析的引用必须显式报错，不允许静默换模型。
- 现有 `~/.code-switch/app.db` 和 `GlobalDBQueue` 是目标数据事实源；OpenCowork 数据只读迁移。

## Tech Stack

Go 1.24, Wails v3 alpha.38, Vue 3.5, TypeScript 5.9, SQLite modernc.org/sqlite。

## Compatibility Boundary

必须保留桌宠悬浮窗、默认点击穿透、置顶、窗口交互焦点、养成动作、经验等级、自动照料、主动聊天、只读项目工具、计划任务、记忆、梦境、梦境历史、TTS、图像生成、chroma-key、精灵标准化、atlas 和中英文行为。供应商管理页面不复制，后端适配继续复用目标配置入口。

## TDD Route

未要求严格 TDD，采用先实现最小切片、再运行比例匹配的单元测试和集成检查。

## Verification

- `go test ./...`
- `npx vue-tsc --noEmit`（工作目录 `frontend`）
- Windows 开发运行下验证窗口、交互、持久化、迁移、AI 流式、语音取消、梦境降级和自定义 atlas。

