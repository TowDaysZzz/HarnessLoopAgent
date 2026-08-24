## Purpose

为到期的一次性 Reminder 提供可恢复、可审计且不会因服务重启永久遗漏的投递边界，并通过租约、事务 Outbox 和稳定幂等身份约束 at-least-once 执行。

## ADDED Requirements

### Requirement: Dispatcher 只精确 claim 到期 Reminder
系统 SHALL 以数据库当前时间、owner、`scheduled` 状态、`next_fire_at`、row version 和有期限的 claim lease 选择到期 Reminder，并 MUST 使用有界批次和稳定顺序，不能依赖模型、Chat Run 或长期持有数据库事务。

#### Scenario: Reminder 到达触发时间
- **WHEN** 一条 `scheduled` Reminder 的 `next_fire_at` 已到且没有有效 claim
- **THEN** 一个 Dispatcher 实例 SHALL 原子取得处理权并将其转换为可识别的处理中状态

#### Scenario: 多实例同时扫描
- **WHEN** 多个 Dispatcher 实例同时发现同一条到期 Reminder
- **THEN** 至多一个实例 SHALL 获得当前 claim，其他实例 MUST 跳过或收到稳定冲突

#### Scenario: Dispatcher 在 claim 后退出
- **WHEN** 实例取得 claim 但在提交投递事实前退出
- **THEN** claim 租约到期后其他实例 SHALL 能重新取得处理权，且旧实例不得覆盖新版本

### Requirement: Reminder 触发与投递意图原子提交
系统 SHALL 在一个 MySQL 事务中验证 claim、记录 Reminder 触发状态与审计事件并创建唯一投递 Outbox；任一步失败 MUST 回滚整个提交。

#### Scenario: 原子创建投递 Outbox
- **WHEN** Dispatcher 成功处理到期的一次性 Reminder
- **THEN** 系统 SHALL 创建由 Reminder ID 和 occurrence identity 唯一确定的 Outbox，并使该 occurrence 不会再次产生第二条投递意图

#### Scenario: Outbox 插入失败
- **WHEN** Reminder 状态或事件准备提交但 Outbox 插入失败
- **THEN** 数据库 SHALL 保留提交前状态，并允许租约恢复后重新执行

#### Scenario: 相同 occurrence 被重放
- **WHEN** 相同稳定执行身份再次提交同一 Reminder occurrence
- **THEN** 系统 SHALL 返回第一次提交结果且不得创建重复 Outbox 或审计事件

### Requirement: 投递采用 at-least-once 与接收端幂等契约
投递 Worker SHALL 使用 Outbox ID 或等价稳定 delivery key 调用投递端口，并 MUST 将失败与成功状态持久化；系统不得宣称跨数据库与外部渠道具有 exactly-once 保证。

#### Scenario: 投递成功但确认前退出
- **WHEN** 外部投递已成功但 Worker 在标记 Outbox 完成前退出
- **THEN** 后续重试 SHALL 使用相同 delivery key，使支持幂等的接收端能够返回原结果而不产生第二条用户通知

#### Scenario: 接收端暂时失败
- **WHEN** 投递端口返回可重试错误
- **THEN** 系统 SHALL 以有界退避更新下一可用时间并保留相同 delivery key

#### Scenario: 接收端不支持幂等
- **WHEN** 某投递适配器无法接受或保证稳定 delivery key
- **THEN** 系统 MUST 拒绝将其配置为生产投递渠道，或明确保持该能力关闭

### Requirement: 一次性 Reminder 具有明确投递终态
系统 SHALL 在投递成功后把一次性 Reminder 置为 `fired`；超过配置的尝试或不可重试错误后 MUST 置为 `failed` 并保留审计信息，且终态 Reminder 不得自动重新调度。

#### Scenario: 投递成功
- **WHEN** Outbox 被投递端确认成功
- **THEN** 系统 SHALL 幂等完成 Outbox、把 Reminder 标记为 `fired` 并记录不含敏感正文的投递成功事件

#### Scenario: 投递永久失败
- **WHEN** 尝试次数耗尽或收到不可重试错误
- **THEN** 系统 SHALL 把 Reminder 标记为 `failed`、完成或隔离对应 Outbox，并保留稳定错误码供用户查询

#### Scenario: 已取消 Reminder 存在未处理意图
- **WHEN** Reminder 在投递意图提交前已取消
- **THEN** Dispatcher SHALL 跳过该 Reminder；若 Outbox 已原子创建，则取消操作 MUST 明确报告冲突或按既定投递阶段策略处理，不得静默产生互相矛盾的状态

### Requirement: Reminder 投递能力可独立关闭和恢复
系统 SHALL 以默认关闭的配置控制 Reminder API、Dispatcher 和投递 Worker；关闭投递 Worker时已提交 Reminder 与 Outbox MUST 保留，且重新启用后 SHALL 能继续处理未完成工作。

#### Scenario: Reminder 功能关闭
- **WHEN** 顶层 Reminder 开关关闭
- **THEN** 服务 SHALL 不注册 Reminder 写入能力或启动后台运行时，并保持现有 Chat、Note、Memory、RAG 和 Workflow readiness

#### Scenario: Dispatcher 暂停后恢复
- **WHEN** Reminder 数据存在但 Dispatcher 或 Worker 暂时关闭
- **THEN** 系统 SHALL 保留到期 Reminder 与 pending Outbox，重新启用后按有界批次恢复处理

#### Scenario: 没有生产投递适配器
- **WHEN** 首版仅配置测试或内部投递端口而没有可用生产渠道
- **THEN** 服务 MUST 拒绝启用生产 Worker，但 SHALL 允许领域、Workflow 和 Outbox 契约在测试环境验证

