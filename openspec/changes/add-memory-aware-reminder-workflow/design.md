## Context

See `proposal.md` for motivation and the two delta specs for observable behavior. 当前项目已有三组可复用能力：`internal/memory` 提供不可变版本、严格选择器和 MySQL exact-only Recall；`internal/memoryworkflow` 提供 LLM Draft、候选冲突、Review 与 Commit；`internal/workflow` 提供带 checkpoint、Wait、claim lease 和幂等执行身份的耐久 Runner。聊天层同时存在业务 Intent Router 与 HTTP 层 `explicitMemoryIntent` 旁路，且“帮我记住”仍可能被路由为 `note.create`。

现有 `memory_records` 只有事实有效期 `expires_at`，没有提醒触发时间、提醒状态或投递 occurrence；Workflow Wait 表示等待人工决定，也没有到期任务扫描器。因而 Reminder 必须拥有独立领域和调度边界，同时可以通过固定 MemoryRef 消费已有用户事实。

## Goals / Non-Goals

**Goals:**

- 复用现有严格 LLM、安全 owner scope、MySQL exact recall 和 Durable Workflow，而不复制 Memory 引擎。
- 为一次性提醒建立确定的时间、版本、状态、审计、并发和投递语义。
- 让同一自然语言请求只产生一种业务副作用，并让查询、修改和取消可精确定位。
- 将外部投递的不确定性限制在事务 Outbox 与幂等适配器边界内。

**Non-Goals:**

- 不支持循环日程、自然语言 RRULE、地理围栏、模糊话题触发、外部日历或跨用户委托。
- 不把 Reminder 投影到 Memory RAG，也不使用向量检索定位待触发任务。
- 不使用 Workflow sleep、Wait expiry 或 `memory_records.expires_at` 驱动触发。
- 不在本次选择具体邮件、短信、系统推送供应商；生产 Worker 必须有显式适配器才能启用。

## Decisions

### 1. Reminder 是独立聚合，Memory 只提供固定上下文

新增 `reminders`、`reminder_memory_refs`、`reminder_events` 和 `reminder_delivery_outbox`。`reminders` 至少保存 owner、正文、`scheduled_for`/`next_fire_at` UTC、原始时区、状态、row version、内容哈希、来源 Chat/Workflow 引用和时间戳。首版一次性 occurrence 使用 Reminder ID 与版本派生稳定 identity。

Memory 关联表保存 Memory ID、Lineage Version 和 Content Hash；触发或展示前通过现有 owner-scoped `BatchGet` 验证。相比把 Reminder 写进 `memory_records.structured_value`，独立聚合可以建立 `next_fire_at` 索引、多个并存提醒、状态机和投递约束，也不会让 active slot 唯一性错误合并不同提醒。相比复制 Memory 正文，固定引用保留事实版本和撤销语义。

### 2. 使用一个严格 ReminderCommandPlan，而不是让模型执行业务操作

新增与 `memoryllm.Adapter` 相同风格的版本化 JSON 契约，字段限定为 `version/action/content/trigger/timezone/target_selector/memory_selectors/confidence/clarification`。Create 只接受一次性 `at_time`；Query 可表达状态和有界 UTC 时间窗口；Update/Cancel 必须携带来自可信 UI 的固定 ReminderRef，或生成可由 Repository 有界查询的正文标签与时间窗口。

Adapter 使用请求接收时刻和 `Asia/Shanghai` 作为显式解析锚点；模型给出 RFC3339 候选后，确定性代码检查时区、未来时间、最大 horizon、字段长度和置信度。模型不输出 owner、状态、SQL 或任意 ID。相比纯关键词规则，该结构可处理中文相对时间；相比 Tool Calling，严格 JSON 不授予模型副作用权限。

### 3. 统一业务 Intent Router，移除 HTTP 副作用旁路

扩展领域意图为 `memory.capture/memory.recall/reminder.create/reminder.query/reminder.update/reminder.cancel`，并把 Note、Memory、Reminder 的互斥决策放在 Chat Service 调用的 Router/Executor 中。HTTP Handler 只负责认证、Chat Run 创建和返回，不再通过 `explicitMemoryIntent` 异步启动 Capture。

初始确定性规则先识别明显的未来时间与 Reminder 动词，随后由严格计划处理歧义；“提醒我之前说过……”若没有未来触发或任务动作则进入 Memory Recall。低置信度时返回澄清。相比继续增加 Handler 关键词旁路，这使副作用只有一个事实入口，也便于测试 `note.create` 与 Memory Capture 不被重复触发。

### 4. Reminder Workflow 处理命令生命周期，但不承担时钟调度

`reminder-command-v1` 使用强类型状态和节点：

```text
ParseCommand
  -> ResolveTimeAndTarget
  -> ExactMemoryRecall
  -> FindReminderConflicts
  -> ReviewOrClarify
  -> CommitMutation
```

Query 为只读应用服务路径，可复用 Parse/Resolve 但不创建耐久写 Workflow。Create、Update、Cancel 都进入 Review Wait；编辑通过 owner-scoped、带 TTL、一次性 payload 重新运行全链路。Chat Run 可以正常结束，候选通过独立 API/UI 查询和恢复。Commit 使用 `ExecutionID + mutation index` 作为幂等键，事务内写 Reminder、引用和事件。

