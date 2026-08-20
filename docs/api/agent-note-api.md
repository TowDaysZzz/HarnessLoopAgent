# Agent 笔记 API

所有接口要求有效的 Agent 登录会话。`user_id`、`tenant_id` 和 `rag_kb_id` 均由服务端主体与配置确定，客户端不得覆盖。

## 创建笔记

```http
POST /v1/notes
Idempotency-Key: <unique-key>
Content-Type: application/json

{"title":"Go GC","content":"...","occurred_at":"2026-08-20T10:00:00+08:00","tags":["Go"]}
```

返回 `202 Accepted`。响应包含本地 `note_id`、`external_note_id` 和 `indexing` 状态。重复幂等键返回同一条笔记。

## 查询笔记

```text
GET /v1/notes?limit=20&cursor=<cursor>
GET /v1/notes/{note_id}
GET /v1/notes/{note_id}/status
```

列表与详情来自 Agent MySQL；状态接口同时展示本地业务状态和最近一次 RAG 投影任务状态。

## 删除笔记

```http
DELETE /v1/notes/{note_id}
Idempotency-Key: <unique-key>
```

返回 `202 Accepted` 并进入 `delete_pending`。只有 RAG 文档删除成功后才转为 `deleted`；失败时保留内容和可重试状态。

笔记状态：`draft`、`indexing`、`indexed`、`index_failed`、`delete_pending`、`deleted`。
