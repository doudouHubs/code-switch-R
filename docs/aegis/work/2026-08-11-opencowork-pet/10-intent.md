# Task Intent

## Outcome

把 `F:\GitlabProjects\OpenCowork` 的宠物模块完整重构进 `F:\GitlabProjects\code-switch-cli`，保持用户行为和数据语义等价，采用 Go/Vue/Wails 目标栈。

## Scope

- Windows 完整复刻；macOS/Linux 只保留公共边界。
- Go 统一承载业务状态、数据库、迁移、provider 后端、AI、媒体和窗口。
- Vue 承载独立透明宠物窗、设置页、动画和输入。
- 迁移宠物状态、经验、照料、皮肤、自定义 atlas、梦境历史和 provider/model 引用。

## Non-goals

- 不复制 OpenCowork 的 React/Electron 实现。
- 不迁移 OpenCowork API Key。
- 不复制源项目供应商管理页面。
- 不在本阶段承诺 macOS/Linux 的等同窗口行为。

## BaselineUsageDraft

- Required baseline refs: `main.go`, `services/database.go`, `services/dbqueue.go`, `services/providerservice.go`, `frontend/src/main.ts`, `frontend/src/components/General/Index.vue`。
- Delivered context refs: OpenCowork pet stores/components/handlers/shared contracts and target Wails/provider/database inspection。
- Acknowledged before plan refs: approved implementation plan in conversation。
- Cited in plan refs: `docs/aegis/plans/2026-08-11-opencowork-pet.md`。
- Missing refs: none known。
- Decision: continue。

## ImpactStatementDraft

- Affected owners: `services`, `main.go`, `frontend/src`, `resources`, Wails bindings, SQLite schema。
- Canonical owner: Go `PetService` and target database；Vue does not own durable pet state。
- Compatibility: migration is additive and idempotent；source OpenCowork files remain unchanged。
- Risk: Wails alpha.38 lacks Electron `setFocusable`；provider matrix is broader than target's current platform-specific provider files。

## Execution Readiness View

- Intent Lock: behavior and persistence parity, Windows first。
- Scope Fence: no source-code copy, no provider UI, no API Key migration, no macOS/Linux parity claim。
- Baseline Lock: target existing Wails lifecycle, DB queue, provider configs and Vue routing。
- Task Batches: contracts/migration -> Go core -> window -> Vue -> AI/media -> integration。
- Test Obligations: Go regression, TypeScript typecheck, Windows manual and visual checks。
- Drift Rule: any new owner or fallback must be reviewed by coordinator before implementation。
