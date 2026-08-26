# Simplify Chat Runtime 验收记录

## 契约环境

验收使用内存持久化 Repository、脚本化 Eino 模型和 owner-scoped RAG Retriever 契约替身。它覆盖真实接口边界：每个 Chat Run 独立传入消息、检索要求和当前知识库绑定；Retriever 不缓存上一 Run 的 Observation。

- 普通聊天：`RequireNoteRetrieval=false`，仍允许模型自主选择只读检索工具。
- 明确笔记问题：`RequireNoteRetrieval=true`；漏调工具、不可用证据或非法引用均 fail-closed。
- 连续追问：第二个 Run 重新调用 Retriever，并使用当前请求的 owner-scoped KB ID。
- 固定拒答：`当前笔记检索结果不足以支持可靠回答，我不能使用模型常识补全。`

2026-08-25 补充验证非强制自主检索：当 `RequireNoteRetrieval=false` 且模型调用 `semantic_search_notes` 时，Runner 在工具完成后动态启用 Grounding，缓冲后续答案并复用引用校验。`TestGroundedRunnerProtectsAutonomousRetrievalAnswers` 覆盖非法引用在无修复预算时返回固定拒答，以及有一次修复预算时只输出引用 `retry.md` / `chunk-1` 的修复答案；两个场景均验证非法草稿未进入文本事件且只产生一个终态。

验证命令：`go test ./internal/agent/eino ./internal/grounding -count=1` 与 `go test ./... -count=1`，结果均通过。

2026-08-25 契约验收样本：明确笔记问题 Run `6b208d6e-423a-4ea5-ba02-d42c4e9a4e7f`，连续追问 Run `033b48cf-0cd5-4941-b299-50f8254f8e0b`。后续可用 `go test -v ./internal/chat -run 'TestServiceUsesLinearChatRAGEventFlow|TestServiceRestoresRecentTurnsAndReevaluatesFollowUp'` 生成新的样本。事件序列固定为：

```text
run.queued -> run.started -> retrieval.decided
           -> run.status/tool.completed/text.delta
           -> run.completed
```

## SSE 与持久化

HTTP 契约测试在创建 Run 后不建立 SSE，等待后台完成并检查助手消息，然后以 `after=1` 模拟 `Last-Event-ID: 1` 重连。结果仅回放序号大于 1 的事件，事件序号单调且最终助手消息完整。

## 数据库与回滚

本变更没有数据库迁移。Chat 继续读取原有 Session、Message、Run 和 Run Event 模型；Repository 生命周期测试验证原子创建与原子完成不变。回滚演练步骤：停止新版本进程，部署上一版本二进制并沿用同一 schema/config；旧 routing/skill/workflow 代码、配置和表均未物理删除，因此无需数据回填。回滚前应等待或显式取消活跃 Run；意外重启遗留的 `queued/running` Run 会被标记为 `interrupted`。
