# 14 项能力实现状态

| # | 能力 | 状态 | 当前实现 |
|---|---|---|---|
| 1 | ADR 和接口契约 | 已实现 | `docs/adr/0005-0008`、`docs/api/*` |
| 2 | 用户认证与 HttpOnly Session | 已实现 | `internal/auth`、Hertz auth routes |
| 3 | MySQL 笔记领域和 Outbox | 已实现 | `internal/note`、`0002_auth_notes_outbox.sql` |
| 4 | RAG Note Client 和 JWT 透传 | 已实现 | `internal/ragclient` |
| 5 | 确定性新增/删除/状态轮询 | 已实现 | `internal/platform/httpserver/auth_note.go` |
| 6 | 意图路由与 Executor | 基础已实现 | `internal/routing` 已有确定性路由；尚未替换全部 Chat Run 入口 |
| 7 | Eino 检索问答和 SSE | 部分实现 | Eino Grounding、检索 Tool、可恢复 SSE 已有；笔记写入意图尚未接入 Eino Tool |
| 8 | 三层记忆 | MySQL-only 试点已实现 | MySQL 事实/版本/Outbox、结构化 LLM、精确 Recall、耐久 Capture/Review API、显式 Chat Pilot 和 correction supersede 已装配；开放式语义召回及更广泛 Workflow 消费尚未启用 |
| 9 | Tool Registry 与权限 | 基础已实现 | `internal/tools` 已有角色校验；现有 Eino Tool 尚未全部迁移到 Registry |
| 10 | Trace、指标、安全 | 部分实现 | Harness Observer、预算、熔断、Grounding 和 Metrics 已有；尚缺 OpenTelemetry/Prometheus 导出及完整审计 |
| 11 | React/Vite 工作台 | 基础已实现 | `web` 提供认证、笔记和聊天最小工作台；尚缺生产构建接入与视觉回归 |
| 12 | MCP Facade | 基础已实现 | `internal/mcpfacade` 提供 JSON-RPC tools/list、tools/call，已接入认证保护的 `POST /v1/mcp`；当前只注册 `notes.list` |
| 13 | 复杂任务 Workflow | Daily Review 已实现 | `internal/skill` 提供版本化注册与执行契约；每日回顾使用有界顺序 Workflow、证据校验、Memory 版本和 MySQL single-flight 缓存，默认关闭 |
| 14 | 多 Agent 灰度评测 | 未启用 | `ENABLE_MULTI_AGENT` 和评测接口预留；当前运行时仍是单主 Agent |

本表严格区分“代码已经可运行”和“仅有架构预留”。
