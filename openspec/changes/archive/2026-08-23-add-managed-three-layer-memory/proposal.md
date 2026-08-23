## Why

当前 `internal/memory` 只有三层枚举、通用 `Put/List/Delete` 和简单候选确认，无法持久化版本、表达冲突与替代关系、隔离并发更新，也没有接入 RAG 召回或生产 Workflow。现有耐久 Workflow 与 Note Outbox 已具备可复用的幂等执行和异步投影基础，现在需要建立可审计、可召回且能在关键业务节点安全写入的记忆管理能力。

## What Changes

- 明确工作记忆、会话记忆和长期记忆的存储边界：工作记忆留在 Chat/Workflow 状态，会话与长期记忆进入独立 Memory 领域。
- 以 MySQL 作为记忆事实来源，增加不可变语义版本、当前状态、版本链、来源证据、实体引用、冲突关系、乐观锁和租户/用户隔离。
- 将候选、激活、拒绝、替代、撤销和过期定义为受约束的生命周期操作；事实变化创建新 Memory，旧 Memory 标记为 obsolete 并保留历史。
- 参考 Mem0 的事实抽取、相关记忆召回和历史记录思路，使用“精确候选 + RAG 语义候选 + LLM 关系判断 + 确定性策略裁决”处理重复、细化、纠正、矛盾和时间变化。
- 增加事务 Outbox，将激活的会话/长期记忆以 append-only 方式投影到独立 RAG 服务；旧向量不更新、不删除，查询结果必须回查 MySQL 并过滤非 active 记录。
- 增加面向 Memory ID 的 RAG 索引与候选搜索契约、自适应过量召回和可重建索引语义，不让 Agent 直接依赖 Milvus。
- 为业务 Workflow 提供显式 Recall、Extract、Conflict、Review 和 Commit 接入方式，使用稳定 `ExecutionID` 保证节点重放不重复写入，并将固定的 Memory ID、版本和哈希保存到 checkpoint。
- 本次不实现 Task/Reminder 调度领域、聊天全量自动抽取、Memory 管理前端、知识图谱或多 Agent 共享记忆；后续业务 Workflow 可通过 EntityRef 使用本能力。

## Capabilities

### New Capabilities

- `managed-three-layer-memory`: 定义三层记忆边界、MySQL 版本化事实、生命周期、冲突关系、来源证据、权限和并发更新契约。
- `append-only-memory-retrieval`: 定义激活记忆向 RAG 的 append-only 投影、基于 Memory ID 的语义召回、MySQL 有效性回查、自适应过量召回和索引重建契约。
- `workflow-memory-integration`: 定义业务 Workflow 在关键语义节点召回、提出、审核和提交记忆的幂等接入及 checkpoint 安全边界。

### Modified Capabilities

- 无。

## Impact

- 主要影响 `internal/memory`、`internal/platform/mysqlstore`、`internal/ragclient`、服务装配、配置、数据库迁移和新增业务 Workflow adapter。
- RAG 服务需要提供带用户/租户服务端过滤的 Memory 索引与候选搜索 HTTP 契约；现有笔记检索、citation 和 Grounding 契约保持不变。
- 新增 Memory 事实、审计、关系和投影 Outbox 表；不复用 `notes`、`note_drafts`、`workflow_runs` 或 RAG 向量记录作为 Memory 事实来源。
- 现有 `internal/memory.Store` 和 `CandidateService` 属于未接生产链路的内部接口，将被新的领域端口替换；现有 HTTP、SSE、Chat Run 和 Note API 不发生破坏性变更。
- 不新增 Agent 对 Milvus 的直接依赖，不把 Access Token、Cookie 或服务凭证写入 Memory、Outbox 或 Workflow checkpoint。
