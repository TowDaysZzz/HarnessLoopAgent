## MODIFIED Requirements

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

## ADDED Requirements

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

