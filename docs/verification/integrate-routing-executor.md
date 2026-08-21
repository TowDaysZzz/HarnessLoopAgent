# 意图路由与 Executor 验证记录

验证日期：2026-08-21

## 发布开关

```text
ENABLE_INTENT_ROUTING=true
ENABLE_LEGACY_ROUTING_FALLBACK=true
INTENT_COMPLEX_THRESHOLD=120
INTENT_MIN_WRITE_CONFIDENCE=0.95
NOTE_DRAFT_TTL=24h
ENABLE_MULTI_AGENT=false
```

`ENABLE_LEGACY_ROUTING_FALLBACK` 只允许 `chat` 和 `note.query` 在 Executor 不可用时回退；`note.create`、`note.delete` 和 `intent.unclear` 不会交给模型执行写操作。

## 自动化验证

以下命令全部通过：

```bash
go test ./...
go test -race ./internal/...
go vet ./...
openspec validate integrate-routing-executor --strict
```

自动化测试覆盖分类一次性、业务意图与复杂度组合、RAG evidence gate 和 citation 校验、只读 fallback、写操作禁 fallback、草稿用户/租户/会话隔离、过期与旧版本拒绝、Prompt Injection、候选 SSE 以及成功/失败/取消/超时终态。

## 真实服务验证

在独立验证用户和个人知识库上完成以下场景：

| 场景 | 路由与执行器 | 结果 |
| --- | --- | --- |
| 普通聊天 | `chat + simple` / `simple_chat` | 流式文本并正常完成 |
| 简单历史查询 | `note.query + simple` / `simple_note_query` | 调用 `semantic_search_notes`，通过证据门禁并返回真实文件名和 chunk ID |
| 复杂历史查询 | `note.query + complex` / `complex_note_query` | 比较两条检索记录并引用两组真实来源 |
| 低置信度写入 | `intent.unclear` / `clarification` | 不调用模型或写工具，输出澄清后正常完成 |
| 明确新增笔记 | `note.create` / `note_create` | 写入 MySQL 和 Outbox，RAG job 最终为 `completed` |
| 历史总结候选 | `note.create` / `note_create` | 输出 `note.draft.candidate`，确认前正式笔记数不变 |
| 同会话确认 | `note.create` / `note_create`，原因 `draft_confirm` | 写入正式笔记并完成 RAG 索引 |
| 聊天删除 | `note.delete` / `note_delete_rejected` | 输出页面/API 引导，笔记仍存在且未产生删除投影 |

历史会话和 SSE 在启用新路由后保持兼容。关闭 `ENABLE_INTENT_ROUTING` 的服务测试会恢复原有 Runner；重新开启后路由事件与既有 SSE 事件可以共同回放。

## 回滚

临时回滚时在 Agent 服务环境中设置：

```bash
ENABLE_INTENT_ROUTING=false
```

然后按部署方式重启 Agent 服务。恢复新链路时重新设置为 `true` 并重启。回滚只改变聊天执行路径，不删除 `note_drafts` 表、Run 事件、正式笔记或 RAG 数据。
