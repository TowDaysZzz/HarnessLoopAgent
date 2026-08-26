# Chat Runtime 第二阶段物理清理清单

本阶段只切断 Chat 生产依赖，不删除旧实现、配置或数据库表。这样可以在无需数据库迁移的情况下回滚上一版本二进制，并保留 Memory、Reminder 等独立控制面。

## 已脱离 Chat、暂时保留的代码

- `internal/routing`：旧 Classifier、RouteDecision、Facade、HandlerSet 与 ComplexHandler。
- `internal/intentexecutor`：Note、Memory、Reminder 的自然语言意图执行器和对话摘要器。
- `internal/notedraft`：候选笔记生成、确认和取消流程。
- `internal/skill` 与 `internal/dailyreview`：Skill 注册、执行、Invocation 生命周期和 Daily Review Workflow。
- `internal/memoryworkflow`、`internal/reminderworkflow` 及相关 LLM Adapter：仍由各自独立 runtime/控制面使用，不能仅因 Chat 不再引用而删除。
- `internal/platform/httpserver` 中的 `WithMemoryChatIntentPilot` 兼容装配入口：生产启动已不再注入，待确认没有外部装配者后删除。

## 暂时保留的配置与文档

- `AGENT.ENABLE_INTENT_ROUTING`、Legacy Fallback、Intent 阈值、Note Draft TTL 等旧 Chat 路由配置。
- `SKILLS.*` 与 Daily Review 配置；在重新提供独立入口前不再影响 Chat。
- `docs/api/daily-review-skill.md` 及历史路由/Workflow 文档，需先标记历史状态，再决定归档或改写。

## 暂时保留的存储

- Skill Invocation、Note Draft、Workflow Run/Event/Outbox，以及 Memory、Reminder、Daily Review Cache 相关表和迁移。
- 既有 Session、Message、Agent Run、Run Event 表保持原样；新 Chat Runtime 没有新增或删除字段。

## 第二阶段删除门槛

1. 用生产调用和仓库依赖扫描确认没有独立 API、Worker、运维脚本或旧版本实例仍使用目标能力。
2. 为需保留的 Memory/Reminder 能力建立明确的独立入口和所有权边界。
3. 先发布停止写入版本并完成数据保留/导出决策，再删除配置、代码和表。
4. 单独编写数据库迁移、灰度和回滚方案；不得把物理删表混入普通 Chat 发布。
