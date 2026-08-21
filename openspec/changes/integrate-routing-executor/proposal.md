## Why

当前 `routing.Classifier` 只在单元测试中使用，聊天主链路仍通过关键词判断是否进入 RAG，导致“记笔记、查笔记、普通聊天、复杂任务和歧义输入”没有统一的可观测路由。现在接入分类器和 Executor，可以把现有单 Agent 能力组织成稳定的意图执行边界，并为后续 Workflow 与多 Agent 灰度保留扩展点。

## What Changes

- 在每次聊天 Run 开始时执行统一意图分类，并持久化路由决策事件。
- 将 `RouteDecision` 拆分为业务意图和任务复杂度两个维度，支持 `note.query + complex` 等组合路由。
- 新增 Executor 编排层，根据组合决策选择笔记候选/写入、RAG 问答、普通模型、复杂任务或澄清执行器。
- 将 `note.query` 继续接入现有 Eino + `semantic_search_notes` + 证据门禁链路。
- 为 `note.create` 提供候选生成和确定性写入端口：Agent 可从当前会话历史总结候选笔记，用户明确确认后才写入；禁止模型自行声称已保存。
- 聊天链路明确禁止删除笔记；删除能力继续只通过笔记页面和 REST API 提供。
- 为复杂任务提供单 Agent Workflow 执行端口，并保留后续多 Agent 替换能力。
- 为 `intent.unclear` 返回澄清文本，随后正常完成当前 Run，不引入新的 Run 状态。
- 为每个分支统一输出状态、错误、耗时和意图指标，保持现有 SSE 和权限边界。

## Capabilities

### New Capabilities

- `chat-intent-routing`: 分离识别聊天业务意图和任务复杂度，并将组合决策路由到受控执行器。
- `chat-intent-execution`: 为不同组合决策提供候选确认、确定性写入、RAG、模型和澄清执行行为，同时禁止聊天删除。

### Modified Capabilities

- 无。

## Impact

- 影响 `internal/chat`、`internal/routing`、`internal/agent/eino`、笔记服务端口和 HTTP/SSE 运行事件。
- 需要新增 Executor 接口及其默认实现，并更新内存仓储、MySQL 事件记录和聊天服务测试。
- 不改变已有会话、Run、SSE、RAG HTTP 契约；新增的路由事件属于向后兼容的内部事件。
- 前端无需改变 API 使用方式，但可在后续消费路由状态事件。
