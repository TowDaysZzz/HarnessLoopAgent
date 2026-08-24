# Reminder API 与运行边界

## 能力范围

首版只接受 `Asia/Shanghai` 下具有明确未来时间的一次性 Reminder。Reminder 是独立聚合，MySQL 是 owner、正文、绝对触发时间、状态、版本、审计事件与投递 Outbox 的唯一事实来源。Memory 仅通过 `(Memory ID, Lineage Version, Content Hash)` 固定引用；被撤销、过期或替代后不会静默切换到新版本。

自然语言写操作执行 `Parse -> Resolve -> Recall -> Conflict -> Review -> Commit` Durable Workflow。Chat Run 生成候选后即可结束，Review Wait 通过独立控制面恢复，不占用原 SSE 连接。

## 认证 API

以下接口均从认证 Principal 构造 owner，不接受请求体覆盖 tenant/user：

- `POST /v1/reminder-commands`：以 `{"query":"提醒我明天九点提交周报"}` 启动创建命令，必须携带 `Idempotency-Key`。
- `GET /v1/reminder-commands/{run_id}`：读取命令与当前 Wait 状态。
- `POST /v1/reminder-commands/{run_id}/resume`：提交 `wait_id`、`version`、`content_hash`、`action`，编辑时增加 `edit`。允许动作是 `approve`、`reject`、`submit_edit`。
- `GET /v1/reminders`：owner-scoped 分页列表；支持 `status`、`from`、`until`、`label`、`cursor_at`、`cursor_id`、`limit`。
- `GET /v1/reminders/{reminder_id}`：详情；跨 owner 与不存在均返回 404。
- `PATCH /v1/reminders/{reminder_id}`：使用 `query`、`row_version` 与 `Idempotency-Key` 启动修改审核。
- `DELETE /v1/reminders/{reminder_id}`：使用 `row_version` 与 `Idempotency-Key` 启动取消审核。

陈旧 Wait、row version、claim 或幂等键冲突返回 409。候选事件只包含绝对时间、时区、Wait 信封、可信 Reminder 引用和有界的不可信 Memory 摘要，不包含 owner、凭证或聊天历史。

## 配置与启动

`REMINDER.ENABLED` 默认 `false`。生产建议按以下顺序逐步开启：

1. 先迁移表，保持所有开关关闭。
2. 开启 `ENABLED` 与 `WORKFLOW_PILOT_ENABLED`，验证创建、查询、修改、取消及重启恢复，暂不触发投递。
3. 开启 `DISPATCHER_ENABLED`，验证到期 claim 与 Outbox 生成。
4. 只有注入支持稳定 delivery key 的真实生产适配器，并设置 `PRODUCTION_DELIVERY_ADAPTER` 后，才开启 `WORKER_ENABLED`。

Worker 开启还要求 Dispatcher 开启。非法时区、非正数批次/lease/horizon、倒置退避区间及缺少生产适配器都会启动失败。环境变量使用 `REMINDER_` 前缀，例如 `REMINDER_ENABLED`、`REMINDER_LEASE_DURATION`。

## 投递保证

Dispatcher 以数据库到期条件、稳定顺序和短租约 claim Reminder，再在一个 MySQL 事务中提交 occurrence、审计事件与唯一 Outbox。Worker 对外调用不持有数据库事务。

投递保证是 **at-least-once**，不是 exactly-once。外部成功而本地完成事务前崩溃会重放同一 `delivery_key`；生产适配器必须用该 key 幂等返回原结果。暂时错误采用有界指数退避，永久错误或超过最大尝试后 Reminder 进入 `failed`。进入 `processing` 后取消返回冲突。

## 灰度、观测与回滚

指标和日志只记录 component、event、status、latency、attempt、claim 结果和稳定错误码，不记录 Reminder/Memory 正文、Cookie、Token 或认证头。

回滚顺序：先关闭 Worker，再关闭 Dispatcher，随后关闭 Workflow Pilot 与顶层 Reminder。已提交 Reminder、checkpoint 与 pending Outbox 保留；重新启用后继续有界处理。已经发生的外部投递不能通过代码回滚撤回，也不得宣称 exactly-once。
