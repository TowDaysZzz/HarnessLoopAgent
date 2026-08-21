## 1. 配置与领域模型

- [x] 1.1 增加 `ENABLE_INTENT_ROUTING=true`、`ENABLE_LEGACY_ROUTING_FALLBACK=true`、复杂度阈值 `120`、写操作最低置信度 `0.95` 和草稿有效期 `24h` 配置，验证 YAML/env 覆盖和非法值测试通过
- [x] 1.2 将 `RouteDecision` 重构为业务意图与复杂度两个维度，并用分类器单测覆盖 `chat/note.query/note.create/note.delete/unclear` 与 `simple/complex` 组合
- [x] 1.3 增加安全的路由事件序列化结构，验证事件不包含 Token、密码、Cookie 或原始敏感输入

## 2. Executor Facade 与执行器

- [x] 2.1 实现 Router/Executor Facade、确定性 handler 和流式 handler 端口，用 fake handler 验证每个组合决策只选择一个执行器
- [x] 2.2 将 `note.query` 和 `chat + simple` 接入现有 Eino Runner，验证 RAG 查询仍经过 evidence gate/citation 校验且普通聊天保持流式输出
- [x] 2.3 将 `complex` 请求接入单 Agent ComplexHandler，验证复杂笔记查询保留 RAG 约束，并受 MaxSteps、调用预算和超时限制
- [x] 2.4 实现 `intent.unclear` ClarificationHandler，验证输出澄清文本后当前 Run 正常 `completed`，且不会调用模型或写工具
- [x] 2.5 实现聊天 `note.delete` 拒绝 handler，验证只输出页面/API 引导且聊天运行时没有可调用的删除端口

## 3. 聊天新增笔记与待确认草稿

- [x] 3.1 增加待确认笔记草稿模型、Repository、MySQL 迁移和内存实现，验证用户/租户/会话隔离、状态转换、内容哈希与过期处理
- [x] 3.2 实现当前消息含完整内容时的 NoteCreateHandler，调用现有 Note Service，验证知识库绑定、幂等重放和索引状态返回
- [x] 3.3 实现会话历史 NoteDraftHandler，基于有界上下文生成标题/正文候选并持久化 pending 草稿，验证确认前 notes/outbox/RAG 均无新增
- [x] 3.4 实现同会话候选确认、取消和修改路径，验证只有存在最新 pending 草稿时确认语句才路由为写入，确认操作幂等且跨会话、过期或旧版本均不能保存

## 4. 聊天主链路与事件接入

- [x] 4.1 在 Chat Service Run 执行前调用 Classifier 并持久化 `route.decided`，用服务单测验证每个 Run 只分类一次且决定包含业务意图与复杂度
- [x] 4.2 接入 Executor Facade 并保留配置控制的 legacy fallback，验证只读请求可受控降级、写请求绝不降级给模型
- [x] 4.3 在 executor started/completed/failed 路径记录耗时、状态、错误码和意图指标，验证敏感信息脱敏
- [x] 4.4 补充 SSE 回归测试，验证路由事件、候选事件、工具事件、文本增量和终态在成功、失败、取消、超时下均可恢复

## 5. 权限和安全验证

- [x] 5.1 补充跨用户会话、草稿、笔记和 Run 的权限测试，验证用户输入不能改变租户、会话或知识库范围
- [x] 5.2 验证聊天 Agent 的 Tool Set 不包含删除笔记能力，并通过集成测试确认删除请求不会产生 note delete/outbox 事件
- [x] 5.3 验证 Prompt Injection、低置信度写操作和过期确认均进入拒绝或澄清路径，不执行写入

## 6. 集成验证与发布

- [x] 6.1 运行 `go test ./...`、`go test -race ./internal/...` 和 `go vet ./...`，确认新增路由、Executor 和草稿测试全部通过
- [x] 6.2 运行 Agent RAG 集成测试和真实模型 CLI，分别验证普通聊天、简单/复杂历史查询、澄清、直接记笔记、聊天历史总结候选/确认和聊天删除拒绝
- [x] 6.3 验证关闭 `ENABLE_INTENT_ROUTING` 可恢复 legacy runner，重新启用后历史会话和 SSE 仍兼容
- [x] 6.4 运行 `openspec validate integrate-routing-executor --strict` 并记录最终验证结果、发布开关和回滚命令
