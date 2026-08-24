## 1. Reminder 领域与安全契约

- [x] 1.1 在 `internal/reminder` 定义 Owner、Reminder、固定 MemoryRef、状态机、版本、内容哈希、时间边界和稳定错误码，并用单元测试验证合法转换、终态不可恢复、过去时间和敏感内容拒绝
- [x] 1.2 定义创建、修改、取消、查询、claim、触发提交和投递完成的领域端口及幂等输入，使用 fake repository 契约测试验证相同输入重放、不同输入冲突和 expected row version
- [x] 1.3 定义 Reminder 查询页、状态过滤、UTC 时间窗口、可信固定引用和有界文本标签，测试空条件只允许分页的 owner-scoped Reminder 列表且不能调用 Memory 全量扫描

## 2. MySQL 事实、审计与 Outbox

- [x] 2.1 新增顺序迁移创建 `reminders`、`reminder_memory_refs`、`reminder_events` 和 `reminder_delivery_outbox`，包含 owner、状态/到期扫描、claim、幂等和 occurrence 唯一索引，并用迁移测试验证约束存在
- [x] 2.2 实现 owner-scoped Reminder 创建、读取、分页查询、修改和取消事务，集成测试验证跨 owner 等价 not found、稳定排序、版本冲突和终态保护
- [x] 2.3 实现到期批次 claim、租约续期/回收以及条件提交 occurrence + event + Outbox，MySQL 集成测试覆盖多实例竞争、过期 token、事务回滚和重复 occurrence
- [x] 2.4 实现 Outbox claim、退避、成功与永久失败事务，集成测试验证相同 delivery key、失败恢复以及 Reminder `processing -> fired|failed`

## 3. 严格自然语言结构化

- [x] 3.1 定义版本化 `ReminderCommandPlan`、动作/触发/目标/Memory selector/clarification schema 及严格解码器，测试未知字段、越权字段、非法枚举、超限集合、低置信度和 Prompt Injection 均 fail closed
- [x] 3.2 实现基于现有 ConversationRunner 的 Reminder LLM Adapter 和有界 repair，测试只接受单个 JSON 对象且模型不能输出 owner、SQL、状态或任意资源 ID
- [x] 3.3 实现以请求时间和 `Asia/Shanghai` 为锚点的确定性时间验证与 UTC 转换，表驱动测试覆盖“明天九点”、跨日边界、过去时间、缺失时间、非法时区和最大 horizon

## 4. Memory 精确关联

- [x] 4.1 将合法 Reminder Memory selector 映射到现有 exact-only Recall Plan，并用测试验证只接受固定 MemoryRef、EntityRef、namespace + slot 和内容哈希
- [x] 4.2 实现 owner-scoped 固定引用加载与提交前版本/hash/active 校验，测试无命中、多义、跨 owner、revoked、expired 和 superseded 时不会静默采用其他 Memory
- [x] 4.3 为审核和投递构造有界、不可信的 Memory 摘要，测试 checkpoint、日志、事件和 Outbox 不包含无界正文或凭证

## 5. Durable Reminder Workflow

- [x] 5.1 定义 `ReminderCommandData`、有界 codec 和 `Parse -> Resolve -> Recall -> Conflict -> Review -> Commit` 节点，节点顺序与 codec 单元测试验证凭证、完整聊天历史和超限数据不能进入 checkpoint
- [x] 5.2 实现 Create、Update、Cancel 的候选、澄清和 Review Wait，测试 approve/reject/submit_edit、编辑后全链路重跑、Wait 版本/hash 校验和稳定 Execution ID
- [x] 5.3 实现 Reminder 应用服务封装 typed DurableRuntime 和 owner-scoped edit payload，测试服务重启恢复、并发 Resume、跨 owner 拒绝以及提交成功但 checkpoint 失败后的幂等重放
- [x] 5.4 实现只读 Query 应用服务而不创建写 Workflow，测试“有哪些提醒”“明天有什么提醒”、零命中和多目标修改澄清

## 6. Intent Router、HTTP 与 Chat 接入

- [x] 6.1 扩展 Router 为互斥的 `memory.capture/memory.recall/reminder.create/query/update/cancel` 意图，语料测试覆盖“提醒我明天九点”“提醒我之前喜欢什么”“帮我记住”和笔记创建不发生双重副作用
- [x] 6.2 将 Memory/Reminder 启动移动到 Executor 的显式业务处理器并移除 HTTP `explicitMemoryIntent` 副作用旁路，Chat 集成测试验证每个请求至多启动一个写 Workflow且 Chat Run 不被 Review Wait 占用
- [x] 6.3 增加认证后的 Reminder 启动、状态、审核恢复、列表、详情、修改和取消 HTTP 契约，Handler 测试验证 Principal 构造 owner、幂等键、分页边界、409 状态冲突和跨 owner 404
- [x] 6.4 增加 SSE/Chat 候选与澄清的有界事件映射，测试响应只包含允许字段、绝对时间、时区、Wait 信封和有界 Memory 引用摘要

## 7. Dispatcher 与可靠投递

- [x] 7.1 实现有界 Dispatcher 循环、数据库到期选择、短租约和优雅停止，fake clock 测试验证未到期不 claim、稳定批次顺序、租约回收和多实例互斥
- [x] 7.2 定义必须接受稳定 delivery key 的 Delivery Adapter 与记录型测试实现，契约测试验证相同 key 重放返回相同结果且非幂等适配器不能启用生产 Worker
- [x] 7.3 实现 Outbox Worker、错误分类、有界退避、最大尝试和优雅停止，测试外部成功后本地提交前崩溃、临时失败重试、永久失败及取消/processing 冲突
- [x] 7.4 增加 Dispatcher/Worker 指标和脱敏日志，测试只记录状态、延迟、attempt、claim 和稳定错误码而不记录 Reminder、Memory 正文或认证数据

## 8. 配置、装配与发布验证

- [x] 8.1 增加默认关闭的 Reminder、Workflow Pilot、Dispatcher、Worker、批次、lease、horizon、时区和重试配置，配置测试验证非法组合 fail fast 且缺少生产 Delivery Adapter 时不能启用 Worker
- [x] 8.2 在 server composition root 按功能矩阵装配 Repository、LLM Adapter、Memory Recall、Durable Workflow、HTTP、Dispatcher 和 Worker，启动测试验证关闭时不改变 Chat/Note/Memory/RAG/readiness
- [x] 8.3 增加 MySQL 端到端测试覆盖自然语言创建、审核、精确 Memory 关联、查询、修改、取消、重启恢复、到期触发、投递重放和跨 owner 隔离
- [x] 8.4 更新 Reminder API、运行配置、at-least-once 边界、灰度与回滚文档，并按仓库约定运行 `go test ./...`、Reminder/Workflow race tests、`go vet ./...` 和 `openspec validate add-memory-aware-reminder-workflow --strict`
