## Purpose

在不依赖外部向量服务的条件下，将自然语言安全地归一化为受限的结构化选择器，并通过 MySQL 精确召回当前有效 Memory，为 Workflow 提供确定、可审核且可逐步增强的用户上下文。

## ADDED Requirements

### Requirement: LLM 只生成受限的结构化 Recall Plan
系统 SHALL 使用严格、版本化且有界的输出契约将自然语言查询转换为 Recall Plan；模型只能提出 scope、layer、kind、namespace、slot key、EntityRef、内容哈希和澄清标记，MUST NOT 控制 owner、SQL、Memory 状态、任意 Memory ID 或结果可见性。

#### Scenario: 模型输出合法选择器
- **WHEN** 模型输出符合 schema、数量、长度、枚举和置信度边界的 Recall Plan
- **THEN** 系统 SHALL 规范化该计划并仅使用认证上下文中的 tenant 和 user 执行查询

#### Scenario: 模型尝试覆盖安全范围
- **WHEN** 模型输出 tenant、user、原始 SQL、任意 Memory ID、状态覆盖或未知字段
- **THEN** 系统 MUST 拒绝该计划，且不得执行模型提出的查询或泄露任何 Memory

#### Scenario: 模型输出低置信度或需要澄清
- **WHEN** Recall Plan 低于配置阈值、包含多个无法确定的业务实体或标记 needs_clarification
- **THEN** 系统 SHALL 返回有界的澄清结果或空召回，不得退化为扫描该用户的全部 Memory

### Requirement: 精确召回只接受稳定业务选择器
系统 SHALL 只通过已固定 MemoryRef、EntityRef、namespace 与 slot key、内容哈希或显式 session/workflow scope 查询 MySQL，并 MUST 为选择器数量、候选数量、执行时间和返回大小设置边界。

#### Scenario: 通过事实槽召回长期偏好
- **WHEN** Recall Plan 包含 user scope、namespace 和 slot key
- **THEN** 系统 SHALL 只返回当前 owner 在该事实槽下可见且 active 的 Memory

#### Scenario: 通过业务实体召回提醒背景
- **WHEN** Recall Plan 包含明确的 task 或 reminder EntityRef
- **THEN** 系统 SHALL 保留稳定实体身份，并不得用文本相似的其他实体替代该结果

#### Scenario: 没有稳定选择器
- **WHEN** Plan 没有固定引用、实体、事实槽、内容哈希或显式局部 scope
- **THEN** 系统 SHALL 返回空结果或请求澄清，且 MUST NOT 执行无条件 owner 全量扫描

### Requirement: 精确候选以 MySQL 当前事实过滤
系统 MUST 在候选进入排序、冲突判断或 Prompt 前验证 tenant、user、scope、状态、过期时间以及固定引用的 Lineage Version 和 Content Hash。

#### Scenario: 候选已被替代或撤销
- **WHEN** 精确查询或固定引用命中 superseded、revoked、rejected、expired 或已超过 expires_at 的 Memory
- **THEN** 系统 SHALL 剔除该记录并记录不含正文的过滤指标

#### Scenario: 固定版本在 Workflow 暂停期间变化
- **WHEN** Workflow 恢复时 Memory ID 仍存在但版本、哈希或 active 状态不再匹配
- **THEN** 系统 SHALL 返回固定引用变化结果，由 Workflow 重新召回、请求确认或显式失败

#### Scenario: 其他用户的 Memory ID 被提交
- **WHEN** 调用方持有不属于当前 owner 的 Memory ID 或 EntityRef
- **THEN** 系统 SHALL 返回 not found 或空结果，且不得泄露记录是否存在

### Requirement: MySQL-only 召回采用确定性重排和上下文预算
系统 SHALL 按固定引用、实体匹配、事实槽匹配、内容哈希、权威等级、重要性和时间因素进行稳定排序，并在注入模型前执行结果数量和上下文字符预算。

#### Scenario: 多种精确来源命中同一 Memory
- **WHEN** 同一 Memory 同时被固定引用、EntityRef 和事实槽命中
- **THEN** 系统 SHALL 去重并以固定引用优先级返回一次

#### Scenario: 候选超过预算
- **WHEN** 有效候选数量或正文总长度超过配置预算
- **THEN** 系统 SHALL 保留高优先级候选、丢弃其余候选并返回可观测的截断计数

#### Scenario: Memory 正文包含指令
- **WHEN** 被选中的 Memory 包含要求模型改变系统规则、调用工具或泄露数据的文本
- **THEN** 系统 MUST 将其封装为不可信数据上下文，不得提升为系统指令

### Requirement: MySQL-only 模式不依赖 RAG 可用性
当 Memory 启用而 RAG 和 Projection 关闭时，系统 SHALL 仅执行结构化 MySQL 精确召回，不得构造或调用语义搜索客户端，也不得将缺少 RAG 报告为运行故障。

#### Scenario: 服务以 MySQL-only Memory 配置启动
- **WHEN** Memory 和数据库启用，RAG 与 Projection 明确关闭且配置合法
- **THEN** 系统 SHALL 成功启动精确召回和 Memory Workflow，并保持服务 readiness

#### Scenario: 精确查询没有结果
- **WHEN** MySQL-only 模式下所有合法选择器均未命中 active Memory
- **THEN** 系统 SHALL 返回空 Memory 上下文和 results_exhausted 或等价结果，不得伪造语义候选

