## Why

当前三层 Memory 已具备 MySQL 事实模型、冲突策略与耐久 Workflow 节点，但生产入口尚未装配，召回服务又强制依赖尚未提供 Memory API 的外部 RAG，导致记忆无法形成真实可运行闭环。现阶段需要先以“LLM 结构化 + MySQL 精确召回”打通显式记忆的生产、审核、更新与消费，同时保留未来接入语义召回的演进边界。

## What Changes

- 新增严格结构化的 LLM Memory Draft 与 Recall Plan 适配器，将自然语言转换为受限的 namespace、slot、entity、scope、layer 和 kind 选择器，禁止模型控制 owner、SQL、状态或任意 Memory ID。
- 新增 MySQL-only 精确召回模式，按固定引用、EntityRef、事实槽和内容哈希加载候选，过滤无效记录并按确定性规则重排、裁剪后注入 Prompt。
- 调整 Memory Capture 顺序为 Extract → Exact Candidate Lookup → Conflict → Review → Commit，使冲突判断基于 owner-scoped MySQL 候选。
- 在服务启动入口按默认关闭的 Memory 开关装配 Repository、LLM 适配器、精确 Recall、耐久 Memory Workflow、审核 API 与运行指标，不要求 RAG 可用。
- 以用户显式“记住/修改偏好”意图作为首个生产试点，并在选定的业务 Workflow 节点消费固定版本的 Memory；不对每条聊天消息隐式创建长期记忆。
- RAG 与 Projection 关闭时保留可恢复的投影意图但不启动 Projector，不影响 MySQL 提交或服务 readiness；未来启用 RAG 时可以回填既有 active Memory。
- 增加 MySQL-only 写入、修改、审核、恢复、精确召回、越权隔离和配置降级的端到端测试。

## Capabilities

### New Capabilities

- `structured-mysql-memory-recall`: 定义 LLM 结构化查询计划、MySQL 精确候选选择、确定性排序、无选择器安全行为和无 RAG 召回契约。

### Modified Capabilities

- `workflow-memory-integration`: 将显式 Memory Capture 调整为先提取结构化 Draft、再精确加载冲突候选，并补充生产装配、审核接口与试点触发要求。
- `append-only-memory-retrieval`: 允许 RAG 作为可选增强层关闭，明确无 RAG 时的投影意图、readiness 和未来回填行为。

## Impact

- 影响 `internal/memory` 的 Recall 契约、精确查询模型、排序和投影模式。
- 影响 `internal/memoryworkflow` 的节点顺序、ConflictResolver 输入和耐久审核恢复状态。
- 新增基于现有模型 Runner 的严格 JSON LLM 适配器，以及认证后的 Memory Capture/Review HTTP API。
- 影响 `internal/platform/mysqlstore` 的精确查询与 Projection Outbox 版本字段，但不改变 MySQL 作为唯一事实来源的原则。
- 影响 `cmd/note-agent-server/main.go`、Memory 配置校验、readiness、指标与优雅关闭装配。
- 第一阶段不依赖外部 RAG 服务或向量数据库，也不实现开放式语义召回与 Reminder 调度领域。
