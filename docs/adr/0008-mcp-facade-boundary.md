# ADR 0008：MCP Facade 边界

## 状态

已接受。

## 决策

Agent 内部调用 RAG 继续使用强类型 HTTP Client，不将 RAG 内部链路改造成 MCP。

未来若需要让飞书、Codex 或其他客户端通过 MCP 使用笔记能力，只在 Agent 边界增加 MCP Facade。MCP Tool 调用必须经过与 HTTP API 相同的 Agent 认证、权限、确认、审计、幂等、Trace 和领域服务，不允许直接访问 RAG 或 Milvus。

## 后果

- 内部链路保持低延迟、强类型和可预测错误语义。
- 外部工具生态可以复用统一的笔记能力。
- MCP 不是权限边界，也不能绕过 Agent 的安全策略。
