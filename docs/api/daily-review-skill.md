# Daily Review Workflow Skill

## 能力与边界

每日回顾是 `daily_review/v1` 只读 Workflow Skill。它复用现有 `POST /v1/sessions/{session_id}/runs` 和 Run SSE，不增加 HTTP 副作用旁路。自然语言示例包括“回顾今天”“复盘昨天”和“回顾 2026-08-24”；“回顾并保存”、日期范围、越界日期和 Prompt Injection 会进入澄清，不会同时触发写操作。

普通聊天、笔记、记忆和提醒继续使用现有内建 Handler。只有注册为多阶段业务 Skill 的请求进入 Workflow。当前不包含自动调度、主动推送、每日洞察、跨日行为分析或自动保存 Note/Memory/Reminder。

## 数据与新鲜度

Workflow 按用户和本地日期读取 `[start,end)`：

- Chat：跨会话、有界、每会话配额，排除关联到 `daily_review` Invocation 的用户触发消息与助手输出。
- Note：按 `occurred_at`（缺失时使用 `created_at`）读取可见记录，固定状态、更新时间版本与内容哈希。
- Memory：元数据快照只读取 owner-scoped 单调 Mutation Version；缓存 miss 后才执行 Recall，并固定 lineage version、内容哈希、active 状态和 scope。

缓存逻辑键包含 owner、日期窗口、时区、规范化选项和 Skill/Schema/Prompt 版本；源指纹包含 Chat/Note 摘要和 Memory Mutation Version。命中仍执行有界 metadata 查询，但跳过正文加载、Memory Recall、工具与模型调用。生成者通过数据库租约和 claim token single-flight；提交前再次计算快照，变化时最多重建一次。有效期取 TTL、相关 Memory 最早过期时间和策略有效期的最早值。

## SSE 事件

既有 `run.queued -> run.started -> route.decided -> executor.started -> ... -> executor.completed -> run.completed|failed` 顺序保持不变。Skill 允许新增：

- `skill.started`：仅含 Skill ID。
- `skill.cache`：`hit` 或 `miss`。
- `skill.step`：允许公开的节点 ID，不含 Prompt 或正文。
- `skill.candidate`：严格 `daily_review_report_v1` 结构化结果。
- `text.delta`：确定性渲染文本。

`route.decided` 只记录 Skill ID、版本与参数哈希；事件、日志和指标不记录日期参数正文、Chat/Note/Memory 正文、Token、Cookie 或数据库凭证。

## 灰度与回滚

迁移应先于开关发布。确认 MySQL 已应用 `0009`–`0011` 后，依次启用 `SKILLS.ENABLED` 和 `SKILLS.DAILY_REVIEW_ENABLED`。重点观察匹配澄清率、Workflow 延迟、缓存 hit/miss、claim 过期、快照持续变化和证据删除率。

回滚先关闭 `DAILY_REVIEW_ENABLED`，再关闭 `SKILLS_ENABLED` 并重启。普通 Chat/Note/Memory/Reminder 不受影响；Invocation、缓存和 Mutation Version 可保留，过期缓存由有界清理删除。不要回滚用户 Chat、Note 或 Memory 事实。
