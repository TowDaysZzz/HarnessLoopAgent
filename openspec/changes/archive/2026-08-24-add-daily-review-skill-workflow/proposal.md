## Why

当前聊天链路可以完成普通问答、笔记操作和独立的 Memory Capture，但还没有可注册的业务 Skill 入口，也无法把用户当天跨会话的活动、每日笔记和相关记忆组合成有证据的每日回顾。用户在同一天重复触发时，系统还会重复读取数据和调用模型，因此需要一个受 Workflow、Harness 预算和输入版本约束的 Daily Review Skill，并在输入事实未变化时安全复用结果。

## What Changes

- 增加版本化 Skill 定义、注册、匹配和执行契约，让 Skill 可以选择 Direct、Streaming、Workflow 或 Durable Workflow 执行模式；本次以 `daily_review-v1` 作为首个 Workflow Skill。
- 将自然语言“回顾今天/昨天”等表达路由到 Daily Review Skill，并保持 Chat Run、SSE 和聊天消息作为统一交互入口与结果载体。
- 增加 owner-scoped、时间窗口有界的每日活动读取能力，收集跨会话的用户/助手消息、每日笔记以及与目标、偏好、约束和结果相关的有效 Memory；Memory 只作为用户背景和关联证据，不代替当天活动事实。
- 使用强类型 Workflow 编排日期解析、数据快照、Memory 召回、回顾生成、证据校验和输出渲染；为模型调用、工具调用、步骤、时长和上下文大小设置 Harness 预算。
- 增加 owner、日期窗口、时区、Skill/Prompt 版本、请求选项和源数据指纹共同限定的回顾缓存。仅当每日会话、每日笔记和相关 Memory 的可见版本及状态均未变化时返回缓存；新增、修改、删除、撤销、过期或 supersede 均使缓存失效。
- 返回结构化且可追溯的回顾结果，包含重点、已完成、未完成、目标进展、反思问题、建议、证据引用和数据覆盖警告；证据不足时不得用模型常识补造用户活动。
- 保持每日回顾默认只读，不自动写入笔记或长期 Memory；后续明确保存请求继续进入独立的候选确认或 Memory Capture 流程。

## Capabilities

### New Capabilities

- `skill-workflow-orchestration`: 定义业务 Skill 的版本化注册、自然语言匹配、执行模式、Workflow 编排、权限、预算、事件和失败隔离契约。
- `daily-review-generation`: 定义每日会话、笔记和 Memory 的有界收集、Daily Review Workflow、证据化输出以及基于源数据指纹的安全缓存行为。

### Modified Capabilities

- 无。

## Impact

- 影响 `internal/routing`、聊天 Executor、Chat Run/SSE 事件映射和 server composition root，新增 Skill Registry 与 Skill Executor，但不改变普通聊天、Note 和现有 Memory Capture 的默认路径。
- 新增 Daily Review 领域模型、Workflow 节点、结构化模型适配器、缓存端口和应用服务；复用 `internal/workflow`、`internal/runtime`、`internal/memory`、Note 与 Chat 权限边界。
- 扩展 Chat、Note 和 Memory 的 owner-scoped 查询端口及 MySQL 索引/聚合查询，用稳定水位和内容版本构造输入快照；不把完整聊天历史、凭证或无界 Memory 正文写入 Workflow checkpoint 或缓存键。
- 增加默认关闭或可灰度的 Skill/Daily Review 配置、指标、审计事件、缓存命中率和失效原因；不新增外部调度或主动推送依赖。
