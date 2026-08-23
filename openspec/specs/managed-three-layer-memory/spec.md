# Managed Three-layer Memory Specification

## Purpose

为 Agent 提供具有明确生命周期、版本、来源、冲突关系和所有者边界的三层记忆事实管理能力，使业务流程能够安全保存和修正用户记忆而不丢失历史。

## Requirements

### Requirement: 三层记忆拥有不同持久化边界
系统 SHALL 将工作记忆、会话记忆和长期记忆区分为不同生命周期，不得把 Workflow checkpoint 或完整聊天历史直接当作长期用户记忆。

#### Scenario: 工作记忆留在运行状态
- **WHEN** Workflow 节点产生仅供当前 Run 后续节点使用的临时推理结果或候选
- **THEN** 系统 SHALL 将其保留在有界 Workflow 状态中，且不得自动创建会话或长期 Memory

#### Scenario: 会话记忆具有作用域和期限
- **WHEN** 系统保存会话摘要、阶段结论或未完成事项
- **THEN** Memory SHALL 绑定 tenant、user 和 session 或 workflow scope，并包含明确的过期策略

#### Scenario: 长期记忆跨会话可用
- **WHEN** 用户明确确认稳定偏好、长期目标、事实或约束
- **THEN** 系统 SHALL 将其保存为 user scope 的长期 Memory，并允许后续授权会话召回

### Requirement: MySQL 是 Memory 唯一事实来源
系统 MUST 以结构化数据库中的 Memory 记录作为当前状态、所有权、版本和替代关系的权威来源，向量索引不得决定一条 Memory 当前是否有效。

#### Scenario: 向量元数据与事实状态不一致
- **WHEN** 向量候选仍包含已被替代的旧 Memory，且其旧元数据看起来仍为 active
- **THEN** 系统 SHALL 以 MySQL 当前状态为准并将该候选视为 obsolete

#### Scenario: 向量系统不可用
- **WHEN** RAG 或向量索引暂时不可用
- **THEN** 已提交的 Memory 事实、版本历史和精确读取 SHALL 保持可用且不得丢失

### Requirement: 事实变化创建不可变语义版本
系统 SHALL 为同一逻辑事实维护稳定 Lineage ID 和单调递增的 Lineage Version；事实正文或结构化值发生实质变化时 MUST 创建新的 Memory ID，不得原地改写旧版本内容。

#### Scenario: 用户修正已有偏好
- **WHEN** 用户将已确认的偏好从旧值明确修正为新值
- **THEN** 系统 SHALL 创建同一 Lineage 的新 Memory，将旧 Memory 标记为 superseded，并保存双向替代引用和不可变审计事件

#### Scenario: 相同内容重复写入
- **WHEN** 相同 owner、scope、事实槽和内容哈希的 Memory 被以相同幂等意图再次提交
- **THEN** 系统 SHALL 返回既有结果且不得创建新的语义版本

### Requirement: Memory 生命周期受状态机约束
系统 SHALL 支持 `candidate`、`active`、`rejected`、`superseded`、`revoked` 和 `expired` 状态，并 MUST 拒绝从终态返回 active 或绕过候选审核的非法转换。

#### Scenario: 推断记忆需要确认
- **WHEN** Memory 仅由模型推断且没有用户明确确认
- **THEN** 系统 SHALL 将其保存为 candidate，且不得作为 active 长期记忆提供给普通召回

#### Scenario: 用户撤销有效记忆
- **WHEN** 已授权用户撤销一条 active Memory 且提交的 expected row version 匹配
- **THEN** 系统 SHALL 将其标记为 revoked、记录 actor 与原因，并使其不再参与有效记忆查询

#### Scenario: 过期记忆被惰性或批量处理
- **WHEN** 当前时间达到 Memory 的有效期限
- **THEN** 系统 SHALL 将其视为不可召回，并最终以幂等方式记录 expired 状态和事件

### Requirement: 冲突关系由语义判断和确定性策略共同决定
系统 SHALL 区分 `duplicate`、`refinement`、`correction`、`contradiction`、`temporal_change` 和 `independent`；LLM MAY 生成结构化关系提议，但 MUST NOT 直接改变 Memory 状态。

#### Scenario: 模型推断与已确认事实冲突
- **WHEN** 新的模型推断与用户已确认的 active Memory 冲突
- **THEN** 策略层 SHALL 保留已确认事实，并将新内容拒绝或保存为待确认 candidate

#### Scenario: 用户明确纠正已有事实
- **WHEN** 当前用户明确纠正属于同一实体或事实槽的旧 Memory
- **THEN** 策略层 SHALL 允许 correction 或 temporal_change 产生新版本，并以事务方式替代旧版本

#### Scenario: 多个相似事实实际独立
- **WHEN** 语义相似候选拥有不同稳定 EntityRef 或不同事实槽
- **THEN** 系统 SHALL 保留独立 Memory，不得仅凭向量相似度合并或替代

### Requirement: 当前事实槽具有唯一性和并发保护
系统 SHALL 使用 owner、scope、namespace、slot key、状态和版本条件保证同一事实槽至多存在一条 active Memory，并 MUST 使用 expected row version 防止陈旧写入覆盖新事实。

#### Scenario: 两个请求并发替代同一记忆
- **WHEN** 两个请求基于相同旧版本并发提交不同的新 Memory
- **THEN** 至多一个请求 SHALL 完成替代，另一个 MUST 返回稳定的状态冲突且不得产生第二条 active 版本

#### Scenario: 陈旧候选在用户确认前已被修改
- **WHEN** 用户提交的 Memory ID、内容哈希或 expected version 不再匹配当前 candidate
- **THEN** 系统 MUST 拒绝确认并保持当前事实及历史不变

### Requirement: Memory 变更原子保存事实、关系、事件和投影意图
系统 SHALL 在一个数据库事务中提交新 Memory、旧 Memory 状态变化、冲突关系、审计事件和投影 Outbox；任一步失败 MUST 回滚完整操作。

#### Scenario: 新版本插入失败
- **WHEN** 旧 Memory 准备被替代但新 Memory、关系、事件或 Outbox 任一步写入失败
- **THEN** 旧 Memory SHALL 保持 active，且不得留下部分替代结果

#### Scenario: 相同 Workflow 执行被重放
- **WHEN** 相同 Execution ID 和 mutation index 再次提交相同 Memory 变更
- **THEN** 系统 SHALL 返回第一次提交的结果且不得重复写入事实、事件或 Outbox

### Requirement: Memory 所有操作实施租户与用户隔离
系统 MUST 从可信认证上下文获得 tenant 和 user 范围，并在创建、批量读取、转换、冲突查询和审计查询中同时应用该范围。

#### Scenario: 持有其他用户的 Memory ID
- **WHEN** 调用方在 BatchGet 或状态转换中提交属于其他 tenant 或 user 的 Memory ID
- **THEN** 系统 SHALL 返回 not found 或等价拒绝结果，且不得泄露该记录是否存在

#### Scenario: 敏感凭证进入记忆写入
- **WHEN** Memory、来源证据或结构化值包含 Access Token、Cookie、密码或服务密钥
- **THEN** 系统 MUST 在持久化前拒绝该变更，且不得将敏感值写入事件或日志

