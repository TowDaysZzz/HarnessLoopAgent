## Purpose

规定业务 Workflow 在关键语义里程碑召回、提出、审核和提交用户记忆的接入行为，使耐久恢复和节点重放不会产生重复或不可信的长期事实。

## ADDED Requirements

### Requirement: Workflow 在显式语义节点使用 Memory
业务 Workflow SHALL 通过显式 Recall、Extract、Conflict、Review 或 Commit 节点使用 Memory 服务，不得在通用 checkpoint、Observer 或每个节点完成事件上隐式创建用户记忆。

#### Scenario: 普通技术节点完成
- **WHEN** Workflow 节点仅完成路由、工具调用或中间计算，没有形成用户确认的事实
- **THEN** 系统 SHALL 不创建会话或长期 Memory

#### Scenario: 用户目标或决定被确认
- **WHEN** Workflow 到达定义中声明的目标确认、用户决定或最终结果里程碑
- **THEN** 对应业务节点 MAY 产生受 Memory Policy 约束的结构化 Memory Draft

### Requirement: Workflow 召回固定 Memory 版本
Workflow SHALL 将采用的 Memory ID、Lineage Version 和 Content Hash 固定在有界 checkpoint 中，并在恢复执行前验证引用仍可使用。

#### Scenario: 暂停期间 Memory 未变化
- **WHEN** Workflow 恢复时固定 Memory 的版本、哈希和有效状态仍匹配
- **THEN** Workflow SHALL 继续使用原版本以保持同一次 Run 的确定性

#### Scenario: 暂停期间 Memory 被撤销或替代
- **WHEN** Workflow 恢复时固定 Memory 已 revoked、expired、superseded 或哈希不匹配
- **THEN** Workflow SHALL 显式重新召回、暂停请求确认或按业务策略失败，不得静默改用最新 Memory

### Requirement: Workflow Memory 写入具有稳定幂等身份
所有产生 Memory 副作用的节点 MUST 使用 Workflow Execution ID 和节点内 mutation index 构造稳定幂等键，使 checkpoint 提交失败后的节点重放返回相同 Memory 结果。

#### Scenario: Memory 已提交但 Workflow checkpoint 提交失败
- **WHEN** Commit 节点已成功提交 Memory 事务，随后 Workflow checkpoint 因临时错误未提交
- **THEN** 节点重放 SHALL 读取并返回第一次提交的 Memory ID、版本和关系，不得创建重复事实

#### Scenario: 相同 Execution ID 携带不同内容
- **WHEN** 调用方以已使用的幂等键提交不同内容哈希或不同目标版本
- **THEN** Memory 服务 MUST 返回幂等冲突并保持第一次提交结果不变

### Requirement: 低权威或歧义冲突进入 HITL
Workflow SHALL 根据 Memory Policy 将模型推断、低置信度冲突、多个可能实体或敏感内容送入可耐久 Review Wait，而不是自动激活或替代长期记忆。

#### Scenario: 用户批准候选
- **WHEN** 已认证 Actor 使用匹配 Wait ID、版本、内容哈希和允许动作批准候选
- **THEN** Workflow SHALL 恢复同一 Run，并以该 Actor 和稳定 Execution ID 激活或提交对应 Memory

#### Scenario: 用户编辑候选
- **WHEN** Actor 对待审核内容提交编辑
- **THEN** Workflow SHALL 创建新内容哈希的候选或新版本并重新执行冲突策略，不得修改已经审核的旧候选正文

#### Scenario: 用户拒绝候选
- **WHEN** Actor 拒绝 Memory Draft
- **THEN** Workflow SHALL 记录 rejected 结果并继续或终止业务流程，且该候选不得进入普通召回或向量投影

### Requirement: Workflow checkpoint 遵守数据最小化
Workflow checkpoint SHALL 只保存业务恢复所需的 Memory 引用、结构化候选和策略结果，MUST NOT 保存认证凭证、未筛选完整聊天历史或无界召回内容。

#### Scenario: 节点准备持久化 Memory 上下文
- **WHEN** Recall 或 Review 节点返回大量 Memory 正文、证据或外部响应
- **THEN** codec 校验 SHALL 只允许有界白名单字段进入 checkpoint，并拒绝凭证和超限内容

#### Scenario: RAG 调用需要用户范围
- **WHEN** 耐久 Workflow 在启动或恢复后调用 Memory RAG 接口
- **THEN** 系统 SHALL 使用运行时可信服务授权和 owner scope，不得从 checkpoint 恢复原始用户 Access Token

### Requirement: Memory Workflow 与 Chat Run 生命周期解耦
长期等待 Memory 审核的 Workflow SHALL 与触发它的 Chat Run 和 SSE 生命周期保持解耦，不得占用聊天会话 active guard。

#### Scenario: Chat Run 产生待审核 Memory
- **WHEN** Chat Run 启动业务 Workflow 且 Workflow 因 Memory Review 进入 suspended
- **THEN** Chat Run MAY 正常完成，后续认证请求 SHALL 能恢复原 Workflow Run

#### Scenario: 服务重启后恢复审核
- **WHEN** 服务重启且 Memory Workflow 存在耐久 Wait
- **THEN** 系统 SHALL 从已提交 checkpoint 与 Wait 恢复同一 Workflow Run，不依赖原 Chat 连接或进程内状态

