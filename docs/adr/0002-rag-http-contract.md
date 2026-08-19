# ADR 0002：Agent 通过 HTTP 调用独立 RAG 服务

- 状态：已采纳
- 日期：2026-08-19

## 背景

笔记 Agent 负责对话编排、工具调用和后续的长期记忆工作流；现有 RAG 项目已经负责知识库、文档切分、向量检索、重排和引用生成。两个项目需要独立部署、独立扩缩容，并避免 Agent 直接依赖 Milvus 数据结构。

## 决策

Agent 使用 Hertz Client 调用 RAG 服务的 `POST /v1/retrieve`。请求由 Agent 服务端补充知识库 ID、策略和 Bearer API Key；模型只能为 Eino Tool `semantic_search_notes` 提供以下参数：

```json
{
  "query": "垃圾回收",
  "top_k": 5
}
```

Agent 使用强类型结构解析统一响应 envelope，并向模型保留 `request_id`、检索内容、分数、citation、source 和 refusal。模型回答历史记录问题时必须先调用该 Tool，只能依据检索结果作答，并使用 citation 的 `file_name` 和 `chunk_id` 标注来源。

调用链如下：

```text
客户端 -> 笔记 Agent -> Eino semantic_search_notes -> RAG HTTP API -> 知识库/Milvus
```

## 安全边界

- RAG API Key 只来自 YAML、环境变量或密钥管理系统，不写入日志和 Tool schema。
- 知识库 ID 由 Agent 服务端按当前用户或租户注入，模型不能覆盖。
- RAG 服务仍负责 API Key、tenant 和知识库权限校验。
- Agent 不直接连接 Milvus，也不复用 RAG 内部 repository。
- 当前开发知识库只用于联调；个人笔记上线前仍需完成 subject/user 级隔离。

## 失败语义

- 网络错误、超时、非 2xx 状态和非法 JSON 作为 Tool 错误返回。
- Client 限制响应体大小，避免异常响应占用过多内存。
- 空结果或 refusal 不视为可供模型自由补全的信号；Agent 必须明确说明证据不足。

## 影响

优点是服务职责清晰，RAG 可独立演进，Agent 只依赖稳定 HTTP 契约。代价是增加一次网络调用，需要维护超时、鉴权、链路标识和跨服务集成测试。
