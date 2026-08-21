## Context

See `proposal.md` for motivation. 当前聊天服务直接加载会话上下文后调用一个 `ConversationRunner`，而 `routing.Classifier` 和 `routing.Executor` 仅存在于预留领域包。现有 Eino Runner 已包含工具调用、RAG 保护和 SSE 事件适配，笔记服务和 RAG Client 也已经具备独立权限边界。

## Goals / Non-Goals

**Goals:**

- 在 Run 生命周期内增加一次统一分类和一次受控执行。
- 让执行器以依赖注入方式复用现有 Eino、笔记和 Workflow 能力。
- 保持当前 HTTP、SSE、会话上下文和 RAG 证据治理兼容。
- 记录路由与执行器级别的状态、耗时和错误。

**Non-Goals:**

- 本次不启用多 Agent，也不改变 `ENABLE_MULTI_AGENT` 的默认值。
- 本次不实现笔记版本更新、长期记忆自动提取或新的 MCP 工具。
- 本次不允许通过聊天删除笔记；删除继续只由笔记页面和 REST API 提供。

## Decisions

### 1. 在聊天服务内增加 Router + Executor Facade

聊天服务在创建 Run 后由 Router 调用 `Classifier.Classify`，写入 `route.decided`，再把统一的 `routing.RunContext` 交给 Executor Facade。`RouteDecision` 将业务意图与复杂度分开建模，例如 `note.query + complex` 进入复杂笔记问答，`chat + simple` 进入普通聊天。Facade 按组合决策选择注入的 handler，避免让 HTTP 层或 Eino Runner 直接分支。相比把分类逻辑塞进 Prompt，这能保证确定性写操作和权限校验不会被模型绕过。

### 2. 用 handler 端口隔离业务执行

定义笔记新增、查询、普通模型、复杂任务和澄清 handler 端口；聊天链路不注册删除 handler。默认实现复用现有 Note Service、Eino Runner、RAG grounding 和 Workflow。确定性 handler 使用 `Execute(ctx, input) (Result, error)`，模型/RAG/复杂任务 handler 使用 `Stream(ctx, input) <-chan agent.Event`，Facade 将两类结果适配为统一 Run 事件。这样单元测试可以用 fake handler 验证路由，不必启动大模型或 RAG。

### 3. 保持 SSE 事件协议，增加内部路由事件

复用已有 `run.status`、`tool.completed`、`text.delta` 和终态事件；额外持久化 `route.decided` 与 `executor.started/completed/failed`，前端不需要立即升级即可继续消费文本流。事件数据只记录安全的意图、耗时、状态和错误码，不记录原始 Token 或认证材料。

### 4. RAG 查询走现有受保护 Runner

`note.query` 继续使用 Eino 的 `semantic_search_notes` 和 grounding 校验，不在 Executor 中复制证据门禁。分类结果将成为程序级保护开关，修复当前仅依赖关键词的分叉不一致问题。

### 5. 聊天新增笔记分为直接写入与候选确认

当前消息已经明确给出完整笔记内容时，NoteCreateHandler 可直接调用 Note Service，并经过用户/知识库绑定和幂等校验。当用户要求“总结刚才聊天并记住”时，NoteDraftHandler 使用当前上下文生成候选标题和正文，只写入待确认草稿；后续同一会话中的明确确认才调用 Note Service。模型不能把候选生成当成已保存事实。

待确认草稿使用独立持久化模型，至少包含 `id/user_id/tenant_id/session_id/title/content/status/content_hash/expires_at/created_at/updated_at`。状态包括 `pending/confirmed/cancelled/expired`；同一会话新候选会替换旧 pending 版本。确认操作使用草稿 ID 和内容哈希实现幂等并防止确认过期版本。只有同一用户、同一会话存在最新 pending 草稿时，“确认保存/保存这条”等表达才解析为 `note.create` 确认操作；否则进入澄清，避免把普通肯定答复误判为写入。

### 6. 聊天删除使用受控拒绝

Classifier 仍识别 `note.delete`，但 Executor Facade 将其映射到固定拒绝 handler，输出“请在笔记页面删除”并正常完成 Run。服务容器不向聊天 Executor 注入 Note Delete Service，因此即使模型产生工具调用也没有删除能力。

### 7. 澄清文本正常完成 Run

`intent.unclear` 不增加 `needs_clarification` 状态。ClarificationHandler 输出确定性澄清文本后发送 `run.completed`；用户补充信息时创建同一会话内的新 Run，由会话历史提供上下文。

### 8. 复杂任务第一版保持单 Agent

复杂度为 `complex` 的请求进入独立 ComplexHandler，但仍使用一个 Eino Agent，并受调用预算、MaxSteps 和超时约束。本期不实现动态多 Agent 调度、并行步骤、长期任务恢复或人工审批。

### 9. 配置开关与降级

新增 `ENABLE_INTENT_ROUTING`、`ENABLE_LEGACY_ROUTING_FALLBACK`、`INTENT_COMPLEX_THRESHOLD`、`INTENT_MIN_WRITE_CONFIDENCE` 和 `NOTE_DRAFT_TTL`。默认值分别为 `true`、`true`、`120`、`0.95` 和 `24h`。只读 handler 未注册或低风险失败时可以回退 legacy runner，任何写操作都不得回退给模型。关闭主开关可恢复原有 Runner 路径。

## Risks / Trade-offs

- [风险] 旧测试和 fake runner 依赖无路由上下文 → [缓解] 为 Service 提供默认 Executor Facade，并保留 runner-only 构造路径用于兼容迁移。
- [风险] 规则分类可能漏判自然语言表达 → [缓解] 保留 Eino 语义工具提示作为二级能力，并记录低置信度路由，后续可替换为模型分类器但不能绕过程序权限。
- [风险] 执行器新增异步边界造成终态丢失 → [缓解] 复用现有事件持久化，补充终态可靠性测试，不使用短超时丢弃事件。
- [风险] 从长会话总结笔记时内容不准确 → [缓解] 先输出候选并要求明确确认，确认前不写入正式笔记或 RAG。
- [风险] 待确认草稿跨 Run 存在并发或过期确认 → [缓解] 使用会话绑定、状态条件更新、内容哈希、有效期和幂等确认。

## Migration Plan

1. 先增加配置项、待确认草稿表、兼容的路由事件和默认 Facade，新路由开关保持可回滚。
2. 接入现有 RAG 查询与普通聊天 handler，运行路由评测和 SSE 回归。
3. 接入笔记直接新增、会话总结候选/确认以及聊天删除拒绝 handler，启用权限、幂等、过期和澄清测试。
4. 出现问题时通过配置开关退回 legacy runner 路径；保留新增事件以便定位。

## Open Questions

无。当前范围内的行为和降级策略已经确定。
