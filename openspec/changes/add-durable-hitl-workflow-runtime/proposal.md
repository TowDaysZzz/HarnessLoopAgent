## Why

现有强类型 Workflow Core 已能在单进程内暂停和恢复，但暂停状态、等待点和节点事件仍由调用方保管，服务重启、多实例并发或重复审批时无法提供耐久恢复与一次性处理保证。现在需要在不改动现有聊天链路的前提下建立持久化运行时，使后续单个业务流程能够安全接入 Human-in-the-loop。

## What Changes

- 增加 Workflow Run、人工等待点和节点审计事件的持久化领域端口，以及基于现有 MySQL store 与迁移体系的实现。
- 增加业务状态 codec 和 schema/version 信封，使强类型 `WorkflowState[T]` 能够安全持久化并在恢复时校验定义与数据版本。
- 增加持久化运行协调器，在首次执行、暂停、恢复和终态转换时保存一致的 Workflow、Wait 与 Event 事实。
- 使用 `StateVersion`、Wait 版本和原子条件更新保证同一个等待点只能被一个恢复请求取得处理权；冲突、陈旧、过期和重复恢复不得启动节点。
- 定义恢复处理中断后的租约回收和失败状态，使服务重启后能够重新加载 suspended Workflow，并避免永久占用等待点。
- 增加内存契约测试和 MySQL 集成测试，覆盖重启恢复、并发恢复、事务回滚、事件唯一性、序列化兼容和敏感字段边界。
- 本次不增加审批 HTTP API、前端待办、聊天 `waiting` 状态、新 SSE 事件、Eino Graph adapter，也不迁移 `note_drafts` 或其他生产 Agent 流程。

## Capabilities

### New Capabilities

- `durable-hitl-workflow-runtime`: 提供 Workflow、Wait 和审计事件的耐久存储、版本化状态恢复、原子恢复权声明及故障后恢复契约。

### Modified Capabilities

- 无。

## Impact

- 主要影响 `internal/workflow` 的持久化端口与协调层、`internal/platform/mysqlstore` 的适配实现、数据库迁移和相关测试。
- 新增 Workflow Run、Wait 和 Event 持久化表，但不修改现有 `agent_runs`、`agent_run_events`、聊天状态机或会话 active guard。
- 不改变现有 HTTP、SSE、前端、RAG、路由和 Agent Runner 契约；在业务试点接入前，现有生产执行路径保持不变。
- 不新增外部基础设施依赖，沿用当前 MySQL 驱动、迁移和集成测试方式。
