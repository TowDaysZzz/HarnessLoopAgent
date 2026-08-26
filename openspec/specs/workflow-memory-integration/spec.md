# Workflow Memory Integration Specification

## Purpose

规定业务 Workflow 在关键语义里程碑召回、提出、审核和提交用户记忆的接入行为，使耐久恢复和节点重放不会产生重复或不可信的长期事实。

## Requirements

### Requirement: Workflow 在显式语义节点使用 Memory
业务 Workflow SHALL 通过显式 Query Plan、Extract、Exact Candidate Lookup、Conflict、Review、Commit 或 Recall 节点使用 Memory 服务；产生记忆时 MUST 先形成合法结构化 Draft，再加载 owner-scoped 精确候选并执行冲突策略，不得在通用 checkpoint、Observer 或每个节点完成事件上隐式创建用户记忆。

#### Scenario: 普通技术节点完成
- **WHEN** Workflow 节点仅完成路由、工具调用或中间计算，没有形成用户确认的事实
- **THEN** 系统 SHALL 不创建会话或长期 Memory

#### Scenario: 用户目标或决定被确认
- **WHEN** Workflow 到达定义中声明的目标确认、用户决定或最终结果里程碑
- **THEN** 对应业务节点 MAY 产生受 Memory Policy 约束的结构化 Memory Draft

#### Scenario: Draft 用于定位冲突候选
- **WHEN** Extract 节点生成包含 namespace、slot key、EntityRef、scope 或内容哈希的合法 Draft
- **THEN** Workflow SHALL 在 Conflict 前通过这些稳定选择器加载 MySQL 候选，并只允许冲突判断引用该 owner-scoped 候选集

#### Scenario: 显式记忆意图启动试点
- **WHEN** 已认证用户明确表达“记住”、长期偏好或对既有记忆的修正
- **THEN** 系统 SHALL 启动与 Chat Run 解耦的 Memory Capture Workflow，且普通对话不得因试点启用而自动写入长期 Memory

### Requirement: Memory Workflow 提供认证且耐久的控制面
系统 SHALL 提供认证后的 Memory Capture 启动、状态查询、待审核读取、批准、拒绝、编辑和恢复操作，并 MUST 使用现有耐久 Workflow 的 Wait 版本、内容哈希、Actor 与幂等保护。

#### Scenario: 用户批准待审核候选
- **WHEN** 当前 owner 使用匹配的 Wait ID、版本和内容哈希提交批准
- **THEN** 系统 SHALL 恢复同一 Workflow Run、激活对应候选并返回固定 MemoryRef

#### Scenario: 用户编辑待审核候选
- **WHEN** 当前 owner 提交符合边界的编辑内容
- **THEN** 系统 SHALL 创建新候选、重新执行精确候选加载和冲突策略，并再次进入耐久 Review Wait

#### Scenario: 用户访问其他 owner 的 Wait
- **WHEN** 已认证用户提交不属于自身 tenant 和 user 的 Run ID、Wait ID 或候选引用
- **THEN** 系统 MUST 返回 not found 或等价拒绝，且不得泄露该 Workflow 或 Memory 是否存在

#### Scenario: 服务重启后继续审核
- **WHEN** Memory Workflow 在 Review Wait 期间发生服务重启
- **THEN** 后续认证请求 SHALL 从数据库 checkpoint 恢复同一 Run，不依赖原进程、Chat 连接或 SSE

### Requirement: Memory 生产运行时由独立开关渐进启用
系统 SHALL 以默认关闭的顶层 Memory 开关和独立 Workflow Pilot 开关控制生产装配；MySQL-only 模式 MUST NOT 要求 RAG、Projection Worker 或外部向量服务可用。

#### Scenario: Memory 总开关关闭
- **WHEN** Memory 总开关关闭
- **THEN** 服务 SHALL 保持现有 Chat、Note 和 Workflow 行为，不注册 Memory API 或启动 Memory 后台任务

#### Scenario: Memory 与 Workflow Pilot 开启且 RAG 关闭
- **WHEN** 数据库、Memory 和 Workflow Pilot 开启，RAG 与 Projection 关闭
- **THEN** 服务 SHALL 装配 MySQL Repository、LLM 结构化适配器、精确 Recall、耐久 Capture Runtime 和审核控制面，并保持 readiness

#### Scenario: LLM 结构化适配器不可初始化
- **WHEN** Memory Workflow Pilot 开启但所选模型或严格结构化输出能力不可用
- **THEN** 服务 MUST 拒绝启用该 Pilot 并返回不含凭证的明确配置错误，不得静默使用非结构化自由文本写入 Memory

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
长期等待审核的 Memory Workflow SHALL 仅通过独立认证控制面启动和恢复，并与 Chat Run、SSE、会话 active guard 及进程内 Chat 状态保持解耦。Chat Runtime MUST 不根据自然语言创建 Memory Capture Workflow、候选或 Wait。

#### Scenario: Chat Run 产生待审核 Memory
- **WHEN** 用户在 Chat 中表达“记住”或长期偏好
- **THEN** Chat Runtime 不启动 Memory Capture Workflow、不创建候选，也不产生 Memory 副作用

#### Scenario: 通过独立控制面启动 Memory Capture
- **WHEN** 当前 owner 通过认证的 Memory 控制面提交合法 Capture 请求
- **THEN** 系统启动独立 Workflow Run，且该 Workflow 的运行或暂停不占用任何聊天会话 active guard

#### Scenario: 服务重启后恢复审核
- **WHEN** 服务重启且 Memory Workflow 存在耐久 Wait
- **THEN** 系统从已提交 checkpoint 与 Wait 恢复同一 Workflow Run，不依赖原 Chat 连接或进程内状态
