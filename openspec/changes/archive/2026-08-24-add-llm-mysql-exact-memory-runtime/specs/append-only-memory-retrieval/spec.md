## MODIFIED Requirements

### Requirement: 激活 Memory 通过事务 Outbox 追加投影
系统 SHALL 只在 Memory 成为 active 后创建携带明确 Projection Version 的唯一投影意图；Projection 启用时通过可重试 Outbox 将新的 Memory ID 和不可变内容追加到 RAG 索引，Projection 关闭时则保留可恢复意图而不得影响 MySQL 事务、服务启动或 readiness。

#### Scenario: 新 Memory 激活
- **WHEN** 会话或长期 Memory 以 active 状态提交，或 candidate 后续被激活，且 Projection 已启用
- **THEN** 系统 SHALL 创建唯一投影 Outbox，并最终向 RAG 插入对应 Memory ID 的新向量

#### Scenario: 新 Memory 激活但 Projection 关闭
- **WHEN** Memory 在 MySQL-only 模式下成为 active
- **THEN** 系统 SHALL 原子保存可恢复的投影意图但不得启动投影调用、重试或故障告警，Memory 提交和精确召回 SHALL 保持可用

#### Scenario: 后续启用 Projection
- **WHEN** 运维在 RAG Memory 契约就绪后启用 Projection
- **THEN** 系统 SHALL 处理既有未投影 active Memory 及新增 Outbox，并使用配置的 Projection Version，不得依赖硬编码默认版本

#### Scenario: 旧 Memory 被替代或撤销
- **WHEN** 已投影 Memory 后续进入 superseded、revoked 或 expired 状态
- **THEN** 在线写入路径 SHALL 保留旧向量原地，不得要求更新或删除旧向量才能完成 MySQL 状态变化

#### Scenario: 投影暂时失败
- **WHEN** Projection 已启用且 RAG 返回临时错误、限流或超时
- **THEN** Outbox SHALL 保留事实引用、记录有界错误并按退避策略重试，且不得回滚已提交的 MySQL Memory

## ADDED Requirements

### Requirement: MySQL-only 运行不产生伪语义降级
当 RAG 和 Projection 被配置为关闭时，系统 SHALL 将该状态视为受支持的精确召回模式，而不是外部依赖故障；相关健康检查和指标 MUST 区分“功能关闭”与“已启用但不可用”。

#### Scenario: RAG 明确关闭
- **WHEN** 服务运行在合法的 MySQL-only Memory 配置
- **THEN** readiness SHALL 不探测 Memory RAG，召回指标 SHALL 标记 exact-only，且不得记录 rag_unavailable 错误

#### Scenario: RAG 已启用但调用失败
- **WHEN** 配置声明 RAG 已启用且语义搜索调用超时或失败
- **THEN** 系统 SHALL 保留 MySQL 精确结果、记录 rag_unavailable 或 time_budget_exceeded，并不得使用无效向量结果补足候选
