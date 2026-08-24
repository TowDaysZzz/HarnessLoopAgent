## Why

现有 Memory Runtime 已能把自然语言严格结构化、在 MySQL 中精确召回并通过耐久 Workflow 审核，但“提醒我……”仍没有独立的任务状态、触发时间和可靠投递语义。现在需要在复用 Memory 事实与精确选择器的同时建立 Reminder 领域，避免把 `memory_records.expires_at`、聊天 Run 或 Workflow Wait 误作定时调度器。

## What Changes

- 增加自然语言 Reminder 意图与严格结构化 Draft，区分创建、查询、修改、取消以及“提醒我之前记住的内容”这类 Memory Recall。
- 增加以 MySQL 为唯一事实来源的 Reminder、审计事件和投递 Outbox，支持一次性时间提醒、状态机、版本、幂等和 owner 隔离。
- 增加 `reminder-capture-v1` 耐久 Workflow，执行结构化、时区解析、相关 Memory 精确召回、冲突检查、必要的澄清/审核和原子提交。
- 允许 Reminder 固定引用当前有效的 Memory ID、Lineage Version 和 Content Hash；Memory 变化时不得静默采用其他版本。
- 增加有界的 Reminder 精确查询、修改和取消入口，不通过 Memory owner 全量扫描或开放式语义检索实现列表查询。
- 增加 Dispatcher 与事务 Outbox 投递边界，按 `next_fire_at` claim 到期 Reminder，并以 at-least-once 语义和稳定幂等身份完成投递。
- 统一 Chat 业务意图入口，避免 HTTP 关键词旁路与现有 `note.create`、Memory Capture 同时处理同一句“记住/提醒”请求。
- 首版只支持 `Asia/Shanghai` 下明确的一次性时间提醒；重复提醒、场景触发、地理围栏、外部日历和跨用户提醒不在本次范围。

## Capabilities

### New Capabilities

- `memory-aware-reminder-management`: 定义自然语言 Reminder 结构化、MySQL 权威状态、Memory 精确关联、Workflow 审核以及创建、查询、修改和取消行为。
- `reliable-reminder-delivery`: 定义到期 Reminder 的 claim、事务 Outbox、幂等投递、失败重试和终态行为。

### Modified Capabilities

- 无。

## Impact

- 影响 `internal/routing`、聊天 Executor 和 HTTP 路由，新增 Reminder 专用意图与认证控制面。
- 新增 Reminder 领域、LLM 严格结构化适配器、Workflow 节点、应用服务和 Dispatcher；复用 `internal/memory`、`internal/memoryworkflow` 与 `internal/workflow` 的既有契约。
- `internal/platform/mysqlstore` 新增 Reminder、事件和投递 Outbox 表、索引及事务适配器，不改变 `memory_records` 作为 Memory 事实来源的语义。
- 服务装配增加默认关闭的 Reminder 开关、Dispatcher 生命周期和有界配置；关闭时不改变现有 Chat、Note、Memory、RAG 或 SSE 行为。
- 首版投递通过内部端口和测试适配器验证，不承诺新增外部消息供应商或客户端推送协议。
