# ADR 0004：持久化会话、Agent Run 与可恢复 SSE

- 状态：已采纳
- 日期：2026-08-19

## 背景

CLI Smoke Test 无法承载多轮对话、断线恢复、Run 审计和后续长期记忆。客户端连接的生命周期也不应决定 Agent Run 是否继续执行。

## 决策

MySQL 保存会话、原始消息、Agent Run 和用户可见事件。创建 Run 与订阅事件分为两个 HTTP 请求；Run 在服务端后台执行，SSE 只回放和跟随已经持久化的事件。每个 Run 的事件序号严格单调递增，客户端通过 `Last-Event-ID` 恢复。

同一会话只允许一个 `queued` 或 `running` Run。`Idempotency-Key` 在会话内唯一，重试创建请求返回原 Run。助手最终消息和 `run.completed` 在同一个事务提交。服务启动时将上次进程遗留的活跃 Run 标记为 `interrupted`，不静默重放可能产生外部副作用的 Agent 操作。

## 上下文管理

参考 harness9 的有界上下文和 Progressive Compactor 方向，但当前阶段只实现 Token-aware `ContextAssembler`。组装器保留最新消息并严格受输入预算约束，预算不足时产生 `context.truncated` 事件。Token Counter 和 Assembler 都是可替换接口。

后续 Progressive Compactor 必须将摘要作为可重建投影单独持久化，记录摘要覆盖的消息序号范围；原始消息不能被覆盖。长期偏好、会话摘要和本次 RAG 证据属于不同上下文层，不能互相替代。

## 安全边界

- SSE 不包含 Prompt、模型密钥或完整的私有 Tool Observation。
- 历史笔记查询保持验证后输出，Grounding 失败时不发送模型草稿。
- 当前尚无用户身份系统，聊天 API 只能部署在本机或受保护的内部网关之后。
- 数据库 DSN 仅来自私有 YAML、环境变量或密钥管理系统。

## 影响

断线不再导致 Run 丢失，事件可以审计和重放，多轮上下文有明确预算。代价是聊天 API 依赖 MySQL，且生产部署需要单独管理迁移。多实例实时通知暂未接入 Redis；MySQL 仍是事件事实来源，后续 Redis 只承担唤醒和分发作用。
