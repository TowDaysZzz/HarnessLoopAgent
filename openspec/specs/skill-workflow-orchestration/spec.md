# skill-workflow-orchestration Specification

## Purpose

为聊天中的可扩展业务能力提供统一、版本化且受权限和预算约束的 Skill 注册与执行契约，使多阶段 Skill 能复用现有 Workflow、Harness 和 Chat Run 生命周期，而不把每个新能力硬编码为独立路由分支。

## Requirements

### Requirement: Skill 定义可注册且版本固定
系统 SHALL 通过唯一 Skill ID 和定义版本注册业务 Skill，并声明其自然语言触发描述、严格输入和输出契约、执行模式、风险等级、依赖能力及预算。系统 MUST 在启动时拒绝重复 ID/版本、未知执行模式、缺少依赖或无效预算的定义。

#### Scenario: 注册 Workflow Skill
- **WHEN** 服务启动并加载一个依赖均可用、契约合法且 ID/版本唯一的 Workflow Skill
- **THEN** 系统将该定义加入可解析注册表，并在执行时固定使用已匹配的定义版本

#### Scenario: 注册冲突或依赖缺失
- **WHEN** 两个定义具有相同 ID/版本，或者已启用 Skill 缺少声明的 Repository、模型、Workflow 或权限依赖
- **THEN** 系统拒绝启用冲突或不完整的 Skill，且不得把请求静默交给普通模型代替执行

### Requirement: 自然语言请求只选择一个受控执行目标
系统 SHALL 在当前认证 Chat Run 内把自然语言请求解析为唯一的内建 Handler 或 Skill Invocation；Skill Invocation MUST 包含 Skill ID、定义版本、置信度以及通过严格校验的有界参数。涉及写入或低置信度的请求 MUST fail closed，不得同时启动多个业务副作用或回退到普通聊天模型执行写操作。

#### Scenario: 匹配已启用 Skill
- **WHEN** 用户输入明确匹配一个已启用 Skill 且参数满足该 Skill 的输入契约
- **THEN** 系统只执行该 Skill，并将 Skill ID、版本和安全的路由原因关联到当前 Chat Run

#### Scenario: Skill 匹配歧义
- **WHEN** 多个 Skill 的匹配置信度无法形成唯一决策，或必要参数缺失
- **THEN** 系统返回可操作的澄清文本并正常完成当前 Chat Run，不调用任一候选 Skill

#### Scenario: Skill 不可用
- **WHEN** 路由命中一个已关闭、未知版本或运行依赖不可用的 Skill
- **THEN** 系统返回稳定的能力不可用错误或安全降级文本，并不得降级为不受控的模型工具调用

### Requirement: Skill 执行模式与 Chat 生命周期解耦
系统 SHALL 支持 Direct、Streaming、Workflow 和 Durable Workflow 执行模式，并将其结果适配到既有 Chat Run 与 SSE 协议。即时 Skill MUST 在当前 Chat Run 内完成；需要人工等待或跨请求恢复的 Durable Skill MUST 使用独立 Workflow Run，且不得让 suspended Workflow 占用聊天会话 active guard。

#### Scenario: 即时 Workflow Skill 完成
- **WHEN** 用户触发一个不需要人工等待的 Workflow Skill
- **THEN** 系统在当前 Chat Run 中执行 Workflow、流式返回允许的进度和文本，并把最终助手消息保存到原会话

#### Scenario: Durable Skill 等待确认
- **WHEN** Skill Workflow 产生需要用户决定的 Wait
- **THEN** 系统持久化独立 Workflow Run 和 Wait 引用、向聊天返回有界候选并正常完成当前 Chat Run，后续请求可以按 owner scope 恢复该 Workflow

### Requirement: Workflow Skill 受统一 Harness 约束
系统 SHALL 对 Workflow Skill 应用步骤、恢复、模型调用、工具调用、持续时间、上下文和输出大小预算，并把 Workflow Node 事件关联到 Harness Run、Skill Invocation 和 Chat Run。预算耗尽、取消或超时 MUST 产生稳定终态，且不得继续调用后续节点。

#### Scenario: 节点和 Harness 事件关联
- **WHEN** Workflow Skill 执行多个节点
- **THEN** 系统记录有序的 Skill 与节点开始、完成、跳过或失败事件，并以同一关联身份记录模型、工具、证据和答案校验事件

#### Scenario: 工具调用预算耗尽
- **WHEN** Skill 已达到声明的工具调用上限并尝试再次调用工具
- **THEN** 系统停止执行，记录预算错误码并使当前 Skill 和 Chat Run 进入一致的失败终态

### Requirement: Skill 权限和状态保持最小化
所有 Skill SHALL 继承认证产生的 tenant、user、session 和知识库范围，不得接受模型或用户正文中的 owner、权限范围或任意资源 ID 覆盖可信上下文。Workflow checkpoint、事件和缓存键 MUST 只保存恢复所需的有界状态、稳定引用、版本和哈希，不得保存 Access Token、Cookie、密码或无界聊天历史。

#### Scenario: 输入尝试覆盖身份
- **WHEN** 用户或模型在 Skill 参数中提供与认证上下文不同的 tenant、user 或知识库标识
- **THEN** 系统忽略或拒绝这些字段，只使用服务端可信范围，且不返回跨 owner 数据

#### Scenario: 持久化 Skill 状态
- **WHEN** Workflow Skill 保存 checkpoint、事件或缓存元数据
- **THEN** 持久化内容只包含允许字段和有界引用，不包含认证凭证或完整原始上下文
