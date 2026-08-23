# Append-only Memory Retrieval Specification

## Purpose

为会话和长期记忆提供可重建的 append-only 语义索引与可靠召回能力，同时通过 MySQL 有效性回查避免旧向量、过期状态和租户边界影响最终上下文。

## Requirements

### Requirement: 激活 Memory 通过事务 Outbox 追加投影
系统 SHALL 只在 Memory 成为 active 后创建投影意图，并通过可重试 Outbox 将新的 Memory ID 和不可变内容追加到 RAG 索引。

#### Scenario: 新 Memory 激活
- **WHEN** 会话或长期 Memory 以 active 状态提交，或 candidate 后续被激活
- **THEN** 系统 SHALL 创建唯一投影 Outbox，并最终向 RAG 插入对应 Memory ID 的新向量

#### Scenario: 旧 Memory 被替代或撤销
- **WHEN** 已投影 Memory 后续进入 superseded、revoked 或 expired 状态
- **THEN** 在线写入路径 SHALL 保留旧向量原地，不得要求更新或删除旧向量才能完成 MySQL 状态变化

#### Scenario: 投影暂时失败
- **WHEN** RAG 返回临时错误、限流或超时
- **THEN** Outbox SHALL 保留事实引用、记录有界错误并按退避策略重试，且不得回滚已提交的 MySQL Memory

### Requirement: RAG Memory 契约返回稳定 Memory ID
RAG 搜索 SHALL 对查询进行向量化并返回稳定 Memory ID、相似度和分页或扩大召回所需信息；Agent 不得直接访问向量数据库内部结构。

#### Scenario: 语义候选搜索
- **WHEN** Agent 在可信 owner scope 下提交 Memory 查询、层级、类型和候选数量
- **THEN** RAG SHALL 在服务端应用 tenant 和 user 过滤并返回该范围内的 Memory ID 候选

#### Scenario: 模型尝试覆盖安全范围
- **WHEN** 模型或客户端提供 tenant、user、知识库或内部 Collection 过滤字段
- **THEN** Agent 与 RAG SHALL 忽略或拒绝这些不可信范围，并使用认证后服务端范围

### Requirement: 召回结果必须回查当前事实
系统 MUST 使用 RAG 返回的 Memory ID 在 MySQL 中执行 owner-scoped BatchGet，并在 rerank 或注入上下文前剔除非 active、过期、不可见或缺失记录。

#### Scenario: 搜索命中过时向量
- **WHEN** RAG 返回已 superseded 的旧 Memory 和其 active 后继版本
- **THEN** 召回层 SHALL 丢弃旧 Memory，只允许当前 active 版本进入有效候选

#### Scenario: 搜索返回未知 Memory ID
- **WHEN** RAG 返回 MySQL 中不存在或不属于当前 owner 的 Memory ID
- **THEN** 系统 SHALL 忽略该候选、记录不含敏感数据的异常指标，并继续处理其他候选

### Requirement: 精确候选与语义候选合并
系统 SHALL 将 EntityRef、事实槽、当前会话引用和内容哈希产生的 MySQL 精确候选与 RAG 语义候选合并，语义相似度不得替代稳定业务身份。

#### Scenario: 相似的多个业务实体
- **WHEN** 多条 Memory 文本高度相似但 EntityRef 不同
- **THEN** 系统 SHALL 保留各自身份，并要求上层通过固定引用或用户澄清选择目标

#### Scenario: 当前 Workflow 已固定 Memory 引用
- **WHEN** Workflow checkpoint 已包含 Memory ID、版本和内容哈希
- **THEN** 召回层 SHALL 优先精确加载该引用，而不是用向量 Top1 静默替换它

### Requirement: 召回按有效结果数量自适应扩大
系统 SHALL 以目标 active 候选数量而非固定原始 TopK 作为停止条件，并 MUST 设置最大扫描深度、超时和去重边界。

#### Scenario: 旧版本占满初始 TopK
- **WHEN** 初始向量候选在 MySQL 回查后不足目标 active 数量且 RAG 仍有更多结果
- **THEN** 系统 SHALL 分页或扩大候选窗口，直到达到目标数量、耗尽结果或命中最大扫描边界

#### Scenario: 最大扫描仍无有效结果
- **WHEN** 召回达到最大扫描或时间预算仍没有足够 active Memory
- **THEN** 系统 SHALL 返回实际有效结果及可观测的降级原因，不得使用 obsolete 内容补足数量

### Requirement: 有效候选经过有界重排和上下文预算
系统 SHALL 在 MySQL 有效性过滤后按语义相关性、精确实体匹配、确认权威、重要性和时间因素重排，并在注入模型前执行数量、字符或 Token 预算。

#### Scenario: 候选超过上下文预算
- **WHEN** active 候选总成本超过本次 Memory 上下文预算
- **THEN** 系统 SHALL 保留高优先级候选、截断其余候选并记录被丢弃数量

#### Scenario: Memory 内容包含指令文本
- **WHEN** 被召回的 Memory 文本包含要求模型改变规则或调用工具的指令
- **THEN** 系统 SHALL 将 Memory 标记为不可信数据上下文，不得把其内容提升为系统指令

### Requirement: 向量索引可由 MySQL 重建
系统 SHALL 能从 MySQL 中符合投影策略的 Memory 重新构建新的索引代际，并在不改变事实记录的情况下切换查询入口。

#### Scenario: Embedding 模型升级
- **WHEN** 系统启用新的 Embedding 模型或索引参数
- **THEN** 系统 SHALL 使用新的索引代际重建向量并在验证后切换 Alias，旧索引 MAY 作为审计或回滚数据保留

#### Scenario: 向量 Collection 丢失
- **WHEN** Memory 向量 Collection 被删除或损坏
- **THEN** 系统 SHALL 能依据 MySQL active Memory 和投影规则恢复可查询索引，且不得丢失事实或审计历史

