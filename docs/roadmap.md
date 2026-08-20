# 笔记 Agent 实施路线图

## 架构决策

- 笔记 Agent 负责原始笔记、笔记版本、会话、Agent Run、SSE 事件、长期记忆和洞察结果。
- RAG RetrievalOps 负责文档分块、Embedding、Milvus 索引、检索、重排、引用和检索评估。
- 笔记 Agent 通过有版本的 HTTP API 调用 RAG，不使用 MCP，也不直接访问 Milvus。
- Agent MySQL 是笔记数据的唯一事实来源；RAG 中的数据是可以重新构建的搜索投影。
- 明确的笔记写入请求使用确定性的笔记服务；日期和标签查询使用 SQL；语义问题使用 RAG；洞察功能使用 Eino Compose；开放式任务使用 Eino Agent。

## 交付阶段

1. 初始化空仓库，加入最小可运行的 Eino 模型、Tool、流式 Runner、配置模块、健康检查、测试和容器镜像。
2. 添加强类型 RAG HTTP Client 和 `semantic_search_notes` Eino Tool；先接入模拟 RAG Server，再连接真实的 `POST /v1/retrieve`。
3. 已完成：添加会话、Agent Run、可持久化的 Run 事件，以及基于单调递增事件序号实现的可恢复 SSE；上下文采用可替换的 Token 预算组装接口。
4. 已完成第一版：Agent BFF 认证会话、MySQL 笔记事实表、事务 Outbox、笔记新增/查询/删除、RAG 用户 JWT Client 和 Job 状态刷新。当前只支持新增与删除，更新及版本策略延期。
5. 添加确定性的意图路由、有边界的上下文压缩、Tool 权限策略和可审计的 Agent Run。
6. 添加候选、启用、拒绝、过期四种状态的长期记忆，并支持证据关联、用户确认、自动过期和撤销。
7. 分别为昨日回声、昨天的梦、那年今日、每日新知、幸福灵药和行为洞察建立独立的 Eino Compose 工作流。
8. 已完成基础服务间用户 JWT 传递和 Agent 侧租户/用户边界；仍需补齐已有聊天表的用户外键隔离、链路追踪、脚本化评测、负载测试和灰度发布。

## RAG 接口要求

RAG 服务后续需要提供 `PUT /v1/index/documents/{external_id}`、`GET /v1/index/jobs/{job_id}`、`DELETE /v1/index/documents/{external_id}`，并保留现有的 `POST /v1/retrieve`。结构化笔记写入需要包含 `external_id`、单调递增的 `external_version`、`document_type=note`、笔记发生时间、标签和来源元数据。RAG 必须拒绝旧版本覆盖新版本。

用户触发的检索必须携带服务端签发的租户和用户身份。RAG 必须在服务端构造 tenant、subject、KB 和 deleted 过滤条件，不能信任模型生成或客户端传入的安全过滤字段。

## 提交顺序

`agent/bootstrap-eino` -> `agent/rag-client-tool` -> `agent/chat-sse` -> `agent/note-domain` -> `rag/structured-index-api` -> `agent/outbox-indexing` -> `agent/memory` -> `agent/insight-workflows` -> `security-evals-rollout`。

当前实现状态详见 `docs/implementation-status.md`。多 Agent 默认关闭，必须通过评测和灰度开关后才能启用；MCP 只能经过 Agent 权限边界，不能直连 RAG 或 Milvus。
