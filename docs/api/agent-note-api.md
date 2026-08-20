# Agent 笔记 API

所有接口要求有效的 Agent 登录会话。`user_id`、`tenant_id` 和 `rag_kb_id` 均由服务端主体与个人知识库绑定确定，客户端不得覆盖。

## 初始化个人知识库

```http
GET /v1/knowledge-base
```

未初始化时返回 `{"configured":false}`。页面随后调用：

```http
POST /v1/knowledge-base
Content-Type: application/json

{"name":"我的笔记"}
```

服务端会优先绑定当前 RAG 用户已有的个人知识库；没有时才创建。成功后返回 `knowledge_base.kb_id`、名称和状态。一个 Agent 用户只能绑定一个知识库，重复调用幂等返回现有绑定。未完成绑定时，新增笔记和创建聊天 Run 返回 `409 knowledge_base_required`。

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

返回 `202 Accepted` 并进入 `delete_pending`：

```json
{
  "note": {"id":"<note_id>","status":"delete_pending"},
  "status_url":"/v1/notes/<note_id>/status",
  "idempotent_replay":false
}
```

响应同时设置 `Location` 为 `status_url`。客户端轮询该地址，只有 RAG 文档删除成功后状态才转为 `deleted`；失败时保留内容、`delete_pending` 状态和 `last_error`，Outbox 可继续重试。同一 `Idempotency-Key` 重复请求不会创建重复删除任务。

笔记状态：`draft`、`indexing`、`indexed`、`index_failed`、`delete_pending`、`deleted`。
