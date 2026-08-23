# Agent Memory Capture API

当前 API 是默认关闭的 MySQL-only 显式记忆试点。所有接口要求有效的 Agent 登录会话；`tenant_id`、`user_id`、Workflow Owner 和审核 Actor 只从认证 `Principal` 构造。请求体即使携带 `owner`、`tenant_id` 或 `user_id` 也会被忽略。

## 启动 Capture

```http
POST /v1/memory-captures
Idempotency-Key: <unique-key>
Content-Type: application/json

{"query":"请记住我喜欢喝茶"}
```

返回 `202 Accepted`。首次执行通常为 `suspended`，响应包含有界 Draft 和 Review：

```json
{
  "run_id":"<run_id>",
  "status":"suspended",
  "draft":{"namespace":"profile","slot_key":"drink","canonical_text":"用户喜欢茶","content_hash":"<sha256>"},
  "review":{"wait_id":"<wait_id>","version":1,"content_hash":"<sha256>","allowed_actions":["approve","reject","submit_edit"]}
}
```

同一 owner 和 `Idempotency-Key` 重试相同输入返回原 Run；复用该键提交不同输入返回 `409`。

## 查询状态和待审核内容

```text
GET /v1/memory-captures/{run_id}
GET /v1/memory-captures/{run_id}/review
```

响应不包含完整 checkpoint、凭证或内部审计字段。其他 tenant/user 的 Run、Wait、candidate 和 edit payload 统一表现为 `404 memory_capture_not_found`，不泄露资源是否存在。

## 批准、拒绝和编辑

```http
POST /v1/memory-captures/{run_id}/resume
Content-Type: application/json

{
  "wait_id":"<wait_id>",
  "version":1,
  "content_hash":"<sha256>",
  "action":"approve"
}
```

`action` 只允许 `approve`、`reject`、`submit_edit`。编辑时增加 `edit`：

```json
{"wait_id":"<wait_id>","version":1,"content_hash":"<sha256>","action":"submit_edit","edit":"改成我喜欢咖啡"}
```

编辑 payload 是 owner-scoped、带 TTL 且一次性消费；系统重新执行 Extract、精确候选加载和冲突策略，创建新 candidate 并返回新的 Review。客户端必须使用最新 Wait ID、version 和 content hash；陈旧、过期或已被其他请求处理的 Wait 返回 `409 memory_capture_conflict`。

批准 correction 时，MySQL 在同一事务内把旧 active Memory 标为 `superseded`、写入 `superseded_by`，再激活新 candidate 并创建唯一 Projection Outbox。旧版本保留供审计，但 exact Recall 只返回当前 active 版本。

## exact-only 边界

当前 Recall 只接受固定 `MemoryRef`、业务 `EntityRef`、`namespace + slot_key`、内容哈希或显式 session/workflow scope。没有稳定 selector、低置信度或多实体歧义时返回空结果/澄清，不扫描 owner 全部 Memory。MySQL exact-only 不支持同义词、模糊措辞和开放式语义召回。

Memory RAG 与 Projection 关闭是受支持模式：不构造 Memory RAG Client、不运行 Projector、不产生 `rag_unavailable`，也不影响 readiness。active 写入仍留下 `pending` Outbox，待未来向量能力上线后回填。