相比把明确命令直接提交，统一 Review 首版会多一次交互，但能暴露模型解析的绝对时间和 Memory 关联，避免无声创建错误提醒。稳定后可通过新 change 为高置信度绝对时间设计自动提交策略。

### 5. Reminder 查询是受限领域查询，不是 Memory owner 扫描

Repository 提供固定 ReminderRef、状态、UTC 时间窗口和有界文本标签的查询，所有条件始终包含 tenant/user，设置 limit、cursor 和确定性排序。`我有哪些提醒` 合法地查询当前 owner 的 `scheduled` Reminder；它不调用 `memory.Repository.FindExact` 扫描全部 Memory。Update/Cancel 命中零条时返回 not found，命中多条时返回有界候选并要求选择。

该边界保留现有 Memory “无稳定 selector 不得 owner 全量扫描”的安全契约，同时满足 Reminder 列表这一正常领域用例。

### 6. Dispatcher 用短租约 claim，事务提交 occurrence 与 Outbox

Dispatcher 以 `(status=scheduled, next_fire_at<=db_now)` 和稳定 `(next_fire_at,id)` 顺序小批量 claim，写随机 token 与 `lease_until` 后在短事务外准备 occurrence，再以 token、row version 和未过期租约条件提交。提交事务将 Reminder 置为 `processing`、插入事件并创建由 `reminder_id + occurrence_version` 唯一约束的 Outbox。

不让投递网络调用持有事务。租约过期允许重取；旧实例不能用过期 token 覆盖新状态。数据库时间用于到期比较，应用时间只用于测试注入和显示，减少多实例时钟漂移影响。

### 7. Outbox Worker 采用 at-least-once，适配器必须接受稳定 delivery key

Worker claim pending Outbox，调用 `Deliver(ctx, Delivery{Key,...})`，按稳定错误分类完成、退避或进入永久失败。外部成功后本地提交前退出会导致重放，所以生产适配器必须支持相同 key 的幂等处理；不能支持的渠道不得启用。成功事务幂等完成 Outbox 并将 Reminder `processing -> fired`，耗尽重试则 `processing -> failed`。

Cancel 只允许 `scheduled`。一旦 occurrence 与 Outbox 已原子创建，取消返回冲突，避免“数据库显示取消但通知已经发出”的假保证。未来若需要撤回或最佳努力取消，应新增独立状态和渠道契约。

### 8. 配置、生命周期与观测保持默认关闭

新增顶层 `REMINDER.ENABLED`、Workflow Pilot、Dispatcher/Worker、扫描批次、lease、horizon、重试和时区配置。启用写 Pilot 需要数据库、严格结构化 Runner、Durable Store 和 edit payload store；启用 Worker 还必须注入生产 Delivery Adapter。所有后台循环派生自服务根 context，在 HTTP shutdown 前停止。

指标只记录 intent、状态、延迟、claim、重试、冲突和稳定错误码，不记录 Reminder 正文、Memory 正文或凭证。关闭功能不影响现有 readiness。

## Risks / Trade-offs

- [相对时间被模型解析错误] → 在 Review 中显示绝对时间与时区，确定性校验过去时间和 horizon，首版所有写操作必须确认。
- [“提醒我”同时表示查询记忆和创建任务] → 路由结合未来时间/动作与严格置信度，歧义无副作用地澄清，并增加中文语料回归集。
- [固定 Memory 在触发前被替代] → 触发时验证 ID/version/hash，只省略关联或报告原因，不静默采用新版本。
- [外部投递成功但本地确认失败造成重复通知] → 固定 delivery key 和生产适配器幂等是启用条件，文档明确只保证 at-least-once。
- [数据库到期扫描随数据增长变慢] → 使用 owner-independent 的 `(status,next_fire_at,id)` 调度索引、有界批次和短租约；用户查询继续按 owner 索引。
- [统一 Router 改变现有“帮我记住”行为] → 增加 Note/Memory/Reminder 互斥契约测试，默认关闭 Reminder，先灰度启用新路径并保留 API 直接 Capture。

## Migration Plan

1. 部署 Reminder 表、索引和兼容代码，保持全部 Reminder 开关关闭，运行 MySQL 迁移与旧 Chat/Note/Memory 回归。
2. 在测试环境启用领域与 Workflow Pilot但关闭 Dispatcher/Worker，验证自然语言结构、审核、重启恢复、精确查询、修改/取消、跨 owner 和 Memory 固定引用。
3. 使用记录型幂等 Delivery Adapter 启用 Dispatcher/Worker，验证租约回收、并发 claim、事务回滚、成功后崩溃重放和永久失败。
4. 小范围启用统一 Chat Intent，观察歧义率、候选确认率和 Note/Memory/Reminder 互斥指标。
5. 配置真实且支持 delivery key 的渠道后再启用生产 Worker；在此之前保留 Worker fail-fast。

回滚时先关闭 Worker 和 Dispatcher，再关闭 Workflow Pilot 与 Reminder 顶层开关。已创建的 Reminder、事件、checkpoint 和 Outbox 保留；重新启用后继续处理。若真实投递已经发生，不通过代码回滚声称撤回。
