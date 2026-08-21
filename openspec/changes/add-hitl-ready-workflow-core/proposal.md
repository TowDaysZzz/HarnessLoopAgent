## Why

当前 `internal/workflow` 仅提供基于 `map[string]any` 的未接入生产链路的顺序 Runner，无法在编译期约束节点状态，也无法区分节点失败与等待人工输入。现在先建立 HITL-ready 的强类型工作流核心，可以在不扰动现有聊天、路由和 SSE 链路的前提下，为后续 Eino Compose checkpoint、人工审批和可恢复业务流程提供稳定领域契约。

## What Changes

- 将工作流状态改为泛型 `WorkflowState[T]`，分离运行元数据、控制状态、预算状态和业务数据，并禁止把认证凭证写入可 checkpoint 的状态。
- 定义强类型节点输入输出和顺序 Runner，使节点可以明确返回继续或暂停指令，而不是把等待人工输入表示为普通错误。
- 定义 `started/completed/suspended/resumed/failed/skipped` 节点生命周期事件、单调事件序号和安全字段白名单。
- 增加 Workflow Run、节点、等待点、定义版本和来源引用等稳定身份，为未来 checkpoint 和恢复提供关联键。
- 增加步骤、恢复次数和运行时限预算检查，以及状态转换、事件顺序、取消、暂停和非法恢复测试。
- 直接替换当前未被生产代码调用的 `map[string]any` Step/Runner 骨架，不保留长期双轨兼容层。
- 本次不增加 Workflow 持久化表、审批 API、Eino Compose adapter、聊天 Run 等待状态或新的 SSE 契约，也不迁移现有聊天执行链路。

## Capabilities

### New Capabilities

- `hitl-workflow-execution`: 提供强类型、可审计、有预算并具备人工暂停/恢复语义的工作流核心执行契约。

### Modified Capabilities

- 无。

## Impact

- 主要影响 `internal/workflow` 及其单元测试；当前没有生产调用点，因此不改变现有运行时行为。
- `internal/workflow.Step` 和 `Runner` 的 Go 内部接口会发生不兼容调整，但不影响 HTTP、SSE、MySQL、RAG 或前端契约。
- 后续 Eino Compose 集成将通过 adapter 映射 `StatefulInterrupt`、checkpoint 和 resume，不要求业务领域类型直接依赖 Eino。
- 现有 `note_drafts` 候选确认流程保持不变，未来可作为首个 HITL 迁移或适配试点。
