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

### Requirement: Skill 权限和状态保持最小化
所有 Skill SHALL 继承认证产生的 tenant、user、session 和知识库范围，不得接受模型或用户正文中的 owner、权限范围或任意资源 ID 覆盖可信上下文。Workflow checkpoint、事件和缓存键 MUST 只保存恢复所需的有界状态、稳定引用、版本和哈希，不得保存 Access Token、Cookie、密码或无界聊天历史。

#### Scenario: 输入尝试覆盖身份
- **WHEN** 用户或模型在 Skill 参数中提供与认证上下文不同的 tenant、user 或知识库标识
- **THEN** 系统忽略或拒绝这些字段，只使用服务端可信范围，且不返回跨 owner 数据

#### Scenario: 持久化 Skill 状态
- **WHEN** Workflow Skill 保存 checkpoint、事件或缓存元数据
- **THEN** 持久化内容只包含允许字段和有界引用，不包含认证凭证或完整原始上下文
